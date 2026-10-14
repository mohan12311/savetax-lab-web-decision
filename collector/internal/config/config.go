// Package config는 환경변수에서 실행 설정을 읽는다.
// 참가 토큰은 SAVETAX_TOKEN 환경변수로만 받는다(파일·플래그·URL 금지).
package config

import (
	"fmt"
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
	TokenPrefix    = "savetax_"
	TokenLength    = 56
	defaultUA      = "Mozilla/5.0 (savetax-lab)"
	defaultDataDir = "./data"
)

var tokenRe = regexp.MustCompile(`^savetax_[A-Za-z0-9_-]+$`)

type Config struct {
	Token     string // 비어 있을 수 있음(공개 엔드포인트만 호출)
	DataDir   string
	UserAgent string

	DataRPS    float64 // 데이터 API 시간 버킷(3,600/h)
	SearchRPS  float64 // 데이터 API 검색 버킷(120/min)
	ContestRPS float64 // 대회 API(한도 미공개)
	RetailRPS  float64 // 시중 사이트 도메인별

	ContestBaseURL string
	DataBaseURL    string
	DatasetBaseURL string
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
	return c, nil
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
