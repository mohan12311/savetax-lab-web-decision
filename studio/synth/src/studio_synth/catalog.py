"""기본 사례: 시드 고정 난수로 만드는 조달 물품 ↔ 판매처 페이지 쌍.

분야·규격·가격대는 대회 데이터셋이 아니라 손으로 정한 일반 값이다. 제조사·판매처·기관 이름은 모두 가상이다.
라벨을 먼저 뽑고, 그 라벨이 참이 되도록 페이지를 만든다.
"""

from __future__ import annotations

import random
from fractions import Fraction

from .model import (D_DIFFERENT, D_NOT_ON_PAGE, DIFFERENT, EQUIVALENT, INSUFFICIENT, SAME_MODEL, Case, Offer, Product,
                    market_unit_price)

MAKERS = ["한빛블록(가상)", "가람산업(가상)", "누리콘크리트(가상)", "다온테크(가상)", "새솔전자(가상)", "바름금속(가상)"]
SELLERS = ["(가상) 바른자재몰", "(가상) 토목나라", "(가상) 오피스마켓"]
MAT_CODE = {"TPE": "T", "주철": "C", "스틸": "S"}

# 판매처 가격과 조달 단가의 비율. 앞쪽은 기준 미달(1.2배 아래), 뒤쪽은 통과.
RATIOS_FAIL = [Fraction(9, 10), Fraction(1), Fraction(11, 10), Fraction(23, 20)]
RATIOS_PASS = [Fraction(13, 10), Fraction(8, 5), Fraction(2), Fraction(5, 2), Fraction(3)]


def round10(x: Fraction) -> int:
    """10원 단위로 반올림(납품 단가처럼 보이게)."""
    return int((x / 10).__round__()) * 10


def _label(rng: random.Random, weights: dict[str, int]) -> str:
    labels = list(weights)
    return rng.choices(labels, weights=[weights[k] for k in labels])[0]


def _blocks(rng: random.Random, i: int, label: str) -> tuple[Product, Offer, list[str], str, list[tuple[str, str]]]:
    w, l, t = rng.choice([(200, 200, 60), (200, 200, 80), (300, 300, 60), (200, 100, 60)])
    perm = rng.choice(["투수", "불투수"])
    maker = rng.choice(MAKERS[:3])
    model = f"HB-{rng.choice('ABCDEFGH')}{w // 10}{t}{rng.randint(1, 9)}"
    name = f"보차도용콘크리트블록, {maker}, {model}, {w}×{l}×{t}mm, 인조화강블록, {perm}"
    base = rng.randint(90, 120) * 100 + (2000 if t == 80 else 0) + (3000 if w == 300 else 0)  # ㎡당, 부가세 별도
    o_w, o_l, o_t, o_perm, o_model, traps, note = w, l, t, perm, None, [], ""
    title_kind = "인조화강블록"
    if label == SAME_MODEL:
        o_model = model
    elif label == DIFFERENT:
        change = rng.choice(["thickness", "permeability", "material"])
        traps.append({"thickness": "thickness_mismatch", "permeability": "permeability_mismatch", "material": "material_mismatch"}[change])
        if change == "thickness":
            o_t = 80 if t == 60 else 60
        elif change == "permeability":
            o_perm = "불투수" if perm == "투수" else "투수"
        else:
            title_kind = "점토블록"
        note = f"조달 {w}×{l}×{t} {perm} 인조화강 ↔ 판매 {o_w}×{o_l}×{o_t} {o_perm} {title_kind}"
    diffs = [] if o_model else [("모델", D_NOT_ON_PAGE)]
    if label == DIFFERENT:
        diffs.insert(0, ({"thickness": "두께", "permeability": "투수 여부", "material": "종류"}[change], D_DIFFERENT))
    spec = f"{o_w}×{o_l}×{o_t}mm"
    title = f"{title_kind}({o_perm}) {spec}" + (f" {o_model}" if o_model else "")
    per_piece = label == EQUIVALENT and w == 200 and l == 200 and rng.random() < 0.5
    offer = Offer(
        sku=f"K{10000 + i}", title=title, category="보도블록", seller=SELLERS[0],
        spec_rows=[("규격", spec), ("종류", f"{title_kind} {o_perm}"), ("두께", f"{o_t}T")] + ([("모델", o_model)] if o_model else []),
        price_krw=base, vat_included=False, sale_unit="㎡",
        shipping="운반비 별도(화물, 지역별 상이)",
    )
    if per_piece:
        # 장 단위 판매(200×200이면 25장/㎡): ㎡로 환산해야 한다
        offer.price_krw, offer.sale_unit, offer.unit_note = base // 25, "장", "25장/㎡"
        offer.units_per_fact_unit = Fraction(1, 25)
        traps.append("unit_conversion")
    product = Product(item_id="", name=name, model=model, manufacturer=maker, category="보차도용콘크리트블록", unit="㎡",
                      p_krw=0, quantities=[rng.randint(3, 40) * 50 for _ in range(rng.randint(1, 3))])
    return product, offer, traps, note, diffs


def _flume(rng: random.Random, i: int, label: str) -> tuple[Product, Offer, list[str], str, list[tuple[str, str]]]:
    kind = rng.choice([2, 3])
    w, h = rng.choice([(600, 500), (800, 700), (400, 400)])
    maker = rng.choice(MAKERS[:3])
    model = f"NF-{kind}{w // 100}{h // 100}"
    name = f"철근콘크리트벤치플룸관, {maker}, {model}, {w}×{h}×2000mm, {kind}종"
    base = rng.randint(70, 140) * 1000 + (15000 if kind == 3 else 0)
    o_kind, o_w, traps, note, o_model = kind, w, [], "", None
    if label == SAME_MODEL:
        o_model = model
    elif label == DIFFERENT:
        if rng.random() < 0.5:
            o_kind = 2 if kind == 3 else 3
            traps.append("class_mismatch")
        else:
            o_w = w + 200
            traps.append("size_mismatch")
        note = f"조달 {kind}종 {w}×{h} ↔ 판매 {o_kind}종 {o_w}×{h}"
    diffs = [] if o_model else [("모델", D_NOT_ON_PAGE)]
    if label == DIFFERENT:
        diffs.insert(0, ("종류" if o_kind != kind else "규격", D_DIFFERENT))
    spec = f"{o_w}×{h}×2000mm"
    title = f"벤치플룸관 {o_kind}종 {spec}" + (f" {o_model}" if o_model else "")
    offer = Offer(
        sku=f"K{10000 + i}", title=title, category="배수관", seller=SELLERS[1],
        spec_rows=[("규격", spec), ("종류", f"{o_kind}종"), ("길이", "2m")] + ([("모델", o_model)] if o_model else []),
        price_krw=base, vat_included=False, sale_unit="본", shipping="운반비 별도(25톤 기준 견적)",
    )
    product = Product(item_id="", name=name, model=model, manufacturer=maker, category="벤치플룸관", unit="본",
                      p_krw=0, quantities=[rng.randint(2, 30) * 10 for _ in range(rng.randint(1, 2))])
    return product, offer, traps, note, diffs


def _grating(rng: random.Random, i: int, label: str) -> tuple[Product, Offer, list[str], str, list[tuple[str, str]]]:
    size = rng.choice([400, 500, 600])
    mat = rng.choice(["TPE", "주철", "스틸"])
    maker = rng.choice(MAKERS[3:])
    model = f"GT-{size // 100}{MAT_CODE[mat]}{rng.randint(10, 99)}"
    name = f"그레이팅덮개, {maker}, {model}, {size}×{size}×50mm, {mat}"
    base = rng.randint(30, 90) * 1000
    o_mat, o_size, traps, note, o_model = mat, size, [], "", None
    if label == SAME_MODEL:
        o_model = model
    elif label == DIFFERENT:
        o_mat = rng.choice([m for m in ["TPE", "주철", "스틸"] if m != mat])
        traps.append("material_mismatch")
        note = f"조달 {mat} ↔ 판매 {o_mat}"
    diffs = [] if o_model else [("모델", D_NOT_ON_PAGE)]
    if label == DIFFERENT:
        diffs.insert(0, ("재질", D_DIFFERENT))
    spec = f"{o_size}×{o_size}×50mm"
    title = f"{o_mat} 그레이팅 덮개 {spec}" + (f" {o_model}" if o_model else "")
    offer = Offer(
        sku=f"K{10000 + i}", title=title, category="배수 자재", seller=SELLERS[1],
        spec_rows=[("규격", spec), ("재질", o_mat)] + ([("모델", o_model)] if o_model else []),
        price_krw=base, vat_included=rng.random() < 0.5, sale_unit="개", shipping="택배 3,000원(5만 원 이상 무료)",
    )
    product = Product(item_id="", name=name, model=model, manufacturer=maker, category="그레이팅", unit="개",
                      p_krw=0, quantities=[rng.randint(5, 80) * 10 for _ in range(rng.randint(1, 3))])
    return product, offer, traps, note, diffs


def _monitor(rng: random.Random, i: int, label: str) -> tuple[Product, Offer, list[str], str, list[tuple[str, str]]]:
    inch, cm = rng.choice([(24, "60.5cm"), (27, "68.6cm"), (32, "80cm")])
    maker = rng.choice(MAKERS[3:5])
    model = f"VM{inch}{rng.choice(['Q', 'F', 'U'])}-{rng.choice('ABC')}{rng.randint(1, 9)}"
    name = f"액정모니터, {maker}, {model}, {cm}"
    base = rng.randint(18, 45) * 10000
    o_model, o_cm, traps, note = model, cm, [], ""
    if label == DIFFERENT:
        change = rng.choice(["variant", "size"])
        if change == "variant":
            o_model = model + "S"
            traps.append("variant_suffix")
        else:
            o_cm = "80cm" if cm != "80cm" else "68.6cm"
            o_model = model.replace(f"VM{inch}", "VM32" if cm != "80cm" else "VM27")
            traps.append("size_mismatch")
        note = f"조달 {model} {cm} ↔ 판매 {o_model} {o_cm}"
    diffs = []
    if label == DIFFERENT:
        diffs = [("모델", D_DIFFERENT)] + ([("화면 크기", D_DIFFERENT)] if change == "size" else [])
    title = f"{maker} {o_model} {o_cm} 모니터"
    offer = Offer(
        sku=f"K{10000 + i}", title=title, category="모니터", seller=SELLERS[2],
        spec_rows=[("모델", o_model), ("화면 크기", o_cm)],
        price_krw=base, vat_included=True, sale_unit="대", shipping="무료배송", shipping_included=True,
    )
    product = Product(item_id="", name=name, model=model, manufacturer=maker, category="액정모니터", unit="대",
                      p_krw=0, quantities=[rng.randint(1, 30) for _ in range(rng.randint(1, 3))])
    return product, offer, traps, note, diffs


CATEGORIES = [
    (_blocks, {SAME_MODEL: 1, EQUIVALENT: 4, DIFFERENT: 3, INSUFFICIENT: 2}),
    (_flume, {SAME_MODEL: 1, EQUIVALENT: 4, DIFFERENT: 3, INSUFFICIENT: 2}),
    (_grating, {SAME_MODEL: 2, EQUIVALENT: 3, DIFFERENT: 3, INSUFFICIENT: 2}),
    (_monitor, {SAME_MODEL: 5, EQUIVALENT: 0, DIFFERENT: 3, INSUFFICIENT: 2}),
]


def base_cases(rng: random.Random, n: int) -> list[Case]:
    cases = []
    for i in range(n):
        make, weights = CATEGORIES[i % len(CATEGORIES)]
        label = _label(rng, weights)
        # 근거 부족은 같은 모델/동급 페이지를 만든 뒤 읽을 수 없게(JS 껍데기) 하거나 가격을 지운다
        shape = _label(rng, {SAME_MODEL: 1, EQUIVALENT: 1}) if label == INSUFFICIENT else label
        if make is _monitor and shape == EQUIVALENT:
            shape = SAME_MODEL
        product, offer, traps, note, diffs = make(rng, i, shape)
        if label == INSUFFICIENT:
            if rng.random() < 0.6:
                offer.render = "spa"
                traps.append("empty_shell")
            else:
                offer.price_krw, offer.vat_included = None, None
                traps.append("no_price")
        # 조달 단가 P: 판매가를 조달 단위·부가세 포함으로 맞춘 값 × 비율
        ref = market_unit_price(offer) if offer.price_krw is not None else rng.randint(10, 300) * 1000
        ratio = rng.choice(RATIOS_PASS if rng.random() < 0.65 else RATIOS_FAIL)
        product.p_krw = max(round10(ref * ratio), 10)
        product.item_id = f"SYN{i + 1:04d}"
        state = rng.random()
        product.lookup_state = "claimed" if state < 0.08 else "reform_found" if state < 0.12 else "available"
        cases.append(Case(case_id=f"base-{i + 1:03d}", product=product, offer=offer, label=label, traps=traps, note=note,
                          diffs=None if label == INSUFFICIENT else diffs))
    return cases
