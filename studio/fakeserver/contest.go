package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 가짜 대회 API. 1부 CLI(savetax lookup·facts·submit)가 쓰는 엔드포인트만 같은 모양으로 흉내 낸다.
// 상태는 메모리에만 있다(다시 띄우면 처음부터).

const (
	limitDaily = 50
	limitTotal = 100
	maxBody    = 1 << 20
)

var kst = time.FixedZone("KST", 9*60*60)

type apiError struct {
	Status    int
	Code      string
	Message   string
	Retryable bool
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errf(status int, code, format string, a ...any) *apiError {
	return &apiError{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

type lintLine struct {
	FactID  string `json:"fact_id"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

type lintHint struct {
	FactID     string `json:"fact_id"`
	Role       string `json:"role"`
	EvidenceNo *int   `json:"evidence_no"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type lintResult struct {
	Status          string     `json:"status"`
	CheckedRevision *int       `json:"checked_revision"`
	CheckedAt       string     `json:"checked_at"`
	Lines           []lintLine `json:"lines"`
	Hints           []lintHint `json:"hints"`
}

type submission struct {
	ID         string
	ClaimID    string
	ItemID     string
	Revision   int
	Amount     string
	AdmittedAt time.Time
	Lint       lintResult
}

type keyRecord struct {
	BodySHA string
	Status  int
	Resp    []byte
}

type server struct {
	cat     *Catalog
	token   string
	shop    *shopSite
	eventID string
	started time.Time
	log     *slog.Logger
	now     func() time.Time

	mu        sync.Mutex
	states    map[string]string // 물품번호 → available | claimed | reform_found | claimed_by_me
	snapAt    map[string]string // 물품번호 → 첫 상세 조회 시각(스냅샷)
	subs      map[string]*submission
	byItem    map[string]string // 물품번호 → 제출 번호
	keys      map[string]keyRecord
	usedTotal int
	usedDay   map[string]int // KST 날짜 → 제출 수
	reqSeq    int
}

func newServer(cat *Catalog, token string, shop *shopSite, log *slog.Logger) *server {
	s := &server{
		cat: cat, token: token, shop: shop, log: log, now: time.Now,
		eventID: "demo-event-" + cat.Manifest.ReleaseID,
		states:  map[string]string{}, snapAt: map[string]string{},
		subs: map[string]*submission{}, byItem: map[string]string{}, keys: map[string]keyRecord{},
		usedDay: map[string]int{},
	}
	s.started = s.now()
	for id, it := range cat.Items {
		s.states[id] = it.LookupState
	}
	return s
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/events/current", s.wrap(false, s.event))
	mux.HandleFunc("GET /v1/events/current/me", s.wrap(true, s.me))
	mux.HandleFunc("GET /v1/events/current/items/lookup", s.wrap(true, s.lookup))
	mux.HandleFunc("GET /v1/events/current/items/{id}", s.wrap(true, s.item))
	mux.HandleFunc("POST /v1/events/current/submissions/preview", s.wrap(true, s.preview))
	mux.HandleFunc("POST /v1/events/current/submissions", s.wrap(true, s.submit))
	mux.HandleFunc("POST /v1/events/current/submissions/{sid}/revisions", s.wrap(true, s.revise))
	mux.HandleFunc("GET /v1/events/current/submissions/{sid}", s.wrap(true, s.submissionDetail))
	mux.HandleFunc("GET /v1/events/current/submission-requests/{key}", s.wrap(true, s.submissionRequest))
	mux.HandleFunc("/", s.wrap(false, func(*http.Request) (int, any, error) {
		return 0, nil, errf(404, "NOT_FOUND", "데모 서버에 없는 경로다")
	}))
	return mux
}

type handlerFunc func(r *http.Request) (int, any, error)

// wrap은 인증·요청 번호·오류 형식(평면 {code, message, request_id, retryable})을 붙인다.
func (s *server) wrap(auth bool, h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqSeq++
		rid := fmt.Sprintf("req-demo-%06d", s.reqSeq)
		s.mu.Unlock()
		w.Header().Set("X-Request-Id", rid)
		status, body, err := 0, any(nil), error(nil)
		if r.TLS == nil {
			err = errf(400, "https_required", "https로만 받는다")
		} else if auth && r.Header.Get("Authorization") != "Bearer "+s.token {
			err = errf(401, "unauthorized", "데모 토큰이 아니다")
		} else {
			status, body, err = h(r)
		}
		var ae *apiError
		if errors.As(err, &ae) {
			status, body = ae.Status, map[string]any{"code": ae.Code, "message": ae.Message, "request_id": rid, "retryable": ae.Retryable}
		} else if err != nil {
			status, body = 500, map[string]any{"code": "INTERNAL", "message": err.Error(), "request_id": rid, "retryable": true}
		}
		var raw []byte
		if b, ok := body.([]byte); ok {
			raw = b // 멱등 재전송: 저장한 응답 바이트를 그대로 돌려준다
		} else {
			raw, _ = json.Marshal(body)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		s.log.Info("contest", "method", r.Method, "path", r.URL.Path, "status", status, "request_id", rid)
	}
}

func (s *server) event(*http.Request) (int, any, error) {
	now := s.now()
	s.mu.Lock()
	n := len(s.subs)
	s.mu.Unlock()
	return 200, map[string]any{
		"event_id": s.eventID, "slug": "savetax-demo", "phase": "RUNNING", "admission_state": "open", "contest_mode": "demo",
		"opens_at": s.started.Add(-24 * time.Hour).UTC().Format(time.RFC3339), "closes_at": s.started.Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
		"server_time": now.UTC().Format(time.RFC3339), "ruleset_version": s.cat.Manifest.RulesetVersion,
		"dataset_release_id": s.cat.Manifest.ReleaseID, "scoring_version": "scoring-v1", "identity_version": "identity-v1",
		"schedule_version": 1, "event_epoch": 1, "evidence_format": "text", "item_catalog": "synthetic",
		"release_claim_on_withdraw": true, "submission_count": n,
		"price_gap": map[string]string{"rule": "enforce", "min_ratio": "1.2", "min_unit_krw": "1000000"},
	}, nil
}

func (s *server) me(*http.Request) (int, any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := s.usedDay[s.now().In(kst).Format("2006-01-02")]
	return 200, map[string]any{
		"handle":    "demo_user",
		"limits":    map[string]int{"daily": limitDaily, "total": limitTotal},
		"remaining": map[string]int{"daily": limitDaily - day, "total": limitTotal - s.usedTotal},
	}, nil
}

func (s *server) lookup(r *http.Request) (int, any, error) {
	raw := r.URL.Query().Get("ids")
	var ids []string
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > 100 {
		return 0, nil, errf(400, "INVALID_IDS", "ids는 1~100개: %d개", len(ids))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		st, ok := s.states[id]
		row := map[string]any{"input": id, "kind": "item", "item_id": id, "state": st, "message": stateMessage(st)}
		if !ok {
			row["kind"], row["item_id"], row["state"], row["message"] = "unknown", "", "not_found", "데모 카탈로그에 없는 번호"
		}
		if st == "claimed_by_me" {
			sub := s.subs[s.byItem[id]]
			row["my_submission_id"], row["current_revision"] = sub.ID, sub.Revision
		}
		items = append(items, row)
	}
	return 200, map[string]any{"items": items}, nil
}

func stateMessage(st string) string {
	switch st {
	case "available":
		return "등록할 수 있다"
	case "claimed":
		return "다른 참가자가 먼저 등록했다"
	case "reform_found":
		return "운영 측이 먼저 찾은 물품이다"
	case "claimed_by_me":
		return "내가 등록한 물품이다"
	}
	return ""
}

func (s *server) item(r *http.Request) (int, any, error) {
	id := r.PathValue("id")
	it := s.cat.Items[id]
	if it == nil {
		return 0, nil, errf(404, "ITEM_NOT_FOUND", "데모 카탈로그에 없는 물품번호다: %s", id)
	}
	s.mu.Lock()
	if s.snapAt[id] == "" {
		s.snapAt[id] = s.now().UTC().Format(time.RFC3339Nano)
	}
	st, snapAt := s.states[id], s.snapAt[id]
	s.mu.Unlock()
	snap := it.Snapshot
	snap.FetchedAt = snapAt
	return 200, map[string]any{
		"item_id": it.ItemID, "snapshot": snap, "facts": it.Facts,
		"availability": map[string]string{"state": st, "message": stateMessage(st)},
	}, nil
}

// 본문은 submission-schema v2 모양이다. 모르는 필드는 거절한다(실서버와 같다).
type claimBody struct {
	DatasetReleaseID   string      `json:"dataset_release_id"`
	RulesetVersion     string      `json:"ruleset_version"`
	CanonicalProductID string      `json:"canonical_product_id"`
	Title              string      `json:"title"`
	Claim              *string     `json:"claim,omitempty"`
	ComparisonScope    *string     `json:"comparison_scope,omitempty"`
	ReproductionNotes  *string     `json:"reproduction_notes,omitempty"`
	Lines              []claimLine `json:"lines"`
}

type claimLine struct {
	FactID                string            `json:"fact_id"`
	BenchmarkUnitPriceKRW string            `json:"benchmark_unit_price_krw"`
	ComparisonBasisID     string            `json:"comparison_basis_id"`
	Unit                  string            `json:"unit"`
	TaxBasis              string            `json:"tax_basis"`
	Period                *string           `json:"period"`
	PriceEvidence         []evidence        `json:"price_evidence"`
	FulfillmentEvidence   []json.RawMessage `json:"fulfillment_evidence,omitempty"`
}

type evidence struct {
	Kind             string  `json:"kind"`
	SourceURL        string  `json:"source_url"`
	Quote            string  `json:"quote"`
	ObservedPriceKRW string  `json:"observed_price_krw"`
	ObservedAt       string  `json:"observed_at"`
	VATIncluded      *bool   `json:"vat_included"`
	ShippingIncluded *bool   `json:"shipping_included"`
	Note             *string `json:"note,omitempty"`
}

func readBody(r *http.Request) ([]byte, *claimBody, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, nil, err
	}
	if len(raw) > maxBody {
		return nil, nil, errf(413, "BODY_TOO_LARGE", "본문이 1MiB를 넘는다")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var b claimBody
	if err := dec.Decode(&b); err != nil {
		return nil, nil, errf(400, "INVALID_JSON", "본문을 읽지 못했다: %v", err)
	}
	if dec.More() {
		return nil, nil, errf(400, "INVALID_JSON", "JSON 값 뒤에 다른 내용이 있다")
	}
	return raw, &b, nil
}

// check는 본문을 물품 fact와 맞춰 보고 금액을 계산한다(잠금 없이 부른다 — 카탈로그는 읽기 전용).
func (s *server) check(b *claimBody) (*Item, evaluation, error) {
	if b.DatasetReleaseID != s.cat.Manifest.ReleaseID || b.RulesetVersion != s.cat.Manifest.RulesetVersion {
		return nil, evaluation{}, errf(422, "RELEASE_MISMATCH", "dataset_release_id·ruleset_version이 현재 값(%s / %s)이 아니다",
			s.cat.Manifest.ReleaseID, s.cat.Manifest.RulesetVersion)
	}
	it := s.cat.Items[b.CanonicalProductID]
	if it == nil {
		return nil, evaluation{}, errf(404, "ITEM_NOT_FOUND", "데모 카탈로그에 없는 물품번호다: %s", b.CanonicalProductID)
	}
	if strings.TrimSpace(b.Title) == "" {
		return nil, evaluation{}, errf(422, "INVALID_TITLE", "title이 비었다")
	}
	if len(b.Lines) == 0 {
		return nil, evaluation{}, errf(422, "NO_LINES", "lines가 비었다")
	}
	facts := map[string]Fact{}
	for _, f := range it.Facts {
		facts[f.FactID] = f
	}
	seen := map[string]bool{}
	var lines []evalLine
	for i, l := range b.Lines {
		f, ok := facts[l.FactID]
		switch {
		case !ok:
			return nil, evaluation{}, errf(422, "FACT_NOT_IN_PRODUCT", "lines[%d]: 이 물품의 fact가 아니다: %s", i, l.FactID)
		case !f.Scorable:
			return nil, evaluation{}, errf(422, "FACT_NOT_SCORABLE", "lines[%d]: 점수 제외 fact다", i)
		case seen[l.FactID]:
			return nil, evaluation{}, errf(422, "DUPLICATE_FACT", "lines[%d]: 같은 fact가 두 번 나온다", i)
		case l.Unit != f.Unit || l.TaxBasis != f.TaxBasis || l.ComparisonBasisID != f.BasisID:
			return nil, evaluation{}, errf(422, "COMPARISON_BASIS_MISMATCH", "lines[%d]: unit·tax_basis·comparison_basis_id가 fact 값과 다르다", i)
		}
		seen[l.FactID] = true
		bm, ok := parseDec(l.BenchmarkUnitPriceKRW)
		if !ok || bm.Sign() <= 0 {
			return nil, evaluation{}, errf(422, "INVALID_BENCHMARK", "lines[%d]: benchmark_unit_price_krw는 0보다 큰 십진 문자열이어야 한다", i)
		}
		if n := len(l.PriceEvidence); n < 1 || n > 8 {
			return nil, evaluation{}, errf(422, "INVALID_EVIDENCE", "lines[%d]: 근거는 1~8개: %d개", i, n)
		}
		for j, e := range l.PriceEvidence {
			if e.Kind != "price_page" || !strings.HasPrefix(e.SourceURL, "https://") || strings.TrimSpace(e.Quote) == "" ||
				e.VATIncluded == nil || e.ShippingIncluded == nil {
				return nil, evaluation{}, errf(422, "INVALID_EVIDENCE", "lines[%d].price_evidence[%d]: kind·https 주소·인용·VAT·배송 여부가 필요하다", i, j)
			}
			if o, ok := parseDec(e.ObservedPriceKRW); !ok || o.Sign() <= 0 || !o.IsInt() {
				return nil, evaluation{}, errf(422, "INVALID_EVIDENCE", "lines[%d].price_evidence[%d]: observed_price_krw는 양의 정수여야 한다", i, j)
			}
		}
		lines = append(lines, evalLine{Fact: f, BasisID: l.ComparisonBasisID, Period: l.Period, Benchmark: bm})
	}
	return it, evaluate(lines, defaultGap), nil
}

func gapJSON(ev evaluation) map[string]any {
	groups := []map[string]any{}
	for _, g := range ev.Groups {
		groups = append(groups, map[string]any{
			"basis_id": g.BasisID, "procurement_unit_krw": g.P.FloatString(2), "market_unit_krw": g.M.FloatString(2),
			"markup_ratio": ratioString(g.P, g.M), "passes": g.Pass,
		})
	}
	return map[string]any{"rule": "enforce", "min_ratio": "1.2", "min_unit_krw": "1000000", "passes": ev.Pass, "by_overall": ev.ByOverall, "groups": groups}
}

func linesJSON(ev evaluation) []map[string]any {
	out := []map[string]any{}
	for _, l := range ev.Lines {
		n, _ := strconv.ParseInt(l.Amount.String(), 10, 64) // 서버는 줄 예상액을 숫자로 준다(실측)
		out = append(out, map[string]any{"fact_id": l.FactID, "score_amount_krw": n})
	}
	return out
}

func (s *server) preview(r *http.Request) (int, any, error) {
	_, b, err := readBody(r)
	if err != nil {
		return 0, nil, err
	}
	it, ev, err := s.check(b)
	if err != nil {
		return 0, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[it.ItemID]
	out := map[string]any{
		"ok": true, "total_amount_krw": ev.Total.String(), "lines": linesJSON(ev), "price_gap": gapJSON(ev),
		"product_already_claimed": st == "claimed" || st == "reform_found" || st == "claimed_by_me",
		"claimed_by_me":           st == "claimed_by_me", "existing_submission_id": "", "existing_submission_state": "", "current_revision": nil,
	}
	switch {
	case st == "claimed_by_me":
		sub := s.subs[s.byItem[it.ItemID]]
		out["next_action"], out["existing_submission_id"], out["existing_submission_state"], out["current_revision"] = "revise_existing", sub.ID, "accepted", sub.Revision
	case st != "available" || !ev.Pass:
		out["next_action"] = "pick_another_product"
	default:
		out["next_action"] = "submit"
	}
	return 200, out, nil
}

// idempotent는 같은 key·같은 본문이면 저장한 응답을 그대로 돌려준다. key는 같은데 본문이 다르면 거절한다.
func (s *server) idempotent(r *http.Request, raw []byte) (key, sha string, replay *keyRecord, err error) {
	key = r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 {
		return "", "", nil, errf(400, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key 헤더가 필요하다")
	}
	sum := sha256.Sum256(raw)
	sha = hex.EncodeToString(sum[:])
	if rec, ok := s.keys[key]; ok {
		if rec.BodySHA != sha {
			return "", "", nil, errf(422, "IDEMPOTENCY_KEY_REUSED", "같은 Idempotency-Key로 다른 본문을 보냈다")
		}
		return key, sha, &rec, nil
	}
	return key, sha, nil, nil
}

func (s *server) submit(r *http.Request) (int, any, error) {
	raw, b, err := readBody(r)
	if err != nil {
		return 0, nil, err
	}
	it, ev, err := s.check(b)
	if err != nil {
		return 0, nil, err
	}
	lint := s.lint(b) // 판매처 페이지만 읽는다(잠금 밖)
	s.mu.Lock()
	defer s.mu.Unlock()
	key, sha, replay, err := s.idempotent(r, raw)
	if err != nil {
		return 0, nil, err
	}
	if replay != nil {
		return replay.Status, replay.Resp, nil
	}
	switch st := s.states[it.ItemID]; st {
	case "available":
	case "reform_found":
		return 0, nil, errf(409, "REFORM_ALREADY_FOUND", "운영 측이 먼저 찾은 물품이다")
	case "claimed_by_me":
		return 0, nil, errf(409, "ALREADY_CLAIMED_BY_ME", "내 제출이 있다 — revisions로 고친다")
	default:
		return 0, nil, errf(409, "ITEM_ALREADY_CLAIMED", "다른 참가자가 먼저 등록했다")
	}
	if ev.Counted == 0 {
		return 0, nil, errf(422, "NO_POSITIVE_AMOUNT", "예상액이 1원 이상인 줄이 없다")
	}
	if !ev.Pass {
		return 0, nil, errf(422, "PRICE_GAP_TOO_SMALL", "가격 차이 기준(P ≥ 1.2×M 또는 P−M ≥ 100만 원)을 넘지 못한다")
	}
	day := s.now().In(kst).Format("2006-01-02")
	if s.usedTotal >= limitTotal || s.usedDay[day] >= limitDaily {
		return 0, nil, &apiError{Status: 429, Code: "PARTICIPANT_LIMIT", Message: "제출 한도(하루 50·누적 100)를 다 썼다", Retryable: false}
	}
	sub := &submission{ID: newID(), ClaimID: newID(), ItemID: it.ItemID, Revision: 1, Amount: ev.Total.String(), AdmittedAt: s.now().UTC()}
	rev := sub.Revision
	lint.CheckedRevision, lint.CheckedAt = &rev, s.now().UTC().Format(time.RFC3339)
	sub.Lint = lint
	s.subs[sub.ID], s.byItem[it.ItemID], s.states[it.ItemID] = sub, sub.ID, "claimed_by_me"
	s.usedTotal++
	s.usedDay[day]++
	resp, _ := json.Marshal(s.receipt(sub))
	s.keys[key] = keyRecord{BodySHA: sha, Status: 201, Resp: resp}
	return 201, resp, nil
}

func (s *server) revise(r *http.Request) (int, any, error) {
	sid := r.PathValue("sid")
	raw, b, err := readBody(r)
	if err != nil {
		return 0, nil, err
	}
	_, ev, err := s.check(b)
	if err != nil {
		return 0, nil, err
	}
	lint := s.lint(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.subs[sid]
	if sub == nil {
		return 0, nil, errf(404, "SUBMISSION_NOT_FOUND", "내 제출이 아니다: %s", sid)
	}
	if b.CanonicalProductID != sub.ItemID {
		return 0, nil, errf(422, "PRODUCT_MISMATCH", "수정으로 물품을 바꿀 수 없다")
	}
	key, sha, replay, err := s.idempotent(r, raw)
	if err != nil {
		return 0, nil, err
	}
	if replay != nil {
		return replay.Status, replay.Resp, nil
	}
	if r.Header.Get("If-Match") != strconv.Itoa(sub.Revision) {
		return 0, nil, errf(412, "REVISION_MISMATCH", "If-Match가 현재 revision %d가 아니다", sub.Revision)
	}
	if ev.Counted == 0 || !ev.Pass {
		return 0, nil, errf(422, "PRICE_GAP_TOO_SMALL", "가격 차이 기준을 넘지 못한다")
	}
	sub.Revision++
	sub.Amount = ev.Total.String()
	rev := sub.Revision
	lint.CheckedRevision, lint.CheckedAt = &rev, s.now().UTC().Format(time.RFC3339)
	sub.Lint = lint
	resp, _ := json.Marshal(s.receipt(sub))
	s.keys[key] = keyRecord{BodySHA: sha, Status: 201, Resp: resp}
	return 201, resp, nil
}

// receipt는 접수 시점의 영수증이다. lint는 접수 직후라 pending으로 내보내고, 상세 조회에서 결과를 준다.
func (s *server) receipt(sub *submission) map[string]any {
	return map[string]any{
		"submission_id": sub.ID, "claim_id": sub.ClaimID, "revision": sub.Revision, "submission_state": "accepted",
		"admitted_at": sub.AdmittedAt.Format(time.RFC3339Nano), "amount_krw": sub.Amount, "verification_status": "not_started",
		"lint_status": "pending", "event_epoch": 1, "ruleset_version": s.cat.Manifest.RulesetVersion,
		"api_path": "/v1/events/current/submissions/" + sub.ID, "web_path": "#/submission/" + sub.ID,
	}
}

func (s *server) submissionDetail(r *http.Request) (int, any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := s.subs[r.PathValue("sid")]
	if sub == nil {
		return 0, nil, errf(404, "SUBMISSION_NOT_FOUND", "내 제출이 아니다")
	}
	return 200, map[string]any{
		"submission_id": sub.ID, "item_id": sub.ItemID, "submission_state": "accepted", "current_revision": sub.Revision,
		"amount_krw": sub.Amount, "lint_status": sub.Lint.Status, "lint": sub.Lint,
	}, nil
}

func (s *server) submissionRequest(r *http.Request) (int, any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.keys[r.PathValue("key")]
	if !ok {
		return 0, nil, errf(404, "NOT_FOUND", "이 Idempotency-Key로 접수된 것이 없다")
	}
	return 200, rec.Resp, nil
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
