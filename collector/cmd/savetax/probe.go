package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/scrape"
	"savetax-lab/collector/internal/scrape/probe"
	"savetax-lab/collector/internal/store/bronze"
)

type probeTarget struct {
	URL     string
	Needles []string
}

// runProbe는 시중 판매 페이지를 정적 HTML로 받아, 본문 글자에 상품명·가격이 있는지 판정한다
// (서버 근거 검사가 읽을 수 있을지 미리 보기). robots.txt를 지키고 도메인별 속도 제한을 건다.
// 토큰은 보내지 않는다. 403을 받은 도메인은 남은 URL을 건너뛴다(같은 도메인 반복 금지).
// 원문은 bronze/retail/probe/html/<sha256>.html.gz, 판정은 bronze/retail/probe/page 봉투에 남는다.
func runProbe(ctx context.Context, a *app, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	var urls, needles stringsFlag
	fs.Var(&urls, "url", "https 페이지 주소. 여러 번 줄 수 있다")
	fs.Var(&needles, "need", "--url 페이지 본문에 있어야 할 문구(상품명·모델명). 여러 번 줄 수 있다")
	from := fs.String("from", "", "목록 파일: 한 줄에 `주소 | 문구1 | 문구2` (#으로 시작하면 주석)")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	var targets []probeTarget
	for _, u := range urls {
		targets = append(targets, probeTarget{URL: u, Needles: needles})
	}
	if *from != "" {
		lines, err := readQueryFile(*from)
		if err != nil {
			return err
		}
		for _, line := range lines {
			parts := strings.Split(line, "|")
			t := probeTarget{URL: strings.TrimSpace(parts[0])}
			for _, n := range parts[1:] {
				if n = strings.TrimSpace(n); n != "" {
					t.Needles = append(t.Needles, n)
				}
			}
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return usageError{"사용법: savetax probe --url <https 주소> [--need 문구]... | --from <파일>"}
	}

	out := os.Stdout
	run := bronze.NewRun(a.bronzeRoot(), "probe", args)
	sites := map[string]*scrape.Site{}
	blocked := map[string]bool{}
	var err error
	for _, t := range targets {
		if err = ctx.Err(); err != nil {
			break
		}
		u, perr := url.Parse(t.URL)
		if perr != nil || u.Scheme != "https" || u.Host == "" {
			fmt.Fprintf(out, "건너뜀  %s — https 주소가 아니다\n", t.URL)
			continue
		}
		if blocked[u.Host] {
			fmt.Fprintf(out, "건너뜀  %s — 이 도메인은 이번 실행에서 403을 받았다\n", t.URL)
			continue
		}
		site := sites[u.Host]
		if site == nil {
			s, serr := a.retailSite(ctx, "https://"+u.Host)
			if serr != nil {
				fmt.Fprintf(out, "실패    %s — robots.txt를 못 읽었다: %v\n", u.Host, serr)
				blocked[u.Host] = true
				continue
			}
			site = s
			sites[u.Host] = s
			run.TrackStats(s.Service(), s.Stats())
		}
		res, perr := probeOne(ctx, site, u.RequestURI(), t.Needles, run)
		switch {
		case errors.Is(perr, scrape.ErrBlocked):
			blocked[u.Host] = true
			fmt.Fprintf(out, "차단403 %s — 이 도메인의 남은 주소는 건너뛴다\n", t.URL)
		case errors.Is(perr, scrape.ErrDisallowed):
			fmt.Fprintf(out, "robots  %s — robots.txt가 막은 경로라 보내지 않았다\n", t.URL)
		case perr != nil:
			if ae, ok := httpx.AsAPIError(perr); ok {
				fmt.Fprintf(out, "HTTP%d %s\n", ae.Status, t.URL)
			} else {
				fmt.Fprintf(out, "실패    %s — %v\n", t.URL, perr)
			}
		default:
			printProbe(out, t.URL, res)
		}
	}
	mpath, ferr := run.Finish(err)
	if mpath != "" {
		fmt.Fprintf(out, "run manifest: %s\n", mpath)
	}
	return errors.Join(err, ferr)
}

// probeOne은 한 페이지를 받아 판정하고 원문과 판정을 bronze에 남긴다.
func probeOne(ctx context.Context, site *scrape.Site, path string, needles []string, run *bronze.Run) (probe.Result, error) {
	pg, err := site.Get(ctx, path)
	if err != nil {
		return probe.Result{}, err
	}
	res := probe.Analyze(pg.Body, pg.ContentType, needles)
	ref, err := run.WriteBlob("retail/probe", "html", pg.FetchedAt, pg.Body, "html", pg.ContentType)
	if err != nil {
		return res, err
	}
	body, err := json.Marshal(res)
	if err != nil {
		return res, err
	}
	w, err := run.Writer("retail/probe", "page")
	if err != nil {
		return res, err
	}
	return res, w.Write(bronze.Envelope{
		FetchedAt: pg.FetchedAt,
		Source:    "probe",
		Endpoint:  site.Host(),
		Params:    map[string]any{"url": pg.URL, "needles": needles},
		Status:    pg.Status,
		Body:      body,
		Raw:       &ref,
	})
}

func printProbe(out io.Writer, u string, r probe.Result) {
	prices := make([]string, 0, 3)
	for _, p := range r.Prices[:min(3, len(r.Prices))] {
		prices = append(prices, commas(fmt.Sprint(p))+"원")
	}
	var miss []string
	for n, h := range r.Needles {
		if !h.InText {
			where := "없음"
			if h.InRaw {
				where = "스크립트·속성에만"
			}
			miss = append(miss, fmt.Sprintf("%q %s", n, where))
		}
	}
	fmt.Fprintf(out, "%-17s %s\n  제목 %q · 본문 %d자 · 스크립트 %dB · 본문 가격 %s",
		r.Verdict, u, truncRunes(r.Title, 60), r.TextChars, r.ScriptBytes, strings.Join(prices, ", "))
	if len(r.JSONLDPrices) > 0 || r.MetaPrice != "" {
		fmt.Fprintf(out, " · 구조화 가격 %v %s", r.JSONLDPrices, r.MetaPrice)
	}
	if len(miss) > 0 {
		fmt.Fprintf(out, "\n  본문에 없는 문구: %s", strings.Join(miss, ", "))
	}
	if len(r.AppShell) > 0 {
		fmt.Fprintf(out, "\n  JS 앱 흔적: %s", strings.Join(r.AppShell, ", "))
	}
	fmt.Fprintln(out)
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
