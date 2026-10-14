package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/time/rate"

	"savetax-lab/collector/internal/scrape"
	"savetax-lab/collector/internal/scrape/danawa"
	"savetax-lab/collector/internal/store/bronze"
	"savetax-lab/collector/internal/store/state"
)

// fakeDanawa는 검색·상품 경로를 한 서버에서 흉내 낸다. 픽스처는 danawa 패키지의 합성 HTML이다.
type fakeDanawa struct {
	mu   sync.Mutex
	hits []string
	auth int
	prod map[string]int // pcode → 상태 코드(없으면 200)
}

func (f *fakeDanawa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits = append(f.hits, r.URL.RequestURI())
	if r.Header.Get("Authorization") != "" {
		f.auth++
	}
	f.mu.Unlock()
	fixture := func(name string) {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "scrape", "danawa", "testdata", name))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	}
	switch r.URL.Path {
	case "/robots.txt":
		w.Write([]byte("User-agent: *\nDisallow: /bridge/\n"))
	case "/dsearch.php":
		fixture("search.html")
	case "/info/":
		if code := f.prod[r.URL.Query().Get("pcode")]; code != 0 {
			w.WriteHeader(code)
			return
		}
		fixture("product.html")
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeDanawa) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, h := range f.hits {
		if strings.HasPrefix(h, prefix) {
			n++
		}
	}
	return n
}

func newTestScraper(t *testing.T, f *fakeDanawa, top int) (*scraper, *state.Store, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	site := func() *scrape.Site {
		s, err := scrape.NewSite(context.Background(), scrape.SiteOptions{
			BaseURL: srv.URL, Limiter: rate.NewLimiter(rate.Inf, 1), HTTPClient: srv.Client(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	st, err := state.Open(filepath.Join(t.TempDir(), "state", "collector.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	out := &bytes.Buffer{}
	return &scraper{
		st: st, run: bronze.NewRun(t.TempDir(), "scrape", nil), out: out,
		dc: danawa.New(site(), site()), top: top,
	}, st, out
}

func readEnvelopes(t *testing.T, root, kind string) []bronze.Envelope {
	t.Helper()
	parts, _ := filepath.Glob(filepath.Join(root, "retail", "danawa", kind, "dt=*", "*.jsonl.gz"))
	var out []bronze.Envelope
	for _, p := range parts {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(gz)
		sc.Buffer(nil, 4<<20)
		for sc.Scan() {
			var e bronze.Envelope
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
		f.Close()
	}
	return out
}

func TestScrapeSearchEnqueuesTopProducts(t *testing.T) {
	// 검색 픽스처의 가격비교 상품은 1001, 2002. 2002는 404로 둔다.
	f := &fakeDanawa{prod: map[string]int{"2002": http.StatusNotFound}}
	s, st, out := newTestScraper(t, f, 2)
	ctx := context.Background()
	if _, err := st.Enqueue(ctx, scrapeKind, []state.Task{{Key: "q:TX-100", Priority: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.loop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.run.Finish(nil); err != nil {
		t.Fatal(err)
	}
	c, _ := st.Counts(ctx, scrapeKind)
	if c[state.Done] != 2 || c[state.Skipped] != 1 || c[state.Pending] != 0 {
		t.Errorf("counts %v (검색 1 + 상품 1 done, 404 상품 1 skipped)", c)
	}
	if f.count("/dsearch.php") != 1 || f.count("/info/") != 2 {
		t.Errorf("hits %v", f.hits)
	}
	if f.auth != 0 {
		t.Error("다나와에 Authorization 헤더가 나갔다")
	}
	if !strings.Contains(out.String(), "없음(404)") {
		t.Errorf("404 안내가 없다:\n%s", out)
	}

	root := s.run.Root()
	search := readEnvelopes(t, root, "search")
	prods := readEnvelopes(t, root, "product")
	if len(search) != 1 || len(prods) != 1 {
		t.Fatalf("records search=%d product=%d", len(search), len(prods))
	}
	e := prods[0]
	if e.Source != "danawa" || e.Endpoint != "/info/" || e.Params["pcode"] != "1001" || e.Raw == nil {
		t.Errorf("envelope %+v", e)
	}
	var p danawa.Product
	if err := json.Unmarshal(e.Body, &p); err != nil || p.PCode != "1001" || len(p.Malls) != 3 {
		t.Errorf("body %s err=%v", e.Body, err)
	}
	// 원문 파일이 레코드의 sha256과 맞아야 인용을 나중에 원문에서 다시 찾을 수 있다.
	gzb, err := os.ReadFile(filepath.Join(root, e.Raw.Path))
	if err != nil {
		t.Fatal(err)
	}
	zr, _ := gzip.NewReader(bytes.NewReader(gzb))
	var raw bytes.Buffer
	raw.ReadFrom(zr)
	if sum := sha256.Sum256(raw.Bytes()); hex.EncodeToString(sum[:]) != e.Raw.SHA256 {
		t.Errorf("원문 sha256 불일치")
	}
}

func TestScrapeBlockedStopsRun(t *testing.T) {
	f := &fakeDanawa{prod: map[string]int{"1001": http.StatusForbidden}}
	s, st, _ := newTestScraper(t, f, 0)
	ctx := context.Background()
	st.Enqueue(ctx, scrapeKind, []state.Task{{Key: "p:1001", Priority: 2}, {Key: "p:3003", Priority: 1}})
	err := s.loop(ctx)
	if !errors.Is(err, scrape.ErrBlocked) {
		t.Fatalf("err=%v, want ErrBlocked", err)
	}
	if f.count("/info/?pcode=3003") != 0 {
		t.Error("403 뒤에도 같은 도메인에 계속 요청했다")
	}
	if f.count("/info/?pcode=1001") != 1 {
		t.Error("403을 재시도했다")
	}
	c, _ := st.Counts(ctx, scrapeKind)
	if c[state.Pending] != 2 { // 1001은 시도 1/3이라 pending, 3003은 손대지 않음
		t.Errorf("counts %v", c)
	}
}

func TestReadQueryFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "q.txt")
	os.WriteFile(p, []byte("# 주석\nS24C310\n\n  LG 27UP850  \n"), 0o644)
	got, err := readQueryFile(p)
	if err != nil || len(got) != 2 || got[0] != "S24C310" || got[1] != "LG 27UP850" {
		t.Errorf("got %q err=%v", got, err)
	}
}
