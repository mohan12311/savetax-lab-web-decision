package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/time/rate"

	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/ratelimit"
	"savetax-lab/collector/internal/scrape/naver"
	"savetax-lab/collector/internal/store/bronze"
	"savetax-lab/collector/internal/store/state"
)

const (
	naverKind        = "scrape_naver"
	naverMaxAttempts = 3
	// 공식 한도(하루 25,000회) 안에서 여유 있게. 검색어 4천 개가 약 35분.
	naverRPS = 2.0
)

// naverKeys는 환경변수에서만 키를 읽는다. Makefile이 비어 있으면 키체인에서 채워 넘긴다.
func naverKeys() (naver.Keys, error) {
	k := naver.Keys{ID: strings.TrimSpace(os.Getenv("NAVER_CLIENT_ID")), Secret: strings.TrimSpace(os.Getenv("NAVER_CLIENT_SECRET"))}
	if k.ID == "" || k.Secret == "" {
		return k, usageError{"NAVER_CLIENT_ID·NAVER_CLIENT_SECRET이 없다 — 키체인(service=savetax-lab)에 넣고 make scrape SOURCE=naver로 실행한다"}
	}
	return k, nil
}

// runScrapeNaver는 검색어마다 네이버 쇼핑 검색 API를 한 번 부르고 응답 원문을 bronze/retail/naver/search에 쌓는다.
// 작업은 state 큐(scrape_naver)에 올려 두므로 끊겨도 다음 실행에서 이어진다.
func runScrapeNaver(ctx context.Context, a *app, args, queries []string, from string, display int, refresh bool) error {
	if display < 1 || display > naver.MaxDisplay {
		return usageError{fmt.Sprintf("--display는 1~%d", naver.MaxDisplay)}
	}
	if from != "" {
		lines, err := readQueryFile(from)
		if err != nil {
			return err
		}
		queries = append(queries, lines...)
	}
	var tasks []state.Task
	for i, q := range queries {
		q = strings.Join(strings.Fields(q), " ")
		if q == "" {
			continue
		}
		if utf8.RuneCountInString(q) > maxQueryRunes {
			return usageError{fmt.Sprintf("검색어가 %d자를 넘는다: %q…", maxQueryRunes, string([]rune(q)[:20]))}
		}
		tasks = append(tasks, state.Task{Key: "q:" + q, Priority: float64(len(queries) - i)})
	}

	st, err := state.Open(filepath.Join(a.stateDir(), "collector.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.RecoverRunning(ctx, naverKind); err != nil {
		return err
	}
	if _, err := st.Enqueue(ctx, naverKind, tasks); err != nil {
		return err
	}
	if refresh && len(tasks) > 0 {
		keys := make([]string, len(tasks))
		for i, t := range tasks {
			keys[i] = t.Key
		}
		if _, err := st.Requeue(ctx, naverKind, keys); err != nil {
			return err
		}
	}
	out := os.Stdout
	counts, err := st.Counts(ctx, naverKind)
	if err != nil {
		return err
	}
	if counts[state.Pending] == 0 {
		if len(tasks) == 0 {
			return usageError{"사용법: savetax scrape --source naver --query <검색어> | --from <파일> [--display 50] [--refresh]"}
		}
		fmt.Fprintln(out, "할 작업이 없다 — 이미 끝난 작업은 --refresh로 다시 받는다")
		return nil
	}
	keys, err := naverKeys()
	if err != nil {
		return err
	}
	lim := a.lim.Get(ratelimit.Retail(naver.Host))
	if lim.Limit() > naverRPS {
		lim.SetLimit(rate.Limit(naverRPS))
	}
	h, err := httpx.New(httpx.Options{
		Service:       "retail:" + naver.Host,
		BaseURL:       naver.BaseURL,
		Token:         "", // 참가 토큰은 대회 호스트에만
		UserAgent:     a.cfg.UserAgent,
		Limiters:      []*rate.Limiter{lim},
		MaxAttempts:   naverMaxAttempts,
		MaxRetryAfter: 2 * time.Minute,
		Timeout:       30 * time.Second,
		Logger:        a.log,
	})
	if err != nil {
		return err
	}
	nc, err := naver.New(h, keys)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "네이버 쇼핑 검색 API: 대기 %d건 · %.1f req/s · 검색어당 %d개\n", counts[state.Pending], float64(lim.Limit()), display)

	run := bronze.NewRun(a.bronzeRoot(), "scrape", args)
	run.TrackStats(h.Service(), h.Stats())
	s := &naverScraper{st: st, run: run, out: out, nc: nc, display: display}
	err = s.loop(ctx)
	mpath, ferr := run.Finish(err)

	if c, cerr := st.Counts(context.WithoutCancel(ctx), naverKind); cerr == nil {
		fmt.Fprintf(out, "\n작업: done %d · pending %d · failed %d · skipped %d\n", c[state.Done], c[state.Pending], c[state.Failed], c[state.Skipped])
	}
	if mpath != "" {
		fmt.Fprintf(out, "run manifest: %s\n", mpath)
	}
	return errors.Join(err, ferr)
}

type naverScraper struct {
	st      *state.Store
	run     *bronze.Run
	out     io.Writer
	nc      *naver.Client
	display int
}

func (s *naverScraper) loop(ctx context.Context) error {
	for {
		keys, err := s.st.Claim(ctx, naverKind, 1)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}
		key := keys[0]
		var terr error
		if q, ok := strings.CutPrefix(key, "q:"); ok {
			terr = s.search(ctx, q)
		} else {
			terr = fmt.Errorf("알 수 없는 작업 키: %q", key)
		}
		if err := s.settle(ctx, key, terr); err != nil {
			return err
		}
	}
}

// settle은 작업 결과를 큐에 반영한다. 계속할 수 없는 오류(인증·한도·취소)만 돌려준다.
func (s *naverScraper) settle(ctx context.Context, key string, terr error) error {
	ctx2 := context.WithoutCancel(ctx)
	switch {
	case terr == nil:
		return s.st.Done(ctx2, naverKind, key)
	case ctx.Err() != nil:
		return ctx.Err()
	case naver.Fatal(terr):
		// 이 작업은 시도 횟수를 쓰지 않게 pending으로 되돌린다(키·한도를 고친 뒤 이어서 받는다).
		_, _ = s.st.Requeue(ctx2, naverKind, []string{key})
		return fmt.Errorf("네이버 API가 거절했다(인증·권한·하루 한도) — 멈춘다: %w", terr)
	default:
		final, err := s.st.Fail(ctx2, naverKind, key, terr, naverMaxAttempts)
		next := "다음 실행에서 다시"
		if final {
			next = "포기(failed)"
		}
		fmt.Fprintf(s.out, "  실패: %s — %v (%s)\n", key, terr, next)
		return err
	}
}

func (s *naverScraper) search(ctx context.Context, q string) error {
	fmt.Fprintf(s.out, "\n검색 %q\n", q)
	resp, res, err := s.nc.Search(ctx, q, s.display)
	if resp != nil {
		// 원문 JSON은 해석에 실패해도 남긴다(구조 변경 확인용).
		if werr := s.save(resp, q); werr != nil {
			return errors.Join(err, werr)
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "  총 %s개 · 받음 %d개\n", commas(fmt.Sprint(res.Total)), len(res.Items))
	for i, it := range res.Items {
		if i == 3 {
			break
		}
		fmt.Fprintf(s.out, "    %12s  %s  %s\n", krw(it.LowPriceKRW), pad(clip(it.Mall, 12), 14), clip(it.Title, 60))
	}
	return nil
}

func (s *naverScraper) save(resp *httpx.Response, q string) error {
	w, err := s.run.Writer("retail/naver", "search")
	if err != nil {
		return err
	}
	params := map[string]any{}
	for k, v := range naver.Params(q, s.display) {
		params[k] = v[0]
	}
	var reqID *string
	if id := resp.Header.Get("X-Naver-Request-Id"); id != "" {
		reqID = &id
	}
	body := json.RawMessage(resp.Body) // API 원문 JSON 그대로
	if !json.Valid(body) {
		// JSON이 아닌 응답(점검 페이지 등)은 문자열로 감싸 원문을 잃지 않는다.
		b, err := json.Marshal(string(resp.Body))
		if err != nil {
			return err
		}
		body = b
	}
	return w.Write(bronze.Envelope{
		FetchedAt: time.Now().UTC(),
		Source:    "naver_shop_api",
		Endpoint:  naver.SearchPath,
		Params:    params,
		Status:    resp.Status,
		RequestID: reqID,
		Body:      body,
	})
}
