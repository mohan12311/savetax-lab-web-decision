import { useState } from "react";
import { LABEL_KO, type CaseSummary } from "./api";

type Filter = "pending" | "human" | "auto" | "done" | "all";
const FILTERS: [Filter, string][] = [
  ["pending", "검토 대기"],
  ["auto", "자동 통과"],
  ["done", "판정함"],
  ["all", "전체"],
];

function match(c: CaseSummary, f: Filter): boolean {
  switch (f) {
    case "pending":
      return c.routing === "human" && !c.verdict;
    case "human":
      return c.routing === "human";
    case "auto":
      return c.routing === "auto";
    case "done":
      return !!c.verdict;
    default:
      return true;
  }
}

const VERDICT_KO = { approve: "승인", reject: "반려", edit: "수정" } as const;

export default function Queue({ cases, selected, onSelect }: { cases: CaseSummary[]; selected?: string; onSelect: (id: string) => void }) {
  const [filter, setFilter] = useState<Filter>("pending");
  const shown = cases.filter((c) => match(c, filter));
  return (
    <aside className="queue">
      <div className="filters">
        {FILTERS.map(([f, name]) => (
          <button key={f} className={filter === f ? "on" : ""} onClick={() => setFilter(f)}>
            {name} <span className="count">{cases.filter((c) => match(c, f)).length}</span>
          </button>
        ))}
      </div>
      <ul>
        {shown.map((c) => (
          <li key={c.case_id} className={c.case_id === selected ? "sel" : ""} onClick={() => onSelect(c.case_id)}>
            <div className="qrow">
              <span className="cid">{c.case_id}</span>
              {c.danger && <span className="chip danger" title="AI에게 하는 말이 있다">danger</span>}
              {c.conclusion === "n_times" && <span className="chip warn">N배</span>}
              {c.unfolded > 0 && <span className="chip" title="객관 신호가 실패해 펼친 줄">펼침 {c.unfolded}</span>}
              {c.verdict && <span className="chip ok">{VERDICT_KO[c.verdict]}</span>}
            </div>
            <div className="qtitle">{c.title}</div>
            <div className="muted small">{LABEL_KO[c.label]} · {c.routing === "auto" ? "자동 통과" : `사람(${c.rules.join(", ")})`}</div>
          </li>
        ))}
        {shown.length === 0 && <li className="muted">없다</li>}
      </ul>
    </aside>
  );
}
