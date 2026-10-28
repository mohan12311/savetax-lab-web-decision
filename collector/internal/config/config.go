// Package config는 환경변수에서 실행 설정을 읽는다.
// 참가 토큰은 SAVETAX_TOKEN 환경변수로만 받는다(파일·플래그·URL 금지).
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	ContestBaseURL = "https://hackathon.savetax.now"
	DataBaseURL    = "https://api.savetax.now"
	DatasetBaseURL = "https://savetax-dataset.reform-dev-infra.workers.dev"

	// 서버 오류 메시지 기준: savetax_로 시작하는 56자(영숫자·_·-).
	TokenPrefix = "savetax_"
	TokenLength = 56
	// 데모 토큰 접두어. 데모 모드(studio/fakeserver)에서는 이 접두어 토큰만 받는다.
	DemoTokenPrefix = "savetax_demo_"
	defaultUA       = "Mozilla/5.0 (savetax-lab)"
	defaultDataDir  = "./data"
)

var tokenRe = regexp.MustCompile(`^savetax_[A-Za-z0-9_-]+$`)

type Config struct {
	Token     string // 비어 있을 수 있음(공개 엔드포인트만 호출)
	DataDir   string
	UserAgent string

	DataRPS    float64 // 데이터 API 시간 버킷(3,600/h)
	SearchRPS  float64 // 데이터 API 검색 버킷(120/min)
	ContestRPS float64 // 대회 API(속도 제한 없음, 새 물품 조회만 분당 30건 — 우리는 기본 1회/초)
	RetailRPS  float64 // 시중 사이트 도메인별

	ContestBaseURL string
	DataBaseURL    string
	DatasetBaseURL string

	// Demo는 가짜 대회 서버(studio/fakeserver)에 붙는 데모 모드다(SAVETAX_DEMO=1). 대회 주소를 바꿀 수 있는 건
	// 이 모드뿐이고, 루프백 https 주소와 데모 토큰만 허용한다 — 실제 토큰이 다른 곳으로 가지 않게 한다.
	Demo bool
	// DemoCAFile은 가짜 서버의 자체 서명 인증서(PEM)다. 데모 모드의 HTTP 클라이언트는 이것만 신뢰하므로
	// 실제 대회·판매 사이트에는 TLS 단계에서 연결되지 않는다.
	DemoCAFile string
}

// Load는 환경변수를 읽고 검증한다. 토큰이 없으면 Token=""로 두고,
// 형식이 틀린 토큰은 오류로 돌려준다(틀린 토큰으로 호출해 봐야 401뿐이다).
func Load() (*Config, error) {
	c := &Config{
		Token:          clean(os.Getenv("SAVETAX_TOKEN")),
		DataDir:        orDefault(clean(os.Getenv("SAVETAX_DATA_DIR")), defaultDataDir),
		UserAgent:      orDefault(clean(os.Getenv("SAVETAX_USER_AGENT")), defaultUA),
		ContestBaseURL: ContestBaseURL,
		DataBaseURL:    DataBaseURL,
		DatasetBaseURL: DatasetBaseURL,
	}
	var err error
	if c.DataRPS, err = rps("SAVETAX_DATA_RPS", 0.9); err != nil {
		return nil, err
	}
	if c.SearchRPS, err = rps("SAVETAX_SEARCH_RPS", 1.8); err != nil {
		return nil, err
	}
	if c.ContestRPS, err = rps("SAVETAX_CONTEST_RPS", 1.0); err != nil {
		return nil, err
	}
	if c.RetailRPS, err = rps("SAVETAX_RETAIL_RPS", 0.5); err != nil {
		return nil, err
	}
	if c.Token != "" {
		if err := ValidateToken(c.Token); err != nil {
			return nil, err
		}
	}
	if err := c.loadDemo(); err != nil {
		return nil, err
	}
	return c, nil
}

// loadDemo는 데모 모드 설정을 읽는다. 데모가 아닐 때 주소·인증서 설정이 있으면 거절한다
// (실수로 실제 토큰을 다른 주소로 보내는 길을 만들지 않는다).
func (c *Config) loadDemo() error {
	base := clean(os.Getenv("SAVETAX_CONTEST_BASE_URL"))
	ca := clean(os.Getenv("SAVETAX_DEMO_CA"))
	if clean(os.Getenv("SAVETAX_DEMO")) != "1" {
		if base != "" || ca != "" {
			return errors.New("SAVETAX_CONTEST_BASE_URL·SAVETAX_DEMO_CA는 데모 모드(SAVETAX_DEMO=1)에서만 쓴다")
		}
		return nil
	}
	u, err := url.Parse(base)
	if base == "" || err != nil || u.Scheme != "https" || !isLoopback(u.Hostname()) || strings.Trim(u.Path, "/") != "" || u.RawQuery != "" {
		return fmt.Errorf("데모 모드의 SAVETAX_CONTEST_BASE_URL은 경로 없는 루프백 https 주소여야 한다(예: https://127.0.0.1:18443): %q", base)
	}
	if ca == "" {
		return errors.New("데모 모드에는 SAVETAX_DEMO_CA(가짜 서버 인증서 PEM 경로)가 필요하다")
	}
	if c.Token != "" && !strings.HasPrefix(c.Token, DemoTokenPrefix) {
		return fmt.Errorf("데모 모드에서는 데모 토큰(%s…)만 쓴다 — 실제 참가 토큰은 가짜 서버로 보내지 않는다", DemoTokenPrefix)
	}
	root := u.Scheme + "://" + u.Host
	c.Demo, c.DemoCAFile = true, ca
	c.ContestBaseURL, c.DataBaseURL, c.DatasetBaseURL = root, root, root
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidateToken은 토큰 형식만 본다. 오류 메시지에 토큰 원문을 넣지 않는다.
func ValidateToken(tok string) error {
	if !strings.HasPrefix(tok, TokenPrefix) {
		return fmt.Errorf("SAVETAX_TOKEN이 %q로 시작하지 않는다 (길이 %d)", TokenPrefix, len(tok))
	}
	if !tokenRe.MatchString(tok) {
		return fmt.Errorf("SAVETAX_TOKEN에 허용되지 않는 문자가 있다 (영숫자·_·-만, 길이 %d)", len(tok))
	}
	if len(tok) != TokenLength {
		return fmt.Errorf("SAVETAX_TOKEN 길이가 %d자다 (기대 %d자) — 복사가 잘렸는지 확인", len(tok), TokenLength)
	}
	return nil
}

// RequireToken은 인증이 필요한 명령에서 쓴다.
func (c *Config) RequireToken() error {
	if c.Token == "" {
		return fmt.Errorf("SAVETAX_TOKEN이 비어 있다 — .env에 넣거나 셸에서 export한다 ('내 경기'에서 발급)")
	}
	return nil
}

// clean은 make의 `-include .env`가 남기는 앞뒤 공백·따옴표를 걷어낸다.
// (make는 `VAR=값   # 주석`에서 값 뒤 공백을 그대로 두고, 따옴표도 벗기지 않는다.)
func clean(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func rps(key string, def float64) (float64, error) {
	v := clean(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("%s=%q: 양수여야 한다", key, v)
	}
	return f, nil
}
