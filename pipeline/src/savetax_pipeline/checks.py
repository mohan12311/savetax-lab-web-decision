"""silver 검증.

차단 검사(blocking)는 데이터셋 manifest의 quality 수치·불변식과 맞지 않으면 silver 게시를 막는다.
리포트(report)는 판단 재료로 manifest에 남기기만 한다.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any

import duckdb

FLOAT_TYPES = {"FLOAT", "DOUBLE", "REAL", "FLOAT4", "FLOAT8"}

# 데이터셋 원본과 silver.listings에 모두 있는 컬럼 중 null 수가 그대로여야 하는 것(null → 0 변환 감시).
NULL_PRESERVED = (
    "price", "discount_price", "unit", "manufacturer", "model", "spec", "contract_end",
    "dlv_key", "dlv_shared_listings", "dlv_lines", "dlv_qty", "dlv_unit", "dlv_amount",
    "dlv_first_date", "dlv_last_date",
)


class CheckError(RuntimeError):
    """차단 검사 실패. 게시하지 않고 기존 결과를 그대로 둔다."""

    def __init__(self, failed: list["Check"], stage: str = "silver"):
        self.failed = failed
        super().__init__(f"{stage} 차단 검사 실패: " + ", ".join(
            f"{c.name}(actual={c.actual!r}, expected={c.expected!r})" for c in failed))


@dataclass(frozen=True)
class Check:
    name: str
    actual: Any
    expected: Any

    @property
    def ok(self) -> bool:
        return self.actual == self.expected

    def as_dict(self) -> dict[str, Any]:
        return {"name": self.name, "ok": self.ok, "actual": self.actual, "expected": self.expected}


def _one(con: duckdb.DuckDBPyConnection, sql: str) -> Any:
    return con.execute(sql).fetchone()[0]


def _int(v: Any) -> int | None:
    # manifest 금액은 문자열("17566373107109"), 개수는 정수다.
    return None if v is None else int(v)


def blocking_checks(con: duckdb.DuckDBPyConnection, manifest: dict[str, Any]) -> list[Check]:
    q = manifest.get("quality") or {}
    checks: list[Check] = []

    def add(name: str, sql: str, expected: Any) -> None:
        checks.append(Check(name, _one(con, sql), expected))

    def add_quality(name: str, key: str, sql: str) -> None:
        # 이 릴리스 manifest에 기준값이 없으면 검사하지 않는다(다른 버전의 수치를 가져다 쓰지 않는다).
        if key in q:
            add(name, sql, _int(q[key]))

    if "rows" in manifest:
        add("listings.rows", "SELECT count(*) FROM listings", _int(manifest["rows"]))
    add("listings.listing_id_unique", "SELECT count(*) - count(DISTINCT listing_id) FROM listings", 0)
    add("listings.contract_no_missing", "SELECT count(*) FROM listings WHERE contract_no IS NULL", 0)
    for col in NULL_PRESERVED:
        checks.append(Check(
            f"listings.{col}.nulls_preserved",
            _one(con, f"SELECT count(*) - count({col}) FROM listings"),
            _one(con, f"SELECT count(*) - count({col}) FROM src_listings"),
        ))

    # dlv_key 중복 제거: 키 합(kept)과 등재별 단순 합(naive)이 데이터셋이 공표한 두 금액과 같아야 한다.
    add_quality("dlv.kept_amount", "kept_amount", "SELECT coalesce(sum(dlv_amount), 0) FROM dlv_keys")
    add_quality("dlv.naive_amount", "naive_amount", "SELECT coalesce(sum(dlv_amount), 0) FROM listings")
    add_quality("dlv.keys", "dlv_keys", "SELECT count(*) FROM dlv_keys")
    add_quality("dlv.listings_with_dlv", "listings_with_dlv", "SELECT count(*) FROM listings WHERE dlv_key IS NOT NULL")
    add_quality("dlv.shared_listings", "shared_listings", "SELECT count(*) FROM listings WHERE dlv_shared_listings > 1")
    add_quality("dlv.max_shared", "max_shared", "SELECT max(n_listings) FROM dlv_keys")
    add_quality("dlv.keys_all_zero", "keys_all_zero", "SELECT count(*) FROM dlv_keys WHERE dlv_lines = 0")
    add_quality("dlv.qty_null_with_dlv", "dlv_qty_null_with_dlv",
                "SELECT count(*) FROM listings WHERE dlv_key IS NOT NULL AND dlv_qty IS NULL")
    add("dlv_keys.values_vary", "SELECT count(*) FROM dlv_keys WHERE n_variants > 1", 0)
    add("dlv_keys.multi_item", "SELECT count(*) FROM dlv_keys WHERE n_items > 1", 0)
    add("dlv_keys.multi_contract", "SELECT count(*) FROM dlv_keys WHERE n_contracts > 1", 0)
    add("dlv_keys.shared_count_mismatch",
        "SELECT count(*) FROM dlv_keys WHERE n_listings IS DISTINCT FROM dlv_shared_listings", 0)

    add_quality("listings.price_lt_10", "price_lt_10", "SELECT count(*) FROM listings WHERE price_flag = 'lt_10'")
    add_quality("listings.price_ge_1e9", "price_ge_1e9", "SELECT count(*) FROM listings WHERE price_flag = 'ge_1e9'")
    add_quality("listings.contract_end_after_2100", "contract_end_after_2100",
                "SELECT count(*) FROM listings WHERE contract_end_clean IS NULL")
    add_quality("listings.discounted", "discounted", "SELECT count(*) FROM listings WHERE discount_price IS NOT NULL")

    add("items.rows", "SELECT count(*) FROM items", _one(con, "SELECT count(DISTINCT item_id) FROM listings"))
    add("items.attrs_vary", """
        SELECT count(*) FROM (
          SELECT item_id FROM listings GROUP BY item_id
          HAVING count(DISTINCT (name, item_name, detail_category, detail_category_code,
                                 manufacturer, model, spec, source)) > 1)""", 0)
    add("items.dlv_amount_equals_keys",
        "SELECT (SELECT sum(dlv_amount) FROM items) IS NOT DISTINCT FROM (SELECT sum(dlv_amount) FROM dlv_keys)", True)
    add("items.dlv_amount_zero_without_keys",
        "SELECT count(*) FROM items WHERE n_dlv_keys = 0 AND dlv_amount IS NOT NULL", 0)

    add("deliveries_final.key_unique",
        "SELECT count(*) - count(DISTINCT (request_no, line_no)) FROM deliveries_final", 0)
    add("claim_state.key_unique", "SELECT count(*) - count(DISTINCT item_id) FROM claim_state", 0)
    return checks


def output_checks(con: duckdb.DuckDBPyConnection, files: dict[str, Path]) -> list[Check]:
    """쓴 Parquet 파일 자체를 본다. 메모리 표의 HUGEINT(sum 결과)는 Parquet에 DOUBLE로 써지므로 표만 보면 놓친다."""
    out = []
    for table, path in files.items():
        lit = "'" + str(path).replace("'", "''") + "'"
        rows = con.execute(f"DESCRIBE SELECT * FROM read_parquet({lit})").fetchall()
        out.append(Check(f"{table}.parquet_float_columns",
                         [name for name, typ, *_ in rows if typ.upper() in FLOAT_TYPES], []))
    return out


def report(con: duckdb.DuckDBPyConnection) -> dict[str, Any]:
    r: dict[str, Any] = {}
    n, lt10, ge1e9 = con.execute("""
        SELECT count(*), count(*) FILTER (WHERE price_flag = 'lt_10'), count(*) FILTER (WHERE price_flag = 'ge_1e9')
        FROM listings""").fetchone()
    r["price_extremes"] = {
        "lt_10": lt10, "ge_1e9": ge1e9,
        "lt_10_pct": round(100 * lt10 / n, 4) if n else None,
        "ge_1e9_pct": round(100 * ge1e9 / n, 4) if n else None,
        "lt_10_by_unit": dict(con.execute("""
            SELECT coalesce(unit_norm, '(null)'), count(*) FROM listings WHERE price_flag = 'lt_10'
            GROUP BY 1 ORDER BY 2 DESC, 1""").fetchall()),
    }
    kept, naive = con.execute(
        "SELECT (SELECT sum(dlv_amount) FROM dlv_keys), (SELECT sum(dlv_amount) FROM listings)").fetchone()
    r["dlv_overcount_pct"] = round(100 * (naive - kept) / kept, 2) if kept else None
    r["contract_active"] = dict(con.execute("""
        SELECT CASE WHEN contract_active IS NULL THEN 'unknown' WHEN contract_active THEN 'active' ELSE 'inactive' END,
               count(*) FROM listings GROUP BY 1 ORDER BY 1""").fetchall())
    r["units"] = {
        "raw_distinct": _one(con, "SELECT count(DISTINCT unit) FROM listings"),
        "norm_distinct": _one(con, "SELECT count(DISTINCT unit_norm) FROM listings"),
        "null": _one(con, "SELECT count(*) FROM listings WHERE unit IS NULL"),
    }
    r["items"] = dict(zip(
        ("items", "with_dlv_keys", "multi_unit"),
        con.execute("""SELECT count(*), count(*) FILTER (WHERE n_dlv_keys > 0), count(*) FILTER (WHERE len(units) > 1)
                       FROM items""").fetchone(),
    ))
    r["deliveries"] = deliveries_report(con)
    r["claim_state"] = dict(con.execute(
        "SELECT coalesce(state, '(null)'), count(*) FROM claim_state GROUP BY 1 ORDER BY 2 DESC, 1").fetchall())
    return r


def deliveries_report(con: duckdb.DuckDBPyConnection) -> dict[str, Any]:
    envelopes, listings = con.execute(
        "SELECT count(*), count(DISTINCT body -> 'data' ->> 'id') FROM src_delivery_envelopes WHERE status = 200"
    ).fetchone()
    out: dict[str, Any] = {
        "envelopes": envelopes,
        "listings": listings,
        "lines": _one(con, "SELECT count(*) FROM deliveries_final"),
        "zero_lines": _one(con, "SELECT count(*) FROM deliveries_final WHERE is_zero"),
        "rows_without_final_flag": _one(con, """
            SELECT count(*) FROM (
              SELECT unnest(from_json(body -> 'data' -> 'rows', '[{"finalRequest":"VARCHAR"}]')) AS r
              FROM src_delivery_envelopes WHERE status = 200)
            WHERE r.finalRequest IS NULL"""),
    }
    # 등재별 최신 finalSummary 금액과 silver 라인 합이 다르면 페이지 일부만 받았거나 받은 뒤 원장이 바뀐 것이다.
    out["summary_mismatch_listings"] = _one(con, """
        WITH s AS (
          SELECT body -> 'data' ->> 'id' AS listing_id,
                 CAST(arg_max(body -> 'data' -> 'finalSummary' ->> 'amountSum', fetched_at) AS DECIMAL(24, 4)) AS amount
          FROM src_delivery_envelopes WHERE status = 200 GROUP BY 1
        ),
        l AS (
          SELECT listing_id, sum(amount) AS amount
          FROM (SELECT unnest(listing_ids) AS listing_id, amount FROM deliveries_final) GROUP BY 1
        )
        SELECT count(*) FROM s LEFT JOIN l USING (listing_id)
        WHERE s.amount IS NOT NULL AND s.amount <> coalesce(l.amount, 0)""")
    return out
