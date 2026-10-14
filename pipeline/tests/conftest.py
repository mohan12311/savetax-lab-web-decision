"""합성 bronze 픽스처. 실데이터 행은 쓰지 않는다(데이터셋 재배포 금지)."""

from __future__ import annotations

import gzip
import hashlib
import json
from datetime import date
from decimal import Decimal
from pathlib import Path
from typing import Any, Callable

import pyarrow as pa
import pyarrow.parquet as pq
import pytest

RELEASE = "test-r1"

# 데이터셋 listings.parquet과 같은 35컬럼·타입
SCHEMA = pa.schema([
    ("listing_id", pa.string()), ("item_id", pa.string()), ("name", pa.string()), ("item_name", pa.string()),
    ("detail_category", pa.string()), ("detail_category_code", pa.string()), ("manufacturer", pa.string()),
    ("model", pa.string()), ("spec", pa.string()), ("unit", pa.string()),
    ("price", pa.decimal128(18, 3)), ("discount_price", pa.decimal128(18, 3)),
    ("contract_begin", pa.date32()), ("contract_end", pa.date32()), ("contract_type", pa.string()),
    ("policy_tags", pa.list_(pa.string())), ("vendor_name", pa.string()), ("vendor_size", pa.string()),
    ("category_l_code", pa.string()), ("category_l_name", pa.string()),
    ("category_m_code", pa.string()), ("category_m_name", pa.string()),
    ("site_sales_qty", pa.int64()), ("review_count", pa.int32()),
    ("image_url", pa.string()), ("detail_url", pa.string()), ("source", pa.string()),
    ("dlv_key", pa.int32()), ("dlv_shared_listings", pa.int32()), ("dlv_lines", pa.int32()),
    ("dlv_qty", pa.int64()), ("dlv_unit", pa.string()), ("dlv_amount", pa.int64()),
    ("dlv_first_date", pa.date32()), ("dlv_last_date", pa.date32()),
])

_seq = 0


def listing(**kw: Any) -> dict[str, Any]:
    """기본값으로 채운 등재 1행. 납품 필드는 기본 null(납품 기록 없음)."""
    global _seq
    _seq += 1
    item_id = kw.get("item_id", f"9{_seq:07d}")
    row = {
        "listing_id": f"{100000000 + _seq:09d}_1000000001",
        "item_id": item_id,
        "name": f"테스트품목, 테스트제조, M-{item_id}, 규격",
        "item_name": "테스트품명", "detail_category": "테스트세부품명", "detail_category_code": "1234567890",
        "manufacturer": "테스트제조", "model": f"M-{item_id}", "spec": "규격",
        "unit": "개", "price": Decimal("1000"), "discount_price": None,
        "contract_begin": date(2025, 1, 1), "contract_end": date(2027, 1, 1), "contract_type": "다수공급자계약",
        "policy_tags": [], "vendor_name": "테스트상사", "vendor_size": "중소기업",
        "category_l_code": "00001", "category_l_name": "대", "category_m_code": "00002", "category_m_name": "중",
        "site_sales_qty": 0, "review_count": 0, "image_url": None, "detail_url": None, "source": "shop",
    }
    row.update({c: None for c in SCHEMA.names if c.startswith("dlv_")})
    row.update(kw)
    return row


def dlv(key: int, amount: int | None, *, shared: int = 1, lines: int = 1, qty: int | None = 1,
        unit: str | None = "개") -> dict[str, Any]:
    return {"dlv_key": key, "dlv_shared_listings": shared, "dlv_lines": lines, "dlv_qty": qty,
            "dlv_unit": unit if qty is not None else None, "dlv_amount": amount,
            "dlv_first_date": date(2026, 1, 1), "dlv_last_date": date(2026, 2, 1)}


def envelope(listing_id: str, rows: list[dict[str, Any]], *, fetched_at: str = "2026-10-14T03:00:00Z",
             status: int = 200, contract_no: str | None = None, amount_sum: str | None = None) -> dict[str, Any]:
    body: dict[str, Any] = {"data": {
        "id": listing_id, "contractNo": contract_no or listing_id[:11], "finalOnly": True,
        "finalSummary": {"basis": "final_request_flag=Y", "lineCount": len(rows), "amountSum": amount_sum},
        "rows": rows, "page": {"limit": 1000, "offset": 0, "total": len(rows), "hasMore": False},
    }} if status == 200 else {"error": {"code": "not_found", "message": "없음"}}
    return {"fetched_at": fetched_at, "source": "data_api", "endpoint": "/v1/listings/{id}/deliveries",
            "params": {"id": listing_id, "final": "1", "offset": 0}, "status": status, "request_id": None,
            "body": body}


def line(request_no: str, line_no: int, *, revision: str = "00", final: str | None = "Y",
         quantity: str | None = "2.0000", unit_price: str | None = "1000.0000",
         amount: str | None = "2000.0000") -> dict[str, Any]:
    r = {"requestNo": request_no, "lineNo": line_no, "revision": revision, "requestDate": "2026-03-02",
         "unit": "개", "quantity": quantity, "unitPrice": unit_price, "contractUnitPrice": unit_price,
         "amount": amount, "quantityAdjustment": quantity, "amountAdjustment": amount,
         "agencyCode": "T000001", "agencyName": "테스트기관", "supplierName": "테스트상사",
         "supplierBusinessNo": "0000000000"}
    if final is not None:
        r["finalRequest"] = final
    return r


def quality_for(rows: list[dict[str, Any]]) -> dict[str, Any]:
    """픽스처 행에서 데이터셋 manifest quality 수치를 계산한다(데이터셋 생성기와 같은 정의)."""
    with_key = [r for r in rows if r["dlv_key"] is not None]
    keys: dict[int, dict[str, Any]] = {}
    for r in with_key:
        keys.setdefault(r["dlv_key"], r)
    n_by_key: dict[int, int] = {}
    for r in with_key:
        n_by_key[r["dlv_key"]] = n_by_key.get(r["dlv_key"], 0) + 1
    return {
        "listings_with_dlv": len(with_key),
        "dlv_keys": len(keys),
        "shared_listings": sum(1 for r in with_key if r["dlv_shared_listings"] > 1),
        "max_shared": max(n_by_key.values(), default=None),
        "naive_amount": str(sum(r["dlv_amount"] or 0 for r in with_key)),
        "kept_amount": str(sum(r["dlv_amount"] or 0 for r in keys.values())),
        "keys_all_zero": sum(1 for r in keys.values() if r["dlv_lines"] == 0),
        "dlv_qty_null_with_dlv": sum(1 for r in with_key if r["dlv_qty"] is None),
        "price_ge_1e9": sum(1 for r in rows if r["price"] >= 10**9),
        "price_lt_10": sum(1 for r in rows if r["price"] < 10),
        "contract_end_after_2100": sum(1 for r in rows if r["contract_end"] >= date(2100, 1, 1)),
        "discounted": sum(1 for r in rows if r["discount_price"] is not None),
    }


BronzeFactory = Callable[..., Path]


@pytest.fixture
def make_bronze(tmp_path: Path) -> BronzeFactory:
    def make(rows: list[dict[str, Any]], *, quality: dict[str, Any] | None = None,
             deliveries: list[dict[str, Any]] | None = None) -> Path:
        bronze = tmp_path / "bronze"
        rel = bronze / "dataset" / RELEASE
        (rel / "_meta").mkdir(parents=True, exist_ok=True)
        parquet = rel / "listings.parquet"
        pq.write_table(pa.Table.from_pylist(rows, schema=SCHEMA), parquet)
        sha = hashlib.sha256(parquet.read_bytes()).hexdigest()
        (bronze / "dataset" / "current.json").write_text(json.dumps(
            {"release_id": RELEASE, "dir": RELEASE, "parquet": f"{RELEASE}/listings.parquet"}))
        (rel / "COMPLETE.json").write_text(json.dumps({"release_id": RELEASE, "files": {"listings.parquet": sha}}))
        (rel / "_meta" / "manifest.json").write_text(json.dumps({
            "release_id": RELEASE, "rows": len(rows),
            "source": {"stamp": {"listing_observed_max": "2026-10-04 04:11:45.975731+00"}},
            "quality": quality if quality is not None else quality_for(rows),
        }))
        if deliveries:
            d = bronze / "data" / "deliveries" / "dt=2026-10-14"
            d.mkdir(parents=True, exist_ok=True)
            with gzip.open(d / "part-test-0001.jsonl.gz", "wt", encoding="utf-8") as f:
                for e in deliveries:
                    f.write(json.dumps(e, ensure_ascii=False) + "\n")
        return bronze

    return make
