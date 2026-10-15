"""gold 후보: silver → data/gold/candidates.parquet + lookup_ids.txt + candidates.manifest.json

후보 점수는 조달 안의 더 싼 등재 가격을 기준으로 한 탐색용 추정이다(sql/mart_candidates.sql).
시중가 근거를 찾을 순서를 정하는 데만 쓰고, 제출 값은 대회 fact로 다시 계산한다.

lookup_ids.txt는 `make lookup`(Go)이 읽는다. 티어가 있거나 납품 1억 원 이상인 물품 중
이미 선점·개혁신당 발견·없음으로 확인된 것을 뺀 목록이다. lookup 뒤 `make silver && make candidates`로 상태를 합친다.
"""

from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import duckdb

from savetax_pipeline import checks
from savetax_pipeline.common import SQL_DIR, data_dir, git_head, lit, publish, read_sql, sha256_file

MIN_U = 10_000_000  # 티어 A·B: 내부 기준가 대비 잠재액 1천만 원 이상
MIN_C_CAT = 100_000_000  # 티어 C: 납품 1억 원 이상이고
MIN_X_CAT = 3  # 세부품명 중앙값의 3배 이상
LOOKUP_MIN_C = 100_000_000  # 티어가 없어도 납품 1억 원 이상이면 선점 여부를 본다
STICKY_STATES = ("claimed", "reform_found", "not_found")  # 다시 묻지 않는다(철회는 watch가 잡는다)
SILVER_TABLES = ("listings", "dlv_keys", "items", "claim_state")
TOP_PER_TIER = 10
# 시중가(다나와 등 정적 판매 페이지)를 먼저 잴 범위: 물품분류번호 앞 2자리 IT(43)·사무기기(44)·AV·사진(45)·가전(52).
# 소프트웨어(4323)는 라이선스 조건(채널·에디션)이 갈려 판매 페이지 단가와 바로 비교하기 어려워 뺀다.
RETAIL_SEGMENTS = ("43", "44", "45", "52")
RETAIL_EXCLUDE_FAMILIES = ("4323",)
# 티어가 없어도(조달 안에서 가격이 고르게 비싸면 내부 기준가에 안 잡힌다) 납품이 이만큼이고 모델명이 있으면 넣는다.
# 모델명 기준은 티어 B와 같다(영숫자 5자 이상, 영문·숫자 모두 포함).
RETAIL_MIN_C = 500_000_000


def build_candidates(silver: Path, gold: Path) -> dict[str, Any]:
    started = datetime.now(timezone.utc)
    silver_manifest = json.loads((silver / "manifest.json").read_text())  # 없으면 make silver가 먼저다

    con = duckdb.connect()
    for t in SILVER_TABLES:
        con.execute(f"CREATE VIEW {t} AS SELECT * FROM read_parquet({lit(str(silver / (t + '.parquet')))})")
    con.execute(f"SET VARIABLE min_u = {MIN_U}")
    con.execute(f"SET VARIABLE min_c_cat = {MIN_C_CAT}")
    con.execute(f"SET VARIABLE min_x_cat = {MIN_X_CAT}")
    con.execute(f"CREATE TABLE candidates AS {read_sql('mart_candidates')}")

    results = [
        checks.Check("candidates.item_id_unique",
                     con.execute("SELECT count(*) - count(DISTINCT item_id) FROM candidates").fetchone()[0], 0),
        checks.Check("candidates.u_est_within_c",
                     con.execute("SELECT count(*) FROM candidates WHERE u_est < 0 OR u_est > c_est").fetchone()[0], 0),
    ]
    if failed := [c for c in results if not c.ok]:
        raise checks.CheckError(failed, "gold")

    sticky = ", ".join(lit(s) for s in STICKY_STATES)
    ids = [r[0] for r in con.execute(f"""
        SELECT item_id FROM candidates
        WHERE (tier IS NOT NULL OR c_est >= {LOOKUP_MIN_C})
          AND (claim_state IS NULL OR claim_state NOT IN ({sticky}))
        ORDER BY u_est DESC NULLS LAST, c_est DESC, item_id""").fetchall()]
    lookup_text = (f"# gold candidates {started.isoformat()} — 티어가 있거나 납품 {LOOKUP_MIN_C:,}원 이상, "
                   f"{'·'.join(STICKY_STATES)} 제외\n" + "".join(i + "\n" for i in ids))

    # 시중가 범위: 아직 등록 가능하거나 미확인인 물품 중 티어가 있거나, 납품이 크고 모델명이 쓸 만한 것.
    # 같은 검색어는 한 번만, 티어 있는 것(잠재액순) 다음 납품액순.
    segs = ", ".join(lit(s) for s in RETAIL_SEGMENTS)
    fams = ", ".join(lit(f) for f in RETAIL_EXCLUDE_FAMILIES)
    con.execute(f"""
        CREATE TEMP TABLE retail AS
        SELECT item_id, retail_query, u_est, c_est, tier IS NOT NULL AS tiered FROM candidates
        WHERE (claim_state IS NULL OR claim_state = 'available')
          AND left(detail_category_code, 2) IN ({segs}) AND left(detail_category_code, 4) NOT IN ({fams})
          AND retail_query IS NOT NULL
          AND (tier IS NOT NULL OR (c_est >= {RETAIL_MIN_C}
               AND length(regexp_replace(upper(retail_query), '[^0-9A-Z]', '', 'g')) >= 5
               AND regexp_matches(upper(retail_query), '[0-9]') AND regexp_matches(upper(retail_query), '[A-Z]')))""")
    order = "bool_or(tiered) DESC, max(u_est) DESC NULLS LAST, max(c_est) DESC"
    retail_ids = [r[0] for r in con.execute(f"""
        SELECT item_id FROM retail GROUP BY item_id ORDER BY {order}, item_id""").fetchall()]
    queries = [r[0] for r in con.execute(f"""
        SELECT retail_query FROM retail GROUP BY retail_query ORDER BY {order}, retail_query""").fetchall()]
    scope_note = (f"available 또는 미확인·분류 {'/'.join(RETAIL_SEGMENTS)}(제외 {'/'.join(RETAIL_EXCLUDE_FAMILIES)})"
                  f"·티어 있음 또는 납품 {RETAIL_MIN_C:,}원 이상이고 모델명 있음")
    retail_ids_text = f"# 시중가 범위 물품 {started.isoformat()} — {scope_note}\n" + "".join(i + "\n" for i in retail_ids)
    queries_text = f"# 시중가 검색어 {started.isoformat()} — {scope_note}\n" + "".join(q + "\n" for q in queries)

    summary = {
        "tiers": {t or "-": {"items": n, "u_est": u} for t, n, u in con.execute("""
            SELECT tier, count(*), CAST(coalesce(sum(u_est), 0) AS BIGINT) FROM candidates
            GROUP BY tier ORDER BY tier NULLS LAST""").fetchall()},
        "claim_state_tiered": dict(con.execute("""
            SELECT coalesce(claim_state, '미확인'), count(*) FROM candidates WHERE tier IS NOT NULL
            GROUP BY 1 ORDER BY 2 DESC, 1""").fetchall()),
        "lookup_ids": len(ids),
        "retail": {"items": len(retail_ids), "queries": len(queries),
                   "untiered": con.execute("SELECT count(*) FROM retail WHERE NOT tiered").fetchone()[0]},
        # 경쟁 지도: 선점 판단에만 쓴다(프로젝트 규칙 2). 미확인은 아직 lookup하지 않은 물품
        "claims_by_tier": _claims_by(con, "coalesce(tier, '-')"),
        "claims_by_band": _claims_by(con, """CASE
            WHEN c_est >= 10000000000 THEN '1) 100억 이상' WHEN c_est >= 1000000000 THEN '2) 10억~100억'
            WHEN c_est >= 100000000 THEN '3) 1억~10억' ELSE '4) 1억 미만' END"""),
    }
    # 티어마다 기준가의 성격이 달라(C는 규격·포장 차이가 많이 섞인다) 티어별로 따로 보여 준다.
    top = con.execute(f"""
        SELECT tier, item_id, u_est, c_est, r_tier, u_basis, coalesce(claim_state, '미확인') AS st, detail_category, name
        FROM candidates
        WHERE tier IS NOT NULL AND (claim_state IS NULL OR claim_state IN ('available', 'claimed_by_me'))
        QUALIFY row_number() OVER (PARTITION BY tier ORDER BY u_est DESC, c_est DESC, item_id) <= {TOP_PER_TIER}
        ORDER BY tier, u_est DESC, c_est DESC, item_id""").fetchall()

    def make_manifest(tables: dict[str, Any], out_checks: list[checks.Check]) -> dict[str, Any]:
        return {
            "stage": "gold.candidates",
            "started_at": started.isoformat(),
            "finished_at": datetime.now(timezone.utc).isoformat(),
            "code": {"git": git_head(), "sql_sha256": {"mart_candidates.sql": sha256_file(SQL_DIR / "mart_candidates.sql")}},
            "inputs": {"silver_finished_at": silver_manifest.get("finished_at"),
                       "silver_tables": {t: silver_manifest["tables"][t]["sha256"] for t in SILVER_TABLES}},
            "params": {"min_u": MIN_U, "min_c_cat": MIN_C_CAT, "min_x_cat": MIN_X_CAT,
                       "lookup_min_c": LOOKUP_MIN_C, "sticky_states": list(STICKY_STATES),
                       "retail_segments": list(RETAIL_SEGMENTS), "retail_exclude_families": list(RETAIL_EXCLUDE_FAMILIES),
                       "retail_min_c": RETAIL_MIN_C},
            "tables": tables,
            "checks": [c.as_dict() for c in results + out_checks],
            "summary": summary,
            "top": [dict(zip(("tier", "item_id", "u_est", "c_est", "r_tier", "u_basis", "claim_state",
                              "detail_category", "name"), r)) for r in top],
        }

    try:
        return publish(con, ("candidates",), gold, "candidates.manifest.json", make_manifest, "gold",
                       extra_text={"lookup_ids.txt": lookup_text, "retail_ids.txt": retail_ids_text,
                                   "scrape_queries.txt": queries_text})
    finally:
        con.close()


def _claims_by(con: duckdb.DuckDBPyConnection, group_expr: str) -> dict[str, dict[str, int]]:
    out: dict[str, dict[str, int]] = {}
    for g, st, n in con.execute(f"""
            SELECT {group_expr} AS g, coalesce(claim_state, '미확인') AS st, count(*)
            FROM candidates GROUP BY 1, 2 ORDER BY 1, 2""").fetchall():
        out.setdefault(g, {})[st] = n
    return out


def _claim_line(label: str, d: dict[str, int]) -> str:
    checked = sum(v for k, v in d.items() if k != "미확인")
    taken = d.get("claimed", 0) + d.get("reform_found", 0)
    pct = f"{100 * taken / checked:.0f}%" if checked else "-"
    return (f"  {label:<14} 확인 {checked:>6,} 중 선점 {taken:>5,}({pct:>4}) · "
            f"가능 {d.get('available', 0):>6,} · 미확인 {d.get('미확인', 0):>7,}")


def _krw(n: int | None) -> str:
    if n is None:
        return "-"
    return f"{n / 1e8:,.1f}억" if abs(n) >= 1e8 else f"{n / 1e4:,.0f}만"


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="savetax_pipeline.candidates", description=__doc__.splitlines()[0])
    ap.add_argument("--data-dir", type=Path, default=None, help="기본: $SAVETAX_DATA_DIR 또는 <repo>/data")
    args = ap.parse_args(argv)
    root = args.data_dir or data_dir()
    try:
        m = build_candidates(root / "silver", root / "gold")
    except checks.CheckError as e:
        print("candidates: 차단 검사 실패 — 기존 gold를 그대로 둔다", file=sys.stderr)
        for c in e.failed:
            print(f"  FAIL {c.name}: actual={c.actual!r} expected={c.expected!r}", file=sys.stderr)
        return 1
    s = m["summary"]
    print(f"candidates: {m['tables']['candidates']['rows']:,}개 물품(납품 있음)")
    names = {"A": "같은 물품 최저가", "B": "같은 모델 최저가", "C": "세부품명 중앙값 배수", "-": "티어 없음"}
    for t, v in s["tiers"].items():
        print(f"  티어 {t} {names.get(t, t):<12} {v['items']:>8,}개  추정 잠재액 합 {_krw(v['u_est'])}")
    print("\n  선점 지도(선점 = claimed + reform_found)")
    for t, d in s["claims_by_tier"].items():
        print(_claim_line(f"티어 {t}", d))
    for b, d in s["claims_by_band"].items():
        print(_claim_line(b[3:], d))
    print(f"  lookup 대상: {s['lookup_ids']:,}개 → data/gold/lookup_ids.txt (make lookup)")
    print(f"  시중가 범위: {s['retail']['items']:,}개 물품(티어 없음 {s['retail']['untiered']:,}) → data/gold/retail_ids.txt (make lookup IDS=retail_ids), "
          f"검색어 {s['retail']['queries']:,}개 → data/gold/scrape_queries.txt")
    if m["top"]:
        print(f"\n  티어별 상위 {TOP_PER_TIER} (선점 안 됐거나 미확인, 추정 잠재액순 — 조달 내부 기준가 기준이며 시중가 아님)")
        for r in m["top"]:
            ratio = "-" if r["r_tier"] is None else f"{r['r_tier']}배"
            print(f"  {r['tier']} {r['item_id']}  잠재 {_krw(r['u_est']):>8}  납품 {_krw(r['c_est']):>8}  {ratio:>8}  "
                  f"{r['claim_state']:<9} {r['detail_category']} | {r['name'][:40]}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
