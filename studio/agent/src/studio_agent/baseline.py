"""규칙 기반 기준선(LLM 없음, 비용 0). LLM 프로토콜을 흉내 내 같은 MCP 도구를 같은 순서로 부른다.

목적: (1) API 없이 실행기·채점기·웹 화면 전체 흐름을 돌린다. (2) 점수표에서 LLM 에이전트와 비교할 바닥선이다.
페이지 구조에 기대는 단순한 규칙("속성 이름 | 값" 표, "판매가 N원" 줄)만 쓴다. 정답 파일은 읽지 않는다.
일부러 똑똑하게 만들지 않는다: 부속품·리퍼비시·허용 오차·숨은 지시문 같은 판단은 못 하는 것이 기준선의 몫이다.
"""

from __future__ import annotations

import json
import re
from itertools import count
from typing import Any

MODEL = "rule-baseline"


class _B:
    def __init__(self, **kw: Any):
        self.__dict__.update(kw)


_ids = count(1)


def _resp(calls: list[tuple[str, dict]]) -> _B:
    content = [_B(type="tool_use", id=f"bl{next(_ids)}", name=n, input=i) for n, i in calls] or [_B(type="text", text="끝")]
    return _B(content=content, stop_reason="tool_use" if calls else "end_turn", model=MODEL,
              usage=_B(input_tokens=0, output_tokens=0, cache_creation_input_tokens=0, cache_read_input_tokens=0))


def _results(messages: list[dict]) -> list[Any]:
    last = messages[-1]["content"]
    out = []
    for c in last if isinstance(last, list) else []:
        try:
            out.append(json.loads(c["content"]))
        except (ValueError, TypeError):
            out.append(c["content"])
    return out


def _policy(tool_names: set[str]):
    item, page = yield [("get_procurement_item", {}), ("get_page_lines", {})]
    lines = page["lines"]
    if not lines:
        yield [("submit_answer", {"label": "insufficient", "rows": [], "price": {"read": None, "llm": None},
                                  "reason": "정적 HTML에 상품 정보가 없다.", "hold_reason": None, "hold_rows": []})]
        if "review_title" in tool_names:
            yield [("review_title", {"agree": True, "supplement": None})]
        return

    table = {}
    for ln in lines:
        if " | " in ln["text"]:
            k, v = ln["text"].split(" | ", 1)
            table[k.strip()] = (v.strip(), ln["line"])
    want: list[tuple[str, str, int | None, str | None]] = []  # (속성, 판매 값, 줄, 조달 값)
    names = [a["name"] for a in item["required_attributes"] or []]
    proc_text = " ".join(str(item[k]) for k in ("name", "model"))
    for attr in names:
        key = "모델" if attr == "모델" else attr
        if key in table:
            v, line = table[key]
            if attr == "모델":
                want.append((attr, v, line, item["model"]))
            else:
                token = v.split()[0] if attr == "종류" else v
                pv = token if token in proc_text else None
                m = re.fullmatch(r"(\d+)T", token)
                if m and f"{m.group(1)}mm" in proc_text:
                    pv = f"{m.group(1)}mm"  # 60T = 60mm
                want.append((attr, token, line, pv))
        else:
            want.append((attr, "", None, item["model"] if attr == "모델" else None))

    price_ln = next((ln for ln in lines if ln["text"].startswith("판매가")), None)
    unit_ln = next((ln for ln in lines if ln["text"].startswith("판매 단위")), None)
    calls = []
    for attr, v, line, pv in want:
        if pv:
            calls.append(("attach_evidence", {"source": "procurement", "text": pv}))
        if line is not None and v:
            calls.append(("attach_evidence", {"source": "page", "text": v, "line": line}))
    price_m = re.search(r"([\d,]+)원", price_ln["text"]) if price_ln else None
    vat_word = None
    if price_ln:
        vat_word = "부가세 별도" if "별도" in price_ln["text"] else "부가세 포함" if "포함" in price_ln["text"] else None
        if price_m:
            calls.append(("attach_evidence", {"source": "page", "text": price_m.group(0), "line": price_ln["line"]}))
        if vat_word:
            calls.append(("attach_evidence", {"source": "page", "text": vat_word, "line": price_ln["line"]}))
    unit, conv, conv_text = None, "1", None
    if unit_ln:
        unit = unit_ln["text"].split(":", 1)[1].strip().split(" ")[0]
        calls.append(("attach_evidence", {"source": "page", "text": f"판매 단위: {unit}", "line": unit_ln["line"]}))
        m1 = re.search(r"(\d+)장/㎡", unit_ln["text"])
        m2 = re.search(r"1\S+ = (\d+)㎡", unit_ln["text"])
        if m1:
            conv, conv_text = f"1/{m1.group(1)}", m1.group(0)
        elif m2:
            conv, conv_text = m2.group(1), m2.group(0)
        if conv_text:
            calls.append(("attach_evidence", {"source": "page", "text": conv_text, "line": unit_ln["line"]}))
    evs = yield calls
    it = iter(evs)
    rows = []
    for attr, v, line, pv in want:
        pid = next(it)["id"] if pv else None
        sid = next(it)["id"] if (line is not None and v) else None
        if sid is None:
            result = "not_on_page"
        elif pv is None:
            result = "different"
        else:
            result = "same" if v == pv or re.fullmatch(r"(\d+)T", v) and pv == v[:-1] + "mm" else "different"
        rows.append({"attribute": attr, "procurement": pid, "seller": sid, "result": result, "confidence": 0.5})
    price_ev = next(it)["id"] if price_m else None
    vat_ev = next(it)["id"] if vat_word else None
    unit_ev = next(it)["id"] if unit_ln else None
    conv_ev = next(it)["id"] if conv_text else None

    if any(r["result"] == "different" for r in rows):
        label = "different"
    elif any(r["attribute"] == "모델" and r["result"] == "same" for r in rows):
        label = "same_model"
    else:
        label = "equivalent"
    read, llm = None, None
    if price_m:
        price = price_m.group(1).replace(",", "")
        read = {"sale_price": {"value": price, "ev": price_ev},
                "vat": {"value": "excluded" if vat_word == "부가세 별도" else "included" if vat_word else "not_stated", "ev": vat_ev},
                "sale_unit": {"value": unit, "ev": unit_ev}, "conversion": {"value": conv, "ev": conv_ev}}
        if label != "different":
            div = "" if conv == "1" else f" ÷ ({conv})"
            vat = " × 1.1" if vat_word == "부가세 별도" else ""
            (calc,) = yield [("calculate", {"expr": f"ceil({price}{div}{vat})"})]
            llm = {"formula": f"ceil({price}{div}{vat})", "benchmark_krw": calc["value"]}
    elif label != "different":
        label = "insufficient"
    reason = "표의 속성 값을 글자 그대로 비교했다(규칙 기준선)."
    yield [("submit_answer", {"label": label, "rows": rows, "price": {"read": read, "llm": llm}, "reason": reason,
                              "hold_reason": None, "hold_rows": []})]
    if "review_title" in tool_names:
        yield [("review_title", {"agree": True, "supplement": None})]


class BaselineLLM:
    model = MODEL

    def __init__(self) -> None:
        self._gens: dict[int, Any] = {}

    def create(self, *, system: list[dict], tools: list[dict], messages: list[dict]) -> _B:
        key = id(messages)
        if len(messages) == 1:
            self._gens[key] = _policy({t["name"] for t in tools})
            return _resp(next(self._gens[key]))
        try:
            return _resp(self._gens[key].send(_results(messages)))
        except StopIteration:
            self._gens.pop(key, None)
            return _resp([])
