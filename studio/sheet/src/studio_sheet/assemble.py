"""에이전트 답 + 코드 계산 + 확인 + 처리 결정 → 답안지(answer-sheet.schema.json).

실행기가 부른다. 에이전트가 낸 부분(agent-answer.schema.json)은 바꾸지 않고, 코드가 채우는 칸을 더한다.
"""

from __future__ import annotations

from collections import Counter
from dataclasses import dataclass
from typing import Any

import studio_schema

from . import calc, checks, route
from .lines import SPLIT_RULE
from .prep import CaseInput

POSITIVE = ("same_model", "equivalent")


@dataclass
class Context:
    case: CaseInput
    facts: list[dict[str, Any]]
    issued: dict[str, dict[str, Any]]
    attach_rejections: int = 0
    calculator_calls: int = 0
    policy_checked: bool = False


@dataclass
class CodePart:
    code: dict[str, Any] | None
    vat_flip: bool = False
    error: str | None = None


def code_part(answer: dict[str, Any], ctx: Context) -> CodePart:
    """라벨이 같은 물건이고 판매가를 읽었으면 비교 단가·배율·가격 차이 기준을 코드로 계산한다."""
    read = answer["price"]["read"]
    if answer["label"] not in POSITIVE or read is None or read["sale_price"]["value"] is None:
        return CodePart(None)
    price = studio_schema.krw(read["sale_price"]["value"])
    conv = read["conversion"]["value"]
    vat = read["vat"]["value"]
    try:
        if vat == "not_stated":
            b, alt, assumption = calc.benchmark(price, "included", conv), calc.benchmark(price, "excluded", conv), "assumed_included"
        else:
            b, alt, assumption = calc.benchmark(price, vat, conv), None, "stated"
        g = calc.gap(ctx.facts, b)
        g_alt = calc.gap(ctx.facts, alt)["gap_pass"] if alt is not None else None
    except calc.CalcError as e:
        return CodePart(None, error=str(e))
    code = {
        "benchmark_krw": str(b), "benchmark_alt_krw": None if alt is None else str(alt), "vat_assumption": assumption,
        "p_krw": g["p_krw"], "ratio": g["ratio"], "gap_pass": g["gap_pass"], "gap_pass_alt": g_alt, "amount_krw": g["amount_krw"],
    }
    return CodePart(code, vat_flip=g_alt is not None and g_alt != g["gap_pass"])


def conclusion_title(label: str, code: dict[str, Any] | None, category: str) -> tuple[str, str]:
    """제목·결론의 코드 틀(답안지 명세 4절). 1부 형식의 사실 서술만 쓴다. 배율은 소수 첫째 자리 내림."""
    if label == "hold":
        return "hold", "판단 보류"
    if label in POSITIVE and code is not None and code["gap_pass"]:
        kind = "같은 모델의" if label == "same_model" else f"동급 {category}"
        return "n_times", f"{kind} 시중가 대비 {code['ratio']}배"
    if label == "different":
        return "nothing_to_flag", "지적할 것 없음(다른 물건)"
    if label == "insufficient":
        return "nothing_to_flag", "지적할 것 없음(근거 부족)"
    if code is None:
        return "nothing_to_flag", "지적할 것 없음(가격을 확인하지 못함)"
    return "nothing_to_flag", f"지적할 것 없음(가격 차이 기준 미달, 시중가 대비 {code['ratio']}배)"


def preview(answer: dict[str, Any], ctx: Context) -> dict[str, Any]:
    """제목 더블체크 단계에서 에이전트에게 보여 줄 것. 납품 단가 P는 넣지 않는다."""
    cp = code_part(answer, ctx)
    conclusion, title = conclusion_title(answer["label"], cp.code, ctx.case.procurement["category"])
    out: dict[str, Any] = {"conclusion": conclusion, "title": title}
    if cp.code is not None:
        out["code_benchmark_krw"] = cp.code["benchmark_krw"]
    if cp.error:
        out["code_error"] = cp.error
    return out


def _agreement(first: dict[str, Any], others: list[dict[str, Any]]) -> tuple[dict[str, Any] | None, dict[str, dict[str, int]]]:
    if not others:
        return None, {}
    runs = [first, *others]
    labels = Counter(a["label"] for a in runs)
    per_row = {}
    for r in first["rows"]:
        agree = sum(1 for a in runs if any(x["attribute"] == r["attribute"] and x["result"] == r["result"] for x in a["rows"]))
        per_row[r["attribute"]] = {"total": len(runs), "agree": agree}
    return {"total": len(runs), "labels": dict(sorted(labels.items()))}, per_row


def build(answer: dict[str, Any], ctx: Context, *, title_review: dict[str, Any] | None, run: dict[str, Any],
          others: list[dict[str, Any]] | None = None) -> dict[str, Any]:
    studio_schema.check("agent-answer", answer)
    case = ctx.case
    issued = ctx.issued
    unissued: list[str] = []
    hidden: list[str] = []
    req_names = [a.name for a in case.required] if case.required is not None else []

    repeats, per_row = _agreement(answer, others or [])
    rows = []
    for r in answer["rows"]:
        ok, sig = checks.check_row(r, issued, unissued, hidden)
        rows.append({**r, "required": r["attribute"] in req_names, "check": {"pass": ok, "signals": sig},
                     "runs": per_row.get(r["attribute"]), "folded": ok})

    cp = code_part(answer, ctx)
    read, llm = answer["price"]["read"], answer["price"]["llm"]
    psig: list[str] = []
    recomputed = None
    if answer["label"] not in POSITIVE:
        status = "not_applicable"
        if read is not None:
            psig += checks.check_read(read, issued, case.procurement["unit"], unissued, hidden)
    elif read is None or read["sale_price"]["value"] is None:
        status = "not_applicable"
        psig.append("price: 같은 물건이라고 했는데 판매가를 읽지 않았다")
    else:
        psig += checks.check_read(read, issued, case.procurement["unit"], unissued, hidden)
        if cp.error:
            psig.append(f"code_calc: {cp.error}")
        status, recomputed, fsig, _ = checks.check_formula(llm, None if cp.code is None else int(cp.code["benchmark_krw"]))
        psig += fsig
    price_ok = not psig and status in ("match", "not_applicable")
    price = {"read": read, "llm": llm, "code": cp.code,
             "check": {"status": status, "recomputed": recomputed, "pass": price_ok, "signals": psig, "folded": price_ok}}

    warnings = list(case.warnings)
    for eid in dict.fromkeys(hidden):
        ev = issued.get(eid, {})
        warnings.append({"level": "caution", "kind": "hidden_evidence", "line": ev.get("line"), "text": f"{eid}: {ev.get('text', '')}"})

    present = {r["attribute"] for r in answer["rows"]}
    texts = {"reason": answer["reason"], "hold_reason": answer["hold_reason"],
             "title_review.supplement": title_review["supplement"] if title_review else None}
    conclusion, title = conclusion_title(answer["label"], cp.code, case.procurement["category"])
    sheet = {
        "schema_version": studio_schema.SCHEMA_VERSION,
        "case_id": case.case_id, "item_id": case.item_id, "page_url": case.page_url, "read_mode": case.read_mode,
        "label": answer["label"], "rows": rows, "price": price,
        "evidence": {k: issued[k] for k in sorted(issued, key=lambda x: int(x.split("-")[1]))},
        "warnings": warnings,
        "checks": {
            "unissued_evidence": sorted(set(unissued)), "missing_required": [n for n in req_names if n not in present],
            "assertive": checks.assertive(texts), "hidden_evidence": sorted(set(hidden)), "vat_flip": cp.vat_flip,
            "vat_policy_checked": ctx.policy_checked, "attach_rejections": ctx.attach_rejections,
            "calculator_calls": ctx.calculator_calls, "has_required_list": case.required is not None,
            # 다른 물건·판단 보류는 가격이 결론에 쓰이지 않으므로 보지 않는다
            "unread_price_text": answer["label"] in ("insufficient", *POSITIVE) and bool(checks.price_text_lines(case.agent_lines))
                                 and (read is None or read["sale_price"]["value"] is None),
        },
        "conclusion": conclusion, "title": title, "title_review": title_review,
        "reason": answer["reason"], "hold_reason": answer["hold_reason"], "hold_rows": answer["hold_rows"],
        "routing": {"to": "human", "because": []},
        "repeats": repeats,
        "run": {"split_rule": SPLIT_RULE, "source_sha256": case.source_sha256, "category": case.procurement["category"], **run},
    }
    sheet["routing"] = route.decide(sheet)
    return studio_schema.check("answer-sheet", sheet)


def needs_repeat(answer: dict[str, Any], ctx: Context) -> bool:
    """통과 못 한 줄이 하나라도 있으면 여러 번 실행한다(답안지 명세 2절). 제목 더블체크 전 답만으로 판단한다."""
    probe = build(answer, ctx, title_review={"agree": True, "supplement": None},
                  run={"model": "-", "prompt_version": "-", "runs": 1, "tool_calls": 0, "tokens_in": 0, "tokens_out": 0,
                       "cost_usd": "0", "elapsed_ms": 0, "started_at": "-", "error": None})
    return any(not r["folded"] for r in probe["rows"]) or not probe["price"]["check"]["folded"]


def failed_sheet(case: CaseInput, *, error: str, run: dict[str, Any], issued: dict[str, dict[str, Any]] | None = None) -> dict[str, Any]:
    """에이전트가 답을 내지 못했을 때: 판단 보류로 사람에게 보낸다."""
    answer = {"label": "hold", "rows": [], "price": {"read": None, "llm": None}, "reason": "에이전트가 답을 내지 못했다.",
              "hold_reason": f"실행 실패: {error}", "hold_rows": []}
    ctx = Context(case=case, facts=[], issued=issued or {})
    return build(answer, ctx, title_review=None, run={**run, "error": error})


__all__ = ["Context", "build", "preview", "needs_repeat", "failed_sheet", "conclusion_title", "code_part"]
