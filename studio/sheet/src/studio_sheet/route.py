"""처리 규칙(답안지 명세 7절). 위에서부터 확인하고, 걸리는 이유를 모두 기록한다(순서는 결과를 바꾸지 않는다)."""

from __future__ import annotations

from typing import Any


def decide(sheet: dict[str, Any]) -> dict[str, Any]:
    because: list[dict[str, Any]] = []

    def add(rule: int, text: str) -> None:
        because.append({"rule": rule, "text": text})

    ch = sheet["checks"]
    if any(w["level"] == "danger" for w in sheet["warnings"]):
        add(1, "AI에게 하는 말이 있다(danger)")
    if not ch["has_required_list"]:
        add(2, "필수 속성 목록이 없는 품목이다")
    if sheet["label"] == "hold":
        add(3, "라벨이 판단 보류다")
    unfolded = [r["attribute"] for r in sheet["rows"] if not r["folded"]]
    if not sheet["price"]["check"]["folded"]:
        unfolded.append("가격")
    if unfolded:
        add(4, "펼쳐진 줄이 있다: " + ", ".join(unfolded))
    if ch["vat_flip"] and not ch["vat_policy_checked"]:
        add(5, "부가세 가정에 따라 결론이 바뀌는데 사이트 정책을 확인하지 못했다")
    tr = sheet["title_review"]
    if tr is None or not tr["agree"]:
        add(6, "LLM이 제목·결론에 동의하지 않았다" if tr else "제목·결론 더블체크가 없다")
    if sheet["conclusion"] == "n_times":
        add(7, "결론이 시중가 대비 N배다")
    if ch["missing_required"]:
        add(8, "필수 속성의 줄이 없다: " + ", ".join(ch["missing_required"]))
    if ch["assertive"]:
        add(9, "단정 표현이 있다: " + ", ".join(sorted({a["phrase"] for a in ch["assertive"]})))
    if because:
        return {"to": "human", "because": because}
    return {"to": "auto", "because": [{"rule": 10, "text": "모든 줄이 접히고 결론이 지적할 것 없음이다"}]}
