package scrape

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

type fakeSite struct {
	mu     sync.Mutex
	paths  []string
	auth   []string
	robots string
	status int // robots.txt 상태(0이면 200)
}

func (f *fakeSite) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.RequestURI())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.mu.Unlock()
	switch r.URL.Path {
	case "/robots.txt":
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		w.Write([]byte(f.robots))
	case "/blocked":
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("<html><center>nginx</center></html>"))
	case "/missing":
		w.WriteHeader(http.StatusNotFound)
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html>ok " + r.URL.RawQuery + "</html>"))
	}
}

func newFake(t *testing.T, f *fakeSite) (*httptest.Server, *rate.Limiter) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return srv, rate.NewLimiter(rate.Inf, 1)
}

func TestSiteRobotsAndCrawlDelay(t *testing.T) {
	f := &fakeSite{robots: "User-agent: *\nDisallow: /private/\nCrawl-delay: 10\n"}
	srv, lim := newFake(t, f)
	lim.SetLimit(0.5)
	s, err := NewSite(context.Background(), SiteOptions{BaseURL: srv.URL, Limiter: lim, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Limit(); got != rate.Every(10*time.Second) {
		t.Errorf("crawl-delay가 리미터에 안 걸렸다: %v", got)
	}
	lim.SetLimit(rate.Inf) // 이후 테스트는 기다리지 않게

	if _, err := s.Get(context.Background(), "/private/x"); !errors.Is(err, ErrDisallowed) {
		t.Errorf("err=%v, want ErrDisallowed", err)
	}
	p, err := s.Get(context.Background(), "/dsearch.php?query=S24C310")
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != srv.URL+"/dsearch.php?query=S24C310" || string(p.Body) != "<html>ok query=S24C310</html>" || len(p.SHA256) != 64 {
		t.Errorf("page %+v", p)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, path := range f.paths {
		if path == "/private/x" {
			t.Error("robots가 막은 경로로 요청이 나갔다")
		}
		if f.auth[i] != "" {
			t.Errorf("시중 사이트에 Authorization 헤더가 나갔다: %s", path)
		}
	}
}

func TestSiteLowerLimitKeptWhenCrawlDelayLooser(t *testing.T) {
	f := &fakeSite{robots: "User-agent: *\nCrawl-delay: 1\n"}
	srv, lim := newFake(t, f)
	lim.SetLimit(0.2) // 설정이 더 느리면 그대로 둔다
	s, err := NewSite(context.Background(), SiteOptions{BaseURL: srv.URL, Limiter: lim, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if s.Limit() != 0.2 {
		t.Errorf("limit %v", s.Limit())
	}
}

func TestSiteNoRobotsAllowsAll(t *testing.T) {
	f := &fakeSite{status: http.StatusNotFound}
	srv, lim := newFake(t, f)
	s, err := NewSite(context.Background(), SiteOptions{BaseURL: srv.URL, Limiter: lim, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Allowed("/anything") {
		t.Error("robots.txt 404면 모두 허용")
	}
}

func TestSite403IsBlocked(t *testing.T) {
	f := &fakeSite{robots: "User-agent: *\n"}
	srv, lim := newFake(t, f)
	s, err := NewSite(context.Background(), SiteOptions{BaseURL: srv.URL, Limiter: lim, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "/blocked"); !errors.Is(err, ErrBlocked) {
		t.Errorf("err=%v, want ErrBlocked", err)
	}
	_, err = s.Get(context.Background(), "/missing")
	if err == nil || errors.Is(err, ErrBlocked) {
		t.Errorf("404는 차단이 아니다: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if p == "/blocked" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("403을 %d번 보냈다(재시도하지 않아야 한다)", n)
	}
}
