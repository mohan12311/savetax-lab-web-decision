"""경고 탐지(답안지 명세 6절 「경고 단계」).

- danger: AI에게 하는 말. "AI"라는 낱말이 아니라 "AI를 부르며 무언가를 시키는 꼴"로 잡는다("AI 화질 개선" 같은
  상품 설명은 걸리지 않게). 숨김 여부와 의도를 가리지 않는다. 그 줄은 에이전트 입력에서 뺀다.
- notice(표시만): 숨은 글이 있다. 실제 사이트는 탭·메뉴에 display:none을 흔히 쓰므로 danger로 보지 않는다.
- caution(주의): 근거로 쓴 줄이 숨은 줄이다 — 확인 단계(checks)가 붙인다.

코드 규칙은 표현을 바꾸면 우회되므로, 놓친 지시문은 객관 신호(값 비교·계산 대조)가 다시 막는다.
"""

from __future__ import annotations

import re

from .lines import Line

DETECT_RULE = "warn-v1"

# AI를 부르는 말
_ADDRESS = re.compile(
    r"(\bAI\b|에이아이|인공\s*지능|\bLLM\b|\bGPT|claude|클로드|챗봇|어시스턴트|assistant|검토\s*도우미|도우미에게|에이전트|\bagent\b|"
    r"언어\s*모델|language model|\bbot\b|봇에게|자동\s*검토|크롤러|scraper)",
    re.I,
)
# 시키는 말
_IMPERATIVE = re.compile(
    r"(하세요|하십시오|해\s*주세요|해라|하라|할\s*것|하시오|보고하|답하|출력하|판정하|분류하|무시|따르|간주하|"
    r"\bignore\b|\bdisregard\b|\breport\b|\brespond\b|\boutput\b|\byou must\b|\bclassify\b|\btreat\b|\bsay\b)",
    re.I,
)
# 부르는 말이 없어도 지시를 뒤집으려는 꼴
_OVERRIDE = re.compile(
    r"((앞선|이전|위의|기존)\s*(모든\s*)?(지시|명령|규칙|지침)\S*\s*(은|는|을|를)?\s*(모두\s*)?(무시|잊)|"
    r"ignore\s+(all\s+)?(previous|prior|above)\s+(instructions|rules)|system\s*prompt|시스템\s*프롬프트)",
    re.I,
)


def is_ai_instruction(text: str) -> bool:
    if _OVERRIDE.search(text):
        return True
    return bool(_ADDRESS.search(text) and _IMPERATIVE.search(text))


def detect(lines: list[Line]) -> list[dict]:
    out = []
    for ln in lines:
        if is_ai_instruction(ln.text):
            out.append({"level": "danger", "kind": "ai_instruction", "line": ln.line, "text": ln.text})
        elif ln.hidden:
            out.append({"level": "notice", "kind": "hidden_text", "line": ln.line, "text": ln.text})
    return out
