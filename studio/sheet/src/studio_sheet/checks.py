"""코드의 확인(답안지 명세 6절): 줄마다의 객관 신호와 사건 단위 확인. 정답 없이 돌아가므로 실데이터에도 쓴다."""

from __future__ import annotations

import math
import re
import unicodedata
from fractions import Fraction
from typing import Any

from studio_schema import units as parse_units

from . import calc
from .attrs import IDENTITY

NORM_RULE = "norm-v1"

# 단정 표현(CLAUDE.md 규칙 6). 기관이나 공급사가 잘못했다고 단정하는 말.
ASSERTIVE = ["부당", "과다 지급", "과다지급", "과다 청구", "과다청구", "폭리", "착복", "횡령", "비리", "바가지", "혈세", "세금 낭비",
             "예산 낭비", "부정 납품", "담합", "사기"]

_MUL = re.compile(r"[×xX*✕✖]")
_T = re.compile(r"(\d)t\b")
_NUM = re.compile(r"\d[\d,]*")


def norm(s: str) -> str:
    """표기 맞추기(norm-v1): 전각·호환 문자 풀기, 소문자, 공백·쉼표 빼기, 곱하기 기호 통일, 60T = 60mm."""
    s = unicodedata.normalize("NFKC", s).lower()
    s = _MUL.sub("x", s)
    s = re.sub(r"[\s,]", "", s)
    s = _T.sub(r"\1mm", s + " ").strip()
    return s


def _ev(issued: dict[str, dict], eid: str | None, want: str, label: str, signals: list[str], unissued: list[str]) -> dict | None:
    if eid is None:
        return None
    ev = issued.get(eid)
    if ev is None:
        signals.append(f"evidence_unissued: {label} 근거 {eid}는 근거 첨부 도구가 발급하지 않았다")
        unissued.append(eid)
        return None
    if ev["source"] != want:
        signals.append(f"evidence_source: {label} 근거 {eid}의 출처가 {ev['source']}다({want}여야 한다)")
    return ev


def check_row(row: dict, issued: dict[str, dict], unissued: list[str], hidden: list[str]) -> tuple[bool, list[str]]:
    sig: list[str] = []
    pe = _ev(issued, row["procurement"], "procurement", "조달 값", sig, unissued)
    se = _ev(issued, row["seller"], "page", "판매 값", sig, unissued)
    if se is not None and se.get("hidden"):
        sig.append(f"hidden_evidence: 판매 값 근거 {row['seller']}는 화면에 안 보이는 줄이다")
        hidden.append(row["seller"])
    res = row["result"]
    if res == "not_on_page":
        if row["seller"] is not None:
            sig.append("value_compare: 페이지에 없다고 했는데 판매 값 근거가 있다")
    elif row["attribute"] in IDENTITY:
        # 구성·상태: 판매 페이지 근거만 요구한다(글자 비교 없음). "같음"이 틀려도 N배 결론은 늘 사람이 본다(처리 규칙 7)
        if row["seller"] is None:
            sig.append("value_compare: 판매 값 근거가 없다(페이지에 없으면 not_on_page)")
        if res == "within_tolerance":
            sig.append("judgment: 허용 오차 안은 사람이 확인한다")
    else:
        if row["procurement"] is None:
            sig.append("value_compare: 조달 값 근거가 없다")
        if row["seller"] is None:
            sig.append("value_compare: 판매 값 근거가 없다(페이지에 없으면 not_on_page)")
        if pe is not None and se is not None:
            same = norm(pe["text"]) == norm(se["text"])
            if res == "same" and not same:
                sig.append(f"value_compare: 값이 다른데 같음이라고 했다({pe['text']} ↔ {se['text']})")
            elif res == "different" and same:
                sig.append(f"value_compare: 값이 같은데 다름이라고 했다({pe['text']} ↔ {se['text']})")
            elif res == "within_tolerance":
                sig.append(f"judgment: 허용 오차 안은 사람이 확인한다({pe['text']} ↔ {se['text']})")
    return not sig, sig


def _digits(text: str) -> set[str]:
    return {m.replace(",", "") for m in _NUM.findall(text)}


def check_read(read: dict, issued: dict[str, dict], procurement_unit: str, unissued: list[str], hidden: list[str]) -> list[str]:
    sig: list[str] = []
    evs = {}
    for k in ("sale_price", "vat", "sale_unit", "conversion"):
        # 환산은 "같은 단위라 1"처럼 조달 물품의 단위가 근거일 수 있다(11/05 Sol 실행에서 찾은 빈틈)
        want = "procurement" if k == "conversion" and read[k]["ev"] and issued.get(read[k]["ev"], {}).get("source") == "procurement" else "page"
        evs[k] = _ev(issued, read[k]["ev"], want, f"읽은 값 {k}", sig, unissued)
        if evs[k] is not None and evs[k].get("hidden"):
            sig.append(f"hidden_evidence: 읽은 값 {k}의 근거 {read[k]['ev']}는 화면에 안 보이는 줄이다")
            hidden.append(read[k]["ev"])
    price = read["sale_price"]["value"]
    if price is not None:
        if read["sale_price"]["ev"] is None:
            sig.append("read_value: 판매가 근거가 없다")
        elif evs["sale_price"] is not None and price not in _digits(evs["sale_price"]["text"]):
            sig.append(f"read_value: 판매가 {price}가 근거 글자에 없다({evs['sale_price']['text']})")
    vat, vev = read["vat"]["value"], evs["vat"]
    if vat == "not_stated":
        if read["vat"]["ev"] is not None:
            sig.append("read_value: 부가세 표기가 없다고 했는데 근거가 있다")
    elif read["vat"]["ev"] is None:
        sig.append("read_value: 부가세 표기 근거가 없다")
    elif vev is not None:
        t = vev["text"].replace(" ", "")
        if vat == "excluded" and not ("별도" in t or "미포함" in t):
            sig.append(f"read_value: 부가세 별도라고 했는데 근거에 '별도'가 없다({vev['text']})")
        if vat == "included" and ("포함" not in t or "미포함" in t):
            sig.append(f"read_value: 부가세 포함이라고 했는데 근거에 '포함'이 없다({vev['text']})")
    unit, uev = read["sale_unit"]["value"], evs["sale_unit"]
    if unit is not None and uev is not None and unit not in uev["text"]:
        sig.append(f"read_value: 판매 단위 {unit}가 근거 글자에 없다({uev['text']})")
    conv = read["conversion"]["value"]
    try:
        conv_v = parse_units(conv)
    except ValueError as e:
        sig.append(f"read_value: {e}")
        conv_v = None
    if unit is not None and conv_v is not None:
        same_unit = norm(unit) == norm(procurement_unit)
        if same_unit and conv_v != 1:
            sig.append(f"unit: 판매 단위가 조달 단위({procurement_unit})와 같은데 환산이 {conv}다")
        if not same_unit and conv_v == 1:
            sig.append(f"unit: 판매 단위 {unit}가 조달 단위 {procurement_unit}와 다른데 환산이 1이다")
        if not same_unit and read["conversion"]["ev"] is None:
            sig.append("unit: 단위 환산의 근거가 없다")
    return sig


def check_formula(llm: dict | None, code_b: int | None) -> tuple[str, str | None, list[str], int]:
    """(상태, 다시 계산한 값, 신호, 계산기 호출 수)."""
    if llm is None:
        return "no_formula", None, ["formula: 계산식이 없다"], 0
    r = calc.evaluate(llm["formula"])
    if not r.get("ok"):
        return "error", None, [f"formula: 식을 계산하지 못했다({r.get('error')})"], 1
    value = Fraction(r["fraction"])
    up = math.ceil(value)
    stated = int(llm["benchmark_krw"])
    if code_b is None:
        return "error", r["value"], ["formula: 코드가 비교 단가를 계산하지 못했다"], 1
    if up != code_b:
        return "understanding_diff", r["value"], [f"understanding_diff: 식의 값 {r['value']}(올림 {up})이 코드 계산 {code_b}와 다르다"], 1
    if stated != code_b:
        return "arithmetic_error", r["value"], [f"arithmetic_error: 식은 맞지만 적은 비교 단가 {stated}가 {code_b}와 다르다"], 1
    return "match", r["value"], [], 1


_PRICE_TEXT = re.compile(r"(\d[\d,]*\s*원|₩\s*\d)")
_SHIPPING = re.compile(r"(배송|택배|운반|화물|배달)")


def price_text_lines(lines) -> list[int]:
    """가격 꼴 글이 있는 줄(배송 안내 줄은 뺀다). 값을 뽑지 않고 "읽을 거리가 있었나"만 본다(처리 규칙 11)."""
    return [ln.line for ln in lines if _PRICE_TEXT.search(ln.text) and not _SHIPPING.search(ln.text)]


def assertive(texts: dict[str, str | None]) -> list[dict[str, str]]:
    out = []
    for field, t in texts.items():
        if not t:
            continue
        for phrase in ASSERTIVE:
            if phrase in t:
                out.append({"field": field, "phrase": phrase})
    return out
