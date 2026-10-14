from __future__ import annotations

from datetime import date
from decimal import Decimal
from pathlib import Path

import duckdb
import pytest

from conftest import dlv, listing, quality_for
from savetax_pipeline import checks
from savetax_pipeline.silver import SilverCheckError, build_silver


def read(silver: Path, table: str, where: str = "true", order: str = "1") -> list[dict]:
    rel = duckdb.sql(f"SELECT * FROM read_parquet('{silver / (table + '.parquet')}') WHERE {where} ORDER BY {order}")
    return [dict(zip(rel.columns, r)) for r in rel.fetchall()]


def test_dlv_key_dedup_matches_published_sums(make_bronze, tmp_path):
    rows = [
        listing(listing_id="100000001_1000000001", item_id="10000001", **dlv(1, 100, shared=2, qty=4)),
        listing(listing_id="100000001_1000000002", item_id="10000001", **dlv(1, 100, shared=2, qty=4)),
        listing(listing_id="100000002_1000000001", item_id="10000001", **dlv(2, 50, qty=1)),
        listing(listing_id="100000003_1000000001", item_id="10000002"),
    ]
    q = quality_for(rows)
    assert (q["kept_amount"], q["naive_amount"]) == ("150", "250")
    silver = tmp_path / "silver"
    m = build_silver(make_bronze(rows), silver)

    assert {c["name"] for c in m["checks"] if c["ok"]} >= {"dlv.kept_amount", "dlv.naive_amount", "dlv.keys"}
    keys = read(silver, "dlv_keys", order="dlv_key")
    assert [(k["dlv_key"], k["dlv_amount"], k["n_listings"]) for k in keys] == [(1, 100, 2), (2, 50, 1)]
    assert keys[0]["listing_ids"] == ["100000001_1000000001", "100000001_1000000002"]

    items = {i["item_id"]: i for i in read(silver, "items")}
    assert (items["10000001"]["dlv_amount"], items["10000001"]["n_dlv_keys"], items["10000001"]["dlv_qty"]) == (150, 2, 5)
    # 납품 기록이 없는 물품은 0이 아니라 null
    assert (items["10000002"]["dlv_amount"], items["10000002"]["n_dlv_keys"]) == (None, 0)
    assert m["report"]["dlv_overcount_pct"] == round(100 * 100 / 150, 2)


def test_failed_check_keeps_previous_silver(make_bronze, tmp_path):
    rows = [listing(listing_id=f"100000001_100000000{i}", item_id="10000001", **dlv(1, 100, shared=2)) for i in (1, 2)]
    silver = tmp_path / "silver"
    build_silver(make_bronze(rows), silver)
    before = {p.name: p.read_bytes() for p in silver.iterdir()}

    bad = quality_for(rows) | {"kept_amount": "200"}  # 등재별로 그냥 더한 값을 기준으로 잘못 적은 경우
    with pytest.raises(SilverCheckError) as e:
        build_silver(make_bronze(rows, quality=bad), silver)
    assert [c.name for c in e.value.failed] == ["dlv.kept_amount"]
    assert {p.name: p.read_bytes() for p in silver.iterdir()} == before


def test_inconsistent_values_within_key_are_blocked(make_bronze, tmp_path):
    rows = [listing(**dlv(7, 100, shared=2)), listing(**dlv(7, 999, shared=2))]
    q = quality_for(rows) | {"naive_amount": "1099", "kept_amount": "100"}
    with pytest.raises(SilverCheckError) as e:
        build_silver(make_bronze(rows, quality=q), tmp_path / "silver")
    assert "dlv_keys.values_vary" in {c.name for c in e.value.failed}


def test_listing_normalization(make_bronze, tmp_path):
    rows = [
        listing(listing_id="R25TA00000001000000001", unit="EA", price=Decimal("1")),
        listing(listing_id="100000001_1000000001", unit="㎡", price=Decimal("1500000000"),
                contract_end=date(7027, 7, 25)),
        listing(unit="M", contract_end=date(2026, 9, 30)),  # as_of(2026-10-04) 이전에 끝남
        listing(unit="50개", discount_price=Decimal("900")),
        listing(unit=None, price=Decimal("12345.678")),
    ]
    silver = tmp_path / "silver"
    build_silver(make_bronze(rows), silver)
    got = {r["listing_id"]: r for r in read(silver, "listings")}

    r = got["R25TA00000001000000001"]
    assert (r["contract_no"], r["unit"], r["unit_norm"], r["price_flag"], r["contract_active"]) == \
        ("R25TA00000001", "EA", "개", "lt_10", True)
    r = got["100000001_1000000001"]
    assert (r["contract_no"], r["unit_norm"], r["price_flag"]) == ("100000001_1", "m2", "ge_1e9")
    # 7027년 같은 입력 오류: 원값은 보존, 정리값·유효 여부는 알 수 없음(null)
    assert (r["contract_end"], r["contract_end_clean"], r["contract_active"]) == (date(7027, 7, 25), None, None)

    by_unit = {r["unit"]: r for r in got.values()}
    assert (by_unit["M"]["unit_norm"], by_unit["M"]["contract_active"]) == ("m", False)
    assert by_unit["50개"]["unit_norm"] == "50개"  # 포장 단위는 합치지 않는다
    assert by_unit[None]["unit_norm"] is None
    assert by_unit[None]["price"] == Decimal("12345.678")  # 소수 단가를 반올림하지 않는다
    assert all(r["dataset_release_id"] == "test-r1" for r in got.values())


def test_item_qty_needs_single_unit(make_bronze, tmp_path):
    rows = [
        listing(item_id="20000001", **dlv(1, 100, qty=3, unit="개")),
        listing(item_id="20000001", **dlv(2, 100, qty=5, unit="박스")),
        listing(item_id="20000002", **dlv(3, 100, qty=None)),
        listing(item_id="20000002", **dlv(4, 100, qty=2)),
    ]
    silver = tmp_path / "silver"
    build_silver(make_bronze(rows), silver)
    items = {i["item_id"]: i for i in read(silver, "items")}
    assert (items["20000001"]["dlv_amount"], items["20000001"]["dlv_qty"], items["20000001"]["dlv_unit"]) == (200, None, None)
    assert (items["20000002"]["dlv_amount"], items["20000002"]["dlv_qty"]) == (200, None)


def test_written_parquet_has_no_float_columns(make_bronze, tmp_path):
    silver = tmp_path / "silver"
    m = build_silver(make_bronze([listing(**dlv(1, 100))]), silver)
    float_checks = [c for c in m["checks"] if c["name"].endswith("parquet_float_columns")]
    assert len(float_checks) == 4 and all(c["actual"] == [] for c in float_checks)


def test_output_check_catches_hugeint_written_as_double(tmp_path):
    # sum()은 HUGEINT이고 Parquet에는 DOUBLE로 써진다 — 메모리 표만 보면 놓치는 경우
    p = tmp_path / "x.parquet"
    con = duckdb.connect()
    con.execute(f"COPY (SELECT sum(x) AS total, CAST(sum(x) AS BIGINT) AS ok FROM range(3) t(x)) TO '{p}' (FORMAT parquet)")
    (c,) = checks.output_checks(con, {"x": p})
    assert (c.ok, c.actual) == (False, ["total"])
