"""점수표를 사람이 읽는 Markdown으로(채점 명세 5절의 순서: 0건 조건 → 결론 → 숫자 → 이유 → 처리 → 진단 → 비용)."""

from __future__ import annotations

from typing import Any

LABEL_KO = {"same_model": "같은 모델", "equivalent": "동급", "different": "다른 물건", "insufficient": "근거 부족", "hold": "판단 보류"}


def markdown(card: dict[str, Any]) -> str:
    z = card["zero_conditions"]
    out = [f"# 점수표 — {card['run_id']}", "",
           f"모델 `{card['model']}`, 프롬프트 `{card['prompt_version']}`, 채점한 사건 {card['graded_cases']}/{card['truth_cases']}", "",
           "## 반드시 0건 조건", "",
           f"- 결과: **{'통과' if z['pass'] else '탈락'}**",
           f"- danger 사건의 자동 통과: {len(z['danger_auto_passed'])}건 {z['danger_auto_passed'] or ''}",
           f"- 발급되지 않은 근거 ID: {len(z['unissued_evidence'])}건 {[u['case_id'] for u in z['unissued_evidence']] or ''}",
           f"- 단정 표현의 자동 통과: {len(z['assertive_auto_passed'])}건 {z['assertive_auto_passed'] or ''}", ""]
    names = [k for k in card["groups"]]
    g = card["groups"]

    def row(title: str, f) -> str:
        return f"| {title} | " + " | ".join(str(f(g[k])) for k in names) + " |"

    out += ["## 묶음별", "", "| 항목 | " + " | ".join(names) + " |", "|---|" + "---|" * len(names),
            row("사건 수", lambda b: b["cases"]),
            row("결론 정밀도(헛짚지 않기)", lambda b: b["conclusion"]["precision"]["text"]),
            row("결론 재현율(놓치지 않기, 보류 제외)", lambda b: b["conclusion"]["recall"]["text"]),
            row("보류한 N배", lambda b: b["conclusion"]["held_n_times"]),
            row("보류율", lambda b: b["conclusion"]["hold_rate"]["text"]),
            row("라벨 정확도", lambda b: b["labels"]["accuracy"]["text"]),
            row("동급을 같은 모델로", lambda b: len(b["labels"]["equivalent_as_same_model"])),
            row("비교 단가 정확 일치", lambda b: b["benchmark"]["exact"]["text"]),
            row("비교 단가 허용범위(±1%) 안 오차", lambda b: b["benchmark"]["within_tolerance"]),
            row("비교 단가 범위 밖", lambda b: b["benchmark"]["outside"]),
            row("이유 점수(동급)", lambda b: b["reason"]["score"]["text"]),
            row("자동 통과", lambda b: b["routing"]["auto"]),
            row("자동 통과 중 오답", lambda b: len(b["routing"]["auto_wrong"])),
            row("사람에게 감", lambda b: b["routing"]["human"]),
            row("사람에게 갔지만 에이전트가 맞음", lambda b: b["routing"]["human_but_agent_right"]),
            row("근거 첨부 거절(실행 중)", lambda b: b["diagnostics"]["attach_rejections"]),
            row("사람 판정 수", lambda b: b["human"]["judged"]),
            row("사람 ↔ 에이전트 결론 일치", lambda b: b["human"]["agree_with_agent"]["text"]),
            row("사람 ↔ 정답 결론 일치", lambda b: b["human"]["agree_with_truth"]["text"]),
            row("총비용(USD)", lambda b: b["cost"].get("total_usd", "-")),
            row("건당 비용 중앙값/최댓값", lambda b: f"{b['cost']['per_case_usd']['median']} / {b['cost']['per_case_usd']['max']}"
                if b["cost"] else "-"),
            row("여러 번 실행한 비율", lambda b: b["cost"]["repeated"]["text"] if b["cost"] else "-"),
            ""]
    a = g["all"]
    out += ["## 결론이 틀린 사건", "",
            f"- 라벨이 틀림(판단 문제): {a['conclusion']['wrong_by_label'] or '없음'}",
            f"- 라벨은 맞고 가격 차이 판정이 틀림(읽기 문제): {a['conclusion']['wrong_by_gap'] or '없음'}", "",
            "## 라벨 표(행: 정답, 열: 에이전트)", "",
            "| 정답 \\ 에이전트 | " + " | ".join(LABEL_KO.values()) + " |", "|---|" + "---|" * 5]
    for t, cols in a["labels"]["matrix"].items():
        out.append(f"| {LABEL_KO[t]} | " + " | ".join(str(cols[c]) for c in LABEL_KO) + " |")
    out += ["", "## 비교 단가가 틀린 사건", ""]
    if a["benchmark"]["misses"]:
        out += ["| 사건 | 칸 | 에이전트 | 정답 | 방향 | 원인 |", "|---|---|---|---|---|---|"]
        for m in a["benchmark"]["misses"]:
            out.append(f"| {m['case_id']} | {m['kind']} | {m['got']} | {m['want']} | {m['direction'] or '-'} | {', '.join(m['cause'])} |")
    else:
        out.append("없음")
    out += ["", "## 처리: 사람에게 간 이유(답안지 명세 7절 번호)", "",
            ", ".join(f"{k}번 {v}건" for k, v in a["routing"]["human_by_rule"].items()) or "없음", "",
            "## hard 사례", "", "| 사례 | 함정 | 결과 | 틀린 칸 | 처리 |", "|---|---|---|---|---|"]
    for h in card["hard_cases"]:
        out.append(f"| {h['case_id']} | {', '.join(h['traps'])} | {'통과' if h['pass'] else '실패'} | {'; '.join(h['wrong']) or '-'} | {h['routing']} |")
    d = a["diagnostics"]
    out += ["", "## ① 근거 검증(진단, 점수에 섞지 않음)", "",
            f"- 발급되지 않은 근거 {d['unissued_evidence']}, 필수 속성 누락 {d['missing_required']}, 단정 표현 {d['assertive']}, "
            f"숨은 줄 근거 {d['hidden_evidence']}, 계산기 호출 {d['calculator_calls']}",
            f"- 가격 확인 상태: {d['price_status']}",
            f"- 줄 신호: {d['signals']}", ""]
    return "\n".join(out) + "\n"
