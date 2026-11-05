"""사례를 파일로 내보낸다. 출력은 가짜 서버(studio/fakeserver)와 에이전트·평가가 나눠 읽는다.

    <out>/manifest.json        생성 조건(시드·버전·관측일)과 파일 해시
    <out>/items.json           가짜 대회 items/{item_id} 응답 모양 + lookup_state   ← 가짜 서버가 읽는다
    <out>/shop/pages.json      판매처 페이지 목록                                    ← 가짜 서버가 읽는다
    <out>/shop/html/<sku>.html 판매처 페이지 원문                                    ← 가짜 서버가 읽는다
    <out>/cases.jsonl          정답(라벨·함정·기대 비교 단가·해야 할 일·읽은 값·차이 속성) ← 평가만 읽는다(서버·에이전트 금지)
    <out>/tasks.jsonl          작업 목록(사건 ID·물품·판매 페이지 짝만, 정답 없음)  ← 에이전트가 읽는다
    <out>/claims/<item>.json   정답에서 만든 참조 본문(데모 흐름 확인용)
    <out>/claims/index.json    참조 본문 목록과 기대 결과(pass | blocked:gap)

같은 시드·같은 인자면 바이트까지 같은 결과가 나온다(시각을 기록하지 않는다).
"""

from __future__ import annotations

import hashlib
import json
import random
import shutil
from collections import Counter
from pathlib import Path

from . import VERSION
from .catalog import base_cases
from .hard import hard_cases, hard_cases_v2
from .model import POSITIVE, SAME_MODEL, Case
from .render import page_html, quote_text, vat_text, won
from .vary import page_html_v2, pick_style

RELEASE_ID = "synthetic-v1"
RULESET = "rules-v1"
AGENCIES = ["(가상) 한빛시 도로관리과", "(가상) 가람군 건설과", "(가상) 누리구청 회계과", "(가상) 다온교육청 재무과"]


def facts_for(case: Case, rng: random.Random) -> list[dict]:
    p = case.product
    num = p.item_id.removeprefix("SYN")
    facts = []
    for k, q in enumerate(p.quantities, start=1):
        unit = p.unit
        facts.append({
            "fact_id": f"dl:{p.item_id}:SL{num}_1:SR{num}{k:02d}:1",
            "listing_id": f"SL{num}_1",
            "contract_no": f"SC{num}",
            "request_no": f"SR{num}{k:02d}",
            "line_no": "1",
            "request_date": f"2025-{rng.randint(1, 12):02d}-{rng.randint(1, 28):02d}",
            "agency_name": rng.choice(AGENCIES),
            "unit": unit,
            "tax_basis": "vat_included",
            "basis_id": f"basis:{unit}:vat_included",
            "comparison_basis_id": f"basis:{unit}:vat_included",
            "period_kind": "",
            "unit_price_krw": str(p.p_krw),
            "quantity": str(q),
            "comparable_amount_krw": str(p.p_krw * q),
            "scorable": True,
            "exclusion_reasons": [],
        })
    return facts


def item_json(case: Case, facts: list[dict]) -> dict:
    p = case.product
    return {
        "item_id": p.item_id,
        "snapshot": {
            "name": p.name, "model": p.model, "manufacturer": p.manufacturer, "category": p.category,
            "source_release": RELEASE_ID, "fetched_at": "", "facts_total": len(facts), "facts_truncated": False,
            "listings": [{"listing_id": facts[0]["listing_id"], "vendor": p.manufacturer, "unit_price_krw": str(p.p_krw)}],
        },
        "facts": facts,
        "lookup_state": p.lookup_state,
    }


def ratio1(p: int, b: int) -> str:
    """P÷B를 소수 첫째 자리까지 내림(제목은 부풀리지 않는다). 정수 계산만 쓴다."""
    tenths = p * 10 // b
    return f"{tenths // 10}.{tenths % 10}"


def claim_json(case: Case, facts: list[dict], shop_base: str, observed_at: str, seed: int) -> dict:
    p, o = case.product, case.offer
    b = case.benchmark_krw
    assert b is not None and o.price_krw is not None
    kind = "같은 모델" if case.label == SAME_MODEL else "동급"
    conv = ""
    if o.units_per_fact_unit != 1:
        conv = f"판매 단위가 {o.sale_unit}({o.unit_note})라 조달 단위 {p.unit}로 환산했다. "
    vat = "부가세 포함 가격이라 그대로" if o.vat_included else "부가세 별도라 부가세 10%를 더해(원 단위 올림)"
    claim = (f"{kind} 비교: 조달 물품과 {'모델 코드·규격이 같은' if case.label == SAME_MODEL else '종류·규격이 같은'} 판매처 제품의 판매가와 비교했다. "
             f"{conv}판매가 {won(o.price_krw)}은 {vat} {won(b)}/{p.unit}을 비교 단가로 썼다. 배송비·운반비는 더하지 않았다.")
    evidence = {
        "kind": "price_page", "source_url": shop_base + o.path, "quote": quote_text(o, observed_at),
        "observed_price_krw": str(o.price_krw), "observed_at": observed_at,
        "vat_included": bool(o.vat_included), "shipping_included": o.shipping_included,
        "note": f"판매가 {won(o.price_krw)}({vat_text(o)}), 판매 단위 {o.sale_unit}",
    }
    return {
        "dataset_release_id": RELEASE_ID,
        "ruleset_version": RULESET,
        "canonical_product_id": p.item_id,
        "title": f"{kind} {p.category} 시중가 대비 {ratio1(p.p_krw, b)}배",
        "claim": claim,
        "comparison_scope": f"{kind}: 조달 {p.name} ⇄ 판매 {o.title}",
        "reproduction_notes": f"합성 데이터 참조 본문(studio-synth {VERSION}, seed {seed}, 사례 {case.case_id}). 정답에서 스크립트가 만들었다.",
        "lines": [{
            "fact_id": f["fact_id"], "benchmark_unit_price_krw": str(b), "comparison_basis_id": f["basis_id"],
            "unit": f["unit"], "tax_basis": f["tax_basis"], "period": None, "price_evidence": [evidence],
        } for f in facts],
    }


def reference_expect(case: Case) -> str | None:
    """참조 본문을 만들 사례와 1부 게이트에서 기대하는 결과."""
    o = case.offer
    if case.label not in POSITIVE or o.render != "static" or o.price_krw is None or case.product.lookup_state != "available":
        return None
    if case.benchmark_krw < o.price_krw:  # 1부 게이트는 B < 관측가를 막는다(파렛트 단위 등) — 알려진 한계
        return None
    return "pass" if case.gap_pass else "blocked:gap"


def dump(path: Path, obj) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(obj, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def prepare_out(out: Path) -> None:
    """이전 생성 결과만 지운다. 다른 파일이 든 디렉터리는 건드리지 않는다."""
    if not out.exists():
        return
    if not any(out.iterdir()):
        return
    m = out / "manifest.json"
    try:
        ok = json.loads(m.read_text(encoding="utf-8")).get("generator") == "studio-synth"
    except (OSError, ValueError):
        ok = False
    if not ok:
        raise SystemExit(f"{out}는 studio-synth 출력이 아니다(manifest.json 없음) — 지우지 않는다")
    shutil.rmtree(out)


VARIANTS = ("v1", "v2")


def generate(out: Path, seed: int, shop_base: str, observed_at: str, base_count: int = 36, variant: str = "v1") -> dict:
    """variant v1: 1부 데모가 쓰는 고른 표기. v2: 같은 사례를 다양한 표기로 그리고 v2 전용 어려운 사례를 더한다(참조 본문은 만들지 않음)."""
    assert variant in VARIANTS, variant
    rng = random.Random(seed)
    cases = base_cases(rng, base_count) + hard_cases() + (hard_cases_v2() if variant == "v2" else [])
    ids = [c.product.item_id for c in cases]
    skus = [c.offer.sku for c in cases]
    assert len(set(ids)) == len(ids) and len(set(skus)) == len(skus), "물품번호·SKU가 겹친다"
    prepare_out(out)

    items, pages, rows, claims_index, tasks = [], [], [], [], []
    for c in cases:
        facts = facts_for(c, rng)
        items.append(item_json(c, facts))
        html_file = f"html/{c.offer.sku}.html"
        (out / "shop" / "html").mkdir(parents=True, exist_ok=True)
        html = page_html(c.offer) if variant == "v1" else page_html_v2(c.offer, pick_style(seed, c.case_id))
        (out / "shop" / html_file).write_text(html, encoding="utf-8")
        pages.append({"sku": c.offer.sku, "path": c.offer.path, "render": c.offer.render, "title": c.offer.title,
                      "vat_included": c.offer.vat_included if c.offer.price_krw is not None else None, "html_file": html_file})
        tasks.append({"case_id": c.case_id, "item_id": c.product.item_id, "sku": c.offer.sku, "page_url": shop_base + c.offer.path})
        b = c.benchmark_krw
        rows.append({
            "case_id": c.case_id, "item_id": c.product.item_id, "sku": c.offer.sku, "page_url": shop_base + c.offer.path,
            "label": c.label, "traps": c.traps, "lookup_state": c.product.lookup_state,
            "expected": {
                "benchmark_unit_price_krw": None if b is None else str(b),
                "gap_pass": c.gap_pass,
                "amount_krw": None if c.amount_krw is None else str(c.amount_krw),
                "action": c.action,
            },
            "truth": {
                "read": c.truth_read,
                "diffs": None if c.diffs is None else [{"attribute": a, "kind": k} for a, k in c.diffs],
            },
            "note": c.note,
        })
        expect = reference_expect(c) if variant == "v1" else None
        if expect:
            dump(out / "claims" / f"{c.product.item_id}.json", claim_json(c, facts, shop_base, observed_at, seed))
            claims_index.append({"item_id": c.product.item_id, "case_id": c.case_id, "page_url": shop_base + c.offer.path,
                                 "need": c.offer.title, "expect": expect})

    dump(out / "items.json", items)
    dump(out / "shop" / "pages.json", pages)
    dump(out / "claims" / "index.json", claims_index)
    (out / "tasks.jsonl").write_text("".join(json.dumps(t, ensure_ascii=False) + "\n" for t in tasks), encoding="utf-8")
    (out / "cases.jsonl").write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in rows), encoding="utf-8")

    files = sorted(p for p in out.rglob("*") if p.is_file() and p.name != "manifest.json")
    digest = hashlib.sha256()
    for p in files:
        digest.update(f"{p.relative_to(out).as_posix()}\0{sha256(p)}\n".encode())
    manifest = {
        "generator": "studio-synth", "version": VERSION, "seed": seed, "variant": variant,
        "dataset_release_id": RELEASE_ID, "ruleset_version": RULESET,
        "shop_base_url": shop_base, "observed_at": observed_at,
        "item_count": len(items), "page_count": len(pages), "case_count": len(rows),
        "reference_claims": len(claims_index),
        "labels": dict(sorted(Counter(r["label"] for r in rows).items())),
        "actions": dict(sorted(Counter(r["expected"]["action"] for r in rows).items())),
        "traps": dict(sorted(Counter(t for r in rows for t in r["traps"]).items())),
        "content_sha256": digest.hexdigest(),
        "notice": "합성 데이터다. 제조사·판매처·기관 이름과 가격은 모두 가상이며 대회 데이터셋의 행·통계를 쓰지 않았다.",
    }
    dump(out / "manifest.json", manifest)
    return manifest
