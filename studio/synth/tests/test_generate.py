import json
import re
from pathlib import Path

import pytest

from studio_synth.generate import generate, prepare_out
from studio_synth.hard import hard_cases
from studio_synth.model import INSUFFICIENT, POSITIVE

SHOP = "https://127.0.0.1:18444"
DAY = "2026-10-28"


@pytest.fixture(scope="module")
def out(tmp_path_factory) -> Path:
    d = tmp_path_factory.mktemp("synth")
    generate(d, 7, SHOP, DAY)
    return d


def load(out: Path):
    items = {i["item_id"]: i for i in json.loads((out / "items.json").read_text())}
    cases = [json.loads(line) for line in (out / "cases.jsonl").read_text().splitlines()]
    pages = {p["sku"]: p for p in json.loads((out / "shop" / "pages.json").read_text())}
    return items, cases, pages


def test_same_seed_same_bytes_other_seed_differs(out, tmp_path):
    again = tmp_path / "again"
    other = tmp_path / "other"
    a = json.loads((out / "manifest.json").read_text())["content_sha256"]
    assert generate(again, 7, SHOP, DAY)["content_sha256"] == a
    assert generate(other, 8, SHOP, DAY)["content_sha256"] != a


def test_hard_case_answers():
    want = {  # 사례 → (기대 비교 단가, 가격 차이 통과)
        "hard-01": (10890, True),  # 9,900 × 1.1
        "hard-04": (11000, True),  # 장당 400 × 25장/㎡ × 1.1
        "hard-05": (10890, True),  # 파렛트 198,000 ÷ 20㎡ × 1.1
        "hard-06": (13200, True),  # 부가세 별도 12,000 × 1.1
        "hard-07": (10780, True),  # 운반비는 더하지 않는다
        "hard-14": (10450, False),  # 12,500 ÷ 10,450 = 1.196배
        "hard-15": (6000000, True),  # 비율 1.17배지만 차이 100만 원
    }
    got = {c.case_id: (c.benchmark_krw, c.gap_pass) for c in hard_cases()}
    for k, v in want.items():
        assert got[k] == v, k
    for c in hard_cases():
        if c.label not in POSITIVE:
            assert c.benchmark_krw is None and c.action != "propose", c.case_id


def test_money_is_integer_strings(out):
    items, cases, _ = load(out)
    digits = re.compile(r"^[1-9][0-9]*$")
    for it in items.values():
        for f in it["facts"]:
            for k in ("unit_price_krw", "quantity", "comparable_amount_krw"):
                assert digits.match(f[k]), (it["item_id"], k, f[k])
            assert int(f["unit_price_krw"]) * int(f["quantity"]) == int(f["comparable_amount_krw"])
    for c in cases:
        for k in ("benchmark_unit_price_krw", "amount_krw"):
            v = c["expected"][k]
            assert v is None or re.match(r"^[0-9]+$", v), (c["case_id"], k, v)


def test_labels_are_consistent_with_pages(out):
    items, cases, pages = load(out)
    assert {c["item_id"] for c in cases} == set(items)
    for c in cases:
        page = pages[c["sku"]]
        html = (out / "shop" / page["html_file"]).read_text()
        if c["label"] == INSUFFICIENT:
            assert page["render"] == "spa" or "가격 문의" in html, c["case_id"]
            assert c["expected"]["action"] != "propose"
        if c["label"] in POSITIVE and page["render"] == "static" and "가격 문의" not in html:
            assert c["expected"]["benchmark_unit_price_krw"] is not None, c["case_id"]
        if c["lookup_state"] != "available":
            assert c["expected"]["action"] == "skip"


def test_reference_claims_match_facts_and_pages(out):
    items, _, pages_by_sku = load(out)
    pages = {p["path"]: p for p in pages_by_sku.values()}
    index = json.loads((out / "claims" / "index.json").read_text())
    assert any(x["expect"] == "pass" for x in index) and any(x["expect"] == "blocked:gap" for x in index)
    for x in index:
        claim = json.loads((out / "claims" / f"{x['item_id']}.json").read_text())
        facts = {f["fact_id"]: f for f in items[x["item_id"]]["facts"]}
        assert {l["fact_id"] for l in claim["lines"]} == set(facts)
        for l in claim["lines"]:
            f = facts[l["fact_id"]]
            # 1부 규칙: unit·tax_basis·comparison_basis_id는 fact 값을 글자 그대로
            assert (l["unit"], l["tax_basis"], l["comparison_basis_id"]) == (f["unit"], f["tax_basis"], f["basis_id"])
            for e in l["price_evidence"]:
                page = pages[e["source_url"].removeprefix(SHOP)]
                html = (out / "shop" / page["html_file"]).read_text()
                assert f"{int(e['observed_price_krw']):,}원" in html  # 1부 게이트의 원문 확인과 같은 조건
                assert page["title"] in e["quote"] and e["observed_at"] == DAY
                assert int(l["benchmark_unit_price_krw"]) >= int(e["observed_price_krw"])


def test_names_are_marked_fictional(out):
    items, cases, _ = load(out)
    for it in items.values():
        assert "(가상)" in it["snapshot"]["manufacturer"]
        assert all("(가상)" in f["agency_name"] for f in it["facts"])
    for p in (out / "shop" / "html").glob("*.html"):
        assert "(가상)" in p.read_text()


def test_prepare_out_refuses_foreign_directory(tmp_path):
    (tmp_path / "keep.txt").write_text("남의 파일")
    with pytest.raises(SystemExit):
        prepare_out(tmp_path)
    assert (tmp_path / "keep.txt").exists()
