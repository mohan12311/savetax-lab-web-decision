package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/ratelimit"
	"savetax-lab/collector/internal/scrape"
	"savetax-lab/collector/internal/scrape/danawa"
	"savetax-lab/collector/internal/store/bronze"
	"savetax-lab/collector/internal/store/state"
)

const (
	scrapeKind        = "scrape_danawa"
	scrapeMaxAttempts = 3
	maxQueryRunes     = 200
	// 검색에서 찾은 상품 페이지는 다음 검색어보다 먼저 받는다(출력이 검색어별로 모이게).
	discoveredPriority = 1e9
)

type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }

// runScrape는 다나와 검색·상품 페이지를 받아 원문(HTML)과 추출 결과를 bronze에 쌓는다.
// 작업은 state 큐(scrape_danawa)에 올려 두고 하나씩 처리하므로 중간에 끊겨도 다음 실행에서 이어진다.
func runScrape(ctx context.Context, a *app, args []string) error {
	fs := flag.NewFlagSet("scrape", flag.ContinueOnError)
	source := fs.String("source", "danawa", "시중가 소스(현재 danawa만)")
	var queries, pcodes stringsFlag
	fs.Var(&queries, "query", "검색어(모델명 등). 여러 번 줄 수 있다")
	fs.Var(&pcodes, "pcode", "다나와 상품 코드를 검색 없이 바로 받는다. 여러 번 줄 수 있다")
	from := fs.String("from", "", "검색어 목록 파일(한 줄에 하나, #으로 시작하면 주석)")
	top := fs.Int("top", 3, "검색어마다 상품 페이지를 받을 가격비교 상품 수(0이면 검색만)")
	refresh := fs.Bool("refresh", false, "이미 끝난 작업도 다시 받는다")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if *source != "danawa" {
		return usageError{fmt.Sprintf("지원하지 않는 소스: %q (현재 danawa만)", *source)}
	}
	if *top < 0 {
		return usageError{"--top은 0 이상"}
	}
	if *from != "" {
		lines, err := readQueryFile(*from)
		if err != nil {
			return err
		}
		queries = append(queries, lines...)
	}
	var tasks []state.Task
	n := len(queries) + len(pcodes)
	for i, q := range queries {
		q = strings.Join(strings.Fields(q), " ")
		if q == "" {
			continue
		}
		if utf8.RuneCountInString(q) > maxQueryRunes {
			return usageError{fmt.Sprintf("검색어가 %d자를 넘는다: %q…", maxQueryRunes, string([]rune(q)[:20]))}
		}
		tasks = append(tasks, state.Task{Key: "q:" + q, Priority: float64(n - i)})
	}
	for i, p := range pcodes {
		if !danawa.ValidPCode(p) {
			return usageError{fmt.Sprintf("pcode는 숫자만: %q", p)}
		}
		tasks = append(tasks, state.Task{Key: "p:" + p, Priority: float64(n - len(queries) - i)})
	}

	st, err := state.Open(filepath.Join(a.stateDir(), "collector.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.RecoverRunning(ctx, scrapeKind); err != nil {
		return err
	}
	if _, err := st.Enqueue(ctx, scrapeKind, tasks); err != nil {
		return err
	}
	if *refresh && len(tasks) > 0 {
		keys := make([]string, len(tasks))
		for i, t := range tasks {
			keys[i] = t.Key
		}
		if _, err := st.Requeue(ctx, scrapeKind, keys); err != nil {
			return err
		}
	}
	out := os.Stdout
	counts, err := st.Counts(ctx, scrapeKind)
	if err != nil {
		return err
	}
	if counts[state.Pending] == 0 {
		if len(tasks) == 0 {
			return usageError{"사용법: savetax scrape --source danawa --query <검색어> | --from <파일> | --pcode <코드> [--top 3] [--refresh]"}
		}
		fmt.Fprintln(out, "할 작업이 없다 — 이미 끝난 작업은 --refresh로 다시 받는다")
		return nil
	}

	searchSite, err := a.retailSite(ctx, danawa.SearchBaseURL)
	if err != nil {
		return err
	}
	prodSite, err := a.retailSite(ctx, danawa.ProdBaseURL)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "다나와: 대기 %d건 · 검색 %s · 상품 %s\n", counts[state.Pending], siteNote(searchSite), siteNote(prodSite))

	run := bronze.NewRun(a.bronzeRoot(), "scrape", args)
	run.TrackStats(searchSite.Service(), searchSite.Stats())
	run.TrackStats(prodSite.Service(), prodSite.Stats())
	s := &scraper{
		st: st, run: run, out: out,
		dc:      danawa.New(searchSite, prodSite),
		top:     *top,
		refresh: *refresh,
	}
	err = s.loop(ctx)
	mpath, ferr := run.Finish(err)

	if c, cerr := st.Counts(context.WithoutCancel(ctx), scrapeKind); cerr == nil {
		fmt.Fprintf(out, "\n작업: done %d · pending %d · failed %d · skipped %d\n", c[state.Done], c[state.Pending], c[state.Failed], c[state.Skipped])
	}
	if mpath != "" {
		fmt.Fprintf(out, "run manifest: %s\n", mpath)
	}
	return errors.Join(err, ferr)
}

func readQueryFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

func siteNote(s *scrape.Site) string {
	note := fmt.Sprintf("%.2f req/s", float64(s.Limit()))
	if d := s.Robots().CrawlDelay; d > 0 {
		note += fmt.Sprintf("(robots Crawl-delay %s)", d)
	}
	return note
}

// retailSite는 시중 사이트 하나의 클라이언트다. 참가 토큰을 넣지 않는다(scrape.NewSite가 토큰 없이 만든다).
func (a *app) retailSite(ctx context.Context, base string) (*scrape.Site, error) {
	host := strings.TrimPrefix(base, "https://")
	return scrape.NewSite(ctx, scrape.SiteOptions{
		BaseURL:   base,
		UserAgent: a.cfg.UserAgent,
		Limiter:   a.lim.Get(ratelimit.Retail(host)),
		Logger:    a.log,
	})
}

type scraper struct {
	st      *state.Store
	run     *bronze.Run
	out     io.Writer
	dc      *danawa.Client
	top     int
	refresh bool
}

func (s *scraper) loop(ctx context.Context) error {
	for {
		keys, err := s.st.Claim(ctx, scrapeKind, 1)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}
		key := keys[0]
		var terr error
		switch {
		case strings.HasPrefix(key, "q:"):
			terr = s.search(ctx, strings.TrimPrefix(key, "q:"))
		case strings.HasPrefix(key, "p:"):
			terr = s.product(ctx, strings.TrimPrefix(key, "p:"))
		default:
			terr = fmt.Errorf("알 수 없는 작업 키: %q", key)
		}
		if err := s.settle(ctx, key, terr); err != nil {
			return err
		}
	}
}

// settle은 작업 결과를 큐에 반영한다. 계속할 수 없는 오류(차단·취소)만 돌려준다.
func (s *scraper) settle(ctx context.Context, key string, terr error) error {
	ctx2 := context.WithoutCancel(ctx)
	ae, isAPI := httpx.AsAPIError(terr)
	switch {
	case terr == nil:
		return s.st.Done(ctx2, scrapeKind, key)
	case ctx.Err() != nil:
		return ctx.Err() // running으로 남은 작업은 다음 실행의 RecoverRunning이 되돌린다
	case errors.Is(terr, scrape.ErrBlocked):
		_, _ = s.st.Fail(ctx2, scrapeKind, key, terr, scrapeMaxAttempts)
		return fmt.Errorf("다나와가 403으로 막았다 — 반복 요청하지 않고 중단한다: %w", terr)
	case errors.Is(terr, scrape.ErrDisallowed):
		fmt.Fprintf(s.out, "  건너뜀(robots): %s\n", key)
		return s.st.Skip(ctx2, scrapeKind, key, terr.Error())
	case isAPI && ae.Status == http.StatusNotFound:
		fmt.Fprintf(s.out, "  없음(404): %s — 가격 정상이라는 뜻이 아니다\n", key)
		return s.st.Skip(ctx2, scrapeKind, key, "404")
	default:
		final, err := s.st.Fail(ctx2, scrapeKind, key, terr, scrapeMaxAttempts)
		next := "다음 실행에서 다시"
		if final {
			next = "포기(failed)"
		}
		fmt.Fprintf(s.out, "  실패: %s — %v (%s)\n", key, terr, next)
		return err
	}
}

func (s *scraper) search(ctx context.Context, q string) error {
	fmt.Fprintf(s.out, "\n검색 %q\n", q)
	pg, res, err := s.dc.Search(ctx, q)
	if err != nil {
		return s.keepUnparsed(pg, "search", "/dsearch.php", map[string]any{"query": q}, err)
	}
	if err := s.save(pg, "search", "/dsearch.php", map[string]any{"query": q, "url": pg.URL}, res); err != nil {
		return err
	}
	if !res.ListFound {
		fmt.Fprintln(s.out, "  결과 없음(목록 영역 없음 — 검색 결과가 없거나 페이지 구조가 바뀌었다)")
		return nil
	}
	fmt.Fprintf(s.out, "  가격비교 상품 %d개 · 쇼핑몰 개별 상품 %d개(탐색 참고용)\n", len(res.Catalog), len(res.Listings))
	var next []state.Task
	for i, c := range res.Catalog {
		mark := " "
		if i < s.top {
			mark = "→"
			next = append(next, state.Task{Key: "p:" + c.PCode, Priority: discoveredPriority - float64(i)})
		}
		extra := ""
		if len(c.Variants) > 1 {
			extra = fmt.Sprintf(" (하위 상품 %d개)", len(c.Variants))
		}
		fmt.Fprintf(s.out, "  %s %-10s %12s  %s%s\n", mark, c.PCode, krw(c.MinPriceKRW), clip(c.Name, 60), extra)
	}
	if len(next) == 0 {
		return nil
	}
	if _, err := s.st.Enqueue(ctx, scrapeKind, next); err != nil {
		return err
	}
	if s.refresh {
		keys := make([]string, len(next))
		for i, t := range next {
			keys[i] = t.Key
		}
		_, err = s.st.Requeue(ctx, scrapeKind, keys)
	}
	return err
}

func (s *scraper) product(ctx context.Context, pcode string) error {
	pg, p, err := s.dc.Product(ctx, pcode)
	if err != nil {
		return s.keepUnparsed(pg, "product", "/info/", map[string]any{"pcode": pcode}, err)
	}
	if err := s.save(pg, "product", "/info/", map[string]any{"pcode": pcode, "url": pg.URL}, p); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "  상품 %s %s\n", p.PCode, p.Name)
	fmt.Fprintf(s.out, "    %s\n", p.URL)
	line := "    최저가 " + krw(p.MinPriceKRW)
	if p.MinPriceWithShippingKRW != nil {
		line += " · 배송비 포함 " + krw(p.MinPriceWithShippingKRW)
	}
	if p.OfferCount != nil {
		line += fmt.Sprintf(" · 판매처 %s곳(정적 목록 %d곳)", commas(fmt.Sprint(*p.OfferCount)), len(p.Malls))
	}
	fmt.Fprintln(s.out, line)
	for _, m := range p.Malls {
		badge := ""
		if m.Lowest {
			badge = " ★최저가"
		}
		fmt.Fprintf(s.out, "      %d %s %12s  배송 %s%s\n", m.Rank, pad(clip(m.Mall, 12), 14), krw(m.PriceKRW), orNone(m.ShippingText), badge)
	}
	return nil
}

// save는 원문 HTML을 내용 주소 파일로, 추출 결과를 bronze 레코드로 남긴다.
func (s *scraper) save(pg *scrape.Page, kind, endpoint string, params map[string]any, extracted any) error {
	ref, err := s.run.WriteBlob("retail/danawa", "html", pg.FetchedAt, pg.Body, "html", pg.ContentType)
	if err != nil {
		return err
	}
	body, err := json.Marshal(extracted)
	if err != nil {
		return err
	}
	w, err := s.run.Writer("retail/danawa", kind)
	if err != nil {
		return err
	}
	return w.Write(bronze.Envelope{
		FetchedAt: pg.FetchedAt,
		Source:    "danawa",
		Endpoint:  endpoint,
		Params:    params,
		Status:    pg.Status,
		Body:      body,
		Raw:       &ref,
	})
}

// keepUnparsed: 받기는 했지만 추출에 실패한 페이지도 원문을 남긴다(구조 변경 확인용, body=null).
func (s *scraper) keepUnparsed(pg *scrape.Page, kind, endpoint string, params map[string]any, err error) error {
	if pg == nil {
		return err
	}
	params["url"] = pg.URL
	return errors.Join(fmt.Errorf("추출 실패: %w", err), s.save(pg, kind, endpoint, params, nil))
}

func krw(v *int64) string {
	if v == nil {
		return "-"
	}
	return commas(fmt.Sprint(*v)) + "원"
}
