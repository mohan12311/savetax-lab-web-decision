"""에이전트 실행기 명령.

    python -m studio_agent run --data studio/demo/synth --out studio/demo/runs [--cases hard-01,hard-04]
        [--model gpt-6.1-terra] [--effort medium] [--repeats 3] [--concurrency 4]

--model: gpt-…(OpenAI, 기본) / claude-…(Anthropic) / rule-baseline(LLM 없음, 비용 0).
실제 API를 부르므로 비용이 든다. API 키는 전용 환경변수 SAVETAX_STUDIO_OPENAI_KEY / SAVETAX_STUDIO_ANTHROPIC_KEY에서만 읽는다.
단가표에 없는 모델은 --price-in/--price-out/--price-cached(1M 토큰당 달러)를 줘야 시작한다(비용 기록을 비우지 않으려고).
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

import anyio

from .baseline import MODEL as BASELINE
from .baseline import BaselineLLM
from decimal import Decimal

from .llm import DEFAULT_MODEL, PRICES, AnthropicLLM
from .openai_llm import OpenAILLM
from .runner import new_run_dir, run_all


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="studio_agent", description="2부 에이전트 실행기")
    sub = ap.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run", help="사건들을 에이전트로 판정해 답안지를 쓴다")
    r.add_argument("--data", type=Path, required=True)
    r.add_argument("--out", type=Path, required=True, help="실행 디렉터리들을 둘 곳(예: studio/demo/runs)")
    r.add_argument("--cases", help="쉼표로 나눈 사건 ID(비우면 전부)")
    r.add_argument("--model", default=DEFAULT_MODEL, help=f"기본 {DEFAULT_MODEL}. {BASELINE}은 LLM 없는 규칙 기준선(비용 0)")
    r.add_argument("--effort", default="medium", help="추론 강도(OpenAI reasoning.effort / Anthropic effort)")
    r.add_argument("--price-in", type=Decimal, help="단가표에 없는 모델의 입력 단가(1M 토큰당 달러)")
    r.add_argument("--price-out", type=Decimal)
    r.add_argument("--price-cached", type=Decimal)
    r.add_argument("--repeats", type=int, default=3, help="객관 신호가 실패한 사건의 총 실행 횟수(1이면 반복 없음)")
    r.add_argument("--concurrency", type=int, default=4)
    r.add_argument("--no-fallback", action="store_true", help="서버 쪽 거절 대체를 끈다")
    a = ap.parse_args(argv)

    if a.price_in is not None and a.price_out is not None:
        PRICES[a.model] = {"in": a.price_in, "out": a.price_out, "cache_read": a.price_cached if a.price_cached is not None else a.price_in}
    if a.model != BASELINE and a.model not in PRICES:
        ap.error(f"{a.model}의 단가를 모른다. --price-in·--price-out(·--price-cached)을 준다. 아는 모델: {', '.join(sorted(PRICES))}")
    if a.model == BASELINE:
        llm = BaselineLLM()
    elif a.model.startswith("claude-"):
        llm = AnthropicLLM(model=a.model, effort=a.effort, fallback=not a.no_fallback)
    else:
        llm = OpenAILLM(model=a.model, effort=a.effort)
    run_dir = new_run_dir(a.out, a.model)
    ids = [c.strip() for c in a.cases.split(",")] if a.cases else None
    print(f"실행 디렉터리: {run_dir}", file=sys.stderr)

    def done(cid, s):
        print(f"{cid:10s} {s['label']:13s} {s['routing']['to']:5s} {s['title']}  ${s['run']['cost_usd']}", file=sys.stderr)

    effort = None if a.model == BASELINE else a.effort
    meta = anyio.run(lambda: run_all(llm, a.data, run_dir, ids, repeats=a.repeats, concurrency=a.concurrency, effort=effort,
                                     on_done=done))
    print(f"끝: {len(meta['cases'])}건, ${meta['cost_usd']} → {run_dir}", file=sys.stderr)
    print(run_dir)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
