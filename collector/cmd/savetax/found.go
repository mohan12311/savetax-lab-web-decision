package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"time"

	"savetax-lab/collector/internal/api/contest"
	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/store/bronze"
)

// runFound는 발견 상품 카드 목록(found)을 cursor로 끝까지 읽어 bronze/contest/found에 쌓는다.
// 남의 근거(링크·인용·가격)는 우리 근거로 쓰지 않는다(CLAUDE.md 규칙 2) — 출처 분포·방법 파악용이다.
func runFound(ctx context.Context, a *app, args []string) error {
	fs := flag.NewFlagSet("found", flag.ContinueOnError)
	status := fs.String("status", "", "checking | confirmed | held | unconfirmed | withdrawn (비우면 서버 기본)")
	limit := fs.Int("limit", 48, "페이지 크기(서버 상한 48)")
	maxPages := fs.Int("max-pages", 0, "읽을 최대 페이지 수(0이면 끝까지)")
	var details stringsFlag
	fs.Var(&details, "detail", "카드 key의 상세(found/{key})만 받는다. 여러 번 줄 수 있다")
	var counters stringsFlag
	fs.Var(&counters, "counter", "제출 id에 달린 공개 반증 목록(submissions/{sid}/counterevidence)만 받는다. 여러 번 줄 수 있다")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	c, err := a.contest()
	if err != nil {
		return err
	}
	if !c.HTTP().HasToken() {
		return httpx.ErrNoToken
	}
	out := os.Stdout
	run := bronze.NewRun(a.bronzeRoot(), "found", args)
	run.TrackStats(c.HTTP().Service(), c.HTTP().Stats())
	if len(details) > 0 || len(counters) > 0 {
		err = foundDetails(ctx, c, run, details)
		if err == nil {
			err = foundCounters(ctx, c, run, counters)
		}
		mpath, ferr := run.Finish(err)
		if mpath != "" {
			fmt.Fprintf(out, "run manifest: %s\n", mpath)
		}
		return errors.Join(err, ferr)
	}
	w, err := run.Writer("contest", "found")
	cursor, pages, cards := "", 0, 0
	for err == nil && (*maxPages == 0 || pages < *maxPages) {
		q := url.Values{"limit": {strconv.Itoa(*limit)}}
		if *status != "" {
			q.Set("status", *status)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var body json.RawMessage
		var resp *httpx.Response
		if body, resp, err = c.Found(ctx, q); err != nil {
			break
		}
		params := map[string]any{}
		for k := range q {
			params[k] = q.Get(k)
		}
		if err = w.Write(bronze.Envelope{FetchedAt: time.Now().UTC(), Source: "contest_api", Endpoint: "/v1/events/current/found",
			Params: params, Status: resp.Status, Body: body}); err != nil {
			break
		}
		pages++
		n, next, keys := foundPage(body)
		cards += n
		if pages == 1 {
			fmt.Fprintf(out, "응답 키: %v\n", keys)
		}
		fmt.Fprintf(out, "페이지 %d: 카드 %d개 (누적 %d)\n", pages, n, cards)
		if next == "" || n == 0 {
			break
		}
		cursor = next
	}
	mpath, ferr := run.Finish(err)
	if mpath != "" {
		fmt.Fprintf(out, "run manifest: %s\n", mpath)
	}
	return errors.Join(err, ferr)
}

// foundPage는 응답에서 카드 수·다음 cursor·최상위 키를 찾는다(스키마 미공개라 흔한 이름을 차례로 본다).
func foundPage(body []byte) (n int, next string, keys []string) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return 0, "", nil
	}
	for k := range top {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range []string{"items", "cards", "products", "results", "data"} {
		var arr []json.RawMessage
		if json.Unmarshal(top[k], &arr) == nil && arr != nil {
			n = len(arr)
			break
		}
	}
	for _, k := range []string{"next_cursor", "cursor", "next"} {
		var s string
		if json.Unmarshal(top[k], &s) == nil && s != "" {
			return n, s, keys
		}
	}
	return n, "", keys
}

// foundCounters는 제출마다 달린 공개 반증 목록을 bronze/contest/counterevidence에 쌓는다.
// 반증 본문은 검토 기준 파악용이고, 거기 실린 링크·가격을 우리 근거로 옮기지 않는다(CLAUDE.md 규칙 2).
func foundCounters(ctx context.Context, c *contest.Client, run *bronze.Run, sids []string) error {
	if len(sids) == 0 {
		return nil
	}
	w, err := run.Writer("contest", "counterevidence")
	if err != nil {
		return err
	}
	for _, sid := range sids {
		body, resp, err := c.Counterevidence(ctx, sid)
		if err != nil {
			return fmt.Errorf("반증 %s: %w", sid, err)
		}
		if err := w.Write(bronze.Envelope{FetchedAt: time.Now().UTC(), Source: "contest_api", Endpoint: "/v1/events/current/submissions/{sid}/counterevidence",
			Params: map[string]any{"sid": sid}, Status: resp.Status, Body: body}); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "반증 %s 받음 (%dB)\n", sid, len(body))
	}
	return nil
}

func foundDetails(ctx context.Context, c *contest.Client, run *bronze.Run, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	w, err := run.Writer("contest", "found_detail")
	if err != nil {
		return err
	}
	for _, k := range keys {
		body, resp, err := c.FoundDetail(ctx, k)
		if err != nil {
			return err
		}
		if err := w.Write(bronze.Envelope{FetchedAt: time.Now().UTC(), Source: "contest_api", Endpoint: "/v1/events/current/found/{key}",
			Params: map[string]any{"key": k}, Status: resp.Status, Body: body}); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "상세 %s 받음\n", k)
	}
	return nil
}
