"""Go 계산기(collector/cmd/studiocalc) 호출. 숫자는 코드가 맡는다(답안지 명세 1절).

- evaluate: 에이전트의 계산기 도구(MCP로 노출). 분수로 정확히 계산하고 ceil·floor를 쓸 수 있다.
- benchmark: 읽은 값 → 비교 단가(코드 계산). 노출하지 않는다.
- gap: 1부 claim.Evaluate로 가격 차이 기준과 예상액. 노출하지 않는다.

생성기(studio_synth, Python Fraction)와 다른 언어로 계산해 채점 명세 3.4절의 계산 규칙 차이를 잡을 수 있게 한다.
"""

from __future__ import annotations

import json
import os
import subprocess
from functools import cache
from pathlib import Path
from typing import Any

REPO = Path(__file__).resolve().parents[4]
DEFAULT_BIN = REPO / "studio" / "demo" / "bin" / "studiocalc"


class CalcError(RuntimeError):
    pass


@cache
def binary() -> Path:
    env = os.environ.get("STUDIO_CALC_BIN")
    if env:
        return Path(env)
    if not DEFAULT_BIN.exists():
        DEFAULT_BIN.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["go", "build", "-o", str(DEFAULT_BIN), "./cmd/studiocalc"], cwd=REPO / "collector", check=True,
                       env={k: v for k, v in os.environ.items() if k not in ("SAVETAX_TOKEN", "ANTHROPIC_API_KEY")})
    return DEFAULT_BIN


def call(req: dict[str, Any]) -> dict[str, Any]:
    env = {"PATH": os.environ.get("PATH", ""), "HOME": os.environ.get("HOME", "")}  # 비밀 값은 넘기지 않는다
    p = subprocess.run([str(binary())], input=json.dumps(req, ensure_ascii=False), capture_output=True, text=True, env=env,
                       timeout=10)
    if p.returncode != 0:
        raise CalcError(p.stderr.strip() or f"studiocalc 종료 코드 {p.returncode}")
    return json.loads(p.stdout)


def evaluate(expr: str) -> dict[str, Any]:
    return call({"op": "eval", "expr": expr})


def benchmark(sale_price_krw: int, vat: str, conversion: str) -> int:
    r = call({"op": "benchmark", "sale_price_krw": str(sale_price_krw), "vat": vat, "procurement_units_per_sale_unit": conversion})
    if not r.get("ok"):
        raise CalcError(r.get("error", "benchmark 실패"))
    return int(r["benchmark_krw"])


def gap(facts: list[dict[str, Any]], benchmark_krw: int) -> dict[str, Any]:
    r = call({"op": "gap", "facts": facts, "benchmark_krw": str(benchmark_krw)})
    if not r.get("ok"):
        raise CalcError(r.get("error", "gap 실패"))
    return r
