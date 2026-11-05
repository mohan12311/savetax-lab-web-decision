import type { CaseSummary } from "./api";

export type Filter = "pending" | "done" | "auto" | "all";
const FILTERS: [Filter, string][] = [
  ["pending", "검토 대기"],
  ["done", "판정함"],
  ["auto", "자동 통과"],
  ["all", "전체"],
];

export function matchFilter(c: CaseSummary, f: Filter): boolean {
  switch (f) {
    case "pending":
      return c.routing === "human" && !c.verdict;
    case "done":
      return !!c.verdict;
    case "auto":
      return c.routing === "auto";
    default:
      return true;
  }
}

/** 먼저 볼 사건이 앞에 오게: AI 지시문 > 보류 > 확인이 필요한 줄·부가세·읽지 않은 가격 > N배만 > 자동 통과 */
export function priority(c: CaseSummary): number {
  if (c.rules.includes(1)) return 0;
  if (c.rules.includes(3)) return 1;
  if (c.rules.some((r) => [4, 5, 6, 8, 9, 11].includes(r))) return 2;
  if (c.rules.includes(7)) return 3;
  return 4;
}

const CHIP: Record<number, [string, string]> = {
  1: ["AI 지시문", "danger"],
  3: ["보류", "warn"],
  4: ["확인할 줄", "warn"],
  5: ["부가세 표기 없음", "warn"],
  6: ["결론 이견", "warn"],
  8: ["속성 누락", "warn"],
  9: ["단정 표현", "warn"],
  11: ["읽지 않은 가격", "warn"],
};
const VERDICT_KO = { approve: "맞음", reject: "결론 반려", edit: "라벨 고침" } as const;

export default function Queue({ cases, ordered, filter, onFilter, selected, onSelect }: {
  cases: CaseSummary[]; ordered: CaseSummary[]; filter: Filter; onFilter: (f: Filter) => void; selected?: string; onSelect: (id: string) => void;
}) {
  return (
    <aside className="queue">
      <div className="filters">
        {FILTERS.map(([f, name]) => (
          <button key={f} className={filter === f ? "on" : ""} onClick={() => onFilter(f)}>
            {name} <span className="count">{cases.filter((c) => matchFilter(c, f)).length}</span>
          </button>
        ))}
      </div>
      <ul>
        {ordered.map((c) => (
          <li key={c.case_id} className={c.case_id === selected ? "sel" : ""} onClick={() => onSelect(c.case_id)}>
            <div className={`qtitle ${c.conclusion === "n_times" ? "ntimes" : ""}`}>{c.title}</div>
            <div className="qrow">
              <span className="cid">{c.case_id}</span>
              {c.rules.filter((r) => CHIP[r]).map((r) => <span key={r} className={`chip ${CHIP[r][1]}`}>{CHIP[r][0]}</span>)}
              {c.routing === "auto" && <span className="chip">자동 통과</span>}
              {c.verdict && <span className="chip ok">✓ {VERDICT_KO[c.verdict]}</span>}
            </div>
          </li>
        ))}
        {ordered.length === 0 && <li className="muted empty-li">{filter === "pending" ? "검토 대기가 없습니다" : "없습니다"}</li>}
      </ul>
      <div className="keys small muted">단축키: <kbd>Y</kbd> 맞음 · <kbd>N</kbd> 틀림 · <kbd>1</kbd>~<kbd>4</kbd> 라벨 · <kbd>Enter</kbd> 저장 · <kbd>O</kbd> 원본 화면 · <kbd>J</kbd>/<kbd>K</kbd> 다음/이전</div>
    </aside>
  );
}
