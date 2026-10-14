package httpx

import (
	"maps"
	"strconv"
	"sync"
)

// Stats는 클라이언트별 호출 집계다. bronze manifest의 api_calls·errors_by_code가 된다.
// Calls는 실제로 서버에 보낸 요청 수(재시도 포함) — 서버도 요청 단위로 한도를 센다.
type Stats struct {
	mu       sync.Mutex
	calls    int
	retries  int
	byStatus map[string]int
	byCode   map[string]int
}

type StatsSnapshot struct {
	Calls    int            `json:"calls"`
	Retries  int            `json:"retries"`
	ByStatus map[string]int `json:"by_status"`
	ByCode   map[string]int `json:"errors_by_code"`
}

func newStats() *Stats {
	return &Stats{byStatus: map[string]int{}, byCode: map[string]int{}}
}

func (s *Stats) call() {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
}

func (s *Stats) retry() {
	s.mu.Lock()
	s.retries++
	s.mu.Unlock()
}

func (s *Stats) status(code int) {
	s.mu.Lock()
	s.byStatus[strconv.Itoa(code)]++
	s.mu.Unlock()
}

func (s *Stats) failure(kind string) {
	s.mu.Lock()
	s.byStatus[kind]++
	s.mu.Unlock()
}

func (s *Stats) errCode(code string) {
	if code == "" {
		code = "(none)"
	}
	s.mu.Lock()
	s.byCode[code]++
	s.mu.Unlock()
}

func (s *Stats) Snapshot() StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return StatsSnapshot{
		Calls:    s.calls,
		Retries:  s.retries,
		ByStatus: maps.Clone(s.byStatus),
		ByCode:   maps.Clone(s.byCode),
	}
}
