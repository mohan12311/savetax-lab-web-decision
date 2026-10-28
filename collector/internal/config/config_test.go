package config

import (
	"strings"
	"testing"
)

func fakeToken() string {
	return TokenPrefix + strings.Repeat("a", TokenLength-len(TokenPrefix))
}

func TestLoadDefaultsAndCleaning(t *testing.T) {
	t.Setenv("SAVETAX_TOKEN", `"`+fakeToken()+`"  `)
	t.Setenv("SAVETAX_DATA_DIR", "")
	t.Setenv("SAVETAX_USER_AGENT", "")
	t.Setenv("SAVETAX_DATA_RPS", "0.9          ")
	t.Setenv("SAVETAX_SEARCH_RPS", "")
	t.Setenv("SAVETAX_CONTEST_RPS", "")
	t.Setenv("SAVETAX_RETAIL_RPS", "")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != fakeToken() {
		t.Errorf("따옴표·공백이 남았다: len=%d", len(c.Token))
	}
	if c.DataRPS != 0.9 || c.SearchRPS != 1.8 || c.ContestRPS != 1.0 {
		t.Errorf("rps = %v %v %v", c.DataRPS, c.SearchRPS, c.ContestRPS)
	}
	if c.DataDir != "./data" || c.UserAgent == "" {
		t.Errorf("기본값 = %q %q", c.DataDir, c.UserAgent)
	}
}

func TestLoadEmptyTokenIsAllowed(t *testing.T) {
	t.Setenv("SAVETAX_TOKEN", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.RequireToken() == nil {
		t.Error("빈 토큰인데 RequireToken이 통과했다")
	}
}

func TestValidateTokenDoesNotLeak(t *testing.T) {
	bad := []string{
		"abc_" + strings.Repeat("x", 52),
		TokenPrefix + "has space" + strings.Repeat("x", 39),
		TokenPrefix + "short",
	}
	for _, tok := range bad {
		err := ValidateToken(tok)
		if err == nil {
			t.Errorf("통과하면 안 된다: len=%d", len(tok))
			continue
		}
		if strings.Contains(err.Error(), tok) {
			t.Errorf("오류 메시지에 토큰 원문이 들어 있다: %v", err)
		}
	}
	if err := ValidateToken(fakeToken()); err != nil {
		t.Error(err)
	}
}

func TestBadRPS(t *testing.T) {
	t.Setenv("SAVETAX_TOKEN", "")
	t.Setenv("SAVETAX_DATA_RPS", "0")
	if _, err := Load(); err == nil {
		t.Error("0 rps를 받아들였다")
	}
}

func demoToken() string {
	return DemoTokenPrefix + strings.Repeat("0", TokenLength-len(DemoTokenPrefix))
}

func setDemoEnv(t *testing.T, demo, base, ca, tok string) {
	t.Helper()
	t.Setenv("SAVETAX_DEMO", demo)
	t.Setenv("SAVETAX_CONTEST_BASE_URL", base)
	t.Setenv("SAVETAX_DEMO_CA", ca)
	t.Setenv("SAVETAX_TOKEN", tok)
}

func TestDemoModeRedirectsAllServicesToLoopback(t *testing.T) {
	setDemoEnv(t, "1", "https://127.0.0.1:18443/", "ca.pem", demoToken())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Demo || c.DemoCAFile != "ca.pem" {
		t.Fatalf("데모 모드가 켜지지 않았다: %+v", c)
	}
	for _, u := range []string{c.ContestBaseURL, c.DataBaseURL, c.DatasetBaseURL} {
		if u != "https://127.0.0.1:18443" {
			t.Errorf("데모 모드인데 다른 주소가 남았다: %s", u)
		}
	}
}

func TestDemoModeRefusesUnsafeSettings(t *testing.T) {
	cases := []struct {
		name                string
		demo, base, ca, tok string
	}{
		{"데모 아닌데 주소 변경", "", "https://127.0.0.1:18443", "", ""},
		{"데모 아닌데 인증서", "", "", "ca.pem", ""},
		{"루프백이 아닌 주소", "1", "https://hackathon.example.com", "ca.pem", demoToken()},
		{"평문 http", "1", "http://127.0.0.1:18443", "ca.pem", demoToken()},
		{"경로가 붙은 주소", "1", "https://127.0.0.1:18443/v1", "ca.pem", demoToken()},
		{"인증서 없음", "1", "https://127.0.0.1:18443", "", demoToken()},
		{"실제 형식 토큰", "1", "https://127.0.0.1:18443", "ca.pem", fakeToken()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setDemoEnv(t, tc.demo, tc.base, tc.ca, tc.tok)
			_, err := Load()
			if err == nil {
				t.Fatal("거절해야 한다")
			}
			if tc.tok != "" && strings.Contains(err.Error(), tc.tok) {
				t.Errorf("오류 메시지에 토큰 원문이 들어 있다: %v", err)
			}
		})
	}
}

func TestDemoModeAllowsEmptyTokenAndLocalhost(t *testing.T) {
	setDemoEnv(t, "1", "https://localhost:18443", "ca.pem", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ContestBaseURL != "https://localhost:18443" {
		t.Errorf("주소 = %s", c.ContestBaseURL)
	}
}
