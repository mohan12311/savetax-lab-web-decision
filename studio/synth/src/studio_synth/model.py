"""합성 데이터의 구성 요소. 금액은 모두 정수 원(int)이다 — float를 쓰지 않는다."""

from __future__ import annotations

from dataclasses import dataclass, field
from fractions import Fraction
from math import ceil

# 정답 라벨
SAME_MODEL = "same_model"  # 같은 모델(모델 코드·규격 일치)
EQUIVALENT = "equivalent"  # 동급(같은 종류·규격 허용 오차 안, 모델 코드는 다름)
DIFFERENT = "different"  # 다른 물건(핵심 속성이 다름)
INSUFFICIENT = "insufficient"  # 근거 부족(페이지를 읽을 수 없거나 가격이 없음)
LABELS = (SAME_MODEL, EQUIVALENT, DIFFERENT, INSUFFICIENT)

# 차이의 종류(채점 명세 2절·3.6절). 속성 이름은 답안지의 품목별 필수 속성 목록(studio/sheet)과 같은 어휘다.
D_DIFFERENT = "different"  # 다름
D_TOLERANCE = "within_tolerance"  # 허용 오차 안
D_NOT_ON_PAGE = "not_on_page"  # 페이지에 없음
POSITIVE = (SAME_MODEL, EQUIVALENT)


@dataclass
class Offer:
    """가짜 판매처의 상품 페이지 하나."""

    sku: str
    title: str
    category: str
    spec_rows: list[tuple[str, str]]
    price_krw: int | None  # None이면 가격이 없다(견적 문의)
    vat_included: bool | None
    sale_unit: str  # 판매 단위(㎡·장·파렛트·본·개·대)
    units_per_fact_unit: Fraction = Fraction(1)  # 판매 단위 1개가 조달 단위 몇 개인가(장 → ㎡이면 1/25)
    unit_note: str = ""
    shipping: str = "배송비 별도(화물)"
    shipping_included: bool = False
    seller: str = "(가상) 바른자재몰"
    render: str = "static"  # static | spa
    extra_html: str = ""  # 숨은 문구(프롬프트 주입 시험) 등

    @property
    def path(self) -> str:
        return f"/item/{self.sku}"


@dataclass
class Product:
    """조달 물품 하나(대회 items/{item_id}의 모양으로 내보낸다)."""

    item_id: str
    name: str
    model: str
    manufacturer: str
    category: str
    unit: str
    p_krw: int  # 납품 단가 P(부가세 포함)
    quantities: list[int]
    lookup_state: str = "available"  # available | claimed | reform_found


@dataclass
class Case:
    case_id: str
    product: Product
    offer: Offer
    label: str
    traps: list[str] = field(default_factory=list)
    note: str = ""
    # 차이 속성: 조달 물품과 판매 페이지가 다른 속성과 차이의 종류. 근거 부족이면 None(비교할 페이지가 없다)
    diffs: list[tuple[str, str]] | None = None

    @property
    def truth_read(self) -> dict | None:
        """읽은 값의 정답(채점 명세 2절). 정적 페이지가 아니면 읽을 값이 없다."""
        o = self.offer
        if o.render != "static":
            return None
        vat = "not_stated" if o.vat_included is None else "included" if o.vat_included else "excluded"
        per = o.units_per_fact_unit
        return {
            "sale_price_krw": None if o.price_krw is None else str(o.price_krw),
            "vat": vat,
            "sale_unit": o.sale_unit,
            "conversion": str(per.numerator) if per.denominator == 1 else f"{per.numerator}/{per.denominator}",
        }

    @property
    def benchmark_krw(self) -> int | None:
        """정답 비교 단가: 판매가를 조달 단위·부가세 포함으로 맞춘 값(배송비는 더하지 않는다, 대회 「자료와 방법」 2절)."""
        if self.label not in POSITIVE or self.offer.price_krw is None:
            return None
        return market_unit_price(self.offer)

    @property
    def gap_pass(self) -> bool | None:
        b = self.benchmark_krw
        return None if b is None else gap_passes(self.product.p_krw, b)

    @property
    def amount_krw(self) -> int | None:
        b = self.benchmark_krw
        if b is None:
            return None
        return sum(max(self.product.p_krw * q - b * q, 0) for q in self.product.quantities)

    @property
    def action(self) -> str:
        """에이전트가 해야 할 일: propose(제안) | reject(제안하지 않음) | skip(선점돼 볼 필요 없음)."""
        if self.product.lookup_state != "available":
            return "skip"
        return "propose" if self.gap_pass else "reject"


def ceil_frac(x: Fraction) -> int:
    return ceil(x)


def market_unit_price(offer: Offer) -> int:
    """판매가 → 조달 단위 1개당 부가세 포함 가격(올림, 비교 단가를 낮게 잡지 않는다)."""
    assert offer.price_krw is not None
    per_unit = Fraction(offer.price_krw) / offer.units_per_fact_unit
    if not offer.vat_included:
        per_unit *= Fraction(11, 10)
    return ceil_frac(per_unit)


def gap_passes(p: int, m: int) -> bool:
    """대회 가격 차이 기준: P ≥ 1.2×M 또는 P − M ≥ 100만 원(한 기준·한 단가일 때)."""
    return Fraction(p) >= Fraction(6, 5) * m or p - m >= 1_000_000
