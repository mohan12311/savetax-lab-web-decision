package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"savetax-lab/collector/internal/api/contest"
	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/store/bronze"
)

const fakeToken = "savetax_test_token"

// fakeLookup은 items/lookup을 흉내 낸다. 번호가 X로 시작하면 응답에서 빼서(서버가 일부만 푼 경우) missing을 만든다.
type fakeLookup struct {
	mu      sync.Mutex
	batches [][]string
	badAuth int
	limited int // 앞에서 이만큼의 요청은 429 RATE_LIMITED로 돌려준다
}

func (f *fakeLookup) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/events/current/items/lookup" {
		http.NotFound(w, r)
		return
	}
	ids := strings.Split(r.URL.Query().Get("ids"), ",")
	f.mu.Lock()
	if f.limited > 0 {
		f.limited--
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"code":"RATE_LIMITED","message":"새 물품을 너무 빠르게 조회하고 있습니다","request_id":"r","retryable":true}`))
		return
	}
	f.batches = append(f.batches, ids)
	if r.Header.Get("Authorization") != "Bearer "+fakeToken {
		f.badAuth++
	}
	f.mu.Unlock()
	var items []contest.LookupItem
	for _, id := range ids {
		if strings.HasPrefix(id, "X") {
			continue
		}
		st := "available"
		if strings.HasSuffix(id, "7") {
			st = "claimed"
		}
		items = append(items, contest.LookupItem{Input: id, Kind: "item", ItemID: id, State: st})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", "req-1")
	json.NewEncoder(w).Encode(contest.LookupResult{Items: items})
}

func TestLookupAllBatchesAndRecords(t *testing.T) {
	f := &fakeLookup{}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	h, err := httpx.New(httpx.Options{Service: "contest", BaseURL: srv.URL, Token: fakeToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	c := contest.New(h)

	var ids []string
	for i := range 205 {
		ids = append(ids, fmt.Sprintf("%08d", 10000000+i))
	}
	ids = append(ids, "X0000001")
	root := t.TempDir()
	run := bronze.NewRun(root, "lookup", nil)
	w, err := run.Writer("contest", "lookup")
	if err != nil {
		t.Fatal(err)
	}
	counts, err := lookupAll(context.Background(), c, w, ids, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Finish(nil); err != nil {
		t.Fatal(err)
	}

	if len(f.batches) != 3 || len(f.batches[0]) != 100 || len(f.batches[2]) != 6 || f.badAuth != 0 {
		t.Fatalf("batches=%d sizes=%d,%d badAuth=%d", len(f.batches), len(f.batches[0]), len(f.batches[2]), f.badAuth)
	}
	// 끝자리 7: 10000007, 17, …, 197 → 20개
	if counts["claimed"] != 20 || counts["available"] != 185 || counts["missing"] != 1 {
		t.Errorf("counts = %v", counts)
	}
	envs := readEnvelopesIn(t, filepath.Join(root, "contest", "lookup"))
	if len(envs) != 3 || envs[0].Status != 200 || envs[0].RequestID == nil || *envs[0].RequestID != "req-1" {
		t.Fatalf("envelopes = %+v", envs)
	}
	var body contest.LookupResult
	if err := json.Unmarshal(envs[2].Body, &body); err != nil || len(body.Items) != 5 {
		t.Errorf("마지막 봉투 본문: %v items=%d", err, len(body.Items))
	}
}

func TestCleanIDs(t *testing.T) {
	got, err := cleanIDs([]string{" 25630496 ", "", "25630496", "R25TA00581651010000003"})
	if err != nil || strings.Join(got, ",") != "25630496,R25TA00581651010000003" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if _, err := cleanIDs([]string{"1,2"}); err == nil {
		t.Error("쉼표가 든 번호를 받아들였다")
	}
}

func TestLookupAllWaitsOutRateLimit(t *testing.T) {
	f := &fakeLookup{limited: 2}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	h, err := httpx.New(httpx.Options{Service: "contest", BaseURL: srv.URL, Token: fakeToken, HTTPClient: srv.Client(), MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	var slept int
	oldSleep := lookupSleep
	lookupSleep = func(context.Context, time.Duration) error { slept++; return nil }
	defer func() { lookupSleep = oldSleep }()

	run := bronze.NewRun(t.TempDir(), "lookup", nil)
	w, _ := run.Writer("contest", "lookup")
	counts, err := lookupAll(context.Background(), contest.New(h), w, []string{"10000001", "10000002"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	run.Finish(nil)
	if slept != 2 || counts["available"] != 2 || len(f.batches) != 1 {
		t.Errorf("slept=%d counts=%v batches=%d", slept, counts, len(f.batches))
	}

	// 계속 막히면 정해진 횟수만 기다리고 멈춘다
	f.limited = 1000
	oldMax := lookupMaxRateWaits
	lookupMaxRateWaits = 3
	defer func() { lookupMaxRateWaits = oldMax }()
	slept = 0
	run2 := bronze.NewRun(t.TempDir(), "lookup", nil)
	w2, _ := run2.Writer("contest", "lookup")
	if _, err := lookupAll(context.Background(), contest.New(h), w2, []string{"10000003"}, io.Discard); !isRateLimited(errors.Unwrap(err)) || slept != 3 {
		t.Errorf("err=%v slept=%d", err, slept)
	}
	run2.Finish(nil)
}
