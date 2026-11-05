"""웹 검토 화면의 백엔드. 표준 라이브러리 HTTP 서버이고 루프백(127.0.0.1)에서만 연다.

    GET  /api/runs                               실행 목록
    GET  /api/runs/{run}/cases                   사건 목록(검토 대기열)
    GET  /api/runs/{run}/cases/{case}            답안지 + 판매 페이지 줄(사람 화면용, 숨은 줄·탐지된 줄 포함) + 조달 물품 + 판정
    PUT  /api/runs/{run}/cases/{case}/judgment   사람 판정 저장(judgment.schema.json) → <실행>/judgments/<case>.json
    GET  /api/runs/{run}/scorecard               채점 결과(있으면)
    그 밖의 경로                                  웹 화면 정적 파일(studio/web/dist)

정답 파일(cases.jsonl)은 읽지 않는다. 사람 판정이 정답에 끌리지 않게 하기 위해서다(점수표는 채점기가 만든 파일만 보여 준다).
"""

from __future__ import annotations

import json
import mimetypes
import re
from datetime import datetime, timezone
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

import studio_schema
from studio_sheet.data import AgentData
from studio_sheet.prep import prepare

_ID = r"[A-Za-z0-9][A-Za-z0-9_.-]{0,80}"
_ROUTES = [
    ("GET", re.compile(r"^/api/runs$"), "runs"),
    ("GET", re.compile(rf"^/api/runs/({_ID})/cases$"), "cases"),
    ("GET", re.compile(rf"^/api/runs/({_ID})/cases/({_ID})$"), "case"),
    ("PUT", re.compile(rf"^/api/runs/({_ID})/cases/({_ID})/judgment$"), "judge"),
    ("GET", re.compile(rf"^/api/runs/({_ID})/scorecard$"), "scorecard"),
]
MAX_BODY = 64 * 1024


class NotFound(Exception):
    pass


class BadRequest(Exception):
    pass


class Store:
    def __init__(self, runs_root: Path, data_dir: Path):
        self.runs_root = runs_root.resolve()
        self.data = AgentData(data_dir)
        self._by_dir: dict[str, AgentData] = {}

    def _data_for(self, rid: str) -> AgentData:
        """실행이 쓴 합성 데이터(run.json의 data_dir, v1/v2). 없거나 사라졌으면 기본 데이터."""
        meta = json.loads((self._run(rid) / "run.json").read_text(encoding="utf-8"))
        d = meta.get("data_dir")
        if not d or not (Path(d) / "tasks.jsonl").exists():
            return self.data
        return self._by_dir.setdefault(d, AgentData(Path(d)))

    def _run(self, rid: str) -> Path:
        d = (self.runs_root / rid).resolve()
        if d.parent != self.runs_root or not (d / "run.json").exists():
            raise NotFound(rid)
        return d

    def _sheet(self, rid: str, cid: str) -> dict[str, Any]:
        p = self._run(rid) / f"{cid}.json"
        if cid in ("run", "scorecard") or not p.exists():
            raise NotFound(cid)
        return studio_schema.load(p)

    def _judgment(self, rid: str, cid: str) -> dict[str, Any] | None:
        p = self._run(rid) / "judgments" / f"{cid}.json"
        return studio_schema.load(p, "judgment") if p.exists() else None

    def runs(self) -> list[dict[str, Any]]:
        out = []
        for d in sorted(self.runs_root.glob("*/run.json"), reverse=True):
            meta = json.loads(d.read_text(encoding="utf-8"))
            run = d.parent
            judged = len(list((run / "judgments").glob("*.json"))) if (run / "judgments").exists() else 0
            out.append({**{k: meta.get(k) for k in ("run_id", "model", "effort", "prompt_version", "started_at", "finished_at", "cost_usd")},
                        "cases": len(meta.get("cases", [])), "judged": judged, "has_scorecard": (run / "scorecard.json").exists()})
        return out

    def cases(self, rid: str) -> list[dict[str, Any]]:
        run = self._run(rid)
        out = []
        for p in sorted(run.glob("*.json")):
            if p.name in ("run.json", "scorecard.json"):
                continue
            s = json.loads(p.read_text(encoding="utf-8"))
            j = self._judgment(rid, s["case_id"])
            out.append({
                "case_id": s["case_id"], "label": s["label"], "conclusion": s["conclusion"], "title": s["title"],
                "routing": s["routing"]["to"], "rules": [b["rule"] for b in s["routing"]["because"]],
                "danger": any(w["level"] == "danger" for w in s["warnings"]),
                "unfolded": sum(1 for r in s["rows"] if not r["folded"]) + (0 if s["price"]["check"]["folded"] else 1),
                "verdict": j["verdict"] if j else None,
            })
        return out

    def case(self, rid: str, cid: str) -> dict[str, Any]:
        s = self._sheet(rid, cid)
        data = self._data_for(rid)
        case = prepare(data, cid)
        item = data.item(case.item_id)
        return {
            "sheet": s,
            "page": {"url": case.page_url, "sha256": case.source_sha256, "lines": [ln.as_dict() for ln in case.lines],
                     "removed": sorted(case.blocked)},
            "procurement": {**case.procurement, "p_krw": item["snapshot"]["listings"][0]["unit_price_krw"],
                            "quantities": [f["quantity"] for f in item["facts"]]},
            "judgment": self._judgment(rid, cid),
        }

    def judge(self, rid: str, cid: str, body: dict[str, Any]) -> dict[str, Any]:
        self._sheet(rid, cid)
        j = {"schema_version": "judgment/v1", "case_id": cid, "verdict": body.get("verdict"), "label": body.get("label"),
             "rows": body.get("rows") or {}, "note": body.get("note") or "", "reviewer": body.get("reviewer") or "reviewer",
             "reviewed_at": datetime.now(timezone.utc).isoformat(timespec="seconds")}
        errs = studio_schema.errors("judgment", j)
        if j["verdict"] == "edit" and j["label"] is None:
            errs.append("label: edit이면 라벨을 고른다")
        if errs:
            raise BadRequest("; ".join(errs))
        d = self._run(rid) / "judgments"
        d.mkdir(exist_ok=True)
        (d / f"{cid}.json").write_text(json.dumps(j, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
        return j

    def scorecard(self, rid: str) -> dict[str, Any]:
        p = self._run(rid) / "scorecard.json"
        if not p.exists():
            raise NotFound("scorecard")
        return json.loads(p.read_text(encoding="utf-8"))


def make_handler(store: Store, static_dir: Path | None):
    class Handler(BaseHTTPRequestHandler):
        server_version = "studio-review"

        def log_message(self, fmt: str, *args: Any) -> None:  # 조용히
            pass

        def _send(self, status: int, body: bytes, ctype: str) -> None:
            self.send_response(status)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("X-Content-Type-Options", "nosniff")
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(body)

        def _json(self, status: int, obj: Any) -> None:
            self._send(status, json.dumps(obj, ensure_ascii=False).encode("utf-8"), "application/json; charset=utf-8")

        def _dispatch(self, method: str) -> None:
            path = urlparse(self.path).path
            try:
                for m, rx, name in _ROUTES:
                    hit = rx.match(path)
                    if hit and m == method:
                        args = hit.groups()
                        if name == "judge":
                            n = int(self.headers.get("Content-Length") or 0)
                            if n > MAX_BODY:
                                raise BadRequest("본문이 너무 크다")
                            try:
                                body = json.loads(self.rfile.read(n) or b"{}")
                            except ValueError:
                                raise BadRequest("JSON이 아니다") from None
                            return self._json(200, store.judge(*args, body))
                        return self._json(200, getattr(store, name)(*args))
                if path.startswith("/api/"):
                    raise NotFound(path)
                if method != "GET":
                    return self._json(405, {"error": "method not allowed"})
                return self._static(path)
            except NotFound as e:
                self._json(404, {"error": f"없다: {e}"})
            except BadRequest as e:
                self._json(400, {"error": str(e)})

        def _static(self, path: str) -> None:
            if static_dir is None or not static_dir.exists():
                return self._send(200, "웹 화면이 빌드되지 않았다: cd studio/web && npm run build".encode(), "text/plain; charset=utf-8")
            target = (static_dir / path.lstrip("/")).resolve()
            if static_dir.resolve() not in target.parents or not target.is_file():
                target = static_dir / "index.html"  # 화면 안 경로는 index.html로
            ctype = mimetypes.guess_type(target.name)[0] or "application/octet-stream"
            self._send(200, target.read_bytes(), ctype)

        def do_GET(self) -> None:  # noqa: N802
            self._dispatch("GET")

        def do_PUT(self) -> None:  # noqa: N802
            self._dispatch("PUT")

    return Handler


def serve(runs_root: Path, data_dir: Path, static_dir: Path | None, port: int = 18480) -> ThreadingHTTPServer:
    httpd = ThreadingHTTPServer(("127.0.0.1", port), make_handler(Store(runs_root, data_dir), static_dir))
    return httpd


__all__ = ["Store", "serve", "HTTPStatus"]
