package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"time"

	"savetax-lab/collector/internal/api/contest"
	"savetax-lab/collector/internal/claim"
	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/scrape/probe"
	"savetax-lab/collector/internal/store/bronze"
)

const lintPollEvery = 10 * time.Second

// runSubmit은 본문을 게이트 검사 → preview까지 하고, --confirm이 있을 때만 실제로 제출한다.
// 제출은 사람의 명시적 승인 뒤에만 한다(프로젝트 규칙 5). 검사가 하나라도 막으면 서버에 아무것도 보내지 않는다.
func runSubmit(ctx context.Context, a *app, args []string) error {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	path := fs.String("claim", "", "본문 파일(data/gold/claims/<item_id>.json)")
	confirm := fs.Bool("confirm", false, "preview가 next_action=submit이면 실제로 제출한다(제출 횟수를 쓴다)")
	revise := fs.Bool("revise", false, "내 기존 제출에 수정(revision)을 올린다(물품 그대로, 제출 횟수 안 씀). 보내는 것은 --confirm과 함께일 때만")
	status := fs.String("status", "", "Idempotency-Key로 접수 결과와 lint를 다시 본다(제출 없음)")
	poll := fs.Duration("poll", 3*time.Minute, "제출 뒤 lint 판정을 기다리는 시간")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if (*path == "") == (*status == "") {
		return usageError{"사용법: savetax submit --claim <파일> [--revise] [--confirm] | --status <Idempotency-Key>"}
	}
	c, err := a.contest()
	if err != nil {
		return err
	}
	if !c.HTTP().HasToken() {
		return httpx.ErrNoToken
	}
	out := os.Stdout
	run := bronze.NewRun(a.bronzeRoot(), "submit", args)
	run.TrackStats(c.HTTP().Service(), c.HTTP().Stats())
	if *status != "" {
		err = submitStatus(ctx, c, run, *status, out)
	} else {
		err = submitClaim(ctx, a, c, run, *path, *confirm, *revise, *poll, out)
	}
	mpath, ferr := run.Finish(err)
	if mpath != "" {
		fmt.Fprintf(out, "run manifest: %s\n", mpath)
	}
	return errors.Join(err, ferr)
}

func submitClaim(ctx context.Context, a *app, c *contest.Client, run *bronze.Run, path string, confirm, revise bool, poll time.Duration, out io.Writer) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cl, err := claim.Parse(raw)
	if err != nil {
		return err
	}
	body, err := cl.Body()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	bodySHA := hex.EncodeToString(sum[:])
	id := cl.CanonicalProductID
	fmt.Fprintf(out, "본문 %s · 물품 %s · 줄 %d개 · sha256 %s\n", path, id, len(cl.Lines), bodySHA[:12])

	// 1) 대회 상수(공개)·접수 기간
	ev, _, err := c.Event(ctx)
	if err != nil {
		return err
	}
	if time.Now().After(ev.ClosesAt) {
		return fmt.Errorf("접수가 끝났다(%s)", ev.ClosesAt.In(kst).Format("2006-01-02 15:04 MST"))
	}
	rule, err := claim.ParseGapRule(ev.PriceGap.Rule, ev.PriceGap.MinRatio, ev.PriceGap.MinUnitKRW)
	if err != nil {
		return err
	}
	// 2) 선점 상태(횟수 안 씀)
	lk, _, err := c.Lookup(ctx, []string{id})
	if err != nil {
		return err
	}
	wantState := "available"
	if revise {
		wantState = "claimed_by_me" // 수정은 내가 가진 물품에만
	}
	if len(lk.Items) != 1 || lk.Items[0].State != wantState {
		st := "(응답 없음)"
		if len(lk.Items) == 1 {
			st = lk.Items[0].State + " — " + lk.Items[0].Message
		}
		return fmt.Errorf("등록할 수 없는 물품이다: %s", st)
	}
	// 3) 물품 상세(fact) — 제출 직전이라 여기서 스냅샷을 만들어도 된다
	w, err := run.Writer("contest", "items")
	if err != nil {
		return err
	}
	it, err := fetchItem(ctx, c, w, id, out)
	if err != nil {
		return err
	}
	if it.Availability.State != wantState {
		return fmt.Errorf("물품 상세의 상태가 %s다(기대 %s)", it.Availability.State, wantState)
	}

	// 4) 게이트 검사: 스키마·fact 값 일치·근거 형식, 근거 원문 보관, 가격 차이
	today := time.Now().In(kst).Format("2006-01-02")
	ps := claim.Check(cl, claim.Env{DatasetReleaseID: ev.DatasetReleaseID, RulesetVersion: ev.RulesetVersion,
		ItemID: it.ItemID, Today: today, Facts: it.Facts})
	if !claim.Blocking(ps) {
		arch, err := checkArchive(a.bronzeRoot(), cl)
		if err != nil {
			return err
		}
		ps = append(ps, arch...)
	}
	var res claim.Result
	if !claim.Blocking(ps) {
		if res, err = claim.Evaluate(cl, it.Facts, rule); err != nil {
			return err
		}
		printEvaluation(out, res)
		if !res.Pass {
			ps = append(ps, claim.Problem{Path: "lines", Msg: "가격 차이 기준(또는 예상액 1원 이상)을 넘지 못한다 — 비교 단가를 낮춰 맞추지 말고 더 싼 근거나 다른 물품을 찾는다"})
		}
	}
	for _, p := range ps {
		fmt.Fprintln(out, " ", p)
	}
	if claim.Blocking(ps) {
		return errors.New("게이트 검사에서 막혔다 — 서버에 보내지 않았다")
	}

	// 5) preview(선점·쓰기 없음)
	pv, presp, err := c.Preview(ctx, body)
	if presp != nil {
		// 응답을 받았으면 해석에 실패해도 원문을 남긴다
		if werr := writeEnvelope(run, "preview", "/v1/events/current/submissions/preview",
			map[string]any{"item_id": id, "body_sha256": bodySHA}, presp); werr != nil {
			return errors.Join(err, werr)
		}
	}
	if err != nil {
		printAPIError(out, err)
		return fmt.Errorf("preview 거절: %w", err)
	}
	fmt.Fprintf(out, "preview: ok=%v · 서버 예상액 %s원 · next_action=%s\n", pv.OK, commas(string(pv.TotalAmountKRW)), pv.NextAction)
	if len(pv.PriceGap) > 0 {
		fmt.Fprintf(out, "  price_gap %s\n", compactJSON(pv.PriceGap))
	}
	if revise {
		return reviseClaim(ctx, a, c, run, pv, id, path, body, bodySHA, confirm, poll, out)
	}
	if !confirm {
		fmt.Fprintln(out, "dry-run: 여기까지다. 사람이 preview와 근거를 확인하고 승인한 뒤에만 --confirm으로 제출한다(make submit ID=…).")
		return nil
	}
	if !pv.OK || pv.NextAction != "submit" {
		return fmt.Errorf("preview가 제출 가능 상태가 아니다(ok=%v, next_action=%s)", pv.OK, pv.NextAction)
	}

	// 6) 제출: key를 먼저 기록하고 보낸다(응답을 못 받으면 같은 key로 결과를 찾는다)
	key, err := newUUID()
	if err != nil {
		return err
	}
	rec := submitRecord{Key: key, ItemID: id, ClaimPath: path, BodySHA256: bodySHA, CreatedAt: time.Now().UTC(), State: "sending"}
	recPath := filepath.Join(a.stateDir(), "submissions", key+".json")
	if err := rec.save(recPath); err != nil {
		return err
	}
	fmt.Fprintf(out, "제출: Idempotency-Key %s (기록 %s)\n", key, recPath)
	rc, sresp, err := c.Submit(ctx, body, key)
	if err != nil {
		printAPIError(out, err)
		rec.State, rec.Error = "error", err.Error()
		_ = rec.save(recPath)
		return fmt.Errorf("제출 실패 — 접수 여부가 불확실하면 새로 내기 전에 make submit-status KEY=%s: %w", key, err)
	}
	if err := writeEnvelope(run, "submissions", "/v1/events/current/submissions",
		map[string]any{"item_id": id, "body_sha256": bodySHA, "idempotency_key": key}, sresp); err != nil {
		return err
	}
	rec.State, rec.SubmissionID, rec.Receipt = "accepted", rc.SubmissionID, json.RawMessage(sresp.Body)
	if err := rec.save(recPath); err != nil {
		return err
	}
	fmt.Fprintf(out, "접수: 제출 %s · revision %d · 예상액 %s원 · 상태 %s · lint %s\n  %s\n",
		rc.SubmissionID, rc.Revision, commas(string(rc.AmountKRW)), rc.SubmissionState, rc.LintStatus, rc.WebPath)

	// 7) lint 판정(보통 5~15초)
	sub, err := waitLint(ctx, c, run, rc.SubmissionID, poll, out)
	if err != nil {
		return err
	}
	if sub != nil {
		rec.LintStatus = sub.LintStatus
		_ = rec.save(recPath)
	}
	return nil
}

func printEvaluation(out io.Writer, r claim.Result) {
	fmt.Fprintf(out, "사전 계산: 예상액 합 %s원 · 계산에 넣는 줄 %d/%d\n", commas(r.Total.String()), r.Counted, len(r.Lines))
	for _, g := range r.Groups {
		mark := "미달"
		if g.Pass {
			mark = "통과"
		}
		fmt.Fprintf(out, "  기준 %s: P %s원 · M %s원 · %s배 · 차이 %s원 → %s",
			g.BasisID, commas(claim.Won(g.P)), commas(claim.Won(g.M)), ratio2(g.P, g.M),
			commas(claim.Won(new(big.Rat).Sub(g.P, g.M))), mark)
		if g.MaxPassingM != nil {
			fmt.Fprintf(out, " (통과 가능한 최대 시중가 %s원)", commas(claim.Won(g.MaxPassingM)))
		}
		fmt.Fprintln(out)
	}
	if r.ByOverall {
		fmt.Fprintln(out, "  단위가 여럿이라 전체 금액 비율로 통과")
	}
}

// waitLint는 lint가 pending에서 벗어날 때까지 제출 상세를 읽는다. 시간이 다 되면 그대로 알린다.
func waitLint(ctx context.Context, c *contest.Client, run *bronze.Run, sid string, poll time.Duration, out io.Writer) (*contest.Submission, error) {
	deadline := time.Now().Add(poll)
	for {
		sub, resp, err := c.Submission(ctx, sid)
		if err != nil {
			return nil, err
		}
		if err := writeEnvelope(run, "submission", "/v1/events/current/submissions/{sid}", map[string]any{"submission_id": sid}, resp); err != nil {
			return nil, err
		}
		st := sub.LintStatus
		if sub.Lint != nil && sub.Lint.Status != "" {
			st = sub.Lint.Status
		}
		if st != "" && st != "pending" {
			printLint(out, sub)
			return sub, nil
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(out, "lint가 아직 pending이다 — 나중에 make submit-status로 본다(10분 넘게 pending이면 사람이 확인)\n")
			return sub, nil
		}
		if err := lookupSleep(ctx, lintPollEvery); err != nil {
			return nil, err
		}
	}
}

func printLint(out io.Writer, s *contest.Submission) {
	fmt.Fprintf(out, "제출 %s: 상태 %s · 예상액 %s원 · lint %s\n", s.SubmissionID, s.SubmissionState, commas(string(s.AmountKRW)), s.LintStatus)
	if s.Lint == nil {
		return
	}
	for _, l := range s.Lint.Lines {
		fmt.Fprintf(out, "  줄 %s: %s — %s\n", l.FactID, l.Verdict, l.Reason)
	}
	for _, h := range s.Lint.Hints {
		fmt.Fprintf(out, "  안내 %s %s: %s\n", h.FactID, h.Code, h.Message)
	}
}

// submitStatus는 Idempotency-Key로 접수 결과를 확인하고, 접수됐으면 lint까지 보여 준다(제출 없음).
func submitStatus(ctx context.Context, c *contest.Client, run *bronze.Run, key string, out io.Writer) error {
	resp, err := c.SubmissionRequest(ctx, key)
	if err != nil {
		if ae, ok := httpx.AsAPIError(err); ok && ae.Status == 404 {
			fmt.Fprintf(out, "%s: 접수된 것 없음(404) — 같은 본문이면 이 key로 다시 보내도 된다\n", key)
			return nil
		}
		return err
	}
	if err := writeEnvelope(run, "submission_requests", "/v1/events/current/submission-requests/{key}", map[string]any{"idempotency_key": key}, resp); err != nil {
		return err
	}
	if resp.Status == 202 {
		fmt.Fprintf(out, "%s: 아직 확정 전(202) — 잠시 뒤 다시 본다\n", key)
		return nil
	}
	var rc contest.Receipt
	if err := json.Unmarshal(resp.Body, &rc); err != nil || rc.SubmissionID == "" {
		fmt.Fprintf(out, "%s: %d %s\n", key, resp.Status, compactJSON(resp.Body))
		return nil
	}
	fmt.Fprintf(out, "%s: 접수됨 — 제출 %s · revision %d · 예상액 %s원\n", key, rc.SubmissionID, rc.Revision, commas(string(rc.AmountKRW)))
	sub, sresp, err := c.Submission(ctx, rc.SubmissionID)
	if err != nil {
		return err
	}
	if err := writeEnvelope(run, "submission", "/v1/events/current/submissions/{sid}", map[string]any{"submission_id": rc.SubmissionID}, sresp); err != nil {
		return err
	}
	printLint(out, sub)
	return nil
}

// checkArchive는 근거마다 우리가 받아 보관한 원문(bronze retail: 다나와 상품·probe 페이지)을 찾아,
// 관측일(KST)에 받은 원문의 본문 글자에 관측가(쉼표 표기)가 있는지 본다. 없으면 그 근거는 쓰지 않는다(규칙 1).
func checkArchive(root string, cl *claim.Claim) ([]claim.Problem, error) {
	idx, err := archiveIndex(root)
	if err != nil {
		return nil, err
	}
	var ps []claim.Problem
	for i, l := range cl.Lines {
		for j, e := range l.PriceEvidence {
			p := fmt.Sprintf("lines[%d].price_evidence[%d]", i, j)
			needle := commas(e.ObservedPriceKRW)
			found, sawURL := "", false
			for _, ar := range idx[e.SourceURL] {
				sawURL = true
				if ar.FetchedAt.In(kst).Format("2006-01-02") != e.ObservedAt {
					continue
				}
				page, err := readBlob(filepath.Join(root, ar.Raw.Path), ar.Raw.SHA256)
				if err != nil {
					return nil, err
				}
				if h := probe.Analyze(page, ar.Raw.ContentType, []string{needle}).Needles[needle]; h.InText {
					found = ar.Raw.SHA256
					break
				}
			}
			switch {
			case found != "":
				// 통과: 어느 원문인지 남겨 사람이 다시 볼 수 있게 한다
				ps = append(ps, claim.Problem{Path: p, Msg: fmt.Sprintf("보관 원문 %s… 본문에 %s 있음", found[:12], needle), OK: true})
			case !sawURL:
				ps = append(ps, claim.Problem{Path: p, Msg: "이 주소를 받아 보관한 원문이 없다 — make scrape ARGS='--pcode …' 또는 make probe로 먼저 받는다"})
			default:
				ps = append(ps, claim.Problem{Path: p, Msg: fmt.Sprintf("%s에 받은 원문 본문에서 %s를 찾지 못했다 — 그날 다시 받거나 관측가·관측일을 원문대로 고친다", e.ObservedAt, needle)})
			}
		}
	}
	return ps, nil
}

type archived struct {
	FetchedAt time.Time
	Raw       bronze.RawRef
}

// archiveIndex는 다나와 상품 페이지와 probe 페이지 봉투를 주소별로 모은다(params.url).
func archiveIndex(root string) (map[string][]archived, error) {
	idx := map[string][]archived{}
	for _, dir := range []string{filepath.Join("retail", "danawa", "product"), filepath.Join("retail", "probe", "page")} {
		files, err := filepath.Glob(filepath.Join(root, dir, "dt=*", "part-*.jsonl.gz"))
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		for _, f := range files {
			envs, err := readEnvelopeFile(f)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			for _, e := range envs {
				u, _ := e.Params["url"].(string)
				if u == "" || e.Raw == nil || e.Status != 200 {
					continue
				}
				idx[u] = append(idx[u], archived{FetchedAt: e.FetchedAt, Raw: *e.Raw})
			}
		}
	}
	return idx, nil
}

func writeEnvelope(run *bronze.Run, kind, endpoint string, params map[string]any, resp *httpx.Response) error {
	w, err := run.Writer("contest", kind)
	if err != nil {
		return err
	}
	var rid *string
	if v := resp.Header.Get("X-Request-Id"); v != "" {
		rid = &v
	}
	return w.Write(bronze.Envelope{FetchedAt: time.Now().UTC(), Source: "contest_api", Endpoint: endpoint,
		Params: params, Status: resp.Status, RequestID: rid, Body: json.RawMessage(resp.Body)})
}

func printAPIError(out io.Writer, err error) {
	ae, ok := httpx.AsAPIError(err)
	if !ok {
		return
	}
	fmt.Fprintf(out, "  서버: %d %s — %s\n", ae.Status, ae.Code, ae.Message)
	if len(ae.Context) > 0 {
		fmt.Fprintf(out, "  context %s\n", compactJSON(ae.Context))
	}
}

func compactJSON(b []byte) string {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return string(b)
	}
	return buf.String()
}

// submitRecord는 제출 시도마다 남기는 로컬 기록이다(data/state/submissions/<key>.json, gitignore).
type submitRecord struct {
	Key          string          `json:"idempotency_key"`
	ItemID       string          `json:"item_id"`
	ClaimPath    string          `json:"claim_path"`
	BodySHA256   string          `json:"body_sha256"`
	CreatedAt    time.Time       `json:"created_at"`
	Kind         string          `json:"kind,omitempty"` // submit(비면) | revision
	State        string          `json:"state"`          // sending | accepted | error
	SubmissionID string          `json:"submission_id,omitempty"`
	LintStatus   string          `json:"lint_status,omitempty"`
	Error        string          `json:"error,omitempty"`
	Receipt      json.RawMessage `json:"receipt,omitempty"`
}

func (r submitRecord) save(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return bronze.WriteFileAtomic(path, append(b, '\n'))
}

// newUUID는 무작위 UUID v4다(Idempotency-Key: 요청마다 새로).
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// ratio2는 p ÷ m을 소수 둘째 자리에서 내린 문자열이다(서버 표시와 같은 방식).
func ratio2(p, m *big.Rat) string {
	if p == nil || m == nil || m.Sign() == 0 {
		return "-"
	}
	r := new(big.Rat).Quo(p, m)
	r.Mul(r, big.NewRat(100, 1))
	q := new(big.Int).Quo(r.Num(), r.Denom())
	return new(big.Rat).SetFrac(q, big.NewInt(100)).FloatString(2)
}

// reviseClaim은 preview가 revise_existing일 때 내 제출에 수정을 올린다(--confirm이 없으면 여기서 멈춘다).
// 수정도 제출 변경이라 사람의 명시적 승인 뒤에만 보낸다(프로젝트 규칙 5). 제출 횟수는 쓰지 않는다.
func reviseClaim(ctx context.Context, a *app, c *contest.Client, run *bronze.Run, pv *contest.Preview, id, path string,
	body []byte, bodySHA string, confirm bool, poll time.Duration, out io.Writer) error {
	if !pv.OK || pv.NextAction != "revise_existing" || pv.ExistingSubmissionID == "" || pv.CurrentRevision == nil {
		return fmt.Errorf("preview가 수정 가능 상태가 아니다(ok=%v, next_action=%s, 제출 %q)", pv.OK, pv.NextAction, pv.ExistingSubmissionID)
	}
	sid, rev := pv.ExistingSubmissionID, *pv.CurrentRevision
	fmt.Fprintf(out, "수정 대상: 제출 %s · 지금 revision %d (%s)\n", sid, rev, pv.ExistingSubmissionState)
	if !confirm {
		fmt.Fprintln(out, "dry-run: 여기까지다. 승인 뒤에만 --revise --confirm으로 수정을 올린다(make revise ID=…).")
		return nil
	}
	key, err := newUUID()
	if err != nil {
		return err
	}
	rec := submitRecord{Key: key, Kind: "revision", ItemID: id, ClaimPath: path, BodySHA256: bodySHA, CreatedAt: time.Now().UTC(),
		State: "sending", SubmissionID: sid}
	recPath := filepath.Join(a.stateDir(), "submissions", key+".json")
	if err := rec.save(recPath); err != nil {
		return err
	}
	fmt.Fprintf(out, "수정: Idempotency-Key %s · If-Match %d (기록 %s)\n", key, rev, recPath)
	rc, rresp, err := c.Revise(ctx, sid, rev, body, key)
	if rresp != nil {
		if werr := writeEnvelope(run, "revisions", "/v1/events/current/submissions/{sid}/revisions",
			map[string]any{"item_id": id, "submission_id": sid, "if_match": rev, "body_sha256": bodySHA, "idempotency_key": key}, rresp); werr != nil {
			return errors.Join(err, werr)
		}
	}
	if err != nil {
		printAPIError(out, err)
		rec.State, rec.Error = "error", err.Error()
		_ = rec.save(recPath)
		return fmt.Errorf("수정 실패 — 반영 여부가 불확실하면 make submit-status KEY=%s: %w", key, err)
	}
	rec.State, rec.Receipt = "accepted", json.RawMessage(rresp.Body)
	if err := rec.save(recPath); err != nil {
		return err
	}
	fmt.Fprintf(out, "수정 반영: 제출 %s · revision %d · 예상액 %s원 · 상태 %s · lint %s\n",
		sid, rc.Revision, commas(string(rc.AmountKRW)), rc.SubmissionState, rc.LintStatus)
	sub, err := waitLint(ctx, c, run, sid, poll, out)
	if err != nil {
		return err
	}
	if sub != nil {
		rec.LintStatus = sub.LintStatus
		_ = rec.save(recPath)
	}
	return nil
}
