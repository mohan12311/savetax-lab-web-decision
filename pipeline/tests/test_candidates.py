from __future__ import annotations

from decimal import Decimal

import duckdb

from conftest import dlv, listing, lookup_envelope
from savetax_pipeline.candidates import build_candidates
from savetax_pipeline.silver import build_silver


def build(make_bronze, tmp_path, rows, lookups=None, **kw):
    root = tmp_path
    build_silver(make_bronze(rows, lookups=lookups), root / "silver")
    m = build_candidates(root / "silver", root / "gold", **kw)
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


def test_tier_c_compares_same_pack_size_only(make_bronze, tmp_path):
    def salt(i, kg, price, **kw):
        return listing(listing_id=f"8{i:08d}_1000000001", item_id=f"8{i:07d}", unit="포",
                       name=f"고상제설제, 테스트, ECO-{i}, {kg}, 환경표지", price=Decimal(price), **kw)
    rows = [salt(i, "20kg", "10000") for i in range(30)]
    rows += [salt(100 + i, "1,000kg", "400000") for i in range(10)]
    # 1000kg 포대를 2억어치 샀다. 20kg 포대와 섞으면 중앙값의 42배지만 같은 1000kg끼리는 1.05배
    rows.append(salt(200, "1,000kg", "420000", **dlv(1, 200_000_000, qty=476)))
    # 500g 표기는 0.5kg로 맞춘다
    rows.append(salt(300, "500g", "3000", **dlv(2, 1_000_000, qty=333)))
    _, got, _ = build(make_bronze, tmp_path, rows)
    big = got["80000200"]
    assert (big["pack_key"], big["tier"], big["cat_n"]) == ("1000.0000kg", None, 11)
    assert big["x_median"] < 2
    assert got["80000300"]["pack_key"] == "0.5000kg"


def test_retail_scope_lists_it_items_and_strips_origin_prefix(make_bronze, tmp_path):
    def tier_a(n, dcc, model):
        item = f"8000000{n}"
        return [listing(listing_id=f"80000000{n}_1000000001", item_id=item, detail_category_code=dcc, model=model,
                        price=Decimal("2000000"), **dlv(n, 200_000_000, qty=100)),
                listing(listing_id=f"80000000{n}_1000000002", item_id=item, detail_category_code=dcc, model=model,
                        price=Decimal("1000000"))]

    def single(item, n, dcc, model, amount, maker="테스트제조"):
        return listing(listing_id=f"8{item[1:]}_1000000001", item_id=item, detail_category_code=dcc, model=model,
                       manufacturer=maker, price=Decimal("3000000"), **dlv(n, amount, qty=amount // 3_000_000))

    rows = (tier_a(1, "4321150101", "(JP)EOS R1") + tier_a(2, "4521160101", "EOS R1")  # 같은 검색어는 한 번
            + tier_a(3, "4323150101", "SW-1000")  # 소프트웨어는 뺀다
            + tier_a(4, "3010150101", "BLK-100")  # IT·사무·AV·가전 밖
            + tier_a(5, "5213150101", "TV-5000")  # 이미 선점
            # 티어 없음: 납품 1억 이상이고 모델명이 4자 이상이면 넣는다
            + [single("80000006", 6, "4321150101", "(CN)MON-2700Q", 600_000_000),
               single("80000007", 7, "4321150101", "X1", 600_000_000),  # 모델명이 짧다
               single("80000008", 8, "4321150101", "MON-2400F", 300_000_000),
               single("80000013", 13, "4321150101", "MON-1900", 60_000_000)]  # 납품 1억 미만
            # 분류 밖이라도 선점 비율이 높은 제조사(확인 4개 중 3개 선점)의 남은 물품은 넣는다
            + [single(str(80_000_000 + i), i, "3010150101", f"HOT-{i}00", 150_000_000, maker="핫제조") for i in (9, 10, 11, 12)]
            # 그 제조사의 소액 물품(1천만 이상)도 넣는다. 소액 물품의 lookup 결과는 제조사 선점률을 희석하지 않는다
            + [single(str(80_000_000 + i), i, "3010150101", f"HOT-{i}00", 30_000_000, maker="핫제조") for i in (14, 15, 16)]
            + [single("80000017", 17, "3010150101", "HOT-1700", 6_000_000, maker="핫제조")])  # 1천만 미만
    lookups = [lookup_envelope([("80000005", "claimed"), ("80000001", "available"), ("80000009", "claimed"),
                                ("80000010", "claimed"), ("80000011", "claimed"), ("80000012", "available"),
                                ("80000014", "available"), ("80000015", "available"), ("80000016", "available")])]
    m, got, _ = build(make_bronze, tmp_path, rows, lookups, retail_segments=("43", "44", "45", "52"))
    assert got["80000001"]["retail_query"] == "EOS R1"
    assert got["80000006"]["tier"] is None
    gold = tmp_path / "gold"
    lines = lambda f: [l for l in (gold / f).read_text().splitlines() if not l.startswith("#")]
    # 선점 잘 되는 제조사 먼저, 그다음 납품액순
    assert lines("retail_ids.txt") == ["80000012", "80000014", "80000015", "80000016",
                                       "80000006", "80000008", "80000001", "80000002"]
    assert lines("scrape_queries.txt") == ["HOT-1200", "HOT-1400", "HOT-1500", "HOT-1600", "MON-2700Q", "MON-2400F", "EOS R1"]
    assert m["summary"]["retail"] == {"items": 8, "queries": 7, "hot_maker": 4}
