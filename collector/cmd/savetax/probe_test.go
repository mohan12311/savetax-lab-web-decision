package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/time/rate"

	"savetax-lab/collector/internal/scrape"
	"savetax-lab/collector/internal/scrape/probe"
	"savetax-lab/collector/internal/store/bronze"
)

func TestProbeOneSavesRawAndVerdict(t *testing.T) {
	var auth int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			auth++
		}
		switch r.URL.Path {
		case "/robots.txt":
			w.Write([]byte("User-agent: *\nDisallow: /cart\n"))
		case "/item":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><head><title>테스트 X1</title></head><body><h1>테스트 X1</h1><p>판매가 12,300원</p>` +
				strings.Repeat("설명 ", 100) + `</body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	site, err := scrape.NewSite(context.Background(), scrape.SiteOptions{
		BaseURL: srv.URL, Limiter: rate.NewLimiter(rate.Inf, 1), HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	run := bronze.NewRun(root, "probe", nil)
	res, err := probeOne(context.Background(), site, "/item?id=1", []string{"X1"}, run)
	if err != nil || res.Verdict != probe.StaticOK || res.Prices[0] != 12300 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := probeOne(context.Background(), site, "/cart", nil, run); !errors.Is(err, scrape.ErrDisallowed) {
		t.Errorf("robots 금지 경로: err=%v", err)
	}
	run.Finish(nil)
	if auth != 0 {
		t.Errorf("시중 사이트에 Authorization을 보냈다(%d회)", auth)
	}
	envs := readEnvelopesIn(t, filepath.Join(root, "retail", "probe", "page"))
	if len(envs) != 1 || envs[0].Raw == nil || envs[0].Params["url"] != srv.URL+"/item?id=1" {
		t.Fatalf("envelopes = %+v", envs)
	}
	var got probe.Result
	if err := json.Unmarshal(envs[0].Body, &got); err != nil || got.Verdict != probe.StaticOK {
		t.Errorf("body: %v %+v", err, got)
	}
}
