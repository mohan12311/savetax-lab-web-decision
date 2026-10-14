// Package ratelimit은 이름 붙은 토큰 버킷을 프로세스 안에서 공유한다.
// 같은 이름을 쓰는 워커는 모두 같은 리미터를 기다린다.
package ratelimit

import (
	"sync"

	"golang.org/x/time/rate"
)

// 버킷 이름. retail은 Retail(domain)으로 도메인별로 만든다.
const (
	DataHourly = "data_hourly" // api.savetax.now·MCP 공유 3,600/h
	DataSearch = "data_search" // 검색 120/min (시간 버킷과 함께 기다린다)
	DataFree   = "data_free"   // /v1/me 등 예산 미차감(분당 120회 별도 상한)
	Contest    = "contest"     // hackathon.savetax.now (한도 미공개)
	Dataset    = "dataset"     // 데이터셋 호스트
)

func Retail(domain string) string { return "retail:" + domain }

type Registry struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	defaults map[string]rate.Limit
	fallback rate.Limit
}

// NewRegistry는 이름별 초당 허용량을 받는다. 등록되지 않은 이름은 fallback rps를 쓴다.
func NewRegistry(rps map[string]float64, fallback float64) *Registry {
	r := &Registry{
		limiters: map[string]*rate.Limiter{},
		defaults: map[string]rate.Limit{},
		fallback: rate.Limit(fallback),
	}
	for name, v := range rps {
		r.defaults[name] = rate.Limit(v)
	}
	return r
}

// Get은 이름에 해당하는 리미터를 돌려준다(없으면 만든다).
// burst는 1: 순간 몰림 없이 일정 간격으로만 보낸다. 시간당 상한 = rps×3600.
func (r *Registry) Get(name string) *rate.Limiter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.limiters[name]; ok {
		return l
	}
	lim, ok := r.defaults[name]
	if !ok {
		lim = r.fallback
	}
	l := rate.NewLimiter(lim, 1)
	r.limiters[name] = l
	return l
}
