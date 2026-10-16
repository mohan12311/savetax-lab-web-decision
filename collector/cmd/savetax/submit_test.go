package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"savetax-lab/collector/internal/api/contest"
	"savetax-lab/collector/internal/claim"
	"savetax-lab/collector/internal/config"
	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/store/bronze"
)

const testItem = "24883583"

// fakeContest는 제출 흐름에 필요한 대회 API만 흉내 낸다. 받은 요청을 센다.
type fakeContest struct {
	mu       sync.Mutex
	state    string // lookup 상태
	previews int
	submits  []string // Idempotency-Key
	revises  []string // "If-Match|Idempotency-Key"
	bodies   [][]byte
}

func (f *fakeContest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/v1/events/current" && r.Header.Get("Authorization") != "Bearer "+fakeToken {
		w.WriteHeader(401)
		w.Write([]byte(`{"code":"AUTH_REQUIRED","message":"x","retryable":false}`))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/events/current":
		json.NewEncoder(w).Encode(map[string]any{
			"dataset_release_id": "rel-1", "ruleset_version": "rules-v1",
			"closes_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			"price_gap": map[string]string{"rule": "enforce", "min_ratio": "1.2", "min_unit_krw": "1000000"},
		})
	case r.Method == "GET" && r.URL.Path == "/v1/events/current/items/lookup":
		json.NewEncoder(w).Encode(contest.LookupResult{Items: []contest.LookupItem{{Input: testItem, Kind: "item", ItemID: testItem, State: f.state}}})
	case r.Method == "GET" && r.URL.Path == "/v1/events/current/items/"+testItem:
		w.Write([]byte(`{"item_id":"24883583","snapshot":{"name":"회전국솥 300인용","model":"DIK-300G","fetched_at":"2026-10-15T00:00:00.000Z","facts_total":1},
		  "availability":{"state":"` + f.state + `"},
		  "facts":[{"fact_id":"dl:24883583:c:r:1","unit":"대","tax_basis":"vat_included","basis_id":"basis:대:vat_included","period_kind":"none",
		            "unit_price_krw":"150000","quantity":"10","comparable_amount_krw":"1500000","scorable":true,"exclusion_reasons":[]}]}`))
	case r.Method == "POST" && r.URL.Path == "/v1/events/current/submissions/preview":
		f.previews++
		b, _ := io.ReadAll(r.Body)
		f.bodies = append(f.bodies, b)
		if f.state == "claimed_by_me" {
			// 실서버처럼 score_amount_krw를 숫자로 준다(10/16 실측)
			w.Write([]byte(`{"ok":true,"total_amount_krw":"500000","lines":[{"fact_id":"dl:24883583:c:r:1","score_amount_krw":500000}],"next_action":"revise_existing",
			  "claimed_by_me":true,"existing_submission_id":"sub-1","existing_submission_state":"accepted","current_revision":1,"price_gap":{"pass":true}}`))
			return
		}
		w.Write([]byte(`{"ok":true,"total_amount_krw":"500000","lines":[{"fact_id":"dl:24883583:c:r:1","score_amount_krw":"500000"}],"next_action":"submit","price_gap":{"pass":true}}`))
	case r.Method == "POST" && r.URL.Path == "/v1/events/current/submissions":
		f.submits = append(f.submits, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(201)
		w.Write([]byte(`{"submission_id":"sub-1","claim_id":"c-1","revision":1,"submission_state":"accepted","amount_krw":"500000","lint_status":"pending","web_path":"/s/sub-1"}`))
	case r.Method == "POST" && r.URL.Path == "/v1/events/current/submissions/sub-1/revisions":
		f.revises = append(f.revises, r.Header.Get("If-Match")+"|"+r.Header.Get("Idempotency-Key"))
		w.WriteHeader(201)
		w.Write([]byte(`{"submission_id":"sub-1","revision":2,"submission_state":"accepted","amount_krw":500000,"lint_status":"pending"}`))
	case r.Method == "GET" && r.URL.Path == "/v1/events/current/submissions/sub-1":
		w.Write([]byte(`{"submission_id":"sub-1","submission_state":"accepted","amount_krw":"500000","lint_status":"clean","lint":{"status":"clean","lines":[{"fact_id":"dl:24883583:c:r:1","verdict":"clean","reason":"일치"}]}}`))
	default:
		w.WriteHeader(404)
		w.Write([]byte(`{"code":"NOT_FOUND","message":"x","retryable":false}`))
	}
}

const evidenceURL = "https://shop.example/item/1"

// setupSubmit은 가짜 서버·임시 데이터 폴더·본문 파일을 만든다. price는 근거 관측가, archive면 그날 받은 원문을 bronze에 둔다.
func setupSubmit(t *testing.T, price string, archive bool) (*app, *contest.Client, *fakeContest, string) {
	t.Helper()
	f := &fakeContest{state: "available"}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	h, err := httpx.New(httpx.Options{Service: "contest", BaseURL: srv.URL, Token: fakeToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a := &app{cfg: &config.Config{DataDir: root}}
	now := time.Now()
	today := now.In(kst).Format("2006-01-02")
	if archive {
		run := bronze.NewRun(a.bronzeRoot(), "probe", nil)
		page := []byte(`<html><head><meta charset="utf-8"><title>회전국솥 DIK-300G</title></head><body><h1>디포 회전국솥 300인용 DIK-300G</h1><p>판매가 ` + commas(price) + `원</p></body></html>`)
		ref, err := run.WriteBlob("retail/probe", "html", now, page, "html", "text/html; charset=utf-8")
		if err != nil {
			t.Fatal(err)
		}
		w, _ := run.Writer("retail/probe", "page")
		if err := w.Write(bronze.Envelope{FetchedAt: now.UTC(), Source: "probe", Endpoint: "/", Status: 200,
			Params: map[string]any{"url": evidenceURL}, Body: json.RawMessage(`{}`), Raw: &ref}); err != nil {
			t.Fatal(err)
		}
		if _, err := run.Finish(nil); err != nil {
			t.Fatal(err)
		}
	}
	yes, no := true, false
	cl := claim.Claim{DatasetReleaseID: "rel-1", RulesetVersion: "rules-v1", CanonicalProductID: testItem,
		Title: "같은 모델의 시중가 대비 1.5배",
		Lines: []claim.Line{{FactID: "dl:24883583:c:r:1", BenchmarkUnitPriceKRW: price, ComparisonBasisID: "basis:대:vat_included",
			Unit: "대", TaxBasis: "vat_included",
			PriceEvidence: []claim.PriceEvidence{{Kind: "price_page", SourceURL: evidenceURL,
				Quote:            "디포 회전국솥 300인용 DIK-300G 판매가 " + commas(price) + "원 부가세 포함, 배송비 별도, shop.example, " + today + " 확인",
				ObservedPriceKRW: price, ObservedAt: today, VATIncluded: &yes, ShippingIncluded: &no}}}}}
	b, _ := json.MarshalIndent(cl, "", "  ")
	path := filepath.Join(root, "gold", "claims", testItem+".json")
	if err := bronze.WriteFileAtomic(path, b); err != nil {
		t.Fatal(err)
	}
	return a, contest.New(h), f, path
}

func runSubmitForTest(t *testing.T, a *app, c *contest.Client, path string, confirm bool) (string, error) {
	t.Helper()
	return runClaimForTest(t, a, c, path, confirm, false)
}

func runClaimForTest(t *testing.T, a *app, c *contest.Client, path string, confirm, revise bool) (string, error) {
	t.Helper()
	old := lookupSleep
	lookupSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { lookupSleep = old })
	var out strings.Builder
	run := bronze.NewRun(a.bronzeRoot(), "submit", nil)
	err := submitClaim(context.Background(), a, c, run, path, confirm, revise, time.Minute, &out)
	if _, ferr := run.Finish(err); ferr != nil {
		t.Fatal(ferr)
	}
	return out.String(), err
}

func TestSubmitDryRunPreviewsOnly(t *testing.T) {
	a, c, f, path := setupSubmit(t, "100000", true)
	out, err := runSubmitForTest(t, a, c, path, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("dry-run 출력:\n%s", out)
	if f.previews != 1 || len(f.submits) != 0 {
		t.Fatalf("preview %d번, 제출 %d번 — dry-run은 preview만\n%s", f.previews, len(f.submits), out)
	}
	if !strings.Contains(out, "P 150,000원 · M 100,000원 · 1.50배") || !strings.Contains(out, "dry-run") {
		t.Errorf("출력:\n%s", out)
	}
	// 보낸 본문은 우리가 정한 필드만 담는다(모르는 필드 없음)
	if _, err := claim.Parse(f.bodies[0]); err != nil {
		t.Errorf("보낸 본문: %v", err)
	}
}

func TestSubmitConfirmSendsOnceWithKeyAndRecords(t *testing.T) {
	a, c, f, path := setupSubmit(t, "100000", true)
	out, err := runSubmitForTest(t, a, c, path, true)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(f.submits) != 1 || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(f.submits[0]) {
		t.Fatalf("제출은 UUID v4 key로 한 번: %v\n%s", f.submits, out)
	}
	rb, err := os.ReadFile(filepath.Join(a.stateDir(), "submissions", f.submits[0]+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec submitRecord
	if err := json.Unmarshal(rb, &rec); err != nil || rec.State != "accepted" || rec.SubmissionID != "sub-1" || rec.LintStatus != "clean" {
		t.Errorf("기록 = %+v (%v)", rec, err)
	}
	if !strings.Contains(out, "lint clean") {
		t.Errorf("lint 결과가 출력에 없다:\n%s", out)
	}
}

func TestSubmitBlocksWithoutArchivedPage(t *testing.T) {
	a, c, f, path := setupSubmit(t, "100000", false)
	out, err := runSubmitForTest(t, a, c, path, true)
	if err == nil || f.previews != 0 || len(f.submits) != 0 || !strings.Contains(out, "보관한 원문이 없다") {
		t.Fatalf("보관 원문 없으면 서버에 보내지 않는다: err=%v preview=%d\n%s", err, f.previews, out)
	}
}

func TestSubmitBlocksSmallGapBeforeServer(t *testing.T) {
	a, c, f, path := setupSubmit(t, "140000", true) // 150,000 ÷ 140,000 = 1.07배, 차이 1만 원
	out, err := runSubmitForTest(t, a, c, path, true)
	if err == nil || f.previews != 0 || !strings.Contains(out, "미달") || !strings.Contains(out, "통과 가능한 최대 시중가 125,000원") {
		t.Fatalf("가격 차이 미달은 서버에 보내지 않는다: err=%v preview=%d\n%s", err, f.previews, out)
	}
}

func TestSubmitBlocksClaimedItem(t *testing.T) {
	a, c, f, path := setupSubmit(t, "100000", true)
	f.state = "claimed"
	out, err := runSubmitForTest(t, a, c, path, true)
	if err == nil || f.previews != 0 || !strings.Contains(err.Error(), "등록할 수 없는 물품") {
		t.Fatalf("선점된 물품: err=%v preview=%d\n%s", err, f.previews, out)
	}
}

func TestFactsDraftCopiesFactValuesAndIsBlockedUntilFilled(t *testing.T) {
	a, c, _, _ := setupSubmit(t, "100000", false)
	os.Remove(filepath.Join(a.cfg.DataDir, "gold", "claims", testItem+".json"))
	ev, _, err := c.Event(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	it, _, err := c.Item(context.Background(), testItem)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	rule, _ := claim.ParseGapRule("enforce", "1.2", "1000000")
	printItem(&out, it, rule)
	if !strings.Contains(out.String(), "납품 단가 P 150,000원 → 시중가가 125,000원 이하여야 등록된다") {
		t.Errorf("요약:\n%s", out.String())
	}
	if err := writeDraft(a, ev, it, &out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "gold", "claims", testItem+".json"))
	if err != nil {
		t.Fatal(err)
	}
	cl, err := claim.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	l := cl.Lines[0]
	if l.Unit != "대" || l.TaxBasis != "vat_included" || l.ComparisonBasisID != "basis:대:vat_included" || len(l.PriceEvidence) != 0 {
		t.Errorf("초안 줄 = %+v", l)
	}
	ps := claim.Check(cl, claim.Env{DatasetReleaseID: "rel-1", RulesetVersion: "rules-v1", ItemID: testItem, Today: "2026-10-15", Facts: it.Facts})
	if !claim.Blocking(ps) {
		t.Error("근거가 빈 초안은 막혀야 한다")
	}
}

func TestReviseDryRunDoesNotPost(t *testing.T) {
	a, c, f, path := setupSubmit(t, "100000", true)
	f.state = "claimed_by_me"
	out, err := runClaimForTest(t, a, c, path, false, true)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(f.revises) != 0 || len(f.submits) != 0 {
		t.Errorf("dry-run인데 보냈다: revises=%v submits=%v", f.revises, f.submits)
	}
	if !strings.Contains(out, "수정 대상: 제출 sub-1 · 지금 revision 1") {
		t.Errorf("출력:\n%s", out)
	}
}

func TestReviseConfirmSendsIfMatchOnce(t *testing.T) {
	a, c, f, path := setupSubmit(t, "100000", true)
	f.state = "claimed_by_me"
	out, err := runClaimForTest(t, a, c, path, true, true)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(f.submits) != 0 {
		t.Errorf("수정인데 새 제출을 보냈다: %v", f.submits)
	}
	if len(f.revises) != 1 || !regexp.MustCompile(`^1\|[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(f.revises[0]) {
		t.Errorf("If-Match|key = %v", f.revises)
	}
	if !strings.Contains(out, "수정 반영: 제출 sub-1 · revision 2 · 예상액 500,000원") || !strings.Contains(out, "lint clean") {
		t.Errorf("출력:\n%s", out)
	}
}

func TestReviseRefusesItemNotMine(t *testing.T) {
	a, c, f, path := setupSubmit(t, "100000", true)
	out, err := runClaimForTest(t, a, c, path, true, true) // state=available
	if err == nil || len(f.revises) != 0 || f.previews != 0 {
		t.Errorf("내 물품이 아닌데 진행했다: err=%v revises=%v previews=%d\n%s", err, f.revises, f.previews, out)
	}
}
