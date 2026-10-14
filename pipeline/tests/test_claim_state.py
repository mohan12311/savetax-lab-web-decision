from __future__ import annotations

from conftest import listing, lookup_envelope
from savetax_pipeline.silver import build_silver
from test_silver import read


def test_latest_lookup_wins_and_unresolved_rows_dropped(make_bronze, tmp_path):
    later = lookup_envelope([("40000001", "claimed")], fetched_at="2026-10-14T07:00:00Z")
    unknown = lookup_envelope([("40000002", "available")])
    unknown["body"]["items"].append({"input": "bad", "kind": "unknown", "state": "not_found", "message": ""})
    lookups = [lookup_envelope([("40000001", "available"), ("40000003", "reform_found")]), later, unknown]
    silver = tmp_path / "silver"
    m = build_silver(make_bronze([listing()], lookups=lookups), silver)

    got = {r["item_id"]: r["state"] for r in read(silver, "claim_state")}
    assert got == {"40000001": "claimed", "40000002": "available", "40000003": "reform_found"}
    assert m["report"]["claim_state"] == {"available": 1, "claimed": 1, "reform_found": 1}
    assert m["inputs"]["lookups"]["files"] == 1
