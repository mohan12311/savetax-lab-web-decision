package ratelimit

import (
	"testing"

	"golang.org/x/time/rate"
)

func TestRegistrySharesLimiter(t *testing.T) {
	r := NewRegistry(map[string]float64{DataHourly: 0.9}, 0.5)
	a, b := r.Get(DataHourly), r.Get(DataHourly)
	if a != b {
		t.Fatal("같은 이름인데 다른 리미터")
	}
	if a.Limit() != rate.Limit(0.9) || a.Burst() != 1 {
		t.Errorf("limit=%v burst=%d", a.Limit(), a.Burst())
	}
	if got := r.Get(Retail("example.com")).Limit(); got != rate.Limit(0.5) {
		t.Errorf("fallback = %v", got)
	}
	if r.Get(Retail("a.com")) == r.Get(Retail("b.com")) {
		t.Error("도메인별 리미터가 공유됐다")
	}
}
