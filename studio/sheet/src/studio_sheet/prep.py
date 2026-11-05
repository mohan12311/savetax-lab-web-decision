"""입력 준비(답안지 명세 3절): 보관 원문 → 줄 번호 원문, 경고, 에이전트에게 줄 줄 목록."""

from __future__ import annotations

from dataclasses import dataclass

from . import lines as L
from . import warn
from .attrs import Attr
from .data import AgentData


@dataclass
class CaseInput:
    case_id: str
    item_id: str
    sku: str
    page_url: str
    read_mode: str
    source_sha256: str
    lines: list[L.Line]  # 사람 화면용(전부)
    warnings: list[dict]
    procurement: dict  # 에이전트에게 주는 조달 물품 정보(P 없음)
    required: list[Attr] | None

    @property
    def blocked(self) -> set[int]:
        """에이전트 입력에서 뺀 줄(AI에게 하는 말)."""
        return {w["line"] for w in self.warnings if w["level"] == "danger" and w["line"] is not None}

    @property
    def agent_lines(self) -> list[L.Line]:
        b = self.blocked
        return [ln for ln in self.lines if ln.line not in b]

    def line(self, n: int) -> L.Line | None:
        for ln in self.lines:
            if ln.line == n:
                return ln
        return None


def prepare(data: AgentData, case_id: str) -> CaseInput:
    t = data.task(case_id)
    html, sha = data.page(t["sku"])
    ls = L.split(html)
    return CaseInput(
        case_id=case_id, item_id=t["item_id"], sku=t["sku"], page_url=t["page_url"], read_mode="static",
        source_sha256=sha, lines=ls, warnings=warn.detect(ls), procurement=data.procurement(t["item_id"]),
        required=data.required(t["item_id"]),
    )
