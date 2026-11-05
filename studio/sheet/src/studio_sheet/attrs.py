"""품목별 필수 속성 목록(답안지 명세 4절).

"그 물건을 살 때 무엇을 보나"로 쓴다. 합성 데이터 생성기의 함정 목록을 베끼지 않는다.
규격 같은 속성뿐 아니라 물건의 정체(본품인가, 새 제품인가, 모델 코드가 끝까지 같은가)를 묻는 줄도 넣는다.
속성 이름은 정답의 차이 속성과 같은 어휘다(채점 명세 2절).
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class Attr:
    name: str
    hint: str


# 11/05 사용자 결정: 판매 페이지는 새 제품이라고 일부러 적지 않는 경우가 많다
STATE_HINT = "새 제품인가. 리퍼비시·중고·전시품·반품 같은 표시가 없으면 새 제품으로 보고 not_on_page로 둔다(보류 사유가 아니다)"

_IDENTITY = [
    Attr("모델", "모델 코드. 접미사까지 끝까지 같은가. 페이지에 없으면 not_on_page"),
    Attr("구성", "본품인가, 거치대·부속품·일부 구성품인가"),
]

REQUIRED: dict[str, list[Attr]] = {
    "보차도용콘크리트블록": [
        Attr("규격", "가로×세로(mm)"),
        Attr("두께", "두께(mm, 60T = 60mm)"),
        Attr("종류", "재료·형태(예: 인조화강블록, 점토블록)"),
        Attr("투수 여부", "투수 / 불투수"),
        *_IDENTITY,
    ],
    "벤치플룸관": [
        Attr("규격", "폭×높이×길이(mm)"),
        Attr("종류", "등급(1종·2종·3종)"),
        *_IDENTITY,
    ],
    "그레이팅": [
        Attr("규격", "가로×세로×두께(mm)"),
        Attr("재질", "TPE·주철·스틸 등"),
        *_IDENTITY,
    ],
    "액정모니터": [
        *_IDENTITY,
        Attr("화면 크기", "대각선(cm)"),
        Attr("상태", STATE_HINT),
    ],
    "전자칠판": [
        *_IDENTITY,
        Attr("화면 크기", "대각선(cm)"),
        Attr("상태", STATE_HINT),
    ],
}


# 물건의 정체를 묻는 속성. 글자를 맞대 볼 값이 아니라 판단이라 객관 신호의 글자 비교를 하지 않는다(checks.check_row).
IDENTITY = frozenset({"구성", "상태"})


def required_for(category: str) -> list[Attr] | None:
    """목록이 없는 새 품목이면 None(처리 규칙 2번: 사람)."""
    return REQUIRED.get(category)
