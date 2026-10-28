package main

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 테스트용 작은 카탈로그: 블록 1개(fact 2줄), 이미 선점된 물품 1개, 판매처 페이지 3개.
func testCatalog() *Catalog {
	vatOff := false
	fact := func(id, p, q string) Fact {
		c := mulInt(p, q)
		return Fact{FactID: id, Unit: "㎡", TaxBasis: "vat_included", BasisID: "basis:㎡:vat_included", ComparisonBasisID: "basis:㎡:vat_included",
			UnitPriceKRW: p, Quantity: q, ComparableAmountKRW: c, Scorable: true, ExclusionReasons: []string{}}
	}
	cat := &Catalog{
		Manifest: Manifest{ReleaseID: "synthetic-test", RulesetVersion: "rules-v1"},
		Items: map[string]*Item{
			"SYN0001": {ItemID: "SYN0001", Snapshot: Snapshot{Name: "인조화강블록"}, LookupState: "available",
				Facts: []Fact{fact("dl:SYN0001:1", "27000", "100"), fact("dl:SYN0001:2", "27000", "50")}},
			"SYN0002": {ItemID: "SYN0002", LookupState: "claimed", Facts: []Fact{fact("dl:SYN0002:1", "1000", "1")}},
		},
		Pages: map[string]*Page{},
	}
	add := func(path, render, title, body string) {
		p := &Page{Path: path, Render: render, Title: title, VATIncluded: &vatOff, html: []byte(body)}
		p.text = visibleText(body)
		cat.Pages[path] = p
	}
	add("/item/K1", "static", "인조화강블록 200×200×60", `<h1>인조화강블록 200×200×60</h1><p>판매가 <b>9,900원</b> 부가세 별도</p>`)
	add("/item/K2", "spa", "인조화강블록 앱", `<div id="root"></div><script>render()</script>`)
	add("/item/K3", "static", "인조화강블록 가격없음", `<h1>인조화강블록 가격없음</h1><p>견적 문의 바랍니다 원하시는 수량</p>`)
	return cat
}

func mulInt(a, b string) string {
	x, _ := parseDec(a)
	y, _ := parseDec(b)
	return x.Mul(x, y).FloatString(0)
}

func newTestServer(t *testing.T) (*server, http.Handler) {
	t.Helper()
	cat := testCatalog()
	shop := &shopSite{cat: cat, host: "127.0.0.1:18444"}
	s := newServer(cat, DemoToken, shop, slog.New(slog.DiscardHandler))
	s.now = func() time.Time { return time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC) }
	return s, s.handler()
}

func call(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.TLS = &tls.ConnectionState{}
	req.Header.Set("Authorization", "Bearer "+DemoToken)
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s %s: JSON이 아니다: %s", method, path, raw)
	}
	return rec.Code, out
}

func claimJSON(item, benchmark, url, quote, price string, vat bool) string {
	b := map[string]any{
		"dataset_release_id": "synthetic-test", "ruleset_version": "rules-v1", "canonical_product_id": item,
		"title": "동급 인조화강블록 시중가 대비 2.4배",
		"lines": []map[string]any{{
			"fact_id": "dl:" + item + ":1", "benchmark_unit_price_krw": benchmark, "comparison_basis_id": "basis:㎡:vat_included",
			"unit": "㎡", "tax_basis": "vat_included", "period": nil,
			"price_evidence": []map[string]any{{"kind": "price_page", "source_url": url, "quote": quote,
				"observed_price_krw": price, "observed_at": "2026-10-28", "vat_included": vat, "shipping_included": false}},
		}},
	}
	raw, _ := json.Marshal(b)
	return string(raw)
}

const goodQuote = "데모 판매처 상품 페이지 — 인조화강블록 200×200×60. 판매가 9,900원(부가세 별도)."

func TestAuthAndHTTPS(t *testing.T) {
	_, h := newTestServer(t)
	if code, _ := call(t, h, "GET", "/v1/events/current", "", map[string]string{"Authorization": ""}); code != 200 {
		t.Errorf("공개 엔드포인트가 %d", code)
	}
	code, body := call(t, h, "GET", "/v1/events/current/items/SYN0001", "", map[string]string{"Authorization": "Bearer savetax_real_token"})
	if code != 401 || body["code"] != "unauthorized" || body["request_id"] == "" {
		t.Errorf("다른 토큰을 받았다: %d %v", code, body)
	}
}

func TestPreviewAmountsAndGap(t *testing.T) {
	_, h := newTestServer(t)
	// 줄 1만 보낸다: C = 27,000×100 = 2,700,000, B = 10,890 → 예상액 2,700,000 − 1,089,000 = 1,611,000
	code, pv := call(t, h, "POST", "/v1/events/current/submissions/preview",
		claimJSON("SYN0001", "10890", "https://127.0.0.1:18444/item/K1", goodQuote, "9900", false), nil)
	if code != 200 || pv["total_amount_krw"] != "1611000" || pv["next_action"] != "submit" {
		t.Fatalf("preview = %d %v", code, pv)
	}
	gap := pv["price_gap"].(map[string]any)
	if gap["passes"] != true || gap["groups"].([]any)[0].(map[string]any)["markup_ratio"] != "2.479" {
		t.Errorf("price_gap = %v", gap)
	}
	// B = 24,000이면 27,000 < 1.2×24,000 이고 차이도 100만 미만 → 통과 못 함
	_, pv = call(t, h, "POST", "/v1/events/current/submissions/preview",
		claimJSON("SYN0001", "24000", "https://127.0.0.1:18444/item/K1", goodQuote, "9900", false), nil)
	if pv["next_action"] != "pick_another_product" || pv["price_gap"].(map[string]any)["passes"] != false {
		t.Errorf("gap 미달인데 %v", pv)
	}
	code, e := call(t, h, "POST", "/v1/events/current/submissions",
		claimJSON("SYN0001", "24000", "https://127.0.0.1:18444/item/K1", goodQuote, "9900", false), map[string]string{"Idempotency-Key": "k-gap"})
	if code != 422 || e["code"] != "PRICE_GAP_TOO_SMALL" {
		t.Errorf("gap 미달 제출 = %d %v", code, e)
	}
}

func TestPreviewRejectsBasisMismatchAndUnknownFields(t *testing.T) {
	_, h := newTestServer(t)
	body := strings.Replace(claimJSON("SYN0001", "10890", "https://127.0.0.1:18444/item/K1", goodQuote, "9900", false), `"unit":"㎡"`, `"unit":"m2"`, 1)
	if code, e := call(t, h, "POST", "/v1/events/current/submissions/preview", body, nil); code != 422 || e["code"] != "COMPARISON_BASIS_MISMATCH" {
		t.Errorf("단위 표기가 다른데 %d %v", code, e)
	}
	body = strings.Replace(claimJSON("SYN0001", "10890", "https://127.0.0.1:18444/item/K1", goodQuote, "9900", false), `"title"`, `"extra":1,"title"`, 1)
	if code, e := call(t, h, "POST", "/v1/events/current/submissions/preview", body, nil); code != 400 || e["code"] != "INVALID_JSON" {
		t.Errorf("모르는 필드를 받았다: %d %v", code, e)
	}
}

func TestSubmitIdempotencyLookupAndRevision(t *testing.T) {
	_, h := newTestServer(t)
	body := claimJSON("SYN0001", "10890", "https://127.0.0.1:18444/item/K1", goodQuote, "9900", false)
	code, rc := call(t, h, "POST", "/v1/events/current/submissions", body, map[string]string{"Idempotency-Key": "k1"})
	if code != 201 || rc["lint_status"] != "pending" || rc["amount_krw"] != "1611000" {
		t.Fatalf("제출 = %d %v", code, rc)
	}
	sid := rc["submission_id"].(string)
	// 같은 key·같은 본문 → 같은 영수증, 같은 key·다른 본문 → 거절
	if code, again := call(t, h, "POST", "/v1/events/current/submissions", body, map[string]string{"Idempotency-Key": "k1"}); code != 201 || again["submission_id"] != sid {
		t.Errorf("멱등 재전송 = %d %v", code, again)
	}
	other := claimJSON("SYN0001", "11000", "https://127.0.0.1:18444/item/K1", goodQuote, "9900", false)
	if code, e := call(t, h, "POST", "/v1/events/current/submissions", other, map[string]string{"Idempotency-Key": "k1"}); code != 422 || e["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Errorf("key 재사용 = %d %v", code, e)
	}
	if code, e := call(t, h, "POST", "/v1/events/current/submissions", other, map[string]string{"Idempotency-Key": ""}); code != 400 || e["code"] != "IDEMPOTENCY_KEY_REQUIRED" {
		t.Errorf("key 없음 = %d %v", code, e)
	}
	// 제출 뒤 선점 상태·상세·lint
	_, lk := call(t, h, "GET", "/v1/events/current/items/lookup?ids=SYN0001,SYN0002,NOPE", "", nil)
	rows := lk["items"].([]any)
	if rows[0].(map[string]any)["state"] != "claimed_by_me" || rows[1].(map[string]any)["state"] != "claimed" || rows[2].(map[string]any)["state"] != "not_found" {
		t.Errorf("lookup = %v", rows)
	}
	_, sub := call(t, h, "GET", "/v1/events/current/submissions/"+sid, "", nil)
	if sub["lint_status"] != "clean" {
		t.Errorf("lint = %v", sub["lint"])
	}
	if code, rq := call(t, h, "GET", "/v1/events/current/submission-requests/k1", "", nil); code != 200 || rq["submission_id"] != sid {
		t.Errorf("submission-requests = %d %v", code, rq)
	}
	// 수정: preview는 revise_existing, If-Match가 틀리면 412, 맞으면 revision 2
	_, pv := call(t, h, "POST", "/v1/events/current/submissions/preview", other, nil)
	if pv["next_action"] != "revise_existing" || pv["existing_submission_id"] != sid {
		t.Errorf("수정 preview = %v", pv)
	}
	if code, e := call(t, h, "POST", "/v1/events/current/submissions/"+sid+"/revisions", other, map[string]string{"Idempotency-Key": "k2", "If-Match": "7"}); code != 412 {
		t.Errorf("If-Match 틀림 = %d %v", code, e)
	}
	if code, r2 := call(t, h, "POST", "/v1/events/current/submissions/"+sid+"/revisions", other, map[string]string{"Idempotency-Key": "k3", "If-Match": "1"}); code != 201 || r2["revision"] != float64(2) {
		t.Errorf("수정 = %d %v", code, r2)
	}
	// 남이 선점한 물품은 409
	if code, e := call(t, h, "POST", "/v1/events/current/submissions", claimJSON("SYN0002", "100", "https://127.0.0.1:18444/item/K1", goodQuote, "9900", false),
		map[string]string{"Idempotency-Key": "k4"}); code != 409 || e["code"] != "ITEM_ALREADY_CLAIMED" {
		t.Errorf("선점 물품 제출 = %d %v", code, e)
	}
}

func TestLintVerdicts(t *testing.T) {
	s, _ := newTestServer(t)
	cases := []struct {
		name, url, quote, price string
		vat                     bool
		want, code              string
	}{
		{"정상", "https://127.0.0.1:18444/item/K1", goodQuote, "9900", false, "clean", ""},
		{"JS 껍데기", "https://127.0.0.1:18444/item/K2", "인조화강블록 앱 9,900원", "9900", false, "unreadable", "EMPTY_SHELL"},
		{"가격 없는 페이지", "https://127.0.0.1:18444/item/K3", "인조화강블록 가격없음", "9900", false, "suspect", "PRICE_NOT_IN_PAGE"},
		{"관측가가 다름", "https://127.0.0.1:18444/item/K1", goodQuote, "9000", false, "suspect", "PRICE_NOT_IN_PAGE"},
		{"부가세 표기 다름", "https://127.0.0.1:18444/item/K1", goodQuote, "9900", true, "suspect", "VAT_MISMATCH"},
		{"인용에 상품명 없음", "https://127.0.0.1:18444/item/K1", "블록 9,900원", "9900", false, "suspect", "QUOTE_MISSING_TITLE"},
		{"없는 페이지", "https://127.0.0.1:18444/item/K9", goodQuote, "9900", false, "suspect", "PAGE_NOT_FOUND"},
		{"외부 페이지", "https://shop.example.com/item/K1", goodQuote, "9900", false, "unreadable", "EXTERNAL_PAGE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b claimBody
			if err := json.Unmarshal([]byte(claimJSON("SYN0001", "10890", tc.url, tc.quote, tc.price, tc.vat)), &b); err != nil {
				t.Fatal(err)
			}
			res := s.lint(&b)
			if res.Status != tc.want {
				t.Fatalf("status = %s, want %s (%v)", res.Status, tc.want, res.Hints)
			}
			if tc.code != "" && (len(res.Hints) != 1 || res.Hints[0].Code != tc.code) {
				t.Errorf("hints = %v, want %s", res.Hints, tc.code)
			}
		})
	}
}

func TestShopRobotsAndPages(t *testing.T) {
	cat := testCatalog()
	h := (&shopSite{cat: cat, host: "127.0.0.1:18444"}).handler()
	for path, want := range map[string]int{"/robots.txt": 200, "/item/K1": 200, "/item/K9": 404} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("%s = %d, want %d", path, rec.Code, want)
		}
	}
}

func TestCommasAndRequireLoopback(t *testing.T) {
	for in, want := range map[string]string{"9900": "9,900", "108900": "108,900", "1000000": "1,000,000", "990": "990"} {
		if got := commas(in); got != want {
			t.Errorf("commas(%s) = %s", in, got)
		}
	}
	if requireLoopback("0.0.0.0:18443") == nil || requireLoopback("192.168.0.2:1") == nil {
		t.Error("루프백이 아닌 주소를 열려고 했다")
	}
	if requireLoopback("127.0.0.1:18443") != nil || requireLoopback("localhost:1") != nil {
		t.Error("루프백 주소를 거절했다")
	}
}
