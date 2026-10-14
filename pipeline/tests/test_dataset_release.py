"""받아 둔 실제 데이터셋으로 silver를 만들어 공표 수치와 맞는지 본다. 데이터셋이 없으면 건너뛴다.

결과는 임시 폴더에만 쓴다(data/silver를 건드리지 않는다).
"""

from __future__ import annotations

import pytest

from savetax_pipeline.silver import build_silver, data_dir

BRONZE = data_dir() / "bronze"


@pytest.mark.skipif(not (BRONZE / "dataset" / "current.json").exists(), reason="데이터셋 없음(make dataset)")
def test_current_release_matches_published_quality(tmp_path):
    m = build_silver(BRONZE, tmp_path / "silver")
    by_name = {c["name"]: c for c in m["checks"]}
    for name in ("dlv.kept_amount", "dlv.naive_amount", "dlv.keys", "dlv.listings_with_dlv", "listings.rows"):
        assert by_name[name]["ok"], by_name[name]
    assert all(c["ok"] for c in m["checks"])
    # README: 그냥 더하면 약 24% 과대
    assert 20 < m["report"]["dlv_overcount_pct"] < 28
