package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"savetax-lab/collector/internal/api/contest"
	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/store/bronze"
)

var lookupIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// 서버가 처음 보는 물품은 lookup도 "새 물품 조회" 한도(429 RATE_LIMITED, Retry-After 없음)에 걸린다(10/14 실측).
// 문서 안내대로 1분쯤 기다렸다가 같은 묶음을 다시 묻는다. 연속으로 이만큼 막히면 멈춘다.
var (
	lookupRateWait     = 65 * time.Second
	lookupMaxRateWaits = 30
	lookupSleep        = sleepCtx
)

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func isRateLimited(err error) bool {
	ae, ok := httpx.AsAPIError(err)
	return ok && ae.Status == 429 && ae.Code == "RATE_LIMITED"
}

// lookupStates는 출력 순서다. 모르는 상태는 뒤에 붙인다.
var lookupStates = []string{"available", "claimed", "claimed_by_me", "reform_found", "not_found"}

// runLookup은 번호 목록의 선점 상태를 items/lookup으로 100개씩 묻고 응답 원문을 bronze/contest/lookup에 쌓는다.
// 조회는 스냅샷을 만들지 않고 제출 횟수도 쓰지 않는다. 결과는 silver claim_state가 읽는다.
// 매번 목록 전체를 다시 묻는다(상태는 바뀌고, 조회는 공짜다). 끊기면 처음부터 다시 돌리면 된다.
func runLookup(ctx context.Context, a *app, args []string) error {
	fs := flag.NewFlagSet("lookup", flag.ContinueOnError)
	from := fs.String("from", "", "번호 목록 파일(한 줄에 하나, #으로 시작하면 주석). 물품번호 또는 등재번호")
	var ids stringsFlag
	fs.Var(&ids, "id", "번호를 직접 준다. 여러 번 줄 수 있다")
	limit := fs.Int("limit", 0, "앞에서부터 이 개수만 묻는다(0이면 전부)")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if *from != "" {
		lines, err := readQueryFile(*from)
		if err != nil {
			return err
		}
		ids = append(ids, lines...)
	}
	list, err := cleanIDs(ids)
	if err != nil {
		return usageError{err.Error()}
	}
	if *limit > 0 && len(list) > *limit {
		list = list[:*limit]
	}
	if len(list) == 0 {
		return usageError{"사용법: savetax lookup --from <파일> | --id <번호> [--limit N]"}
	}

	c, err := a.contest()
	if err != nil {
		return err
	}
	if !c.HTTP().HasToken() {
		return httpx.ErrNoToken
	}
	out := os.Stdout
	batches := (len(list) + contest.LookupMax - 1) / contest.LookupMax
	fmt.Fprintf(out, "lookup: %d개 · %d회 호출 · %.1f회/초\n", len(list), batches, a.cfg.ContestRPS)

	run := bronze.NewRun(a.bronzeRoot(), "lookup", args)
	run.TrackStats(c.HTTP().Service(), c.HTTP().Stats())
	w, err := run.Writer("contest", "lookup")
	var counts map[string]int
	if err == nil {
		counts, err = lookupAll(ctx, c, w, list, out)
	}
	mpath, ferr := run.Finish(err)
	printLookupCounts(out, counts)
	if mpath != "" {
		fmt.Fprintf(out, "run manifest: %s\n", mpath)
	}
	return errors.Join(err, ferr)
}

// cleanIDs는 공백을 지우고 중복을 빼며 순서를 지킨다. 쉼표 등이 섞인 번호는 질의 문자열을 깨므로 거절한다.
func cleanIDs(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		if !lookupIDRe.MatchString(id) {
			return nil, fmt.Errorf("번호 형식이 아니다(영문·숫자·_·- 80자 이하): %q", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// lookupAll은 100개씩 묻고 응답마다 봉투 하나를 쓴다. 상태별 개수를 돌려준다.
// 응답에 빠진 번호는 "missing"으로 센다(서버가 일부만 풀 수 있다 — 그 번호만 다시 묻는다).
func lookupAll(ctx context.Context, c *contest.Client, w *bronze.Writer, ids []string, out io.Writer) (map[string]int, error) {
	counts := map[string]int{}
	total := (len(ids) + contest.LookupMax - 1) / contest.LookupMax
	waited := 0 // 이번 실행에서 한도 때문에 기다린 횟수
	for i, chunk := range slices.Collect(slices.Chunk(ids, contest.LookupMax)) {
		res, resp, err := c.Lookup(ctx, chunk)
		for streak := 1; isRateLimited(err) && streak <= lookupMaxRateWaits; streak++ {
			waited++
			fmt.Fprintf(out, "  %d/%d회: 새 물품 조회 한도(429) — %s 뒤 같은 묶음을 다시 묻는다 (연속 %d/%d, 누적 %d)\n",
				i+1, total, lookupRateWait, streak, lookupMaxRateWaits, waited)
			if serr := lookupSleep(ctx, lookupRateWait); serr != nil {
				return counts, serr
			}
			res, resp, err = c.Lookup(ctx, chunk)
		}
		if err != nil {
			return counts, fmt.Errorf("lookup %d/%d번째 호출: %w", i+1, total, err)
		}
		var rid *string
		if v := resp.Header.Get("X-Request-Id"); v != "" {
			rid = &v
		}
		if err := w.Write(bronze.Envelope{
			FetchedAt: time.Now().UTC(),
			Source:    "contest_api",
			Endpoint:  "/v1/events/current/items/lookup",
			Params:    map[string]any{"ids": chunk},
			Status:    resp.Status,
			RequestID: rid,
			Body:      json.RawMessage(resp.Body),
		}); err != nil {
			return counts, err
		}
		got := map[string]bool{}
		for _, it := range res.Items {
			got[it.Input] = true
			st := it.State
			if st == "" {
				st = "(빈 상태)"
			}
			counts[st]++
		}
		for _, id := range chunk {
			if !got[id] {
				counts["missing"]++
			}
		}
		if (i+1)%10 == 0 || i+1 == total {
			fmt.Fprintf(out, "  %d/%d회 · %d개\n", i+1, total, min((i+1)*contest.LookupMax, len(ids)))
		}
	}
	return counts, nil
}

func printLookupCounts(out io.Writer, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	var parts []string
	for _, st := range lookupStates {
		parts = append(parts, fmt.Sprintf("%s %d", st, counts[st]))
	}
	var extra []string
	for st := range counts {
		if !slices.Contains(lookupStates, st) {
			extra = append(extra, st)
		}
	}
	slices.Sort(extra)
	for _, st := range extra {
		parts = append(parts, fmt.Sprintf("%s %d", st, counts[st]))
	}
	fmt.Fprintf(out, "상태: %s\n", strings.Join(parts, " · "))
}
