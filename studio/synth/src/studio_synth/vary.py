"""v2: 판매 페이지 표기를 다양하게 만든다(같은 사례·같은 정답, 겉모습만 다르다).

v1 페이지는 모두 같은 표("속성 | 값")와 "판매가 N원 부가세 별도" 한 줄이라, 규칙 기준선이 글자 위치만으로 거의 다 맞힌다.
실제 판매 페이지는 표·목록·문장이 섞이고 속성 이름과 가격·부가세·단위 표기가 제각각이다. v2는 그 변형을 시드 고정으로 고른다.

- 정답(라벨·기대 비교 단가·차이 속성)은 바꾸지 않는다. 겉모습만 바꾼다.
- 사례의 난수(catalog)와 겉모습의 난수를 따로 둔다. 그래서 v1과 v2는 같은 사례 목록을 쓴다.
- 문구는 직접 쓴 것이고 실제 판매처 문구를 베끼지 않는다.
"""

from __future__ import annotations

import random
from html import escape

from .model import Offer
from .render import NOTICE, won

LABELS = {
    "규격": ["규격", "사이즈", "치수", "제품 크기"],
    "종류": ["종류", "제품 구분", "품목"],
    "두께": ["두께", "두께(T)", "높이"],
    "모델": ["모델", "모델명", "품번", "제품 코드"],
    "재질": ["재질", "소재"],
    "화면 크기": ["화면 크기", "화면", "대각 길이"],
    "길이": ["길이", "전장"],
}
NOISE = ["오늘 주문 시 내일 출고(영업일 기준)", "상품 문의 0건", "구매 후기 12건", "대량 구매는 고객센터로 문의하세요",
         "이미지는 이해를 돕기 위한 것으로 실제와 다를 수 있습니다", "교환·반품 안내는 하단을 참고하세요"]


def pick_style(seed: int, case_id: str) -> dict:
    """사례마다 겉모습을 고른다(사례 난수와 독립)."""
    r = random.Random(f"{seed}:v2:{case_id}")
    return {
        "layout": r.choice(["table", "dl", "prose"]),
        "labels": {k: r.choice(v) for k, v in LABELS.items()},
        "price": r.randrange(4),
        "unit": r.randrange(3),
        "price_first": r.random() < 0.4,
        "noise": r.sample(NOISE, r.randint(1, 3)),
        "model_in_title": r.random() < 0.5,
    }


def _vat_words(offer: Offer, k: int) -> str:
    if offer.vat_included is None:
        return ""
    if offer.vat_included:
        return ["부가세 포함", "VAT 포함", "부가세 포함 금액", "VAT 포함가"][k]
    return ["부가세 별도", "VAT 별도", "부가세 별도 금액", "VAT 미포함"][k]


def _price_lines(offer: Offer, k: int) -> list[str]:
    if offer.price_krw is None:
        return ["<div class=\"price\"><strong>가격 문의</strong> <span>견적을 요청하시면 담당자가 회신합니다</span></div>"]
    v = _vat_words(offer, k)
    p = offer.price_krw
    if offer.vat_included is None:  # 부가세 표기가 없는 페이지
        return [f"<div class=\"price\">판매가 <strong>{won(p)}</strong></div>"]
    if k == 0:
        return [f"<div class=\"price\"><span class=\"label\">판매가</span> <strong>{won(p)}</strong> <span class=\"vat\">{v}</span></div>"]
    if k == 1:
        return [f"<div class=\"price\"><strong>₩{p:,}</strong> <small>({v})</small></div>"]
    if k == 2:
        return [f"<div class=\"price\">가격 <strong>{won(p)}</strong></div>", f"<p class=\"note\">※ 표시 가격은 {v}입니다.</p>"]
    return [f"<div class=\"price\"><b>{won(p)}</b> / {v}</div>"]


def _unit_line(offer: Offer, k: int) -> str:
    u, note = offer.sale_unit, offer.unit_note
    if not note:
        return [f"<p class=\"unit\">판매 단위: {escape(u)}</p>", f"<p class=\"unit\">{escape(u)}당 가격</p>",
                f"<p class=\"unit\">단위 {escape(u)}</p>"][k]
    return [f"<p class=\"unit\">판매 단위: {escape(u)} ({escape(note)})</p>",
            f"<p class=\"unit\">1{escape(u)} 가격입니다. ({escape(note)})</p>",
            f"<p class=\"unit\">수량 단위 {escape(u)} · {escape(note)}</p>"][k]


def _spec(offer: Offer, style: dict) -> str:
    rows = [(style["labels"].get(k, k), v) for k, v in offer.spec_rows]
    if style["layout"] == "table":
        return "<table class=\"spec\">" + "".join(f"<tr><th>{escape(k)}</th><td>{escape(v)}</td></tr>" for k, v in rows) + "</table>"
    if style["layout"] == "dl":
        return "<dl class=\"spec\">" + "".join(f"<dt>{escape(k)}</dt><dd>{escape(v)}</dd>" for k, v in rows) + "</dl>"
    parts = ", ".join(f"{escape(k)} {escape(v)}" for k, v in rows)
    return f"<p class=\"desc\">이 상품의 사양은 다음과 같습니다: {parts}.</p>"


def page_html_v2(offer: Offer, style: dict) -> str:
    if offer.render == "spa":
        return ("<!doctype html>\n<html lang=\"ko\"><head><meta charset=\"utf-8\">"
                f"<title>{escape(offer.seller)}</title></head>\n"
                "<body><div id=\"root\"></div><script src=\"/static/bundle.js\"></script></body></html>\n")
    model = dict(offer.spec_rows).get("모델")
    title = offer.title
    if model and style["model_in_title"] and model not in title:
        title = f"{title} [{model}]"
    spec = _spec(offer, style)
    price = "\n".join(_price_lines(offer, style["price"]))
    body = [price, spec] if style["price_first"] else [spec, price]
    noise = "".join(f"<li>{escape(n)}</li>" for n in style["noise"])
    return (
        "<!doctype html>\n<html lang=\"ko\"><head><meta charset=\"utf-8\">"
        f"<title>{escape(title)} - {escape(offer.seller)}</title></head>\n<body>\n"
        f"<header><a class=\"logo\">{escape(offer.seller)}</a><nav>{escape(offer.category)}</nav></header>\n"
        f"<main>\n<h2>{escape(title)}</h2>\n" + "\n".join(body) + "\n"
        f"{_unit_line(offer, style['unit'])}\n"
        f"<p class=\"delivery\">배송 안내: {escape(offer.shipping)}</p>\n"
        f"<ul class=\"info\">{noise}</ul>\n"
        f"{offer.extra_html}</main>\n"
        f"<footer><p>{escape(offer.seller)} | {NOTICE}</p></footer>\n</body></html>\n"
    )
