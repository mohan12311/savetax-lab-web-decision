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
