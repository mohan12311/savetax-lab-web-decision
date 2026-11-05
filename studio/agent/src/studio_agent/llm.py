"""모델 호출과 비용 계산. 실행기는 LLM 프로토콜(Anthropic 모양의 메시지·응답)만 본다.

공급자: OpenAI(기본, openai_llm.OpenAILLM), Anthropic(AnthropicLLM), 규칙 기준선(baseline). 테스트는 각본 LLM을 쓴다.
API 키는 이 프로젝트 전용 환경변수(SAVETAX_STUDIO_OPENAI_KEY / SAVETAX_STUDIO_ANTHROPIC_KEY)에서만 읽고, 전역
OPENAI_API_KEY·ANTHROPIC_API_KEY로 넘어가지 않는다. 코드·로그·명령줄에 키를 두지 않는다(CLAUDE.md 규칙 2).
Makefile의 studio-run 목표가 키체인(service=savetax-lab, 같은 이름의 account)에서 이 프로세스에만 넘긴다.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal
from typing import Any, Protocol

from .openai_llm import DEFAULT_OPENAI_MODEL, OPENAI_PRICES

# 1M 토큰당 달러(Anthropic 공시 단가, 2026-10 기준). 캐시 쓰기는 입력의 1.25배, 캐시 읽기는 모델별 공시값.
PRICES: dict[str, dict[str, Decimal]] = {
    "claude-opus-5-5": {"in": Decimal("4"), "out": Decimal("20"), "cache_read": Decimal("0.20")},
    "claude-sonnet-5-5": {"in": Decimal("2"), "out": Decimal("10"), "cache_read": Decimal("0.20")},
    "claude-haiku-5-5": {"in": Decimal("0.10"), "out": Decimal("0.50"), "cache_read": Decimal("0.01")},
}
PRICES.update(OPENAI_PRICES)
DEFAULT_MODEL = DEFAULT_OPENAI_MODEL  # 11/05 사용자 결정: ChatGPT(OpenAI) API 키로 실행한다
FALLBACK_MODELS = ("claude-opus-5-5", "claude-sonnet-5-5")  # 서버 쪽 거절 대체를 쓰는 모델(하이쿠는 없다)


@dataclass
class Usage:
    tokens_in: int = 0
    tokens_out: int = 0
    cache_write: int = 0
    cache_read: int = 0

    def add(self, u: Any) -> None:
        self.tokens_in += getattr(u, "input_tokens", 0) or 0
        self.tokens_out += getattr(u, "output_tokens", 0) or 0
        self.cache_write += getattr(u, "cache_creation_input_tokens", 0) or 0
        self.cache_read += getattr(u, "cache_read_input_tokens", 0) or 0

    def cost(self, model: str) -> Decimal:
        p = PRICES.get(model)
        if p is None:
            return Decimal(0)
        m = Decimal(1_000_000)
        write = p.get("cache_write", p["in"] * Decimal("1.25"))  # Anthropic은 입력의 1.25배
        return (Decimal(self.tokens_in) * p["in"] + Decimal(self.cache_write) * write
                + Decimal(self.cache_read) * p["cache_read"] + Decimal(self.tokens_out) * p["out"]) / m


class LLM(Protocol):
    model: str

    def create(self, *, system: list[dict], tools: list[dict], messages: list[dict]) -> Any: ...


class AnthropicLLM:
    def __init__(self, model: str = DEFAULT_MODEL, effort: str = "medium", max_tokens: int = 16000, fallback: bool = True):
        import os

        import anthropic

        key = os.environ.get("SAVETAX_STUDIO_ANTHROPIC_KEY")
        if not key:
            raise RuntimeError("SAVETAX_STUDIO_ANTHROPIC_KEY가 비어 있다. 키체인(service=savetax-lab)에 넣고 make studio-run으로 실행한다")
        self.client = anthropic.Anthropic(api_key=key)
        self.model = model
        self.effort = effort
        self.max_tokens = max_tokens
        self.fallback = fallback and model in FALLBACK_MODELS

    def create(self, *, system: list[dict], tools: list[dict], messages: list[dict]) -> Any:
        kw: dict[str, Any] = {}
        if self.fallback:
            # 안전 분류기가 거절하면 서버가 같은 요청을 대체 모델로 다시 돌린다. 실제로 답한 모델은 response.model에 남는다.
            kw = {"betas": ["server-side-fallback-2026-07-01"], "fallbacks": "default"}
        return self.client.beta.messages.create(
            model=self.model,
            max_tokens=self.max_tokens,
            system=system,
            tools=tools,
            messages=messages,
            output_config={"effort": self.effort},
            cache_control={"type": "ephemeral"},
            **kw,
        )
