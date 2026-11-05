// 검토 백엔드(studio_review) API. 답안지·판정 타입은 JSON Schema에서 만든 것(src/types)을 쓴다.
import type { V1 as AnswerSheet, Label } from "./types/answer-sheet";
import type { V1 as Judgment } from "./types/judgment";

export type { AnswerSheet, Judgment, Label };

export interface RunSummary {
  run_id: string;
  model: string | null;
  effort: string | null;
  prompt_version: string | null;
  started_at: string | null;
  finished_at: string | null;
  cost_usd: string | null;
  cases: number;
  judged: number;
  has_scorecard: boolean;
}

export interface CaseSummary {
  case_id: string;
  label: Label;
  conclusion: "n_times" | "nothing_to_flag" | "hold";
  title: string;
  routing: "auto" | "human";
  rules: number[];
  danger: boolean;
  unfolded: number;
  verdict: Judgment["verdict"] | null;
}

export interface PageLine {
  line: number;
  text: string;
  hidden: boolean;
}

export interface CaseDetail {
  sheet: AnswerSheet;
  page: { url: string; sha256: string; lines: PageLine[]; removed: number[] };
  procurement: { item_id: string; name: string; model: string; manufacturer: string; category: string; unit: string; p_krw: string; quantities: string[] };
  judgment: Judgment | null;
}

export interface JudgmentInput {
  verdict: Judgment["verdict"];
  label: Judgment["label"];
  rows: Record<string, "agree" | "disagree">;
  note: string;
  reviewer?: string;
}

async function call<T>(url: string, init?: RequestInit): Promise<T> {
  const r = await fetch(url, init);
  const body = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error((body as { error?: string }).error ?? `HTTP ${r.status}`);
  return body as T;
}

export const api = {
  runs: () => call<RunSummary[]>("/api/runs"),
  cases: (run: string) => call<CaseSummary[]>(`/api/runs/${encodeURIComponent(run)}/cases`),
  caseDetail: (run: string, id: string) => call<CaseDetail>(`/api/runs/${encodeURIComponent(run)}/cases/${encodeURIComponent(id)}`),
  judge: (run: string, id: string, j: JudgmentInput) =>
    call<Judgment>(`/api/runs/${encodeURIComponent(run)}/cases/${encodeURIComponent(id)}/judgment`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(j),
    }),
  scorecard: (run: string) => call<Scorecard>(`/api/runs/${encodeURIComponent(run)}/scorecard`),
};

export interface Frac { n: number; d: number; text: string }
export interface ScoreGroup {
  cases: number;
  conclusion: { precision: Frac; recall: Frac; held_n_times: number; hold_rate: Frac; wrong_by_label: string[]; wrong_by_gap: string[] };
  labels: { matrix: Record<string, Record<string, number>>; accuracy: Frac; equivalent_as_same_model: string[] };
  benchmark: { exact: Frac; within_tolerance: number; outside: number; misses: { case_id: string; kind: string; got: string | null; want: string; direction: string | null; cause: string[] }[] };
  reason: { score: Frac };
  routing: { auto: number; auto_wrong: string[]; human: number; human_by_rule: Record<string, number>; human_but_agent_right: number };
  cost: { total_usd?: string; per_case_usd?: { median: string; max: string }; repeated?: Frac };
  human?: { judged: number; agree_with_agent: Frac; agree_with_truth: Frac };
}
export interface Scorecard {
  run_id: string;
  model: string | null;
  zero_conditions: { pass: boolean; danger_auto_passed: string[]; unissued_evidence: { case_id: string }[]; assertive_auto_passed: string[] };
  groups: Record<string, ScoreGroup>;
  hard_cases: { case_id: string; traps: string[]; pass: boolean; wrong: string[]; routing: string }[];
}

export const LABEL_KO: Record<string, string> = {
  same_model: "같은 모델",
  equivalent: "동급",
  different: "다른 물건",
  insufficient: "근거 부족",
  hold: "판단 보류",
};
export const RESULT_KO: Record<string, string> = {
  same: "같음",
  within_tolerance: "허용 오차 안",
  different: "다름",
  not_on_page: "페이지에 없음",
};
export const RULE_KO: Record<number, string> = {
  1: "AI에게 하는 말",
  2: "필수 속성 목록 없음",
  3: "판단 보류",
  4: "펼친 줄",
  5: "부가세 가정",
  6: "제목 동의 안 함",
  7: "N배 결론",
  8: "필수 속성 누락",
  9: "단정 표현",
  10: "자동 통과",
  11: "읽지 않은 가격",
};

export function won(s: string | null | undefined): string {
  if (s == null) return "-";
  return s.replace(/\B(?=(\d{3})+(?!\d))/g, ",") + "원";
}
