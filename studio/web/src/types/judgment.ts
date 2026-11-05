/* 자동 생성: studio/schema/judgment.schema.json → npm run gen:types. 손으로 고치지 않는다. */

/**
 * 웹 검토 화면에서 사람이 사건 하나에 내린 판정. <실행 디렉터리>/judgments/<case_id>.json. 채점기가 읽어 에이전트와의 일치와 완화 기준 기록(채점 명세 6절)을 만든다.
 */
export interface V1 {
  schema_version: "judgment/v1";
  case_id: string;
  /**
   * approve: 에이전트 결론을 승인 / reject: 결론을 반려(지적하지 않음) / edit: 라벨을 고쳐 다시 계산
   */
  verdict: "approve" | "reject" | "edit";
  /**
   * 사람이 본 라벨(edit일 때 필수, 아니면 선택)
   */
  label: null | ("same_model" | "equivalent" | "different" | "insufficient");
  /**
   * 펼친 줄 등에 대한 줄 단위 판정(속성 → agree / disagree)
   */
  rows: {
    [k: string]: "agree" | "disagree";
  };
  note: string;
  reviewer: string;
  reviewed_at: string;
}
