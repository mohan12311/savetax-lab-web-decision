"""답안지 라이브러리(답안지 명세 studio/docs/answer-sheet.md).

판단은 LLM이, 숫자와 확인은 코드가 한다. 이 패키지는 코드 쪽이다.

    data      에이전트 쪽이 읽어도 되는 파일만 여는 저장소(정답 파일 cases.jsonl은 열지 않는다)
    lines     원문 HTML → 줄 번호 원문(숨은 줄 표시)
    warn      경고 탐지(AI에게 하는 말, 숨은 글)
    attrs     품목별 필수 속성 목록
    prep      입력 준비(줄·경고·에이전트에게 줄 줄 목록)
    evidence  근거 첨부(그 줄에 그 글자가 있는지 확인하고 ID를 발급, 발급 기록 파일)
    calc      Go 계산기(collector/cmd/studiocalc) 호출
    checks    객관 신호와 사건 단위 확인
    route     처리 규칙(자동 통과 / 사람)
    assemble  에이전트 답 + 코드 계산 + 확인 + 처리 → 답안지
    mcp_server 에이전트 도구 MCP 서버(stdio, 사건 하나에 프로세스 하나)
"""

VERSION = "0.1.0"
