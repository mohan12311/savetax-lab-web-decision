"""에이전트 쪽이 읽어도 되는 합성 데이터 파일만 여는 저장소.

정답 파일(cases.jsonl)은 평가 단계만 읽는다(CLAUDE.md 규칙 4). 이 저장소는 허용 목록의 파일만 열고,
그 밖의 경로는 거절한다. 도구 서버와 실행기는 이 클래스로만 합성 데이터를 읽는다.
"""

from __future__ import annotations

import hashlib
import json
import re
from functools import cached_property
from pathlib import Path
from typing import Any

from .attrs import required_for

_ALLOWED = re.compile(r"^(items\.json|tasks\.jsonl|shop/pages\.json|shop/html/[A-Za-z0-9_-]+\.html)$")


class ForbiddenPath(PermissionError):
    pass


class AgentData:
    def __init__(self, root: Path | str):
        self.root = Path(root).resolve()

    def _read(self, rel: str) -> bytes:
        if not _ALLOWED.match(rel):
            raise ForbiddenPath(f"에이전트 쪽에서 열 수 없는 파일이다: {rel}")
        p = (self.root / rel).resolve()
        if self.root not in p.parents:
            raise ForbiddenPath(f"데이터 디렉터리 밖이다: {rel}")
        return p.read_bytes()

    @cached_property
    def tasks(self) -> list[dict[str, Any]]:
        return [json.loads(line) for line in self._read("tasks.jsonl").decode("utf-8").splitlines() if line.strip()]

    def task(self, case_id: str) -> dict[str, Any]:
        for t in self.tasks:
            if t["case_id"] == case_id:
                return t
        raise KeyError(case_id)

    @cached_property
    def _items(self) -> dict[str, dict[str, Any]]:
        return {i["item_id"]: i for i in json.loads(self._read("items.json"))}

    @cached_property
    def _pages(self) -> dict[str, dict[str, Any]]:
        return {p["sku"]: p for p in json.loads(self._read("shop/pages.json"))}

    def item(self, item_id: str) -> dict[str, Any]:
        """가짜 대회 items/{item_id} 응답(납품 단가 P 포함). 코드 계산만 쓰고 에이전트에게는 그대로 주지 않는다."""
        return self._items[item_id]

    def facts(self, item_id: str) -> list[dict[str, Any]]:
        return self.item(item_id)["facts"]

    def procurement(self, item_id: str) -> dict[str, Any]:
        """에이전트에게 주는 조달 물품 정보. 납품 단가 P는 넣지 않는다(배율에 끌리지 않게)."""
        snap = self.item(item_id)["snapshot"]
        units = sorted({f["unit"] for f in self.facts(item_id)})
        return {
            "item_id": item_id,
            "name": snap["name"],
            "model": snap["model"],
            "manufacturer": snap["manufacturer"],
            "category": snap["category"],
            "unit": units[0] if len(units) == 1 else ", ".join(units),
        }

    def page(self, sku: str) -> tuple[str, str]:
        """판매 페이지 보관본(HTML 글, sha256)."""
        meta = self._pages[sku]
        raw = self._read(f"shop/{meta['html_file']}")
        return raw.decode("utf-8"), hashlib.sha256(raw).hexdigest()

    def required(self, item_id: str):
        return required_for(self.item(item_id)["snapshot"]["category"])
