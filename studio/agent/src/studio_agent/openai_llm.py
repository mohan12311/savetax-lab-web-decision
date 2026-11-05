"""OpenAI(ChatGPT API) 어댑터. 실행기의 LLM 프로토콜(Anthropic 모양의 메시지·응답)을 OpenAI Responses API로 옮긴다.

실행기는 공급자를 모른다. 이 어댑터가
- 도구 정의를 Responses API의 function 도구로,
- 실행기가 쌓는 메시지(tool_result 블록·사용자 글)를 function_call_output·user 입력 항목으로,
- 응답 출력(function_call·message)을 tool_use·text 블록으로
바꾼다.

상태 없이(store=False) 쓴다: 대화마다 입력 항목 목록을 이 어댑터가 들고 있고, 응답의 출력 항목(추론 항목 포함)을
그대로 다시 보낸다(OpenAI 문서 "Preserve reasoning without stored responses"). 판매 페이지 글이 OpenAI 쪽에 저장되지 않는다.

API 키는 이 프로젝트 전용 환경변수 SAVETAX_STUDIO_OPENAI_KEY에서만 읽는다(Makefile이 키체인 account=SAVETAX_STUDIO_OPENAI_KEY에서
넘긴다). 셸에 있을 수 있는 전역 OPENAI_API_KEY로는 넘어가지 않는다. 코드·로그·명령줄에 두지 않는다(CLAUDE.md 규칙 2).
"""

from __future__ import annotations

import json
from decimal import Decimal
from typing import Any

# 1M 토큰당 달러(OpenAI 공시 단가, developers.openai.com/api/docs/pricing·models, 2026-11 확인, 입력 27.2만 토큰 이하 기준).
# GPT-6 계열은 캐시 쓰기(30분 보존)가 입력의 1.25배로 따로 붙는다. 그 밖의 모델은 캐시 쓰기 요금이 없다.
OPENAI_PRICES: dict[str, dict[str, Decimal]] = {
    "gpt-6.1-terra": {"in": Decimal("1"), "out": Decimal("5"), "cache_read": Decimal("0.05"), "cache_write": Decimal("1.25")},
    "gpt-6.1-sol": {"in": Decimal("2"), "out": Decimal("10"), "cache_read": Decimal("0.10"), "cache_write": Decimal("2.50")},
    "gpt-5.4-mini": {"in": Decimal("0.75"), "out": Decimal("4.50"), "cache_read": Decimal("0.075"), "cache_write": Decimal("0")},
    "gpt-5.4-nano": {"in": Decimal("0.20"), "out": Decimal("1.25"), "cache_read": Decimal("0.02"), "cache_write": Decimal("0")},
    "gpt-5.2": {"in": Decimal("1.75"), "out": Decimal("14.00"), "cache_read": Decimal("0.175"), "cache_write": Decimal("0")},
    "gpt-5.1": {"in": Decimal("1.25"), "out": Decimal("10.00"), "cache_read": Decimal("0.125"), "cache_write": Decimal("0")},
    "gpt-5": {"in": Decimal("1.25"), "out": Decimal("10.00"), "cache_read": Decimal("0.125"), "cache_write": Decimal("0")},
    "gpt-5-mini": {"in": Decimal("0.25"), "out": Decimal("2.00"), "cache_read": Decimal("0.025"), "cache_write": Decimal("0")},
    "gpt-5-nano": {"in": Decimal("0.05"), "out": Decimal("0.40"), "cache_read": Decimal("0.005"), "cache_write": Decimal("0")},
    "gpt-4.1": {"in": Decimal("2.00"), "out": Decimal("8.00"), "cache_read": Decimal("0.50"), "cache_write": Decimal("0")},
    "gpt-4.1-mini": {"in": Decimal("0.40"), "out": Decimal("1.60"), "cache_read": Decimal("0.10"), "cache_write": Decimal("0")},
}
DEFAULT_OPENAI_MODEL = "gpt-6.1-terra"
KEY_ENV = "SAVETAX_STUDIO_OPENAI_KEY"


class _B:
    def __init__(self, **kw: Any):
        self.__dict__.update(kw)


def _reasoning_model(model: str) -> bool:
    return model.startswith(("gpt-5", "gpt-6", "o1", "o3", "o4"))


def _any_of(node: Any) -> Any:
    """oneOf → anyOf. 우리 스키마의 oneOf는 null과 객체처럼 겹치지 않는 경우뿐이라 뜻이 같다(함수 도구 스키마 호환)."""
    if isinstance(node, dict):
        return {("anyOf" if k == "oneOf" else k): _any_of(v) for k, v in node.items()}
    if isinstance(node, list):
        return [_any_of(v) for v in node]
    return node


def to_openai_tools(tools: list[dict]) -> list[dict]:
    return [{"type": "function", "name": t["name"], "description": t.get("description", ""),
             "parameters": _any_of(t["input_schema"]), "strict": False} for t in tools]


def _user_items(content: Any) -> list[dict]:
    if isinstance(content, str):
        return [{"role": "user", "content": content}]
    out = []
    for b in content:
        if b.get("type") == "tool_result":
            text = b["content"] if isinstance(b["content"], str) else json.dumps(b["content"], ensure_ascii=False)
            out.append({"type": "function_call_output", "call_id": b["tool_use_id"],
                        "output": ("[오류] " + text) if b.get("is_error") else text})
        elif b.get("type") == "text":
            out.append({"role": "user", "content": b["text"]})
    return out


def from_openai(resp: Any, model: str) -> _B:
    """Responses API 응답 → 실행기가 읽는 모양(content 블록, stop_reason, usage, model)."""
    content: list[_B] = []
    refused = False
    for item in resp.output:
        if item.type == "function_call":
            try:
                args = json.loads(item.arguments) if item.arguments else {}
            except ValueError:
                args = {"_invalid_json": item.arguments}
            content.append(_B(type="tool_use", id=item.call_id, name=item.name, input=args))
        elif item.type == "message":
            for part in item.content:
                if part.type == "output_text":
                    content.append(_B(type="text", text=part.text))
                elif part.type == "refusal":
                    refused = True
    reason = getattr(getattr(resp, "incomplete_details", None), "reason", None)
    if refused or reason == "content_filter":
        stop = "refusal"
    elif resp.status == "incomplete" and reason == "max_output_tokens":
        stop = "max_tokens"
    elif any(b.type == "tool_use" for b in content):
        stop = "tool_use"
    else:
        stop = "end_turn"
    u = resp.usage
    details = getattr(u, "input_tokens_details", None)
    cached = getattr(details, "cached_tokens", 0) or 0
    written = getattr(details, "cache_write_tokens", 0) or 0
    # input_tokens는 캐시 읽기·쓰기를 포함한 전체다. 나머지가 보통 입력이다(OpenAI 문서 "Calculate input cost")
    usage = _B(input_tokens=max((u.input_tokens or 0) - cached - written, 0), output_tokens=u.output_tokens or 0,
               cache_creation_input_tokens=written, cache_read_input_tokens=cached)
    served = resp.model if not str(resp.model).startswith(model) else model
    return _B(content=content or [_B(type="text", text="")], stop_reason=stop, usage=usage, model=served)


class OpenAILLM:
    def __init__(self, model: str = DEFAULT_OPENAI_MODEL, effort: str = "medium", max_output_tokens: int = 16000,
                 client: Any = None):
        if client is None:
            import os

            from openai import OpenAI

            key = os.environ.get(KEY_ENV)
            if not key:
                raise RuntimeError(f"{KEY_ENV}가 비어 있다. 키체인(service=savetax-lab, account={KEY_ENV})에 넣고 make studio-run으로 실행한다")
            client = OpenAI(api_key=key)
        self.client = client
        self.model = model
        self.effort = effort
        self.max_output_tokens = max_output_tokens
        self._convs: dict[int, list[dict]] = {}

    def create(self, *, system: list[dict], tools: list[dict], messages: list[dict]) -> Any:
        key = id(messages)  # 실행기는 대화마다 messages 목록 하나를 계속 늘려 쓴다
        if len(messages) == 1:
            self._convs[key] = []
        items = self._convs[key]
        items.extend(_user_items(messages[-1]["content"]))
        kw: dict[str, Any] = {}
        if _reasoning_model(self.model):
            kw["reasoning"] = {"effort": self.effort}
        resp = self.client.responses.create(
            model=self.model,
            instructions="\n\n".join(b["text"] for b in system),
            input=items,
            tools=to_openai_tools(tools),
            store=False,
            max_output_tokens=self.max_output_tokens,
            prompt_cache_key="savetax-studio-agent",
            **kw,
        )
        # 출력 항목(추론·함수 호출·메시지)을 그대로 다음 입력에 이어 붙인다
        items.extend(item.model_dump(exclude_none=True, exclude={"status"}) for item in resp.output)
        return from_openai(resp, self.model)
