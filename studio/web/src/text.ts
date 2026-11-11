// 화면에 보이는 말. 코드 이름(value_compare, not_applicable 등)을 사람이 읽는 말로 바꾼다.
import type { AnswerSheet } from "./api";
import { won } from "./api";

export const VAT_KO: Record<string, string> = { included: "부가세 포함", excluded: "부가세 별도", not_stated: "부가세 표기 없음" };

export const PRICE_STATUS_KO: Record<string, string> = {
  match: "계산 일치",
  arithmetic_error: "산수 실수",
  understanding_diff: "계산 방식이 다름",
  no_formula: "계산식 없음",
  not_applicable: "가격을 쓰지 않는 판정",
  error: "계산 오류",
};

const SIGNAL_KO: Record<string, string> = {
  value_compare: "값 비교",
  judgment: "판단 필요",
  evidence_unissued: "확인 안 된 근거",
  evidence_source: "근거 출처",
  hidden_evidence: "숨은 글을 근거로 씀",
  read_value: "읽은 값",
  unit: "판매 단위",
  formula: "계산식",
  arithmetic_error: "산수 실수",
  understanding_diff: "계산 방식",
  code_calc: "코드 계산",
  price: "가격",
};

/** "value_compare: 값이 다른데 …" → { kind: "값 비교", text: "값이 다른데 …" } */
export function signal(x: string): { kind: string; text: string } {
  const i = x.indexOf(": ");
  if (i < 0) return { kind: "", text: x };
  const code = x.slice(0, i);
  return { kind: SIGNAL_KO[code] ?? code, text: x.slice(i + 2) };
}

/** 사람에게 온 이유(판정 결과 명세 7절)를 "무엇을 확인하나"로 바꾼다 */
export function checklist(s: AnswerSheet): { tone: "danger" | "warn" | "info"; text: string }[] {
  const out: { tone: "danger" | "warn" | "info"; text: string }[] = [];
  const code = s.price.code;
  for (const b of s.routing.because) {
    switch (b.rule) {
      case 1:
        out.push({ tone: "danger", text: "판매 페이지에 AI에게 지시하는 글이 있습니다(빨간 줄). 에이전트에게는 그 글을 빼고 보여 줬습니다. 판정이 페이지 내용과 맞는지 확인하세요." });
        break;
      case 2:
        out.push({ tone: "warn", text: "필수 속성 목록이 없는 새 품목입니다. 비교한 속성이 충분한지 확인하세요." });
        break;
      case 3:
        out.push({ tone: "warn", text: `에이전트가 판단을 보류했습니다: ${s.hold_reason ?? ""}` });
        break;
      case 4: {
        const rows = s.rows.filter((r) => !r.folded).map((r) => r.attribute);
        if (!s.price.check.folded) rows.push("가격");
        out.push({ tone: "warn", text: `노란 줄을 확인하세요: ${rows.join(", ")}. 코드의 확인을 통과하지 못한 줄입니다.` });
        break;
      }
      case 5:
        out.push({
          tone: "warn",
          text: `페이지에 부가세 표기가 없습니다. 포함으로 보면 비교 단가 ${won(code?.benchmark_krw)}(기준 ${code?.gap_pass ? "통과" : "미달"}), 별도로 보면 ${won(code?.benchmark_alt_krw)}(기준 ${code?.gap_pass_alt ? "통과" : "미달"})입니다. 판매처의 부가세 정책을 확인하세요.`,
        });
        break;
      case 6:
        out.push({ tone: "warn", text: `에이전트가 코드가 만든 결론에 동의하지 않았습니다: ${s.title_review?.supplement ?? ""}` });
        break;
      case 7:
        out.push({ tone: "info", text: "\"시중가 대비 N배\" 결론은 항상 사람이 확인합니다. 같은 물건인지, 가격 계산이 맞는지 보세요." });
        break;
      case 8:
        out.push({ tone: "warn", text: `필수 속성 줄이 빠졌습니다: ${s.checks.missing_required.join(", ")}` });
        break;
      case 9:
        out.push({ tone: "warn", text: `판정 글에 단정 표현이 있습니다: ${s.checks.assertive.map((a) => a.phrase).join(", ")}` });
        break;
      case 11:
        out.push({ tone: "warn", text: "페이지에 가격처럼 보이는 글이 있는데 에이전트가 가격을 읽지 않았습니다. 놓친 근거가 없는지 보세요." });
        break;
      case 10:
        out.push({ tone: "info", text: "자동 통과한 사례입니다(표본 확인용). 모든 줄이 코드 확인을 통과했고 결론이 \"지적할 것 없음\"입니다." });
        break;
    }
  }
  return out;
}

/** 판매 단위 1개가 조달 단위 몇 개인가 → 사람이 읽는 말 */
export function conversionText(conv: string, saleUnit: string | null, procUnit: string): string {
  if (conv === "1") return `1${saleUnit ?? ""} = 1${procUnit}`;
  if (conv.startsWith("1/")) return `${conv.slice(2)}${saleUnit ?? ""} = 1${procUnit}`;
  return `1${saleUnit ?? ""} = ${conv}${procUnit}`;
}
