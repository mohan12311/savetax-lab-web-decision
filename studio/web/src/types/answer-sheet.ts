/* 자동 생성: studio/schema/answer-sheet.schema.json → npm run gen:types. 손으로 고치지 않는다. */

/**
 * 같은 모델 · 동급 · 다른 물건 · 근거 부족 · 판단 보류
 */
export type Label = "same_model" | "equivalent" | "different" | "insufficient" | "hold";
export type EvidenceRef = null | EvidenceId;
export type EvidenceId = string;
/**
 * 같음 · 허용 오차 안 · 다름 · 페이지에 없음(채점 명세 3.6절의 차이 종류와 같은 어휘)
 */
export type RowResult = "same" | "within_tolerance" | "different" | "not_on_page";
/**
 * 정수 원 십진 문자열
 */
export type Krw = string;
/**
 * 판매 단위 1개가 조달 단위 몇 개인가(예: 1, 20, 1/25)
 */
export type Units = string;
/**
 * 배율, 소수 첫째 자리까지 내림
 */
export type RatioText = string;
/**
 * 달러 십진 문자열
 */
export type Usd = string;

/**
 * 사건(조달 물품 하나 ↔ 판매 페이지 하나)마다 실행기가 쓰는 답안지. 명세: studio/docs/answer-sheet.md. 금액은 정수 원 십진 문자열이다(float 금지). 칸 사이의 규칙(근거 ID가 evidence에 있는가 등)은 확인 코드(studio/sheet)가 맡는다.
 */
export interface V1 {
  schema_version: "answer-sheet/v1";
  case_id: string;
  item_id: string;
  page_url: string;
  read_mode: "static" | "rendered";
  label: Label;
  rows: Row[];
  price: Price;
  evidence: {
    [k: string]: Evidence;
  };
  warnings: Warning[];
  checks: CaseChecks;
  /**
   * n_times = 시중가 대비 N배. 라벨과 코드의 가격 차이 계산으로 정해진다
   */
  conclusion: "n_times" | "nothing_to_flag" | "hold";
  title: string;
  title_review: null | TitleReview;
  reason: string;
  hold_reason: string | null;
  hold_rows: string[];
  routing: Routing;
  repeats: null | Repeats;
  run: Run;
}
export interface Row {
  attribute: string;
  /**
   * 품목별 필수 속성 목록에 있는 줄인가(코드)
   */
  required: boolean;
  procurement: EvidenceRef;
  seller: EvidenceRef;
  result: RowResult;
  confidence: number;
  check: Check;
  runs: null | Agreement;
  folded: boolean;
}
export interface Check {
  pass: boolean;
  /**
   * 통과하지 못한 객관 신호의 이름과 설명
   */
  signals: string[];
}
export interface Agreement {
  total: number;
  agree: number;
}
export interface Price {
  read: null | ReadValues;
  llm: null | LlmCalc;
  code: null | CodeCalc;
  check: PriceCheck;
}
/**
 * LLM이 읽은 값. 값마다 근거 ID를 단다
 */
export interface ReadValues {
  sale_price: ReadValueKrw;
  vat: ReadValueVat;
  sale_unit: ReadValueText;
  conversion: ReadValueUnits;
}
export interface ReadValueKrw {
  value: null | Krw;
  ev: EvidenceRef;
}
export interface ReadValueVat {
  value: "included" | "excluded" | "not_stated";
  ev: EvidenceRef;
}
export interface ReadValueText {
  value: string | null;
  ev: EvidenceRef;
}
/**
 * 판매 단위 1개가 조달 단위 몇 개인가. 같은 단위면 "1"
 */
export interface ReadValueUnits {
  value: Units;
  ev: EvidenceRef;
}
export interface LlmCalc {
  /**
   * 예: ceil(400 × 25 × 1.1)
   */
  formula: string;
  benchmark_krw: Krw;
}
export interface CodeCalc {
  benchmark_krw: Krw;
  /**
   * 부가세 표기가 없을 때 별도 가정의 비교 단가
   */
  benchmark_alt_krw: null | Krw;
  vat_assumption: "stated" | "assumed_included";
  /**
   * 정수 원 십진 문자열
   */
  p_krw: string;
  ratio: RatioText;
  gap_pass: boolean;
  gap_pass_alt: boolean | null;
  amount_krw: Krw;
}
export interface PriceCheck {
  /**
   * match: 식·산수·코드 계산이 모두 같다. arithmetic_error: 식은 맞는데 적은 값이 다르다. understanding_diff: 식의 값이 코드 계산과 다르다
   */
  status: "match" | "arithmetic_error" | "understanding_diff" | "no_formula" | "not_applicable" | "error";
  /**
   * LLM 식을 코드가 다시 계산한 값
   */
  recomputed: string | null;
  pass: boolean;
  signals: string[];
  folded: boolean;
}
export interface Evidence {
  source: "procurement" | "page" | "site_policy";
  /**
   * 판매 페이지 줄 번호(조달 물품이면 null)
   */
  line: number | null;
  /**
   * 조달 물품의 칸 이름(name, model 등)
   */
  field?: string;
  text: string;
  hidden?: boolean;
}
export interface Warning {
  /**
   * danger · 주의 · 표시만
   */
  level: "danger" | "caution" | "notice";
  kind: "ai_instruction" | "hidden_evidence" | "hidden_text";
  line: number | null;
  text: string;
}
export interface CaseChecks {
  unissued_evidence: string[];
  missing_required: string[];
  assertive: {
    field: string;
    phrase: string;
  }[];
  hidden_evidence: string[];
  /**
   * 부가세 두 가정으로 결론이 바뀐다
   */
  vat_flip: boolean;
  vat_policy_checked: boolean;
  attach_rejections: number;
  calculator_calls: number;
  has_required_list: boolean;
}
export interface TitleReview {
  agree: boolean;
  supplement: string | null;
}
export interface Routing {
  to: "auto" | "human";
  because: {
    rule: number;
    text: string;
  }[];
}
export interface Repeats {
  total: number;
  labels: {
    [k: string]: number;
  };
}
export interface Run {
  model: string;
  prompt_version: string;
  source_sha256: string;
  split_rule: string;
  category: string;
  runs: number;
  tool_calls: number;
  tokens_in: number;
  tokens_out: number;
  cache_read_tokens?: number;
  cost_usd: Usd;
  elapsed_ms: number;
  started_at: string;
  error: string | null;
}
