// Package httpx는 세 서비스(대회·데이터·데이터셋)가 공유하는 HTTP 클라이언트다.
//   - https만 보낸다(평문 http로 토큰을 보내면 400 + 토큰 재발급).
//   - 시도마다 리미터를 기다린다(재시도도 서버 한도를 쓴다).
//   - 429·5xx·retryable:true만 지수 백오프+지터로 재시도, Retry-After를 따른다.
//   - 네트워크 오류 재시도는 GET/HEAD 또는 Idempotency-Key가 있는 요청만.
//   - 토큰은 Authorization 헤더로만 보내고, 다른 호스트로는 보내지 않는다.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	maxBodyBytes  = 64 << 20 // 버퍼링 응답 상한
	maxErrorBytes = 1 << 20
	fallbackUA    = "Mozilla/5.0"
)

type Options struct {
	Service       string // 오류·통계 표시용: contest | data | dataset
	BaseURL       string // https://host
	Token         string
	UserAgent     string
	Limiters      []*rate.Limiter // 모든 시도 전에 차례로 기다린다
	MaxAttempts   int             // 기본 6
	BaseBackoff   time.Duration   // 기본 1s
	MaxBackoff    time.Duration   // 기본 60s
	MaxRetryAfter time.Duration   // Retry-After가 이보다 길면 기다리지 않고 실패(기본 15m)
	Timeout       time.Duration   // 버퍼링 요청의 시도당 제한(기본 60s). Open에는 적용 안 함
	Logger        *slog.Logger
	HTTPClient    *http.Client // 테스트용 주입
}

type Client struct {
	opt   Options
	base  *url.URL
	hc    *http.Client
	log   *slog.Logger
	stats *Stats
	sleep func(context.Context, time.Duration) error

	mu sync.Mutex
	ua string
}

type Request struct {
	Method   string
	Path     string // "/v1/…" 또는 같은 호스트의 절대 https URL
	Query    url.Values
	Body     []byte // 재시도 때 그대로 다시 보낸다
	Header   http.Header
	Auth     bool
	Limiters []*rate.Limiter // 클라이언트 기본 리미터에 더해 기다릴 것(예: 검색 버킷)
}

type Response struct {
	Status   int
	Header   http.Header
	Body     []byte
	Attempts int
}

func New(opt Options) (*Client, error) {
	base, err := url.Parse(opt.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("base url: %w", err)
	}
	if base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("base url은 https여야 한다: %s", opt.BaseURL)
	}
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 6
	}
	if opt.BaseBackoff <= 0 {
		opt.BaseBackoff = time.Second
	}
	if opt.MaxBackoff <= 0 {
		opt.MaxBackoff = time.Minute
	}
	if opt.MaxRetryAfter <= 0 {
		opt.MaxRetryAfter = 15 * time.Minute
	}
	if opt.Timeout <= 0 {
		opt.Timeout = time.Minute
	}
	if opt.UserAgent == "" {
		opt.UserAgent = fallbackUA
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.DiscardHandler)
	}
	hc := opt.HTTPClient
	if hc == nil {
		hc = &http.Client{Transport: defaultTransport()}
	}
	// 기존 클라이언트를 고치지 않도록 얕은 복사 후 리다이렉트 정책만 덮어쓴다.
	// 같은 출처(scheme+host+port)로의 리다이렉트만 따라간다. 다른 출처는 헤더를 지워 보내는 대신
	// 아예 거부한다 — 토큰이 갈 곳을 서버 응답이 정하지 못하게 한다(https→http 강등도 여기서 막힌다).
	hcCopy := *hc
	hcCopy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("%w: 10회 초과", ErrRedirectRejected)
		}
		if from, to := origin(via[0].URL), origin(req.URL); from != to {
			return fmt.Errorf("%w: cross-origin %s → %s", ErrRedirectRejected, from, to)
		}
		return nil
	}
	return &Client{
		opt:   opt,
		base:  base,
		hc:    &hcCopy,
		log:   opt.Logger.With("service", opt.Service),
		stats: newStats(),
		sleep: sleepCtx,
		ua:    opt.UserAgent,
	}, nil
}

func defaultTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = 60 * time.Second
	return t
}

func (c *Client) Stats() *Stats   { return c.stats }
func (c *Client) HasToken() bool  { return c.opt.Token != "" }
func (c *Client) Service() string { return c.opt.Service }

// Do는 응답 본문을 메모리에 읽는다. 2xx가 아니면 *APIError를 돌려준다.
func (c *Client) Do(ctx context.Context, r *Request) (*Response, error) {
	resp, body, attempts, err := c.execute(ctx, r, false)
	if err != nil {
		return nil, err
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: body, Attempts: attempts}, nil
}

// GetJSON은 GET 후 본문을 out으로 디코딩한다(out이 nil이면 디코딩 생략).
func (c *Client) GetJSON(ctx context.Context, path string, q url.Values, auth bool, out any) (*Response, error) {
	resp, err := c.Do(ctx, &Request{Method: http.MethodGet, Path: path, Query: q, Auth: auth})
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp, fmt.Errorf("%s GET %s: JSON 해석 실패: %w", c.opt.Service, path, err)
		}
	}
	return resp, nil
}

// Open은 2xx 응답을 본문을 읽지 않은 채 돌려준다(큰 파일 스트리밍용). 호출자가 Body를 닫는다.
// 상태 코드 기반 재시도는 여기서 하고, 본문 도중 끊김은 호출자가 전체 재시도한다.
func (c *Client) Open(ctx context.Context, r *Request) (*http.Response, error) {
	resp, _, _, err := c.execute(ctx, r, true)
	return resp, err
}

func (c *Client) execute(ctx context.Context, r *Request, stream bool) (*http.Response, []byte, int, error) {
	if r.Auth && c.opt.Token == "" {
		return nil, nil, 0, ErrNoToken
	}
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	u, err := c.resolve(r)
	if err != nil {
		return nil, nil, 0, err
	}
	logPath := u.Path
	retryNet := method == http.MethodGet || method == http.MethodHead || r.Header.Get("Idempotency-Key") != ""
	triedUAFallback := false

	for attempt := 1; ; attempt++ {
		if err := c.wait(ctx, r); err != nil {
			return nil, nil, attempt, err
		}
		resp, body, err := c.attempt(ctx, method, u, r, stream)
		if errors.Is(err, ErrRedirectRejected) {
			// 정책 위반이라 재시도해도 같다. 오류로 남기고 멈춘다.
			// *url.Error는 Location 전체(서명 쿼리 포함 가능)를 담으므로 벗기고 출처만 남긴다.
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			c.stats.failure("redirect_rejected")
			c.log.Error("redirect rejected", "method", method, "path", logPath, "err", err)
			return nil, nil, attempt, fmt.Errorf("%s %s %s: %w", c.opt.Service, method, logPath, err)
		}
		if err != nil {
			c.stats.failure("network")
			if ctx.Err() != nil {
				return nil, nil, attempt, ctx.Err()
			}
			if !retryNet || attempt >= c.opt.MaxAttempts {
				return nil, nil, attempt, fmt.Errorf("%s %s %s: %w", c.opt.Service, method, logPath, err)
			}
			d := c.backoff(attempt)
			c.log.Warn("network error, retrying", "method", method, "path", logPath, "attempt", attempt, "wait", d.String(), "err", err)
			c.stats.retry()
			if err := c.sleep(ctx, d); err != nil {
				return nil, nil, attempt, err
			}
			continue
		}
		c.stats.status(resp.StatusCode)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			c.log.Debug("ok", "method", method, "path", logPath, "status", resp.StatusCode, "attempt", attempt)
			return resp, body, attempt, nil
		}

		apiErr := parseError(c.opt.Service, method, logPath, resp, body)
		c.stats.errCode(apiErr.Code)

		if apiErr.Is1010() && !triedUAFallback && c.userAgent() != fallbackUA {
			// 문서 지침: User-Agent를 Mozilla/5.0으로 바꿔 1회만 재시도. 이후 요청에도 유지.
			triedUAFallback = true
			c.setUserAgent(fallbackUA)
			c.log.Warn("cloudflare 1010, switching user-agent", "path", logPath)
			c.stats.retry()
			continue
		}
		if !apiErr.ShouldRetry() || attempt >= c.opt.MaxAttempts {
			if apiErr.Code == "https_required" {
				c.log.Error("평문 http로 토큰이 나갔을 수 있다 — '내 경기'에서 토큰을 재발급할 것", "path", logPath)
			}
			return nil, nil, attempt, apiErr
		}
		d := c.backoff(attempt)
		if ra, ok := retryAfter(resp.Header, time.Now()); ok {
			if ra > c.opt.MaxRetryAfter {
				return nil, nil, attempt, fmt.Errorf("Retry-After %s가 상한 %s를 넘는다: %w", ra, c.opt.MaxRetryAfter, apiErr)
			}
			d = max(d, ra)
		}
		c.log.Warn("retrying", "method", method, "path", logPath, "status", resp.StatusCode, "code", apiErr.Code, "attempt", attempt, "wait", d.String())
		c.stats.retry()
		if err := c.sleep(ctx, d); err != nil {
			return nil, nil, attempt, err
		}
	}
}

// attempt는 요청 1회. stream이고 2xx면 본문을 열어 둔 채 돌려준다.
// 그 밖에는 본문을 읽고 닫는다.
func (c *Client) attempt(ctx context.Context, method string, u *url.URL, r *Request, stream bool) (*http.Response, []byte, error) {
	if !stream {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.opt.Timeout)
		defer cancel() // 본문은 아래에서 다 읽은 뒤 반환한다
	}
	var body io.Reader
	if r.Body != nil {
		body = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, nil, err
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", c.userAgent())
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if r.Body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.Auth {
		req.Header.Set("Authorization", "Bearer "+c.opt.Token)
	}
	c.stats.call()
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if stream && ok {
		return resp, nil, nil // 호출자가 닫는다
	}
	defer resp.Body.Close()
	limit := int64(maxBodyBytes)
	if !ok {
		limit = maxErrorBytes
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, fmt.Errorf("본문 읽기: %w", err)
	}
	if int64(len(b)) > limit {
		if ok {
			return nil, nil, fmt.Errorf("응답이 %d바이트 상한을 넘는다", limit)
		}
		b = b[:limit]
	}
	return resp, b, nil
}

// resolve는 상대 경로를 base에 붙이고, 절대 URL이면 base와 같은 https 호스트인지 확인한다.
// (토큰이 다른 호스트로 새지 않게 한다.)
func (c *Client) resolve(r *Request) (*url.URL, error) {
	var u *url.URL
	if strings.Contains(r.Path, "://") {
		p, err := url.Parse(r.Path)
		if err != nil {
			return nil, err
		}
		if p.Scheme != "https" || p.Host != c.base.Host {
			return nil, fmt.Errorf("%s 클라이언트는 %s만 호출한다: %s", c.opt.Service, c.base.Host, p.Redacted())
		}
		u = p
	} else {
		if !strings.HasPrefix(r.Path, "/") {
			return nil, fmt.Errorf("경로는 /로 시작해야 한다: %q", r.Path)
		}
		p, err := url.Parse(r.Path)
		if err != nil {
			return nil, err
		}
		u = c.base.ResolveReference(p)
	}
	if len(r.Query) > 0 {
		q := u.Query()
		for k, vs := range r.Query {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}
	return u, nil
}

func (c *Client) wait(ctx context.Context, r *Request) error {
	for _, l := range c.opt.Limiters {
		if err := l.Wait(ctx); err != nil {
			return err
		}
	}
	for _, l := range r.Limiters {
		if err := l.Wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

// backoff: min(Max, Base·2^(n-1))의 절반 + [0, 절반) 지터.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.opt.BaseBackoff << min(attempt-1, 20)
	if d <= 0 || d > c.opt.MaxBackoff {
		d = c.opt.MaxBackoff
	}
	half := d / 2
	return half + rand.N(half+1)
}

func (c *Client) userAgent() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ua
}

func (c *Client) setUserAgent(ua string) {
	c.mu.Lock()
	c.ua = ua
	c.mu.Unlock()
}

// origin은 scheme://host:port(기본 포트 채움, 소문자)다.
func origin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
