"""silver 빌드: bronze → data/silver/*.parquet + manifest.json

입력
  bronze/dataset/current.json            현재 데이터셋 릴리스(Go `dataset`가 검증 후 기록, COMPLETE.json 필수)
  bronze/data/deliveries/**/*.jsonl.gz   GET /v1/listings/{id}/deliveries 봉투(없으면 deliveries_final은 빈 표)
출력
  silver/{listings,dlv_keys,items,deliveries_final}.parquet, silver/manifest.json

차단 검사(checks.blocking_checks)가 하나라도 실패하면 기존 silver를 건드리지 않고 실패한다.
전체를 매번 다시 만든다(등재 약 105만 행, 수 초). bronze는 읽기만 한다.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import uuid
from dataclasses import dataclass
from datetime import date, datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import duckdb

from savetax_pipeline import checks

REPO = Path(__file__).resolve().parents[3]
SQL_DIR = REPO / "sql"
KST = timezone(timedelta(hours=9))
TABLES = ("listings", "dlv_keys", "items", "deliveries_final")  # 만드는 순서(뒤 표가 앞 표를 읽는다)
RELEASE_RE = re.compile(r"^[0-9A-Za-z._-]{1,64}$")


class SilverCheckError(RuntimeError):
    def __init__(self, failed: list[checks.Check]):
        self.failed = failed
        super().__init__("silver 차단 검사 실패: " + ", ".join(
            f"{c.name}(actual={c.actual!r}, expected={c.expected!r})" for c in failed))


@dataclass(frozen=True)
class Dataset:
    release_id: str
    parquet: Path
    parquet_sha256: str
    manifest: dict[str, Any]  # 데이터셋 _meta/manifest.json
    as_of: date  # 등재 관측일(KST) — 계약 유효 판단 기준일


def data_dir() -> Path:
    return Path(os.environ.get("SAVETAX_DATA_DIR") or REPO / "data")


def load_dataset(bronze: Path) -> Dataset:
    root = bronze / "dataset"
    cur = json.loads((root / "current.json").read_text())
    rel = root / cur["dir"]
    complete = json.loads((rel / "COMPLETE.json").read_text())  # 없으면 검증이 끝나지 않은 다운로드다
    manifest = json.loads((rel / "_meta" / "manifest.json").read_text())
    release_id = cur["release_id"]
    if not RELEASE_RE.match(release_id) or {complete["release_id"], manifest["release_id"]} != {release_id}:
        raise ValueError(f"데이터셋 릴리스 불일치: current={release_id!r} complete={complete['release_id']!r} "
                         f"manifest={manifest['release_id']!r}")
    parquet = root / cur["parquet"]
    # 등재 관측 시각(예: "2026-10-04 04:11:45.975731+00")의 KST 날짜. 3.12 fromisoformat은 "+00"을 읽는다.
    observed = datetime.fromisoformat(manifest["source"]["stamp"]["listing_observed_max"])
    return Dataset(
        release_id=release_id,
        parquet=parquet,
        parquet_sha256=complete["files"][parquet.name],
        manifest=manifest,
        as_of=observed.astimezone(KST).date(),
    )


def delivery_files(bronze: Path) -> list[Path]:
    # Go bronze writer는 쓰는 중에 .jsonl.gz 이름을 쓰지 않으므로 보이는 파일은 모두 닫힌 파일이다.
    return sorted((bronze / "data" / "deliveries").glob("**/*.jsonl.gz"))


def _lit(s: str) -> str:
    return "'" + s.replace("'", "''") + "'"


def read_sql(name: str) -> str:
    return (SQL_DIR / f"{name}.sql").read_text()


def register_sources(con: duckdb.DuckDBPyConnection, ds: Dataset, deliveries: list[Path]) -> None:
    con.execute(f"SET VARIABLE as_of = DATE {_lit(ds.as_of.isoformat())}")
    con.execute(f"SET VARIABLE release_id = {_lit(ds.release_id)}")
    con.execute(f"CREATE VIEW src_listings AS SELECT * FROM read_parquet({_lit(str(ds.parquet))})")
    if deliveries:
        files = "[" + ", ".join(_lit(str(p)) for p in deliveries) + "]"
        con.execute(f"""
            CREATE VIEW src_delivery_envelopes AS
            SELECT * FROM read_json({files}, format = 'newline_delimited', compression = 'gzip',
                columns = {{fetched_at: 'TIMESTAMPTZ', endpoint: 'VARCHAR', status: 'INTEGER', body: 'JSON'}})""")
    else:
        con.execute("""
            CREATE VIEW src_delivery_envelopes AS
            SELECT NULL::TIMESTAMPTZ AS fetched_at, NULL::VARCHAR AS endpoint,
                   NULL::INTEGER AS status, NULL::JSON AS body
            WHERE false""")


def build_tables(con: duckdb.DuckDBPyConnection) -> None:
    for t in TABLES:
        con.execute(f"CREATE TABLE {t} AS {read_sql('staging_' + t)}")


def sha256_file(p: Path) -> str:
    h = hashlib.sha256()
    with p.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def git_head() -> str | None:
    try:
        out = subprocess.run(["git", "-C", str(REPO), "describe", "--always", "--dirty", "--abbrev=12"],
                             capture_output=True, text=True, check=True, timeout=10)
        return out.stdout.strip() or None
    except (OSError, subprocess.SubprocessError):
        return None


def _write_json_atomic(path: Path, obj: Any) -> None:
    tmp = path.with_name(f".{path.name}.{uuid.uuid4().hex[:8]}.tmp")
    tmp.write_text(json.dumps(obj, ensure_ascii=False, indent=2, default=str) + "\n")
    os.replace(tmp, path)


def build_silver(bronze: Path, silver: Path) -> dict[str, Any]:
    started = datetime.now(timezone.utc)
    ds = load_dataset(bronze)
    deliveries = delivery_files(bronze)

    con = duckdb.connect()
    register_sources(con, ds, deliveries)
    build_tables(con)

    results = checks.blocking_checks(con, ds.manifest)
    failed = [c for c in results if not c.ok]
    if failed:
        raise SilverCheckError(failed)
    rep = checks.report(con)

    # 임시 폴더에 다 쓰고 파일 자체를 검사한 뒤 옮긴다.
    # manifest를 먼저 지우고 마지막에 쓰므로, manifest가 있으면 파일들은 그 manifest의 것이다.
    silver.mkdir(parents=True, exist_ok=True)
    tmp = silver / f".tmp-{uuid.uuid4().hex[:8]}"
    tmp.mkdir()
    try:
        files = {t: tmp / f"{t}.parquet" for t in TABLES}
        tables: dict[str, Any] = {}
        for t, out in files.items():
            con.execute(f"COPY {t} TO {_lit(str(out))} (FORMAT parquet, COMPRESSION zstd)")
            tables[t] = {"path": f"{t}.parquet", "rows": con.execute(f"SELECT count(*) FROM {t}").fetchone()[0],
                         "sha256": sha256_file(out)}
        out_checks = checks.output_checks(con, files)
        con.close()
        results += out_checks
        if failed := [c for c in out_checks if not c.ok]:
            raise SilverCheckError(failed)
        (silver / "manifest.json").unlink(missing_ok=True)
        for t in TABLES:
            os.replace(tmp / f"{t}.parquet", silver / f"{t}.parquet")
    finally:
        shutil.rmtree(tmp, ignore_errors=True)

    manifest = {
        "stage": "silver",
        "started_at": started.isoformat(),
        "finished_at": datetime.now(timezone.utc).isoformat(),
        "code": {"git": git_head(),
                 "sql_sha256": {p.name: sha256_file(p) for p in sorted(SQL_DIR.glob("staging_*.sql"))}},
        "inputs": {
            "dataset": {"release_id": ds.release_id, "parquet": str(ds.parquet.relative_to(bronze)),
                        "sha256": ds.parquet_sha256, "as_of": ds.as_of.isoformat()},
            "deliveries": {"files": len(deliveries),
                           "sha256": {str(p.relative_to(bronze)): sha256_file(p) for p in deliveries}},
        },
        "tables": tables,
        "checks": [c.as_dict() for c in results],
        "report": rep,
    }
    _write_json_atomic(silver / "manifest.json", manifest)
    return manifest


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="savetax_pipeline.silver", description=__doc__.splitlines()[0])
    ap.add_argument("--data-dir", type=Path, default=None, help="기본: $SAVETAX_DATA_DIR 또는 <repo>/data")
    args = ap.parse_args(argv)
    root = args.data_dir or data_dir()
    try:
        m = build_silver(root / "bronze", root / "silver")
    except SilverCheckError as e:
        print("silver: 차단 검사 실패 — 기존 silver를 그대로 둔다", file=sys.stderr)
        for c in e.failed:
            print(f"  FAIL {c.name}: actual={c.actual!r} expected={c.expected!r}", file=sys.stderr)
        return 1
    ds = m["inputs"]["dataset"]
    print(f"silver: dataset {ds['release_id']} (as_of {ds['as_of']}), deliveries 파일 {m['inputs']['deliveries']['files']}개")
    for t, info in m["tables"].items():
        print(f"  {t:<17} {info['rows']:>10,}행")
    print(f"  검사 {len(m['checks'])}개 통과")
    r = m["report"]
    pe = r["price_extremes"]
    print(f"  단가 이상치: 10원 미만 {pe['lt_10']}건({pe['lt_10_pct']}%), 10억 이상 {pe['ge_1e9']}건({pe['ge_1e9_pct']}%)")
    print(f"  납품 합계 단순 합산 시 과대: {r['dlv_overcount_pct']}%")
    print(f"  계약 유효(as_of): {r['contract_active']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
