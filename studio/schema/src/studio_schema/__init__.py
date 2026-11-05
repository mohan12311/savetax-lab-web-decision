"""답안지 JSON Schema의 Python 로더. 형식만 다룬다(판단 로직 없음).

원본은 저장소의 `studio/schema/*.schema.json`이다. 다른 언어의 로더도 같은 파일과 같은 예시(`examples/`)로
적합성 시험을 한다: valid/는 모두 받아들이고 invalid/는 모두 거절해야 한다.

금액은 정수 원 십진 문자열로만 오간다. 여기서 int·Fraction으로 바꾸고 float는 쓰지 않는다.
"""

from __future__ import annotations

import copy
import json
import re
from decimal import Decimal
from fractions import Fraction
from functools import cache
from pathlib import Path
from typing import Any

from jsonschema import Draft202012Validator
from referencing import Registry, Resource

SCHEMA_DIR = Path(__file__).resolve().parents[2]
EXAMPLES_DIR = SCHEMA_DIR / "examples"
SCHEMA_VERSION = "answer-sheet/v1"
NAMES = ("answer-sheet", "agent-answer", "judgment")

_KRW = re.compile(r"^(0|[1-9][0-9]*)$")
_UNITS = re.compile(r"^[0-9]+(\.[0-9]+)?(/[0-9]+(\.[0-9]+)?)?$")
_USD = re.compile(r"^(0|[1-9][0-9]*)(\.[0-9]+)?$")


class SchemaError(ValueError):
    """스키마에 맞지 않는다. errors에 위치별 설명이 있다."""

    def __init__(self, what: str, errors: list[str]):
        super().__init__(f"{what}: " + "; ".join(errors[:5]) + (f" 외 {len(errors) - 5}건" if len(errors) > 5 else ""))
        self.errors = errors


@cache
def schema(name: str) -> dict[str, Any]:
    if name not in NAMES:
        raise KeyError(name)
    return json.loads((SCHEMA_DIR / f"{name}.schema.json").read_text(encoding="utf-8"))


@cache
def _registry() -> Registry:
    return Registry().with_resources((schema(n)["$id"], Resource.from_contents(schema(n))) for n in NAMES)


@cache
def validator(name: str) -> Draft202012Validator:
    return Draft202012Validator(schema(name), registry=_registry())


def errors(name: str, obj: Any) -> list[str]:
    """스키마 위반 목록(없으면 빈 목록). 위치는 JSON 경로로 적는다."""
    out = []
    for e in sorted(validator(name).iter_errors(obj), key=lambda e: list(e.absolute_path)):
        where = "/".join(str(p) for p in e.absolute_path) or "(맨 위)"
        out.append(f"{where}: {e.message}")
    return out


def check(name: str, obj: Any) -> Any:
    errs = errors(name, obj)
    if errs:
        raise SchemaError(name, errs)
    return obj


def load(path: Path | str, name: str = "answer-sheet") -> dict[str, Any]:
    """파일을 읽고 검증한다. JSON 소수는 Decimal로 읽는다(금액 칸은 스키마가 문자열만 받는다)."""
    obj = json.loads(Path(path).read_text(encoding="utf-8"), parse_float=Decimal)
    return check(name, _plain(obj))


def _plain(obj: Any) -> Any:
    """Decimal로 읽은 숫자(확신도 등 금액이 아닌 칸)를 다시 float로 돌린다."""
    if isinstance(obj, dict):
        return {k: _plain(v) for k, v in obj.items()}
    if isinstance(obj, list):
        return [_plain(v) for v in obj]
    if isinstance(obj, Decimal):
        return float(obj)
    return obj


def inline(name: str) -> dict[str, Any]:
    """$ref를 모두 풀어 한 덩어리로 만든다(LLM 도구의 input_schema용). 순환 참조는 없다고 가정한다."""
    root = copy.deepcopy(schema(name))

    def resolve(ref: str, base: dict[str, Any]) -> dict[str, Any]:
        doc, _, frag = ref.partition("#")
        target = base if not doc else schema(doc.removesuffix(".schema.json"))
        node: Any = target
        for part in frag.strip("/").split("/") if frag else []:
            node = node[part]
        return walk(copy.deepcopy(node), target)

    def walk(node: Any, base: dict[str, Any]) -> Any:
        if isinstance(node, dict):
            if "$ref" in node:
                extra = {k: v for k, v in node.items() if k != "$ref"}
                out = resolve(node["$ref"], base)
                out.update(extra)
                return out
            return {k: walk(v, base) for k, v in node.items() if k not in ("$defs", "$schema", "$id")}
        if isinstance(node, list):
            return [walk(v, base) for v in node]
        return node

    return walk(root, root)


def krw(s: str) -> int:
    """정수 원 십진 문자열 → int. 소수·지수·공백·float는 거절한다."""
    if not isinstance(s, str) or not _KRW.match(s):
        raise ValueError(f"정수 원 문자열이 아니다: {s!r}")
    return int(s)


def units(s: str) -> Fraction:
    """환산 값("1", "20", "1/25", "0.04") → 양의 Fraction."""
    if not isinstance(s, str) or not _UNITS.match(s):
        raise ValueError(f"환산 값이 아니다: {s!r}")
    num, _, den = s.partition("/")
    v = Fraction(num) / (Fraction(den) if den else 1)
    if v <= 0:
        raise ValueError(f"환산 값은 양수여야 한다: {s!r}")
    return v


def usd(s: str) -> Decimal:
    if not isinstance(s, str) or not _USD.match(s):
        raise ValueError(f"달러 문자열이 아니다: {s!r}")
    return Decimal(s)
