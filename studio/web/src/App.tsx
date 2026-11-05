import { useCallback, useEffect, useMemo, useState } from "react";
import { api, type CaseSummary, type RunSummary } from "./api";
import Queue from "./Queue";
import CaseView from "./CaseView";
import ScorecardView from "./Scorecard";

// 해시 경로: #/<run>/case/<case_id> · #/<run>/score
function parseHash(): { run?: string; view: "case" | "score"; caseId?: string } {
  const [, run, view, caseId] = window.location.hash.replace(/^#/, "").split("/");
  return { run: run ? decodeURIComponent(run) : undefined, view: view === "score" ? "score" : "case", caseId: caseId ? decodeURIComponent(caseId) : undefined };
}

export default function App() {
  const [runs, setRuns] = useState<RunSummary[]>([]);
  const [route, setRoute] = useState(parseHash());
  const [cases, setCases] = useState<CaseSummary[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const on = () => setRoute(parseHash());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);

  useEffect(() => {
    api.runs().then(setRuns).catch((e) => setError(String(e)));
  }, []);

  const run = route.run ?? runs[0]?.run_id;
  const refreshCases = useCallback(() => {
    if (run) api.cases(run).then(setCases).catch((e) => setError(String(e)));
  }, [run]);
  useEffect(refreshCases, [refreshCases]);

  const current = runs.find((r) => r.run_id === run);
  const go = (r: string, view: "case" | "score", id?: string) => {
    window.location.hash = `/${encodeURIComponent(r)}/${view}${id ? "/" + encodeURIComponent(id) : ""}`;
  };
  const selected = route.caseId ?? cases.find((c) => c.routing === "human" && !c.verdict)?.case_id ?? cases[0]?.case_id;
  const nextPending = useMemo(() => {
    const idx = cases.findIndex((c) => c.case_id === selected);
    const after = [...cases.slice(idx + 1), ...cases.slice(0, idx)];
    return after.find((c) => c.routing === "human" && !c.verdict)?.case_id;
  }, [cases, selected]);

  return (
    <div className="app">
      <header className="top">
        <div className="brand">
          <strong>savetax studio</strong>
          <span className="muted">에이전트 답안지 검토</span>
        </div>
        <select value={run ?? ""} onChange={(e) => go(e.target.value, "case")} aria-label="실행 선택">
          {runs.map((r) => (
            <option key={r.run_id} value={r.run_id}>
              {r.run_id} · {r.model} · {r.cases}건{r.judged ? ` · 판정 ${r.judged}` : ""}
            </option>
          ))}
        </select>
        {run && (
          <nav className="tabs">
            <button className={route.view === "case" ? "on" : ""} onClick={() => go(run, "case", selected)}>검토</button>
            <button className={route.view === "score" ? "on" : ""} onClick={() => go(run, "score")} disabled={!current?.has_scorecard} title={current?.has_scorecard ? "" : "채점 전이다(make studio-grade)"}>
              점수표
            </button>
          </nav>
        )}
        {current && (
          <div className="muted small runmeta">
            프롬프트 {current.prompt_version} · effort {current.effort ?? "-"} · ${current.cost_usd ?? "-"}
          </div>
        )}
      </header>
      {error && <div className="banner danger">{error}</div>}
      {!run && !error && <div className="empty">실행이 없다. <code>make studio-baseline</code> 또는 <code>make studio-run</code>으로 만든다.</div>}
      {run && route.view === "score" && <ScorecardView run={run} />}
      {run && route.view === "case" && (
        <div className="main">
          <Queue cases={cases} selected={selected} onSelect={(id) => go(run, "case", id)} />
          {selected ? (
            <CaseView key={`${run}/${selected}`} run={run} caseId={selected} onSaved={() => { refreshCases(); if (nextPending) go(run, "case", nextPending); }} nextPending={nextPending} />
          ) : (
            <div className="empty">사건을 고른다.</div>
          )}
        </div>
      )}
    </div>
  );
}
