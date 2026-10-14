package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 가짜 토큰은 런타임에 조립한다 — 저장소에 실제 토큰처럼 보이는 리터럴을 두지 않는다(비밀 스캔 오탐 방지).
var testToken = "savetax" + "_" + strings.Repeat("T", 48)

type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) fn(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	s.d = append(s.d, d)
	s.mu.Unlock()
	return nil
}

func newTestClient(t *testing.T, srv *httptest.Server, mod func(*Options)) (*Client, *sleeps) {
	t.Helper()
	opt := Options{
		Service:     "test",
		BaseURL:     srv.URL,
		Token:       testToken,
		UserAgent:   "Mozilla/5.0 (savetax-lab)",
		MaxAttempts: 4,
		HTTPClient:  srv.Client(),
	}
	if mod != nil {
		mod(&opt)
	}
	c, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	s := &sleeps{}
	c.sleep = s.fn
	return c, s
}

func TestNewRejectsPlainHTTP(t *testing.T) {
	if _, err := New(Options{BaseURL: "http://hackathon.savetax.now"}); err == nil {
		t.Fatal("http base url을 받아들였다")
	}
}

func TestRetry429HonorsRetryAfter(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
			io.WriteString(w, `{"code":"RATE_LIMITED","message":"slow down","request_id":"r1"}`)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	c, s := newTestClient(t, srv, nil)

	var out struct{ OK bool }
	resp, err := c.GetJSON(context.Background(), "/v1/x", nil, false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK || resp.Attempts != 2 {
		t.Fatalf("ok=%v attempts=%d", out.OK, resp.Attempts)
	}
	if len(s.d) != 1 || s.d[0] < 7*time.Second {
		t.Errorf("Retry-After를 따르지 않았다: %v", s.d)
	}
	st := c.Stats().Snapshot()
	if st.Calls != 2 || st.Retries != 1 || st.ByStatus["429"] != 1 || st.ByCode["RATE_LIMITED"] != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestContestErrorNotRetried(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(422)
		io.WriteString(w, `{"code":"PRICE_GAP_TOO_SMALL","message":"gap","request_id":"req-9","retryable":false,
			"context":{"price_gap":{"max_passing_market_unit_krw":"47500"}}}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv, nil)

	_, err := c.Do(context.Background(), &Request{Method: "POST", Path: "/v1/p", Body: []byte(`{}`), Auth: true})
	ae, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("APIError가 아니다: %v", err)
	}
	if n.Load() != 1 {
		t.Errorf("422를 재시도했다: %d회", n.Load())
	}
	if ae.Code != "PRICE_GAP_TOO_SMALL" || ae.RequestID != "req-9" || ae.Retryable == nil || *ae.Retryable {
		t.Errorf("파싱 = %+v", ae)
	}
	if !strings.Contains(string(ae.Context), "max_passing_market_unit_krw") {
		t.Errorf("context 누락: %s", ae.Context)
	}
}

func TestExplicitRetryableFalseOn429(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(429)
		io.WriteString(w, `{"code":"PARTICIPANT_LIMIT","message":"한도 소진","retryable":false}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv, nil)
	_, err := c.Do(context.Background(), &Request{Path: "/v1/x"})
	if ae, ok := AsAPIError(err); !ok || ae.Code != "PARTICIPANT_LIMIT" {
		t.Fatalf("err = %v", err)
	}
	if n.Load() != 1 {
		t.Errorf("retryable:false인데 %d회 보냈다", n.Load())
	}
}

func TestNestedDataAPIError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"code":"bad_arguments","message":"unknown arg","hint":"use id"}}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv, nil)
	_, err := c.Do(context.Background(), &Request{Path: "/v1/x"})
	ae, ok := AsAPIError(err)
	if !ok || ae.Code != "bad_arguments" || ae.Hint != "use id" || ae.ShouldRetry() {
		t.Fatalf("err = %#v", err)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(503)
		io.WriteString(w, `<html>bad gateway</html>`)
	}))
	defer srv.Close()
	c, s := newTestClient(t, srv, nil)
	_, err := c.Do(context.Background(), &Request{Path: "/v1/x"})
	ae, ok := AsAPIError(err)
	if !ok || ae.Status != 503 || !strings.Contains(ae.Raw, "bad gateway") {
		t.Fatalf("err = %v", err)
	}
	if n.Load() != 4 || len(s.d) != 3 {
		t.Errorf("calls=%d sleeps=%d", n.Load(), len(s.d))
	}
	for i := 1; i < len(s.d); i++ {
		if s.d[i] < s.d[i-1]/2 {
			t.Errorf("백오프가 늘지 않았다: %v", s.d)
		}
	}
}

func TestRetryAfterTooLongFailsFast(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	c, s := newTestClient(t, srv, func(o *Options) { o.MaxRetryAfter = time.Minute })
	if _, err := c.Do(context.Background(), &Request{Path: "/v1/x"}); err == nil {
		t.Fatal("성공하면 안 된다")
	}
	if len(s.d) != 0 {
		t.Errorf("상한을 넘는 Retry-After를 기다렸다: %v", s.d)
	}
}

func TestAuthHeader(t *testing.T) {
	var got []string
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization"))
		mu.Unlock()
		if strings.Contains(r.URL.RawQuery, "savetax_") {
			t.Error("토큰이 쿼리에 들어갔다")
		}
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv, nil)
	ctx := context.Background()
	if _, err := c.Do(ctx, &Request{Path: "/v1/a", Auth: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(ctx, &Request{Path: "/v1/b"}); err != nil {
		t.Fatal(err)
	}
	if got[0] != "Bearer "+testToken || got[1] != "" {
		t.Errorf("Authorization = %q", got)
	}

	anon, _ := newTestClient(t, srv, func(o *Options) { o.Token = "" })
	if _, err := anon.Do(ctx, &Request{Path: "/v1/c", Auth: true}); !errors.Is(err, ErrNoToken) {
		t.Errorf("err = %v", err)
	}
	if anon.Stats().Snapshot().Calls != 0 {
		t.Error("토큰 없이 인증 요청을 보냈다")
	}
}

func TestAbsoluteURLMustMatchHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c, _ := newTestClient(t, srv, nil)
	for _, p := range []string{"https://evil.example/x", strings.Replace(srv.URL, "https", "http", 1) + "/x", "v1/no-slash"} {
		if _, err := c.Do(context.Background(), &Request{Path: p, Auth: true}); err == nil {
			t.Errorf("%s 를 허용했다", p)
		}
	}
	if c.Stats().Snapshot().Calls != 0 {
		t.Error("요청이 나갔다")
	}
	if _, err := c.Do(context.Background(), &Request{Path: srv.URL + "/ok"}); err != nil {
		t.Errorf("같은 호스트 절대 URL 거부: %v", err)
	}
}

func TestCrossOriginRedirectRejected(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherHits.Add(1)
		io.WriteString(w, `{}`)
	}))
	defer other.Close()
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// 같은 127.0.0.1의 다른 포트 = 다른 출처(net/http 기본값이면 토큰이 넘어가는 경우).
		http.Redirect(w, r, other.URL+"/landing?sig=secret", http.StatusFound)
	}))
	defer srv.Close()

	// httptest 서버들은 같은 테스트 인증서를 쓰므로 한 클라이언트로 둘 다 신뢰한다.
	c, s := newTestClient(t, srv, nil)
	_, err := c.Do(context.Background(), &Request{Path: "/v1/r", Auth: true})
	if !errors.Is(err, ErrRedirectRejected) {
		t.Fatalf("err = %v", err)
	}
	if otherHits.Load() != 0 {
		t.Error("다른 출처로 요청이 나갔다")
	}
	if hits.Load() != 1 || len(s.d) != 0 {
		t.Errorf("재시도하면 안 된다: hits=%d sleeps=%v", hits.Load(), s.d)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("오류에 Location 쿼리가 들어갔다: %v", err)
	}
	if st := c.Stats().Snapshot(); st.ByStatus["redirect_rejected"] != 1 {
		t.Errorf("stats = %+v", st)
	}
}

func TestSameOriginRedirectFollowed(t *testing.T) {
	var landingAuth atomic.Value
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/landing" {
			landingAuth.Store(r.Header.Get("Authorization"))
			io.WriteString(w, `{}`)
			return
		}
		http.Redirect(w, r, "/landing", http.StatusFound)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv, nil)
	if _, err := c.Do(context.Background(), &Request{Path: "/v1/r", Auth: true}); err != nil {
		t.Fatal(err)
	}
	if v := landingAuth.Load(); v != "Bearer "+testToken {
		t.Errorf("같은 출처 리다이렉트에서 Authorization = %v", v)
	}
}

func TestOrigin(t *testing.T) {
	same := [][2]string{
		{"https://api.savetax.now/v1/me", "https://API.savetax.now:443/x?y=1"},
	}
	diff := [][2]string{
		{"https://api.savetax.now/", "http://api.savetax.now/"},
		{"https://api.savetax.now/", "https://api.savetax.now:8443/"},
		{"https://api.savetax.now/", "https://hackathon.savetax.now/"},
	}
	for _, p := range same {
		a, _ := url.Parse(p[0])
		b, _ := url.Parse(p[1])
		if origin(a) != origin(b) {
			t.Errorf("같은 출처여야: %s %s", origin(a), origin(b))
		}
	}
	for _, p := range diff {
		a, _ := url.Parse(p[0])
		b, _ := url.Parse(p[1])
		if origin(a) == origin(b) {
			t.Errorf("다른 출처여야: %s", origin(a))
		}
	}
}

func TestCloudflare1010SwitchesUserAgentOnce(t *testing.T) {
	var uas []string
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		uas = append(uas, r.UserAgent())
		mu.Unlock()
		if r.UserAgent() != "Mozilla/5.0" {
			w.WriteHeader(403)
			io.WriteString(w, "error code: 1010")
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c, s := newTestClient(t, srv, nil)
	ctx := context.Background()
	if _, err := c.Do(ctx, &Request{Path: "/a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(ctx, &Request{Path: "/b"}); err != nil {
		t.Fatal(err)
	}
	if len(uas) != 3 || uas[2] != "Mozilla/5.0" || len(s.d) != 0 {
		t.Errorf("uas=%q sleeps=%v", uas, s.d)
	}
}

func hijackClose(w http.ResponseWriter) {
	conn, _, _ := w.(http.Hijacker).Hijack()
	conn.Close()
}

func TestNetworkRetryOnlyWhenIdempotent(t *testing.T) {
	var n atomic.Int32
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if n.Add(1)%2 == 1 {
			hijackClose(w)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv, func(o *Options) { o.HTTPClient.Transport.(*http.Transport).DisableKeepAlives = true })
	ctx := context.Background()

	// Idempotency-Key 없는 POST: 끊겨도 다시 보내지 않는다(중복 제출 방지).
	_, err := c.Do(ctx, &Request{Method: "POST", Path: "/v1/s", Body: []byte(`{"a":1}`)})
	if err == nil || n.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, n.Load())
	}
	// Idempotency-Key 있는 POST: 같은 본문으로 재전송.
	h := http.Header{}
	h.Set("Idempotency-Key", "k-1")
	n.Store(0)
	bodies = nil
	if _, err := c.Do(ctx, &Request{Method: "POST", Path: "/v1/s", Body: []byte(`{"a":2}`), Header: h}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || bodies[1] != `{"a":2}` {
		t.Errorf("재전송 본문 = %q", bodies)
	}
}

func TestErrorMessageMasksToken(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"code":"AUTH_REQUIRED","message":"bad token `+testToken+`"}`)
	}))
	defer srv.Close()
	c, _ := newTestClient(t, srv, nil)
	_, err := c.Do(context.Background(), &Request{Path: "/v1/x", Auth: true})
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("Retry-After", now.Add(90*time.Second).Format(http.TimeFormat))
	if d, ok := retryAfter(h, now); !ok || d != 90*time.Second {
		t.Errorf("date: %v %v", d, ok)
	}
	h.Set("Retry-After", "garbage")
	if _, ok := retryAfter(h, now); ok {
		t.Error("garbage를 해석했다")
	}
}
