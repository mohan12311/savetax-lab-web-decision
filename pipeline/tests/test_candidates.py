from __future__ import annotations

from decimal import Decimal

import duckdb

from conftest import dlv, listing, lookup_envelope
from savetax_pipeline.candidates import build_candidates
from savetax_pipeline.silver import build_silver


def build(make_bronze, tmp_path, rows, lookups=None):
    root = tmp_path
    build_silver(make_bronze(rows, lookups=lookups), root / "silver")
    m = build_candidates(root / "silver", root / "gold")
    rel = duckdb.sql(f"SELECT * FROM read_parquet('{root / 'gold' / 'candidates.parquet'}')")
    got = {r[0]: dict(zip(rel.columns, r)) for r in rel.fetchall()}
    ids = [l for l in (root / "gold" / "lookup_ids.txt").read_text().splitlines() if not l.startswith("#")]
    return m, got, ids


def test_tier_a_uses_cheaper_listing_of_same_item(make_bronze, tmp_path):
    rows = [
        # 같은 물품: 비싼 계약으로 2억 주문, 다른 계약은 절반 값(주문 없음)
        listing(listing_id="500000001_1000000001", item_id="50000001", price=Decimal("2000000"),
                **dlv(1, 200_000_000, qty=100)),
        listing(listing_id="500000002_1000000001", item_id="50000001", price=Decimal("1000000")),
        # 1.1배라 gap 기준(1.2배·100만 원) 미달 → 잠재액 없음
        listing(listing_id="500000003_1000000001", item_id="50000002", price=Decimal("110000"),
                **dlv(2, 50_000_000, qty=454)),
        listing(listing_id="500000004_1000000001", item_id="50000002", price=Decimal("100000")),
    ]
    m, got, ids = build(make_bronze, tmp_path, rows)
    a = got["50000001"]
    assert (a["tier"], a["u_basis"], a["u_item"], a["u_est"], a["r_tier"]) == ("A", "item_min", 100_000_000, 100_000_000, Decimal("2.00"))
    assert (got["50000002"]["tier"], got["50000002"]["u_item"]) == (None, None)
    assert ids == ["50000001"]
    assert all(c["actual"] == [] for c in m["checks"] if c["name"].endswith("parquet_float_columns"))


def test_tier_b_same_model_other_item(make_bronze, tmp_path):
    rows = [
        listing(listing_id="600000001_1000000001", item_id="60000001", model="AB-1234X", price=Decimal("3000000"),
                **dlv(1, 300_000_000, qty=100)),
        listing(listing_id="600000002_1000000001", item_id="60000002", model="ab 1234x", price=Decimal("1000000")),
        # 모델명이 너무 짧으면(숫자·영문 5자 미만) 비교하지 않는다
        listing(listing_id="600000003_1000000001", item_id="60000003", model="A1", price=Decimal("3000000"),
                **dlv(2, 300_000_000, qty=100)),
        listing(listing_id="600000004_1000000001", item_id="60000004", model="A1", price=Decimal("1000000")),
    ]
    _, got, _ = build(make_bronze, tmp_path, rows)
    b = got["60000001"]
    assert (b["tier"], b["m_model_item_id"], b["u_model"], b["r_tier"]) == ("B", "60000002", 200_000_000, Decimal("3.00"))
    assert got["60000003"]["tier"] is None


def test_lookup_ids_skip_sticky_states_and_join_state(make_bronze, tmp_path):
    rows = [listing(item_id=f"7000000{i}", price=Decimal("1000000"), **dlv(i, 150_000_000, qty=150)) for i in range(1, 4)]
    lookups = [lookup_envelope([("70000001", "claimed"), ("70000002", "available")])]
    m, got, ids = build(make_bronze, tmp_path, rows, lookups)
    assert (got["70000001"]["claim_state"], got["70000002"]["claim_state"], got["70000003"]["claim_state"]) == \
        ("claimed", "available", None)
    # 티어는 없지만 납품 1억 이상이라 lookup 대상. 이미 선점된 것은 다시 묻지 않는다
    assert sorted(ids) == ["70000002", "70000003"]
    assert m["summary"]["lookup_ids"] == 2
