// Package scrape는 시중 판매 페이지를 가져오는 공통 부분이다.
//   - 시중 사이트에는 참가 토큰을 보내지 않는다. 클라이언트를 토큰 없이 만든다.
//   - 호스트마다 robots.txt를 먼저 읽고, Disallow 경로는 보내기 전에 막는다. Crawl-delay는 리미터 하한으로 건다.
//   - 403은 그 호스트의 차단으로 보고 ErrBlocked를 돌려준다(호출자는 그 도메인을 중단한다).
//
// 사이트별 추출기는 하위 패키지(danawa 등)에 둔다.
package scrape

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"savetax-lab/collector/internal/httpx"
)

// AgentToken은 robots.txt 그룹을 고를 때 쓰는 우리 제품 토큰이다.
const AgentToken = "savetax-lab"

var (
	// ErrDisallowed는 robots.txt가 막은 경로라 요청을 보내지 않았다는 뜻이다.
	ErrDisallowed = errors.New("robots.txt가 막은 경로")
	// ErrBlocked는 사이트가 403으로 막았다는 뜻이다. 같은 도메인을 계속 두드리지 않는다.
	ErrBlocked = errors.New("사이트가 403으로 막았다")
)

type SiteOptions struct {
	BaseURL    string // https://host
	UserAgent  string
	Limiter    *rate.Limiter // 도메인별 리미터(ratelimit.Retail(host))
	Logger     *slog.Logger
	HTTPClient *http.Client // 테스트용 주입
}

// Site는 한 호스트의 HTTP 클라이언트·robots 규칙·리미터다.
type Site struct {
	base   string // https://host (끝 / 없음)
	host   string
	http   *httpx.Client
	lim    *rate.Limiter
	robots *Robots
	log    *slog.Logger
}

// Page는 가져온 원문이다. SHA256은 Body(압축 전)의 해시다.
type Page struct {
	URL         string
	Status      int
	FetchedAt   time.Time
	ContentType string
	Body        []byte
	SHA256      string
}

// NewSite는 클라이언트를 만들고 robots.txt를 읽어 규칙·Crawl-delay를 건다.
func NewSite(ctx context.Context, opt SiteOptions) (*Site, error) {
	if opt.Limiter == nil {
		return nil, errors.New("scrape: 리미터가 없다")
	}
	if opt.Logger == nil {
		opt.Logger = slog.New(slog.DiscardHandler)
	}
	u, err := url.Parse(opt.BaseURL)
	if err != nil {
		return nil, err
	}
	h, err := httpx.New(httpx.Options{
		Service:       "retail:" + u.Host,
		BaseURL:       opt.BaseURL,
		Token:         "", // 시중 사이트에는 절대 보내지 않는다
		UserAgent:     opt.UserAgent,
		Limiters:      []*rate.Limiter{opt.Limiter},
		MaxAttempts:   3,
		MaxRetryAfter: 2 * time.Minute,
		Timeout:       30 * time.Second,
		Logger:        opt.Logger,
		HTTPClient:    opt.HTTPClient,
	})
	if err != nil {
		return nil, err
	}
	s := &Site{base: strings.TrimRight(opt.BaseURL, "/"), host: u.Host, http: h, lim: opt.Limiter, log: opt.Logger.With("host", u.Host)}
	if err := s.loadRobots(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Site) Host() string          { return s.host }
func (s *Site) Stats() *httpx.Stats   { return s.http.Stats() }
func (s *Site) Robots() *Robots       { return s.robots }
func (s *Site) Limit() rate.Limit     { return s.lim.Limit() }
func (s *Site) Service() string       { return s.http.Service() }
func (s *Site) Allowed(p string) bool { return s.robots.Allowed(p) }

// loadRobots: 2xx면 규칙을 읽고, 4xx면(없음) 모두 허용, 그 밖의 실패는 오류로 멈춘다
// (RFC 9309: 서버 오류면 전부 막힌 것으로 본다).
func (s *Site) loadRobots(ctx context.Context) error {
	resp, err := s.http.Do(ctx, &httpx.Request{
		Method: http.MethodGet,
		Path:   "/robots.txt",
		Header: http.Header{"Accept": {"text/plain,*/*;q=0.5"}},
	})
	if ae, ok := httpx.AsAPIError(err); ok && ae.Status >= 400 && ae.Status < 500 {
		s.robots = AllowAll()
		s.log.Info("robots.txt 없음 — 모두 허용", "status", ae.Status)
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s robots.txt: %w", s.host, err)
	}
	s.robots = ParseRobots(resp.Body, AgentToken)
	if d := s.robots.CrawlDelay; d > 0 {
		if want := rate.Every(d); want < s.lim.Limit() {
			s.lim.SetLimit(want)
		}
	}
	s.log.Debug("robots.txt", "rules", len(s.robots.rules), "crawl_delay", s.robots.CrawlDelay.String(), "limit_rps", float64(s.lim.Limit()))
	return nil
}

// Get은 HTML 페이지 하나를 가져온다. robots가 막은 경로는 보내지 않고 ErrDisallowed,
// 403이면 ErrBlocked로 감싼 오류, 그 밖의 2xx 아닌 응답은 *httpx.APIError다.
func (s *Site) Get(ctx context.Context, pathAndQuery string) (*Page, error) {
	if !strings.HasPrefix(pathAndQuery, "/") {
		return nil, fmt.Errorf("경로는 /로 시작해야 한다: %q", pathAndQuery)
	}
	if !s.robots.Allowed(pathAndQuery) {
		return nil, fmt.Errorf("%s%s: %w", s.host, pathAndQuery, ErrDisallowed)
	}
	resp, err := s.http.Do(ctx, &httpx.Request{
		Method: http.MethodGet,
		Path:   pathAndQuery,
		Header: http.Header{
			"Accept":          {"text/html,application/xhtml+xml;q=0.9,*/*;q=0.5"},
			"Accept-Language": {"ko-KR,ko;q=0.9"},
		},
	})
	if ae, ok := httpx.AsAPIError(err); ok && ae.Status == http.StatusForbidden {
		return nil, fmt.Errorf("%w: %w", ErrBlocked, err)
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(resp.Body)
	return &Page{
		URL:         s.base + pathAndQuery,
		Status:      resp.Status,
		FetchedAt:   time.Now().UTC(),
		ContentType: resp.Header.Get("Content-Type"),
		Body:        resp.Body,
		SHA256:      hex.EncodeToString(sum[:]),
	}, nil
}
