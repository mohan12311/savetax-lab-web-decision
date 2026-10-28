"""가짜 판매처 페이지 HTML과 참조 본문의 인용 글. 실제 판매처 문구를 베끼지 않고 직접 쓴 템플릿만 쓴다."""

from __future__ import annotations

from html import escape

from .model import Offer

NOTICE = "이 페이지는 savetax-lab 2부 데모를 위해 합성한 것이며 실제 판매처·상품이 아닙니다."


def won(n: int) -> str:
    return f"{n:,}원"


def vat_text(offer: Offer) -> str:
    return "부가세 포함" if offer.vat_included else "부가세 별도"


def page_html(offer: Offer) -> str:
    if offer.render == "spa":
        # JS가 그려야 내용이 나오는 껍데기 — 정적 HTML만 읽는 판정기는 상품·가격을 못 본다
        return (
            "<!doctype html>\n<html lang=\"ko\"><head><meta charset=\"utf-8\">"
            f"<title>{escape(offer.seller)}</title></head>\n"
            "<body><div id=\"app\"></div><script src=\"/static/app.js\"></script></body></html>\n"
        )
    rows = "".join(f"<tr><th>{escape(k)}</th><td>{escape(v)}</td></tr>" for k, v in offer.spec_rows)
    if offer.price_krw is None:
        price = "<div class=\"price\"><strong>가격 문의</strong> <span>견적을 요청하시면 담당자가 회신합니다</span></div>"
    else:
        price = (
            f"<div class=\"price\"><span class=\"label\">판매가</span> <strong>{won(offer.price_krw)}</strong> "
            f"<span class=\"vat\">{vat_text(offer)}</span></div>"
        )
    note = f" ({escape(offer.unit_note)})" if offer.unit_note else ""
    return (
        "<!doctype html>\n<html lang=\"ko\"><head><meta charset=\"utf-8\">"
        f"<title>{escape(offer.title)} | {escape(offer.seller)}</title></head>\n<body>\n"
        f"<header><p class=\"shop\">{escape(offer.seller)}</p><nav>홈 &gt; {escape(offer.category)}</nav></header>\n"
        f"<main>\n<h1 class=\"product-title\">{escape(offer.title)}</h1>\n"
        f"<table class=\"spec\">{rows}</table>\n{price}\n"
        f"<p class=\"unit\">판매 단위: {escape(offer.sale_unit)}{note}</p>\n"
        f"<p class=\"shipping\">배송: {escape(offer.shipping)}</p>\n"
        f"{offer.extra_html}</main>\n"
        f"<footer><p>상호: {escape(offer.seller)} · {NOTICE}</p></footer>\n</body></html>\n"
    )


def quote_text(offer: Offer, observed_at: str) -> str:
    """참조 본문의 인용 글: 페이지에 보이는 상품명·규격·가격·부가세·판매 단위·배송을 그대로 옮긴다."""
    assert offer.price_krw is not None
    spec = ", ".join(f"{k} {v}" for k, v in offer.spec_rows)
    note = f" ({offer.unit_note})" if offer.unit_note else ""
    return (
        f"{offer.seller} 상품 페이지 — {offer.title}. {spec}. 판매가 {won(offer.price_krw)}({vat_text(offer)}). "
        f"판매 단위 {offer.sale_unit}{note}. 배송: {offer.shipping}. {observed_at} 확인."
    )
