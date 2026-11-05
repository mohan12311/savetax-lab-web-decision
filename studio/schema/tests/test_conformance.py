"""적합성 시험: 공용 예시(examples/)를 이 로더가 명세대로 받아들이고 거절하는가.

다른 언어의 로더(나중의 TypeScript)도 같은 예시로 같은 결과를 내야 한다.
"""

from fractions import Fraction
from pathlib import Path

import pytest

import studio_schema as s

EX = s.EXAMPLES_DIR


def files(kind: str, which: str) -> list[Path]:
    out = sorted((EX / kind / which).glob("*.json"))
    assert out, f"예시가 없다: {kind}/{which}"
    return out


@pytest.mark.parametrize("name", s.NAMES)
def test_valid_examples_load(name):
    for p in files(name, "valid"):
        s.load(p, name)


@pytest.mark.parametrize("name", s.NAMES)
def test_invalid_examples_rejected(name):
    for p in files(name, "invalid"):
        with pytest.raises(s.SchemaError):
            s.load(p, name)


def test_inline_has_no_refs_and_validates_same():
    import json

    from jsonschema import Draft202012Validator

    flat = s.inline("agent-answer")
    assert "$ref" not in json.dumps(flat)
    v = Draft202012Validator(flat)
    for p in files("agent-answer", "valid"):
        assert not list(v.iter_errors(json.loads(p.read_text(encoding="utf-8"))))
    for p in files("agent-answer", "invalid"):
        assert list(v.iter_errors(json.loads(p.read_text(encoding="utf-8")))), p.name


def test_money_parsers():
    assert s.krw("10890") == 10890
    for bad in ["10890.0", "1e4", " 1", "-1", "01", 10890]:
        with pytest.raises(ValueError):
            s.krw(bad)
    assert s.units("1/25") == Fraction(1, 25)
    assert s.units("20") == 20
    assert s.units("0.04") == Fraction(1, 25)
    for bad in ["0", "1/0", "1/25장", ""]:
        with pytest.raises((ValueError, ZeroDivisionError)):
            s.units(bad)
