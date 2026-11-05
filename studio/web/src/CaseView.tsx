import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, LABEL_KO, RESULT_KO, won, type AnswerSheet, type CaseDetail, type JudgmentInput } from "./api";
import { checklist, conversionText, PRICE_STATUS_KO, signal, VAT_KO } from "./text";

type Row = AnswerSheet["rows"][number];
type Choice = "agree" | "disagree" | null;
const LABEL_KEYS = ["same_model", "equivalent", "different", "insufficient"] as const;

function evLine(s: AnswerSheet, id: string | null | undefined): number | null {
  const ev = id ? s.evidence[id] : undefined;
  return ev && ev.source === "page" && ev.line ? ev.line : null;
}

function Ev({ s, id, onPick }: { s: AnswerSheet; id: string | null | undefined; onPick: (line: number | null) => void }) {
  if (!id) return <span className="muted">—</span>;
  const ev = s.evidence[id];
  if (!ev) return <span className="chip danger" title="근거 첨부 도구가 발급하지 않은 ID">확인 안 된 근거</span>;
  const line = ev.source === "page" ? ev.line ?? null : null;
  return (
    <button type="button" className={`ev ${ev.hidden ? "hidden-ev" : ""} ${ev.source}`} onClick={() => onPick(line)}
      title={ev.source === "page" ? `판매 페이지 ${line}번 줄${ev.hidden ? "(화면에 안 보이는 글)" : ""}` : "조달 물품 정보"}>
      {ev.text}
      {line && <span className="ln-badge">{line}</span>}
    </button>
  );
}

function Reason({ s, text, onPick }: { s: AnswerSheet; text: string | null; onPick: (l: number | null) => void }) {
  if (!text) return null;
  return (
    <p className="reason">
      {text.split(/(\{ev-\d+\})/g).map((p, i) => {
        const m = p.match(/^\{(ev-\d+)\}$/);
        return m ? <Ev key={i} s={s} id={m[1]} onPick={onPick} /> : <Fragment key={i}>{p}</Fragment>;
      })}
    </p>
  );
}

function PriceCard({ d, onPick }: { d: CaseDetail; onPick: (l: number | null) => void }) {
  const s = d.sheet;
  const read = s.price.read;
  const code = s.price.code;
  const unit = d.procurement.unit;
  if (!read) return <p className="muted">에이전트가 가격을 읽지 않았습니다{s.label === "different" ? "(다른 물건이라 가격이 필요 없음)" : ""}.</p>;
  const conv = read.conversion.value;
  const steps: string[] = [won(read.sale_price.value)];
  if (conv !== "1") steps.push(conv.startsWith("1/") ? `× ${conv.slice(2)}` : `÷ ${conv}`);
  if (read.vat.value === "excluded") steps.push("× 1.1(부가세)");
  const status = s.price.check.status;
  return (
    <div className="price-card">
      <div className="price-read">
        <span>판매가 <Ev s={s} id={read.sale_price.ev} onPick={onPick} /></span>
        <span>{VAT_KO[read.vat.value]} {read.vat.ev && <Ev s={s} id={read.vat.ev} onPick={onPick} />}</span>
        <span>판매 단위 <Ev s={s} id={read.sale_unit.ev} onPick={onPick} /> → {conversionText(conv, read.sale_unit.value, unit)}
          {read.conversion.ev && read.conversion.ev !== read.sale_unit.ev && <> <Ev s={s} id={read.conversion.ev} onPick={onPick} /></>}</span>
      </div>
      {code ? (
        <>
          <div className="calc">
            <span className="muted">{unit}당 비교 단가</span> {steps.join(" ")} = <b>{won(code.benchmark_krw)}</b>
            {code.vat_assumption === "assumed_included" && <span className="chip warn">부가세 포함으로 가정</span>}
          </div>
          <div className="calc">
            <span className="muted">배율</span> 납품 단가 {won(code.p_krw)} ÷ {won(code.benchmark_krw)} = <b className={code.gap_pass ? "ntimes" : ""}>{code.ratio}배</b>
            <span className={`chip ${code.gap_pass ? "warn" : ""}`}>{code.gap_pass ? "가격 차이 기준 통과" : "기준 미달"}</span>
            {code.gap_pass && <span className="muted small"> 예상 차액 {won(code.amount_krw)}</span>}
          </div>
        </>
      ) : (
        <div className="muted small">코드가 비교 단가를 계산하지 않았습니다({PRICE_STATUS_KO[status]}).</div>
      )}
      {s.price.llm && (
        <div className="small">
          에이전트 계산식 <code>{s.price.llm.formula}</code> = {won(s.price.llm.benchmark_krw)}{" "}
          <span className={`chip ${s.price.check.folded ? "ok" : "warn"}`}>{PRICE_STATUS_KO[status]}</span>
        </div>
      )}
      {s.price.check.signals.map((x, i) => {
        const g = signal(x);
        return <div key={i} className="problem">⚠ {g.text}</div>;
      })}
    </div>
  );
}

export default function CaseView({ run, caseId, onSaved, onPrev, onNext, hasNextPending }: {
  run: string; caseId: string; onSaved: () => void; onPrev: () => void; onNext: () => void; hasNextPending: boolean;
}) {
  const [d, setD] = useState<CaseDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [active, setActive] = useState<number[]>([]);
  const [choice, setChoice] = useState<Choice>(null);
  const [label, setLabel] = useState<JudgmentInput["label"]>(null);
  const [rejectOnly, setRejectOnly] = useState(false);
  const [rowsJ, setRowsJ] = useState<JudgmentInput["rows"]>({});
  const [note, setNote] = useState("");
  const [saving, setSaving] = useState(false);
  const [pageView, setPageView] = useState<"lines" | "original">("lines");
  const pageRef = useRef<HTMLOListElement>(null);

  useEffect(() => {
    window.scrollTo(0, 0); // 새 사건은 맨 위부터
    api.caseDetail(run, caseId).then((x) => {
      setD(x);
      const j = x.judgment;
      if (j) {
        setChoice(j.verdict === "approve" ? "agree" : "disagree");
        setLabel(j.label);
        setRejectOnly(j.verdict === "reject");
        setRowsJ(j.rows as JudgmentInput["rows"]);
        setNote(j.note);
      }
    }).catch((e) => setErr(String(e)));
  }, [run, caseId]);

  const pick = useCallback((line: number | null) => {
    if (line == null) return;
    setActive([line]);
    setPageView("lines"); // 근거를 누르면 줄 보기로 돌아가 그 줄을 보여 준다
    setTimeout(() => pageRef.current?.querySelector(`[data-line="${line}"]`)?.scrollIntoView({ block: "center", behavior: "smooth" }), 0);
  }, []);

  const canSave = choice === "agree" || (choice === "disagree" && (rejectOnly || !!label));
  const save = useCallback(async () => {
    if (!canSave || saving) return;
    setSaving(true);
    try {
      const verdict = choice === "agree" ? "approve" : rejectOnly ? "reject" : "edit";
      await api.judge(run, caseId, { verdict, label: verdict === "edit" ? label : null, rows: rowsJ, note });
      onSaved();
    } catch (e) {
      setErr(String(e));
    } finally {
      setSaving(false);
    }
  }, [canSave, saving, choice, rejectOnly, label, rowsJ, note, run, caseId, onSaved]);

  const agentLabel = d?.sheet.label;
  // 단축키: 글을 쓰는 중이 아닐 때만
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement;
      if (t.closest("textarea, input, select") || e.metaKey || e.ctrlKey || e.altKey) return;
      const k = e.key.toLowerCase();
      if (k === "y") { setChoice("agree"); setRejectOnly(false); setLabel(null); }
      else if (k === "n") setChoice("disagree");
      else if (k === "r") { setChoice("disagree"); setRejectOnly(true); setLabel(null); }
      else if (["1", "2", "3", "4"].includes(k)) {
        const l = LABEL_KEYS[Number(k) - 1];
        if (l === agentLabel) return; // 에이전트와 같은 라벨로 "틀리다"는 고를 수 없다
        setChoice("disagree"); setRejectOnly(false); setLabel(l);
      }
      else if (k === "enter") { if (t.closest("button")) return; e.preventDefault(); void save(); }
      else if (k === "o") setPageView((v) => (v === "lines" ? "original" : "lines"));
      else if (k === "j" || k === "arrowright") onNext();
      else if (k === "k" || k === "arrowleft") onPrev();
      else return;
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [save, onNext, onPrev, agentLabel]);

  const s = d?.sheet;
  const usedLines = useMemo(() => {
    const m = new Map<number, string[]>();
    if (!s) return m;
    for (const r of s.rows) for (const id of [r.seller]) { const l = evLine(s, id); if (l) m.set(l, [...(m.get(l) ?? []), r.attribute]); }
    if (s.price.read) for (const [k, v] of Object.entries(s.price.read)) { const l = evLine(s, v.ev); if (l) m.set(l, [...(m.get(l) ?? []), { sale_price: "판매가", vat: "부가세", sale_unit: "판매 단위", conversion: "환산" }[k] ?? k]); }
    return m;
  }, [s]);

  if (err) return <div className="banner danger">{err}</div>;
  if (!d || !s) return <div className="empty">불러오는 중…</div>;
  const removed = new Set(d.page.removed);
  const pageSrc = `/api/runs/${encodeURIComponent(run)}/cases/${encodeURIComponent(caseId)}/page`;
  const rows = [...s.rows].sort((a, b) => Number(a.folded) - Number(b.folded));
  const items = checklist(s);

  const rowView = (r: Row) => {
    const lines = [evLine(s, r.procurement), evLine(s, r.seller)].filter((x): x is number => x != null);
    return (
      <Fragment key={r.attribute}>
        <tr className={`${r.folded ? "" : "problem-row"} ${lines.some((l) => active.includes(l)) ? "active" : ""}`}
          onClick={() => { setActive(lines); if (lines[0]) pick(lines[0]); }}>
          <td className="attr">{r.attribute}</td>
          <td><Ev s={s} id={r.procurement} onPick={pick} /></td>
          <td><Ev s={s} id={r.seller} onPick={pick} /></td>
          <td><span className={`res res-${r.result}`}>{RESULT_KO[r.result]}</span></td>
          <td className="judge-cell">
            {!r.folded && (
              <div className="seg small-seg" onClick={(e) => e.stopPropagation()}>
                {(["agree", "disagree"] as const).map((v) => (
                  <button key={v} className={rowsJ[r.attribute] === v ? "on" : ""} onClick={() => setRowsJ({ ...rowsJ, [r.attribute]: v })}>
                    {v === "agree" ? "판단 맞음" : "판단 틀림"}
                  </button>
                ))}
              </div>
            )}
          </td>
        </tr>
        {!r.folded && (
          <tr className="problem-detail">
            <td colSpan={5}>
              {r.check.signals.map((x, i) => <div key={i}>⚠ {signal(x).text}</div>)}
              {r.runs && <div className="muted">에이전트를 {r.runs.total}번 돌려 {r.runs.agree}번 같은 판단</div>}
            </td>
          </tr>
        )}
      </Fragment>
    );
  };

  return (
    <section className="case">
      <div className="case-head">
        <div className="meta small muted">{s.case_id} · 조달 {d.procurement.item_id} · {d.procurement.category}</div>
        <h1 className={s.conclusion === "n_times" ? "ntimes" : ""}>{s.title}</h1>
        <div className="small">
          에이전트 판정 <b>{LABEL_KO[s.label]}</b>
          {s.title_review && !s.title_review.agree && <span className="chip warn">에이전트가 결론에 이견</span>}
          {s.title_review?.supplement && <span className="muted"> — {s.title_review.supplement}</span>}
        </div>
        <div className="navs">
          <button onClick={onPrev} title="이전(K)">← 이전</button>
          <button onClick={onNext} title="다음(J)">다음 →</button>
        </div>
      </div>

      <div className="checklist">
        <div className="panel-title">확인할 것</div>
        <ul>{items.map((c, i) => <li key={i} className={c.tone}>{c.text}</li>)}</ul>
        {s.run.error && <div className="problem">실행 오류: {s.run.error}</div>}
      </div>

      <div className="split">
        <div className="left">
          <div className="panel">
            <div className="panel-title">같은 물건인가 <span className="muted small">줄을 누르면 오른쪽 페이지의 근거로 갑니다</span></div>
            <table className="rows">
              <thead><tr><th>속성</th><th>조달 물품</th><th>판매 페이지</th><th>에이전트 판단</th><th></th></tr></thead>
              <tbody>{rows.map(rowView)}</tbody>
            </table>
            {s.rows.length === 0 && <p className="muted small">대조표가 비어 있습니다.</p>}
          </div>
          <div className="panel">
            <div className="panel-title">가격</div>
            <PriceCard d={d} onPick={pick} />
          </div>
          <div className="panel">
            <div className="panel-title">에이전트의 판정 이유</div>
            <Reason s={s} text={s.reason} onPick={pick} />
            {s.hold_reason && <><div className="panel-title">보류 사유</div><Reason s={s} text={s.hold_reason} onPick={pick} /></>}
            <details className="small muted">
              <summary>실행 정보</summary>
              {s.run.model} · 프롬프트 {s.run.prompt_version} · 실행 {s.run.runs}회 · 도구 호출 {s.run.tool_calls}번 · 계산기 {s.checks.calculator_calls}번 · 근거 거절 {s.checks.attach_rejections}번 · ${s.run.cost_usd} · {(s.run.elapsed_ms / 1000).toFixed(1)}초
              {s.repeats && <> · 반복 라벨 {Object.entries(s.repeats.labels).map(([k, v]) => `${LABEL_KO[k]} ${v}`).join(", ")}</>}
            </details>
          </div>
        </div>

        <div className="right">
          <div className="panel page">
            <div className="panel-title">
              판매 페이지
              <div className="seg small-seg">
                <button className={pageView === "lines" ? "on" : ""} onClick={() => setPageView("lines")}>줄 보기(근거 표시)</button>
                <button className={pageView === "original" ? "on" : ""} onClick={() => setPageView("original")}>원본 화면 <kbd>O</kbd></button>
              </div>
              <a className="small" href={pageSrc} target="_blank" rel="noreferrer">새 탭에서 원본 열기 ↗</a>
            </div>
            <div className="small muted page-meta">
              보관본 · {d.page.url} · sha256 {d.page.sha256.slice(0, 12)}…
            </div>
            {pageView === "original" ? (
              <>
                <p className="small muted">보관해 둔 HTML을 그대로 그린 화면입니다. 스크립트·폼·외부 요청은 막았습니다. 화면에서 숨긴 글(AI 지시문 등)은 여기 보이지 않을 수 있으니 줄 보기에서 확인하세요.</p>
                <iframe className="original" src={pageSrc} sandbox="" title="판매 페이지 보관본 원본" referrerPolicy="no-referrer" />
              </>
            ) : (
              <>
                {d.page.lines.length === 0 && <p className="muted small">정적 HTML에 글이 없습니다(자바스크립트로 그리는 페이지). 원본 화면에서 빈 껍데기인지 확인할 수 있습니다.</p>}
                <ol className="lines" ref={pageRef}>
                  {d.page.lines.map((l) => {
                    const tags = usedLines.get(l.line);
                    return (
                      <li key={l.line} data-line={l.line}
                        className={[tags ? "used" : "", active.includes(l.line) ? "hl" : "", l.hidden ? "hidden-line" : "", removed.has(l.line) ? "danger-line" : ""].join(" ")}>
                        <span className="ln">{l.line}</span>
                        <span className="lt">{l.text}</span>
                        {tags && <span className="tag">{tags.join("·")}</span>}
                        {l.hidden && <span className="tag muted">화면에 안 보임</span>}
                        {removed.has(l.line) && <span className="tag danger">AI 지시문 · 에이전트에게 안 보여 줌</span>}
                      </li>
                    );
                  })}
                </ol>
              </>
            )}
          </div>
          <div className="panel">
            <div className="panel-title">조달 물품</div>
            <dl className="proc">
              <dt>이름</dt><dd>{d.procurement.name}</dd>
              <dt>모델</dt><dd>{d.procurement.model}</dd>
              <dt>단위</dt><dd>{d.procurement.unit}</dd>
              <dt>납품 단가</dt><dd>{won(d.procurement.p_krw)} <span className="muted small">(에이전트에게는 보여 주지 않음)</span></dd>
              <dt>수량</dt><dd>{d.procurement.quantities.join(", ")}</dd>
            </dl>
          </div>
        </div>
      </div>

      <div className="judge-bar">
        <div className="q">에이전트 판정 <b>{LABEL_KO[s.label]}</b> · 결론 <b>{s.conclusion === "n_times" ? "지적(N배)" : s.conclusion === "hold" ? "보류" : "지적할 것 없음"}</b>이 맞습니까?</div>
        <div className="choices">
          <button className={`big ${choice === "agree" ? "on ok" : ""}`} onClick={() => { setChoice("agree"); setRejectOnly(false); setLabel(null); }}>✓ 맞다 <kbd>Y</kbd></button>
          <button className={`big ${choice === "disagree" ? "on bad" : ""}`} onClick={() => setChoice("disagree")}>✗ 틀리다 <kbd>N</kbd></button>
          {choice === "disagree" && (
            <div className="labels">
              <span className="small muted">맞는 판정:</span>
              {LABEL_KEYS.map((l, i) => (
                <button key={l} className={!rejectOnly && label === l ? "on" : ""} disabled={l === s.label}
                  onClick={() => { setRejectOnly(false); setLabel(l); }}>{LABEL_KO[l]} <kbd>{i + 1}</kbd></button>
              ))}
              {s.conclusion === "n_times" && (
                <button className={rejectOnly ? "on" : ""} onClick={() => { setRejectOnly(true); setLabel(null); }}
                  title="같은 물건 판단은 맞지만 지적하면 안 된다(가격 계산 등)">판정은 맞고 결론만 틀림 <kbd>R</kbd></button>
              )}
            </div>
          )}
        </div>
        <input className="note" placeholder="메모(선택): 왜 그렇게 판단했는지" value={note} onChange={(e) => setNote(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); void save(); } }} />
        <button className="primary" disabled={!canSave || saving} onClick={() => void save()}>
          {saving ? "저장 중…" : hasNextPending ? "저장하고 다음" : "저장"} <kbd>Enter</kbd>
        </button>
        {d.judgment && <span className="small muted">이미 판정함</span>}
      </div>
    </section>
  );
}
