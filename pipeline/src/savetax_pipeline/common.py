"""스테이지들이 같이 쓰는 경로·파일 도우미."""

from __future__ import annotations

import hashlib
import json
import os
import subprocess
import uuid
from pathlib import Path
from typing import Any

import duckdb

REPO = Path(__file__).resolve().parents[3]
SQL_DIR = REPO / "sql"


def data_dir() -> Path:
    return Path(os.environ.get("SAVETAX_DATA_DIR") or REPO / "data")


def lit(s: str) -> str:
    """SQL 문자열 리터럴. 경로처럼 우리가 만든 값에만 쓴다."""
    return "'" + s.replace("'", "''") + "'"


def read_sql(name: str) -> str:
    return (SQL_DIR / f"{name}.sql").read_text()


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


def write_json_atomic(path: Path, obj: Any) -> None:
    tmp = path.with_name(f".{path.name}.{uuid.uuid4().hex[:8]}.tmp")
    tmp.write_text(json.dumps(obj, ensure_ascii=False, indent=2, default=str) + "\n")
    os.replace(tmp, path)


def write_text_atomic(path: Path, text: str) -> None:
    tmp = path.with_name(f".{path.name}.{uuid.uuid4().hex[:8]}.tmp")
    tmp.write_text(text)
    os.replace(tmp, path)


def bronze_files(bronze: Path, source: str, kind: str) -> list[Path]:
    # Go bronze writer는 쓰는 중에 .jsonl.gz 이름을 쓰지 않으므로 보이는 파일은 모두 닫힌 파일이다.
    return sorted((bronze / source / kind).glob("**/*.jsonl.gz"))


def envelope_view(con: duckdb.DuckDBPyConnection, name: str, files: list[Path]) -> None:
    """bronze 봉투 파일들을 (fetched_at, endpoint, status, body) 뷰로 연다. 파일이 없으면 같은 모양의 빈 뷰."""
    if files:
        lst = "[" + ", ".join(lit(str(p)) for p in files) + "]"
        con.execute(f"""
            CREATE VIEW {name} AS
            SELECT * FROM read_json({lst}, format = 'newline_delimited', compression = 'gzip',
                columns = {{fetched_at: 'TIMESTAMPTZ', endpoint: 'VARCHAR', status: 'INTEGER', body: 'JSON'}})""")
    else:
        con.execute(f"""
            CREATE VIEW {name} AS
            SELECT NULL::TIMESTAMPTZ AS fetched_at, NULL::VARCHAR AS endpoint,
                   NULL::INTEGER AS status, NULL::JSON AS body
            WHERE false""")


def publish(con: duckdb.DuckDBPyConnection, tables: tuple[str, ...], out_dir: Path, manifest_name: str,
            make_manifest, stage: str, extra_text: dict[str, str] | None = None) -> dict[str, Any]:
    """표들을 임시 폴더에 Parquet으로 쓰고, 쓴 파일 자체를 검사한 뒤 out_dir로 옮긴다.

    manifest를 먼저 지우고 마지막에 쓰므로, manifest가 있으면 옆 파일들은 그 manifest의 것이다.
    make_manifest(tables_info, output_checks)가 manifest 본문을 만든다. 검사가 실패하면 아무것도 바꾸지 않는다.
    """
    import shutil

    from savetax_pipeline import checks

    out_dir.mkdir(parents=True, exist_ok=True)
    tmp = out_dir / f".tmp-{uuid.uuid4().hex[:8]}"
    tmp.mkdir()
    try:
        files = {t: tmp / f"{t}.parquet" for t in tables}
        info: dict[str, Any] = {}
        for t, out in files.items():
            con.execute(f"COPY {t} TO {lit(str(out))} (FORMAT parquet, COMPRESSION zstd)")
            info[t] = {"path": f"{t}.parquet", "rows": con.execute(f"SELECT count(*) FROM {t}").fetchone()[0],
                       "sha256": sha256_file(out)}
        out_checks = checks.output_checks(con, files)
        if failed := [c for c in out_checks if not c.ok]:
            raise checks.CheckError(failed, stage)
        for name, text in (extra_text or {}).items():
            (tmp / name).write_text(text)
        (out_dir / manifest_name).unlink(missing_ok=True)
        for t in tables:
            os.replace(files[t], out_dir / f"{t}.parquet")
        for name in extra_text or {}:
            os.replace(tmp / name, out_dir / name)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    manifest = make_manifest(info, out_checks)
    write_json_atomic(out_dir / manifest_name, manifest)
    return manifest
