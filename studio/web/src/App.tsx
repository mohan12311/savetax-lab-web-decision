import { useCallback, useEffect, useMemo, useState } from "react";
import { api, type CaseSummary, type RunSummary } from "./api";
import Queue, { type Filter, matchFilter, priority } from "./Queue";
import CaseView from "./CaseView";
import ScorecardView from "./Scorecard";

// 해시 경로: #/<run>/case/<case_id> · #/<run>/score
function parseHash(): { run?: string; view: "case" | "score"; caseId?: string } {
  const [, run, view, caseId] = window.location.hash.replace(/^#/, "").split("/");
  return { run: run ? decodeURIComponent(run) : undefined, view: view === "score" ? "score" : "case", caseId: caseId ? decodeURIComponent(caseId) : undefined };
}

function runLabel(r: RunSummary): string {
  const t = r.started_at ? new Date(r.started_at).toLocaleString("ko-KR", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" }) : "";
  const model = r.model === "rule-baseline" ? "규칙 기준선(LLM 없음)" : r.model;
  return `${r.variant} · ${model} · ${r.cases}건 · ${t}${r.pending ? ` · 검토 대기 ${r.pending}` : ""}`;
}

export default function App() {
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [route, setRoute] = useState(parseHash());
  const [cases, setCases] = useState<CaseSummary[]>([]);
  const [filter, setFilter] = useState<Filter>("pending");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const on = () => setRoute(parseHash());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);

  const refreshRuns = useCallback(() => {
    api.runs().then(setRuns).catch((e) => setError(String(e)));
  }, []);
  useEffect(refreshRuns, [refreshRuns]);

  // 기본 실행: 검토 대기가 있는 가장 최근 LLM 실행
  const run = route.run ?? (runs.find((r) => r.pending > 0 && r.model !== "rule-baseline") ?? runs[0])?.run_id;
  const refreshCases = useCallback(() => {
    if (run) api.cases(run).then(setCases).catch((e) => setError(String(e)));
  }, [run]);
  useEffect(refreshCases, [refreshCases]);

  const ordered = useMemo(
    () => cases.filter((c) => matchFilter(c, filter)).sort((a, b) => priority(a) - priority(b) || a.case_id.localeCompare(b.case_id)),
    [cases, filter],
  );
  const current = runs.find((r) => r.run_id === run);
  const go = useCallback((r: string, view: "case" | "score", id?: string) => {
    window.location.hash = `/${encodeURIComponent(r)}/${view}${id ? "/" + encodeURIComponent(id) : ""}`;
  }, []);
  const selected = route.caseId ?? ordered[0]?.case_id ?? cases[0]?.case_id;
  const idx = ordered.findIndex((c) => c.case_id === selected);
  const neighbor = (d: 1 | -1) => (idx < 0 ? ordered[0] : ordered[(idx + d + ordered.length) % ordered.length])?.case_id;
  // 저장 뒤에는 다음 "검토 대기"로 간다
  const nextPending = useMemo(() => {
    const pend = cases.filter((c) => c.routing === "human" && !c.verdict && c.case_id !== selected)
      .sort((a, b) => priority(a) - priority(b) || a.case_id.localeCompare(b.case_id));
    return pend[0]?.case_id;
  }, [cases, selected]);
  const pendingCount = cases.filter((c) => c.routing === "human" && !c.verdict).length;
  const humanCount = cases.filter((c) => c.routing === "human").length;

  return (
    <div className="app">
      <header className="top">
        <div className="brand">
          <strong>savetax studio</strong>
          <span className="muted">에이전트 판정 검토</span>
        </div>
        <select value={run ?? ""} onChange={(e) => go(e.target.value, "case")} aria-label="실행 선택">
          {runs.map((r) => <option key={r.run_id} value={r.run_id}>{runLabel(r)}</option>)}
        </select>
        {run && (
          <nav className="tabs">
            <button className={route.view === "case" ? "on" : ""} onClick={() => go(run, "case", selected)}>검토</button>
            <button className={route.view === "score" ? "on" : ""} onClick={() => go(run, "score")} disabled={!current?.has_scorecard}
              title={current?.has_scorecard ? "정답과 대조한 점수표(판정을 마친 뒤 보세요)" : "채점 전이다"}>점수표</button>
          </nav>
        )}
        {run && route.view === "case" && humanCount > 0 && (
          <div className="progress" title="사람 검토 대상 중 판정한 수">
            <div className="bar"><div style={{ width: `${((humanCount - pendingCount) / humanCount) * 100}%` }} /></div>
            <span className="small">판정 {humanCount - pendingCount} / {humanCount}</span>
          </div>
        )}
      </header>
      {error && <div className="banner danger">{error}</div>}
      {!run && !error && <div className="empty">실행이 없습니다. <code>make studio-baseline</code> 또는 <code>make studio-run</code>으로 만듭니다.</div>}
      {run && route.view === "score" && <ScorecardView run={run} />}
      {run && route.view === "case" && (
        <div className="main">
          <Queue cases={cases} ordered={ordered} filter={filter} onFilter={setFilter} selected={selected} onSelect={(id) => go(run, "case", id)} />
          {selected ? (
            <CaseView
              key={`${run}/${selected}`}
              run={run}
              caseId={selected}
              onPrev={() => { const id = neighbor(-1); if (id) go(run, "case", id); }}
              onNext={() => { const id = neighbor(1); if (id) go(run, "case", id); }}
              onSaved={() => {
                refreshCases();
                refreshRuns();
                if (nextPending) go(run, "case", nextPending);
              }}
              hasNextPending={!!nextPending}
            />
          ) : (
            <div className="empty">{filter === "pending" ? "검토 대기가 없습니다. 모두 판정했습니다." : "사건을 고르세요."}</div>
          )}
        </div>
      )}
    </div>
  );
}
