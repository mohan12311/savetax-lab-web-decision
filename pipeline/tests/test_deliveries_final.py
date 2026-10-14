from __future__ import annotations

from decimal import Decimal

from conftest import envelope, line, listing
from savetax_pipeline.silver import build_silver
from test_silver import read

L1, L2, L3 = "300000001_1000000001", "300000001_1000000002", "300000002_1000000001"


def build(make_bronze, tmp_path, deliveries):
    rows = [listing(listing_id=L1, item_id="30000001"), listing(listing_id=L2, item_id="30000001"),
            listing(listing_id=L3, item_id="30000002")]
    silver = tmp_path / "silver"
    m = build_silver(make_bronze(rows, deliveries=deliveries), silver)
    return silver, m


def test_final_lines_dedup_across_shared_listings_and_refetch(make_bronze, tmp_path):
    shared = [line("R26TB00000001", 1), line("R26TB00000002", 1, revision="00", final="N", amount="3000.0000"),
              line("R26TB00000002", 1, revision="01", amount="1000.0000", quantity="1.0000")]
    deliveries = [
        envelope(L1, shared, amount_sum="3000.0000"),
        envelope(L2, shared, amount_sum="3000.0000"),
        # 나중에 다시 받았더니 원장이 바뀌었다 — 최신 값을 쓴다
        envelope(L3, [line("R26TB00000003", 2, amount="5000.0000")], fetched_at="2026-10-14T01:00:00Z"),
        envelope(L3, [line("R26TB00000003", 2, amount="7000.0000")], fetched_at="2026-10-14T05:00:00Z",
                 amount_sum="7000.0000"),
        envelope("399999999_1000000001", [], status=404),
    ]
    silver, m = build(make_bronze, tmp_path, deliveries)
    got = {(r["request_no"], r["line_no"]): r for r in read(silver, "deliveries_final")}

    assert set(got) == {("R26TB00000001", 1), ("R26TB00000002", 1), ("R26TB00000003", 2)}
    r = got[("R26TB00000001", 1)]
    assert (r["listing_ids"], r["item_id"], r["contract_no"]) == ([L1, L2], "30000001", "300000001_1")
    assert (r["amount"], r["quantity"], r["is_zero"]) == (Decimal("2000.0000"), Decimal("2.0000"), False)
    assert got[("R26TB00000002", 1)]["revision"] == "01"  # 최종 표시가 없는 이전 차수는 버린다
    assert got[("R26TB00000003", 2)]["amount"] == Decimal("7000.0000")

    d = m["report"]["deliveries"]
    assert (d["envelopes"], d["listings"], d["lines"], d["summary_mismatch_listings"]) == (4, 3, 3, 0)
    assert m["inputs"]["deliveries"]["files"] == 1


def test_null_amount_stays_null_and_zero_line_flagged(make_bronze, tmp_path):
    deliveries = [envelope(L3, [line("R26TB00000009", 1, amount=None),
                                line("R26TB00000009", 2, quantity="0.0000", amount="0.0000")],
                           amount_sum="999.0000")]
    silver, m = build(make_bronze, tmp_path, deliveries)
    got = {r["line_no"]: r for r in read(silver, "deliveries_final")}
    # 금액은 null 그대로. 수량이 0이 아니므로 금액을 몰라도 전량 취소 라인은 아니다.
    assert (got[1]["amount"], got[1]["is_zero"]) == (None, False)
    assert got[2]["is_zero"] is True
    # finalSummary 999 ≠ 라인 합 0 → 일부만 받았거나 원장이 바뀐 등재로 리포트
    assert m["report"]["deliveries"]["summary_mismatch_listings"] == 1


def test_rows_without_final_flag_are_reported_not_used(make_bronze, tmp_path):
    silver, m = build(make_bronze, tmp_path, [envelope(L3, [line("R26TB00000010", 1, final=None)])])
    assert read(silver, "deliveries_final") == []
    assert m["report"]["deliveries"]["rows_without_final_flag"] == 1


def test_no_delivery_files_gives_empty_typed_table(make_bronze, tmp_path):
    silver, m = build(make_bronze, tmp_path, None)
    assert m["tables"]["deliveries_final"]["rows"] == 0
    assert m["report"]["deliveries"]["envelopes"] == 0
