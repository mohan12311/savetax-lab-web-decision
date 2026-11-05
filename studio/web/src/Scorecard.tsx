import { useEffect, useState } from "react";
import { api, LABEL_KO, RULE_KO, type Scorecard, type ScoreGroup } from "./api";

const ROWS: [string, (g: ScoreGroup) => string | number][] = [
  ["사건 수", (g) => g.cases],
  ["결론 정밀도(헛짚지 않기)", (g) => g.conclusion.precision.text],
  ["결론 재현율(놓치지 않기, 보류 제외)", (g) => g.conclusion.recall.text],
  ["보류한 N배", (g) => g.conclusion.held_n_times],
  ["보류율", (g) => g.conclusion.hold_rate.text],
  ["라벨 정확도", (g) => g.labels.accuracy.text],
  ["동급을 같은 모델로", (g) => g.labels.equivalent_as_same_model.length],
  ["비교 단가 정확 일치", (g) => g.benchmark.exact.text],
  ["비교 단가 ±1% 안 오차", (g) => g.benchmark.within_tolerance],
  ["비교 단가 범위 밖", (g) => g.benchmark.outside],
  ["이유 점수(동급)", (g) => g.reason.score.text],
  ["자동 통과", (g) => g.routing.auto],
  ["자동 통과 중 오답", (g) => g.routing.auto_wrong.length],
  ["사람에게 감", (g) => g.routing.human],
  ["사람에게 갔지만 에이전트가 맞음", (g) => g.routing.human_but_agent_right],
  ["사람 판정 수", (g) => g.human?.judged ?? 0],
  ["사람 ↔ 에이전트 결론 일치", (g) => g.human?.agree_with_agent.text ?? "-"],
  ["사람 ↔ 정답 결론 일치", (g) => g.human?.agree_with_truth.text ?? "-"],
  ["총비용(USD)", (g) => g.cost.total_usd ?? "-"],
  ["여러 번 실행한 비율", (g) => g.cost.repeated?.text ?? "-"],
];

export default function ScorecardView({ run }: { run: string }) {
  const [c, setC] = useState<Scorecard | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    api.scorecard(run).then(setC).catch((e) => setErr(String(e)));
  }, [run]);
  if (err) return <div className="banner warn">{err} — 채점: <code>make studio-grade RUN={run}</code></div>;
  if (!c) return <div className="empty">불러오는 중…</div>;
  const groups = Object.keys(c.groups);
  const all = c.groups.all;
  const z = c.zero_conditions;
  return (
    <section className="score">
      <h1>점수표 <span className="muted">{c.run_id} · {c.model}</span></h1>
      <p className="muted small">정답 파일(cases.jsonl)과 대조한 결과다. 사람 판정 전에 보면 판단이 끌릴 수 있으니 판정을 마친 뒤 본다.</p>
      <div className={`banner ${z.pass ? "ok" : "danger"}`}>
        반드시 0건 조건: <b>{z.pass ? "통과" : "탈락"}</b> — danger 자동 통과 {z.danger_auto_passed.length} · 발급되지 않은 근거 {z.unissued_evidence.length} · 단정 표현 자동 통과 {z.assertive_auto_passed.length}
      </div>
      <table className="grid">
        <thead><tr><th>항목</th>{groups.map((g) => <th key={g}>{g}</th>)}</tr></thead>
        <tbody>{ROWS.map(([name, f]) => <tr key={name}><td>{name}</td>{groups.map((g) => <td key={g}>{String(f(c.groups[g]))}</td>)}</tr>)}</tbody>
      </table>
      <h2>라벨 표 <span className="muted small">행: 정답, 열: 에이전트</span></h2>
      <table className="grid">
        <thead><tr><th></th>{Object.keys(LABEL_KO).map((l) => <th key={l}>{LABEL_KO[l]}</th>)}</tr></thead>
        <tbody>
          {Object.entries(all.labels.matrix).map(([t, cols]) => (
            <tr key={t}><td>{LABEL_KO[t]}</td>{Object.keys(LABEL_KO).map((l) => <td key={l} className={t === l ? "diag" : cols[l] ? "off" : ""}>{cols[l]}</td>)}</tr>
          ))}
        </tbody>
      </table>
      <h2>사람에게 간 이유</h2>
      <p>{Object.entries(all.routing.human_by_rule).map(([k, v]) => `${k}. ${RULE_KO[Number(k)]} ${v}건`).join(" · ") || "없음"}</p>
      <h2>결론이 틀린 사건</h2>
      <p>라벨이 틀림: {all.conclusion.wrong_by_label.join(", ") || "없음"} · 가격 차이 판정이 틀림: {all.conclusion.wrong_by_gap.join(", ") || "없음"}</p>
      {all.benchmark.misses.length > 0 && (
        <table className="grid">
          <thead><tr><th>사건</th><th>칸</th><th>에이전트</th><th>정답</th><th>방향</th><th>원인</th></tr></thead>
          <tbody>{all.benchmark.misses.map((m) => <tr key={m.case_id}><td>{m.case_id}</td><td>{m.kind}</td><td>{m.got}</td><td>{m.want}</td><td>{m.direction}</td><td>{m.cause.join(", ")}</td></tr>)}</tbody>
        </table>
      )}
      <h2>hard 사례</h2>
      <table className="grid">
        <thead><tr><th>사례</th><th>함정</th><th>결과</th><th>틀린 칸</th><th>처리</th></tr></thead>
        <tbody>{c.hard_cases.map((h) => <tr key={h.case_id}><td>{h.case_id}</td><td>{h.traps.join(", ")}</td><td className={h.pass ? "pass" : "fail"}>{h.pass ? "통과" : "실패"}</td><td>{h.wrong.join("; ") || "-"}</td><td>{h.routing}</td></tr>)}</tbody>
      </table>
    </section>
  );
}
