"""근거 첨부(답안지 명세 5절). 출처·줄 번호·글자를 받아 그 자리에 그 글자가 있는지 확인하고 근거 ID를 발급한다.

"그 글자가 원문에 있다"만 보장하고 "그 근거가 주장과 맞다"는 보장하지 않는다(그건 checks의 값 비교).
발급과 거절을 모두 발급 기록 파일(JSON Lines)에 남긴다. 실행 뒤 확인과 채점이 이 파일을 읽는다.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from .prep import CaseInput

PROCUREMENT_FIELDS = ("name", "model", "manufacturer", "category", "unit")
MAX_TEXT = 200


class EvidenceBook:
    def __init__(self, case: CaseInput, log_path: Path | None):
        self.case = case
        self.log_path = log_path
        self.issued: dict[str, dict[str, Any]] = {}
        self.rejections = 0
        if log_path is not None:
            log_path.parent.mkdir(parents=True, exist_ok=True)
            log_path.write_text("", encoding="utf-8")

    def _log(self, rec: dict[str, Any]) -> None:
        if self.log_path is not None:
            with self.log_path.open("a", encoding="utf-8") as f:
                f.write(json.dumps(rec, ensure_ascii=False) + "\n")

    def _reject(self, reason: str, req: dict[str, Any]) -> dict[str, Any]:
        self.rejections += 1
        self._log({"rejected": True, "reason": reason, **req})
        return {"ok": False, "error": reason}

    def attach(self, source: str, text: str, line: int | None = None, field: str | None = None) -> dict[str, Any]:
        req = {"source": source, "line": line, "field": field, "text": text}
        if not isinstance(text, str) or not text.strip():
            return self._reject("글자가 비어 있다", req)
        if len(text) > MAX_TEXT:
            return self._reject(f"글자는 {MAX_TEXT}자 이하로 짧게 고른다(값만)", req)
        ev: dict[str, Any]
        if source == "procurement":
            fields = [field] if field else list(PROCUREMENT_FIELDS)
            if field and field not in PROCUREMENT_FIELDS:
                return self._reject(f"조달 물품의 칸은 {', '.join(PROCUREMENT_FIELDS)}다", req)
            hit = next((f for f in fields if text in str(self.case.procurement.get(f, ""))), None)
            if hit is None:
                return self._reject("조달 물품 정보에 그 글자가 없다(글자 그대로 옮긴다)", req)
            ev = {"source": "procurement", "line": None, "field": hit, "text": text}
        elif source == "page":
            if not isinstance(line, int):
                return self._reject("판매 페이지 근거에는 줄 번호가 필요하다", req)
            if line in self.case.blocked:
                return self._reject("그 줄은 근거로 쓸 수 없다(입력에서 뺀 줄)", req)
            ln = self.case.line(line)
            if ln is None:
                return self._reject(f"{line}번 줄이 없다", req)
            if text not in ln.text:
                return self._reject(f"{line}번 줄에 그 글자가 없다(글자 그대로 옮긴다). 그 줄: {ln.text}", req)
            ev = {"source": "page", "line": line, "text": text, "hidden": ln.hidden}
        elif source == "site_policy":
            return self._reject("이 사이트에 확인된 정책이 없다", req)
        else:
            return self._reject("출처는 procurement, page, site_policy 가운데 하나다", req)
        for eid, old in self.issued.items():
            if old == ev:
                return {"ok": True, "id": eid, **ev}
        eid = f"ev-{len(self.issued) + 1}"
        self.issued[eid] = ev
        self._log({"id": eid, **ev})
        return {"ok": True, "id": eid, **ev}


def read_log(path: Path) -> tuple[dict[str, dict[str, Any]], int]:
    """발급 기록 파일 → (발급한 근거, 거절 횟수)."""
    issued: dict[str, dict[str, Any]] = {}
    rejected = 0
    if not path.exists():
        return issued, rejected
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        rec = json.loads(line)
        if rec.get("rejected"):
            rejected += 1
            continue
        eid = rec.pop("id")
        issued[eid] = rec
    return issued, rejected
