package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/scrape/naver"
	"savetax-lab/collector/internal/store/bronze"
	"savetax-lab/collector/internal/store/state"
)

// 합성 응답(실제 API 응답을 옮기지 않는다).
const naverFixture = `{"total":1,"start":1,"display":1,"items":[{"title":"<b>TX-100</b> 테스트","link":"https://example.com/p/1",
"lprice":"12000","hprice":"","mallName":"테스트몰","productId":"1","productType":"2","brand":"","maker":"",
"category1":"a","category2":"","category3":"","category4":""}]}`

type fakeNaver struct {
	mu      sync.Mutex
	queries []string
	auth    int
	keyless int
	status  map[string]int    // 검색어 → 상태 코드(없으면 200)
	body    map[string]string // 검색어 → 본문(없으면 naverFixture)
}

func (f *fakeNaver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("query")
	f.mu.Lock()
	f.queries = append(f.queries, q)
	if r.Header.Get("Authorization") != "" {
		f.auth++
	}
	if r.Header.Get("X-Naver-Client-Id") == "" || r.Header.Get("X-Naver-Client-Secret") == "" {
		f.keyless++
	}
	f.mu.Unlock()
	if r.URL.Path != naver.SearchPath {
		http.NotFound(w, r)
		return
	}
	if code := f.status[q]; code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, `{"errorMessage":"테스트 오류","errorCode":"024"}`)
		return
	}
	body, ok := f.body[q]
	if !ok {
		body = naverFixture
	}
	io.WriteString(w, body)
}

func newTestNaver(t *testing.T, f *fakeNaver) (*naverScraper, *state.Store) {
	t.Helper()
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	h, err := httpx.New(httpx.Options{Service: "retail:naver", BaseURL: srv.URL, HTTPClient: srv.Client(), MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	nc, err := naver.New(h, naver.Keys{ID: "id-x", Secret: "secret-y"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(filepath.Join(t.TempDir(), "state", "collector.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &naverScraper{st: st, run: bronze.NewRun(t.TempDir(), "scrape", nil), out: &bytes.Buffer{}, nc: nc, display: 50}, st
}

func TestNaverSearchSavesRawJSON(t *testing.T) {
	f := &fakeNaver{body: map[string]string{"BAD-1": "<html>점검 중</html>"}}
	s, st := newTestNaver(t, f)
	ctx := context.Background()
	st.Enqueue(ctx, naverKind, []state.Task{{Key: "q:TX-100", Priority: 2}, {Key: "q:BAD-1", Priority: 1}})
	if err := s.loop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run.Finish(nil); err != nil {
		t.Fatal(err)
	}
	if f.auth != 0 || f.keyless != 0 {
		t.Errorf("Authorization %d회 · 키 없는 요청 %d회", f.auth, f.keyless)
	}
	c, _ := st.Counts(ctx, naverKind)
	if c[state.Done] != 1 || c[state.Failed] != 1 {
		t.Errorf("counts %v (TX-100 done, 해석 실패한 BAD-1은 %d회 시도 뒤 failed)", c, naverMaxAttempts)
	}
	envs := readEnvelopesIn(t, filepath.Join(s.run.Root(), "retail", "naver", "search"))
	if len(envs) != 1+naverMaxAttempts {
		t.Fatalf("records %d (해석 실패한 응답도 시도마다 원문을 남긴다)", len(envs))
	}
	for _, e := range envs {
		switch e.Params["query"] {
		case "TX-100":
			if e.Source != "naver_shop_api" || e.Endpoint != naver.SearchPath || e.Params["display"] != "50" || e.Params["exclude"] != naver.Exclude {
				t.Errorf("envelope %+v", e)
			}
			// bronze writer가 <·>를 \u003c로 적으므로 값으로 비교한다.
			var got, want any
			json.Unmarshal(e.Body, &got)
			json.Unmarshal([]byte(naverFixture), &want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("원문과 다르다: %s", e.Body)
			}
		case "BAD-1":
			var s string
			if json.Unmarshal(e.Body, &s) != nil || !strings.Contains(s, "점검 중") {
				t.Errorf("JSON 아닌 원문은 문자열로 남긴다: %s", e.Body)
			}
		default:
			t.Errorf("모르는 검색어 %v", e.Params["query"])
		}
	}
}

func TestNaverAuthFailureStopsAndKeepsTask(t *testing.T) {
	f := &fakeNaver{status: map[string]int{"TX-100": http.StatusUnauthorized}}
	s, st := newTestNaver(t, f)
	ctx := context.Background()
	st.Enqueue(ctx, naverKind, []state.Task{{Key: "q:TX-100", Priority: 2}, {Key: "q:TX-200", Priority: 1}})
	err := s.loop(ctx)
	if err == nil || !naver.Fatal(err) {
		t.Fatalf("인증 실패로 멈춰야 한다: %v", err)
	}
	if len(f.queries) != 1 {
		t.Errorf("멈춘 뒤에도 보냈다: %v", f.queries)
	}
	if c, _ := st.Counts(ctx, naverKind); c[state.Pending] != 2 || c[state.Failed] != 0 {
		t.Errorf("counts %v (둘 다 pending으로 남아야 키를 고친 뒤 이어진다)", c)
	}
}
