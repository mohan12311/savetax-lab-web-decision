"""실행기: 사건마다 MCP 도구 서버를 띄우고 LLM 도구 루프(공급자는 어댑터: OpenAI 기본, Anthropic 선택)를 돌려 답안지를 쓴다(답안지 명세 2절).

    사건 하나 = 도구 서버 프로세스 하나(stdio). 서버는 그 사건의 페이지와 조달 물품만 보여 준다.
    에이전트 답(submit_answer) → 코드가 제목·결론을 만들어 돌려줌 → 에이전트가 더블체크(review_title)
    → 객관 신호가 하나라도 실패하면 여러 번 실행(제목 더블체크 없이)하고 일치도를 기록
    → 답안지 조립(코드 계산·확인·처리 결정) → <실행 디렉터리>/<case_id>.json

정답 파일(cases.jsonl)은 읽지 않는다. 합성 데이터는 AgentData(허용 목록)로만 읽는다.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import sys
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from decimal import Decimal
from pathlib import Path
from typing import Any

import anyio
from mcp import Client, StdioServerParameters

import studio_schema
from studio_sheet import assemble
from studio_sheet.data import AgentData
from studio_sheet.evidence import read_log
from studio_sheet.prep import prepare

from .llm import LLM, Usage
from .prompt import PROMPT_VERSION, SYSTEM, first_message

MAX_REQUESTS = 40
MAX_SUBMIT_ERRORS = 3

SUBMIT_TOOL = {
    "name": "submit_answer",
    "description": "판정 답을 낸다. 형식이 틀리면 오류 목록이 돌아온다. 값은 근거 ID(ev-N)로 가리킨다.",
    "input_schema": studio_schema.inline("agent-answer"),
}
REVIEW_TOOL = {
    "name": "review_title",
    "description": "코드가 만든 제목·결론에 동의하는지 답한다. 보충할 사실이 있으면 supplement에 짧게 쓴다.",
    "input_schema": {
        "type": "object", "additionalProperties": False, "required": ["agree", "supplement"],
        "properties": {"agree": {"type": "boolean"}, "supplement": {"type": ["string", "null"], "maxLength": 500}},
    },
}


@dataclass
class Attempt:
    answer: dict[str, Any] | None = None
    title_review: dict[str, Any] | None = None
    usage: Usage = field(default_factory=Usage)
    requests: int = 0
    tool_calls: int = 0
    elapsed_ms: int = 0
    served: set[str] = field(default_factory=set)
    error: str | None = None


def _server_params(data_dir: Path, case_id: str, log_dir: Path, attempt: int) -> StdioServerParameters:
    # 도구 서버에는 API 키 같은 비밀 값을 넘기지 않는다
    env = {k: os.environ[k] for k in ("PATH", "HOME", "LANG", "STUDIO_CALC_BIN") if k in os.environ}
    return StdioServerParameters(command=sys.executable, env=env, args=[
        "-m", "studio_sheet.mcp_server", "--data", str(data_dir), "--case", case_id, "--log-dir", str(log_dir), "--attempt", str(attempt)])


def _tool_result_text(r: Any) -> tuple[str, bool]:
    if r.structured_content is not None:
        sc = r.structured_content
        if isinstance(sc, dict) and set(sc) == {"result"}:
            sc = sc["result"]
        return json.dumps(sc, ensure_ascii=False), bool(r.is_error)
    text = "".join(getattr(c, "text", "") for c in r.content)
    return text, bool(r.is_error)


def _context(data: AgentData, case_id: str, log_dir: Path, attempt: int) -> assemble.Context:
    case = prepare(data, case_id)
    issued, rejected = read_log(log_dir / f"{case_id}.evidence.{attempt}.jsonl")
    calc_log = log_dir / f"{case_id}.calc.{attempt}.jsonl"
    policy_log = log_dir / f"{case_id}.policy.{attempt}.jsonl"
    calls = len(calc_log.read_text(encoding="utf-8").splitlines()) if calc_log.exists() else 0
    policy = any(json.loads(x).get("found") for x in policy_log.read_text(encoding="utf-8").splitlines()) if policy_log.exists() else False
    return assemble.Context(case=case, facts=data.facts(case.item_id), issued=issued, attach_rejections=rejected,
                            calculator_calls=calls, policy_checked=policy)


async def run_attempt(llm: LLM, data: AgentData, case_id: str, log_dir: Path, attempt: int, *, review: bool,
                      server: Any = None) -> Attempt:
    """에이전트 1회 실행. server를 주면(테스트) 그 MCP 서버 객체에 프로세스 안에서 붙는다."""
    out = Attempt()
    t0 = time.monotonic()
    target = server if server is not None else _server_params(data.root, case_id, log_dir, attempt)
    try:
        async with Client(target) as mcp:
            listed = await mcp.list_tools()
            mcp_names = {t.name for t in listed.tools}
            tools = [{"name": t.name, "description": t.description or "", "input_schema": t.input_schema}
                     for t in sorted(listed.tools, key=lambda t: t.name)]
            tools += [SUBMIT_TOOL] + ([REVIEW_TOOL] if review else [])
            system = [{"type": "text", "text": SYSTEM}]
            messages: list[dict[str, Any]] = [{"role": "user", "content": first_message(case_id)}]
            submit_errors = 0
            nudged = False
            while out.requests < MAX_REQUESTS:
                resp = await anyio.to_thread.run_sync(lambda: llm.create(system=system, tools=tools, messages=messages))
                out.requests += 1
                out.usage.add(resp.usage)
                out.served.add(getattr(resp, "model", llm.model))
                if resp.stop_reason in ("refusal", "max_tokens"):
                    out.error = f"stop_reason={resp.stop_reason}"
                    break
                messages.append({"role": "assistant", "content": resp.content})
                uses = [b for b in resp.content if b.type == "tool_use"]
                if not uses:
                    if out.answer is None and not nudged:
                        nudged = True
                        messages.append({"role": "user", "content": "아직 submit_answer로 답을 내지 않았다. 답을 내라."})
                        continue
                    if out.answer is not None and review and out.title_review is None and not nudged:
                        nudged = True
                        messages.append({"role": "user", "content": "review_title로 제목·결론에 대한 동의 여부를 답하라."})
                        continue
                    break
                results = []
                done = False
                for u in uses:
                    out.tool_calls += 1
                    inp = u.input if isinstance(u.input, dict) else json.loads(u.input)
                    if u.name in mcp_names:
                        r = await mcp.call_tool(u.name, inp)
                        text, is_err = _tool_result_text(r)
                    elif u.name == "submit_answer" and out.answer is None:
                        errs = studio_schema.errors("agent-answer", inp)
                        if errs:
                            submit_errors += 1
                            text, is_err = "형식 오류:\n" + "\n".join(errs[:20]), True
                            if submit_errors >= MAX_SUBMIT_ERRORS:
                                out.error = "submit_answer 형식 오류가 반복됐다"
                                done = True
                        else:
                            out.answer = inp
                            if not review:
                                done = True
                                text, is_err = "받았다.", False
                            else:
                                ctx = _context(data, case_id, log_dir, attempt)
                                pv = assemble.preview(inp, ctx)
                                text = json.dumps({"코드가_만든_제목": pv["title"], "결론": pv["conclusion"],
                                                   "코드가_계산한_비교_단가": pv.get("code_benchmark_krw"),
                                                   "안내": "review_title로 동의 여부를 답하라."}, ensure_ascii=False)
                                is_err = False
                    elif u.name == "review_title" and review and out.answer is not None:
                        if isinstance(inp.get("agree"), bool) and (inp.get("supplement") is None or isinstance(inp.get("supplement"), str)):
                            out.title_review = {"agree": inp["agree"], "supplement": inp.get("supplement")}
                            text, is_err = "기록했다. 끝.", False
                            done = True
                        else:
                            text, is_err = "agree(참/거짓)와 supplement(글 또는 null)를 준다.", True
                    else:
                        text, is_err = f"지금은 {u.name}을 쓸 수 없다.", True
                    results.append({"type": "tool_result", "tool_use_id": u.id, "content": text, **({"is_error": True} if is_err else {})})
                if done:
                    break
                messages.append({"role": "user", "content": results})
            else:
                out.error = f"요청 {MAX_REQUESTS}번 안에 끝나지 않았다"
    except Exception as e:  # 실행 하나의 실패가 전체 실행을 멈추지 않게 한다
        out.error = _describe(e)
    out.elapsed_ms = int((time.monotonic() - t0) * 1000)
    if out.answer is None and out.error is None:
        out.error = "답을 내지 않았다"
    return out


# API 오류 메시지에 일부 가린 키가 섞여 오므로 답안지에 남기기 전에 지운다(CLAUDE.md 규칙 2)
_SECRET = re.compile(r"(sk-[A-Za-z0-9_*\-]{6,}|savetax_[A-Za-z0-9_\-]{10,})")


def _describe(e: BaseException) -> str:
    """ExceptionGroup(anyio 작업 그룹)을 풀어 안쪽 예외를 적는다."""
    if isinstance(e, BaseExceptionGroup):
        return "; ".join(_describe(x) for x in e.exceptions)
    msg = _SECRET.sub("[키 가림]", str(e).replace("\n", " "))
    return f"{type(e).__name__}: {msg[:500]}"


def _run_record(llm: LLM, attempts: list[Attempt], started_at: str) -> dict[str, Any]:
    usage = Usage()
    for a in attempts:
        for k in ("tokens_in", "tokens_out", "cache_write", "cache_read"):
            setattr(usage, k, getattr(usage, k) + getattr(a.usage, k))
    served = sorted(set().union(*(a.served for a in attempts)) - {llm.model})
    # 대체 모델이 답한 요청도 설정한 모델 단가로 센다(근사)
    cost = sum((a.usage.cost(llm.model) for a in attempts), start=Decimal(0))
    return {
        "model": llm.model + (f" (대체 응답: {', '.join(served)})" if served else ""),
        "prompt_version": PROMPT_VERSION,
        "runs": len(attempts),
        "tool_calls": sum(a.tool_calls for a in attempts),
        "tokens_in": usage.tokens_in + usage.cache_write,
        "tokens_out": usage.tokens_out,
        "cache_read_tokens": usage.cache_read,
        "cost_usd": format(cost.quantize(studio_schema.usd("0.000001")), "f"),
        "elapsed_ms": sum(a.elapsed_ms for a in attempts),
        "started_at": started_at,
        "error": attempts[0].error if attempts[0].answer is None else None,
    }


async def run_case(llm: LLM, data: AgentData, case_id: str, run_dir: Path, *, repeats: int = 3, server_factory=None) -> dict[str, Any]:
    log_dir = run_dir / "logs"
    log_dir.mkdir(parents=True, exist_ok=True)
    started = datetime.now(timezone.utc).isoformat(timespec="seconds")

    def srv(attempt: int):
        return None if server_factory is None else server_factory(case_id, log_dir, attempt)

    first = await run_attempt(llm, data, case_id, log_dir, 1, review=True, server=srv(1))
    attempts = [first]
    if first.answer is None:
        case = prepare(data, case_id)
        issued, _ = read_log(log_dir / f"{case_id}.evidence.1.jsonl")
        sheet = assemble.failed_sheet(case, error=first.error or "알 수 없음", run=_run_record(llm, attempts, started), issued=issued)
    else:
        ctx = _context(data, case_id, log_dir, 1)
        others: list[dict[str, Any]] = []
        if repeats > 1 and assemble.needs_repeat(first.answer, ctx):
            for n in range(2, repeats + 1):
                a = await run_attempt(llm, data, case_id, log_dir, n, review=False, server=srv(n))
                attempts.append(a)
                if a.answer is not None:
                    others.append(a.answer)
        sheet = assemble.build(first.answer, ctx, title_review=first.title_review, run=_run_record(llm, attempts, started),
                               others=others)
    (run_dir / f"{case_id}.json").write_text(json.dumps(sheet, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    return sheet


def new_run_dir(root: Path, model: str) -> Path:
    rid = f"{datetime.now():%Y%m%d-%H%M%S}-{model.removeprefix('claude-')}"
    d = root / rid
    d.mkdir(parents=True, exist_ok=False)
    return d


async def run_all(llm: LLM, data_dir: Path, run_dir: Path, case_ids: list[str] | None = None, *, repeats: int = 3,
                  concurrency: int = 4, effort: str | None = None, on_done=None) -> dict[str, Any]:
    data = AgentData(data_dir)
    ids = case_ids or [t["case_id"] for t in data.tasks]
    meta = {
        "run_id": run_dir.name, "model": llm.model, "effort": effort, "prompt_version": PROMPT_VERSION, "repeats": repeats,
        "data_dir": str(Path(data_dir).resolve()), "tasks_sha256": hashlib.sha256(data._read("tasks.jsonl")).hexdigest(),
        "cases": ids, "started_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
    }
    (run_dir / "run.json").write_text(json.dumps(meta, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    sem = anyio.Semaphore(concurrency)
    sheets: dict[str, dict[str, Any]] = {}

    async def one(cid: str) -> None:
        async with sem:
            s = await run_case(llm, data, cid, run_dir, repeats=repeats)
            sheets[cid] = s
            if on_done:
                on_done(cid, s)

    async with anyio.create_task_group() as tg:
        for cid in ids:
            tg.start_soon(one, cid)
    meta["finished_at"] = datetime.now(timezone.utc).isoformat(timespec="seconds")
    meta["cost_usd"] = format(sum((studio_schema.usd(s["run"]["cost_usd"]) for s in sheets.values()), start=studio_schema.usd("0")), "f")
    (run_dir / "run.json").write_text(json.dumps(meta, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    return meta
