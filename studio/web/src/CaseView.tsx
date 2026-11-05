import { Fragment, useEffect, useMemo, useState } from "react";
import { api, LABEL_KO, RESULT_KO, RULE_KO, won, type AnswerSheet, type CaseDetail, type JudgmentInput, type Label } from "./api";

type Ev = AnswerSheet["evidence"][string];

function evLines(sheet: AnswerSheet, ids: (string | null | undefined)[]): number[] {
  return ids.flatMap((id) => {
    const ev = id ? sheet.evidence[id] : undefined;
    return ev && ev.source === "page" && ev.line ? [ev.line] : [];
  });
}

function EvText({ sheet, id, onHover }: { sheet: AnswerSheet; id: string | null | undefined; onHover: (lines: number[]) => void }) {
  if (!id) return <span className="muted">—</span>;
  const ev: Ev | undefined = sheet.evidence[id];
  if (!ev) return <span className="chip danger" title="근거 첨부 도구가 발급하지 않은 ID">{id} 미발급</span>;
  return (
    <span className={`ev ${ev.hidden ? "hidden-ev" : ""}`} onMouseEnter={() => onHover(evLines(sheet, [id]))} onMouseLeave={() => onHover([])}
      title={ev.source === "page" ? `${id} · ${ev.line}번 줄${ev.hidden ? " (숨은 줄)" : ""}` : `${id} · 조달 물품 ${ev.field ?? ""}`}>
      {ev.text}
      <sup>{ev.source === "page" ? `L${ev.line}` : "조달"}</sup>
    </span>
  );
}

// 판정 이유 속 {ev-N}을 근거 글자로 보여 준다
function Reason({ sheet, text, onHover }: { sheet: AnswerSheet; text: string | null; onHover: (l: number[]) => void }) {
  if (!text) return null;
  const parts = text.split(/(\{ev-\d+\})/g);
  return (
    <p className="reason">
      {parts.map((p, i) => {
        const m = p.match(/^\{(ev-\d+)\}$/);
        return m ? <EvText key={i} sheet={sheet} id={m[1]} onHover={onHover} /> : <Fragment key={i}>{p}</Fragment>;
      })}
    </p>
  );
}

export default function CaseView({ run, caseId, onSaved, nextPending }: { run: string; caseId: string; onSaved: () => void; nextPending?: string }) {
  const [d, setD] = useState<CaseDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [hl, setHl] = useState<number[]>([]);
  const [showFolded, setShowFolded] = useState(false);
  const [form, setForm] = useState<JudgmentInput>({ verdict: "approve", label: null, rows: {}, note: "" });
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState<string | null>(null);

  useEffect(() => {
    api.caseDetail(run, caseId).then((x) => {
      setD(x);
      if (x.judgment) setForm({ verdict: x.judgment.verdict, label: x.judgment.label, rows: x.judgment.rows as JudgmentInput["rows"], note: x.judgment.note });
    }).catch((e) => setErr(String(e)));
  }, [run, caseId]);

  const used = useMemo(() => new Set(d ? evLines(d.sheet, Object.keys(d.sheet.evidence)) : []), [d]);
  if (err) return <div className="banner danger">{err}</div>;
  if (!d) return <div className="empty">불러오는 중…</div>;
  const s = d.sheet;
  const danger = s.warnings.filter((w) => w.level === "danger");
  const dangerLines = new Set(d.page.removed);
  const unfolded = s.rows.filter((r) => !r.folded);
  const folded = s.rows.filter((r) => r.folded);
  const code = s.price.code;
  const read = s.price.read;

  const save = async () => {
    setSaving(true);
    try {
      await api.judge(run, caseId, form);
      setSaved(new Date().toLocaleTimeString());
      onSaved();
    } catch (e) {
      setErr(String(e));
    } finally {
      setSaving(false);
    }
  };

  const rowView = (r: AnswerSheet["rows"][number]) => (
    <tr key={r.attribute} className={r.folded ? "folded" : "unfolded"} onMouseEnter={() => setHl(evLines(s, [r.procurement, r.seller]))} onMouseLeave={() => setHl([])}>
      <td className="attr">{r.attribute}{r.required && <span className="req" title="필수 속성">*</span>}</td>
      <td><EvText sheet={s} id={r.procurement} onHover={setHl} /></td>
      <td><EvText sheet={s} id={r.seller} onHover={setHl} /></td>
      <td><span className={`res res-${r.result}`}>{RESULT_KO[r.result]}</span><div className="muted small">확신 {r.confidence.toFixed(2)}</div></td>
      <td className="sig">
        {r.check.pass ? <span className="muted small">통과</span> : r.check.signals.map((x, i) => <div key={i} className="signal">{x}</div>)}
        {r.runs && <div className="small">반복 {r.runs.total}회 중 {r.runs.agree}회 같은 답</div>}
      </td>
      <td className="judge">
        {!r.folded && (
          <div className="seg">
            {(["agree", "disagree"] as const).map((v) => (
              <button key={v} className={form.rows[r.attribute] === v ? "on" : ""}
                onClick={() => setForm({ ...form, rows: { ...form.rows, [r.attribute]: v } })}>{v === "agree" ? "맞음" : "틀림"}</button>
            ))}
          </div>
        )}
      </td>
    </tr>
  );

  return (
    <section className="case">
      <div className="case-head">
        <div>
          <div className="muted small">{s.case_id} · {d.procurement.item_id} · {LABEL_KO[s.label]}</div>
          <h1 className={s.conclusion === "n_times" ? "ntimes" : ""}>{s.title}</h1>
          {s.title_review && (
            <div className="small">에이전트 더블체크: {s.title_review.agree ? "동의" : <b className="bad">동의 안 함</b>}{s.title_review.supplement && ` — ${s.title_review.supplement}`}</div>
          )}
        </div>
        <div className="routing">
          <span className={`chip ${s.routing.to === "auto" ? "ok" : "warn"}`}>{s.routing.to === "auto" ? "자동 통과" : "사람 검토"}</span>
          {s.routing.because.map((b, i) => <div key={i} className="small" title={b.text}><b>{b.rule}</b> {RULE_KO[b.rule]} — {b.text}</div>)}
        </div>
      </div>
      {danger.length > 0 && (
        <div className="banner danger">
          <b>AI에게 하는 말이 탐지됐다.</b> 그 줄({danger.map((w) => w.line).join(", ")})은 에이전트 입력에서 뺐다. 이 사건은 자동 통과에서 늘 빠진다.
        </div>
      )}
      {s.run.error && <div className="banner danger">실행 오류: {s.run.error}</div>}

      <div className="split">
        <div className="page">
          <div className="panel-title">판매 페이지 보관본 <a href={d.page.url} className="muted small" target="_blank" rel="noreferrer">{d.page.url}</a></div>
          {d.page.lines.length === 0 && <div className="empty small">정적 HTML에 글이 없다(자바스크립트로 그리는 페이지).</div>}
          <ol className="lines">
            {d.page.lines.map((l) => (
              <li key={l.line} className={[hl.includes(l.line) ? "hl" : "", l.hidden ? "hidden-line" : "", dangerLines.has(l.line) ? "danger-line" : "", used.has(l.line) ? "used" : ""].join(" ")}>
                <span className="ln">{l.line}</span>
                <span className="lt">{l.text}</span>
                {l.hidden && <span className="chip small">숨김</span>}
                {dangerLines.has(l.line) && <span className="chip danger small">입력에서 뺌</span>}
              </li>
            ))}
          </ol>
          <div className="panel-title">조달 물품</div>
          <dl className="proc">
            <dt>이름</dt><dd>{d.procurement.name}</dd>
            <dt>모델</dt><dd>{d.procurement.model}</dd>
            <dt>단위</dt><dd>{d.procurement.unit}</dd>
            <dt>납품 단가 P</dt><dd>{won(d.procurement.p_krw)} <span className="muted small">(에이전트에게는 보여 주지 않는다)</span></dd>
            <dt>수량</dt><dd>{d.procurement.quantities.join(", ")}</dd>
          </dl>
        </div>

        <div className="sheet">
          <div className="panel-title">대조표</div>
          <table className="rows">
            <thead><tr><th>속성</th><th>조달 값</th><th>판매 값</th><th>비교</th><th>객관 신호</th><th>사람</th></tr></thead>
            <tbody>
              {unfolded.map(rowView)}
              {folded.length > 0 && (
                <tr className="fold-toggle"><td colSpan={6}><button className="link" onClick={() => setShowFolded(!showFolded)}>
                  {showFolded ? "접기" : `객관 신호를 통과해 접힌 줄 ${folded.length}개 보기`}</button></td></tr>
              )}
              {showFolded && folded.map(rowView)}
            </tbody>
          </table>
          {s.checks.missing_required.length > 0 && <div className="banner warn small">필수 속성 줄이 없다: {s.checks.missing_required.join(", ")}</div>}

          <div className="panel-title">가격 <span className={`chip ${s.price.check.folded ? "ok" : "warn"}`}>{s.price.check.status}</span></div>
          {read ? (
            <table className="kv">
              <tbody>
                <tr><th>판매가</th><td><EvText sheet={s} id={read.sale_price.ev} onHover={setHl} /> → {won(read.sale_price.value)}</td></tr>
                <tr><th>부가세</th><td><EvText sheet={s} id={read.vat.ev} onHover={setHl} /> → {read.vat.value}</td></tr>
                <tr><th>판매 단위</th><td><EvText sheet={s} id={read.sale_unit.ev} onHover={setHl} /> → {read.sale_unit.value}</td></tr>
                <tr><th>환산</th><td><EvText sheet={s} id={read.conversion.ev} onHover={setHl} /> → 판매 단위 1개 = 조달 단위 {read.conversion.value}개</td></tr>
                {s.price.llm && <tr><th>LLM 계산식</th><td><code>{s.price.llm.formula}</code> = {won(s.price.llm.benchmark_krw)} <span className="muted small">(다시 계산 {s.price.check.recomputed ?? "-"})</span></td></tr>}
                {code && (
                  <>
                    <tr><th>코드 비교 단가</th><td><b>{won(code.benchmark_krw)}</b>{code.benchmark_alt_krw && <span className="muted"> · 별도 가정 {won(code.benchmark_alt_krw)}</span>}</td></tr>
                    <tr><th>배율</th><td>{won(code.p_krw)} ÷ {won(code.benchmark_krw)} = <b>{code.ratio}배</b> · 기준 {code.gap_pass ? "통과" : "미달"} · 예상액 {won(code.amount_krw)}</td></tr>
                  </>
                )}
              </tbody>
            </table>
          ) : (
            <div className="muted small">가격을 읽지 않았다.</div>
          )}
          {s.price.check.signals.map((x, i) => <div key={i} className="signal">{x}</div>)}

          <div className="panel-title">판정 이유</div>
          <Reason sheet={s} text={s.reason} onHover={setHl} />
          {s.hold_reason && <><div className="panel-title">보류 사유</div><Reason sheet={s} text={s.hold_reason} onHover={setHl} /></>}
          {s.repeats && <div className="small">반복 {s.repeats.total}회 라벨: {Object.entries(s.repeats.labels).map(([k, v]) => `${LABEL_KO[k]} ${v}`).join(" · ")}</div>}
          <div className="muted small runline">
            {s.run.model} · 프롬프트 {s.run.prompt_version} · 실행 {s.run.runs}회 · 도구 {s.run.tool_calls}번 · 계산기 {s.checks.calculator_calls}번 · 근거 거절 {s.checks.attach_rejections}번 · ${s.run.cost_usd} · {(s.run.elapsed_ms / 1000).toFixed(1)}초
          </div>

          <div className="judgment">
            <div className="panel-title">사람 판정</div>
            <div className="seg big">
              {([["approve", s.conclusion === "n_times" ? "결론 승인" : "지적할 것 없음 확인"], ["reject", "반려"], ["edit", "라벨 수정"]] as const).map(([v, name]) => (
                <button key={v} className={form.verdict === v ? "on" : ""} onClick={() => setForm({ ...form, verdict: v })}>{name}</button>
              ))}
            </div>
            {form.verdict === "edit" && (
              <select value={form.label ?? ""} onChange={(e) => setForm({ ...form, label: (e.target.value || null) as Exclude<Label, "hold"> | null })}>
                <option value="">라벨을 고른다</option>
                {(["same_model", "equivalent", "different", "insufficient"] as const).map((l) => <option key={l} value={l}>{LABEL_KO[l]}</option>)}
              </select>
            )}
            <textarea placeholder="메모(사람 판정의 이유 — 완화 기준 데이터로 쌓인다)" value={form.note} onChange={(e) => setForm({ ...form, note: e.target.value })} />
            <div className="actions">
              <button className="primary" onClick={save} disabled={saving || (form.verdict === "edit" && !form.label)}>{saving ? "저장 중…" : nextPending ? "저장하고 다음" : "저장"}</button>
              {saved && <span className="muted small">저장함 {saved}</span>}
              {d.judgment && !saved && <span className="muted small">이전 판정 {d.judgment.reviewed_at}</span>}
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
