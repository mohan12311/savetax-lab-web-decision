// savetax는 수집·조회·제출 CLI다. 사람이 읽는 결과는 stdout, JSON 로그는 stderr로 나간다.
//
//	savetax [-v] <command> [flags]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"savetax-lab/collector/internal/api/contest"
	"savetax-lab/collector/internal/api/data"
	"savetax-lab/collector/internal/config"
	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/logx"
	"savetax-lab/collector/internal/ratelimit"
)

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, a *app, args []string) error
}

var commands = []command{
	{"check", "대회 상수(/v1/events/current)·내 한도(/me)·데이터 API 사용량·데이터셋 버전 확인", runCheck},
	{"dataset", "데이터셋 zip 다운로드 → SHA256 검증 → data/bronze/dataset/<ver>/", runDataset},
	{"crawl", "(M4) 후보 큐 기반 listings/items/deliveries 수집", notYet("M4")},
	{"lookup", "물품번호 목록의 선점 상태를 items/lookup으로 100개씩 조회 → data/bronze/contest/lookup/", runLookup},
	{"facts", "지정한 물품의 items/{item_id}(스냅샷·납품 fact) → data/bronze/contest/items/, 가격 차이 기준 계산(--draft 본문 초안)", runFacts},
	{"watch", "(M6) SSE 피드 감시", notYet("M6")},
	{"scrape", "다나와 검색·상품 페이지 수집 → data/bronze/retail/danawa/ (원문 HTML + 추출)", runScrape},
	{"probe", "시중 판매 페이지가 정적 HTML로 읽히는지(본문에 상품명·가격) 판정 → data/bronze/retail/probe/", runProbe},
	{"submit", "본문 게이트 검사 → preview(dry-run). --confirm일 때만 제출, --status <key>로 접수·lint 확인", runSubmit},
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func main() {
	os.Exit(run())
}

func run() int {
	verbose := flag.Bool("v", false, "debug 로그")
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() == 0 {
		usage()
		return 2
	}
	name, args := flag.Arg(0), flag.Args()[1:]
	var cmd *command
	for i := range commands {
		if commands[i].name == name {
			cmd = &commands[i]
		}
	}
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "알 수 없는 명령: %s\n\n", name)
		usage()
		return 2
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := logx.New(os.Stderr, level)
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "설정 오류:", logx.Mask(err.Error()))
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := newApp(cfg, log)
	if err := cmd.run(ctx, a, args); err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(os.Stderr, ue.msg)
			return 2
		}
		fmt.Fprintln(os.Stderr, "오류:", logx.Mask(err.Error()))
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, "사용법: savetax [-v] <command> [flags]\n\n명령:")
	for _, c := range commands {
		fmt.Fprintf(os.Stderr, "  %-8s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(os.Stderr, "\n환경변수: SAVETAX_TOKEN(필수: 인증 명령), SAVETAX_DATA_DIR, SAVETAX_*_RPS, SAVETAX_USER_AGENT")
}

func notYet(milestone string) func(context.Context, *app, []string) error {
	return func(context.Context, *app, []string) error {
		return usageError{fmt.Sprintf("아직 구현되지 않음 (%s 예정)", milestone)}
	}
}

// app은 명령들이 공유하는 설정·로거·리미터다. 리미터는 프로세스 안에서 이름별로 하나다.
type app struct {
	cfg *config.Config
	log *slog.Logger
	lim *ratelimit.Registry
}

func newApp(cfg *config.Config, log *slog.Logger) *app {
	lim := ratelimit.NewRegistry(map[string]float64{
		ratelimit.DataHourly: cfg.DataRPS,
		ratelimit.DataSearch: cfg.SearchRPS,
		ratelimit.DataFree:   1.0,
		ratelimit.Contest:    cfg.ContestRPS,
		ratelimit.Dataset:    1.0,
	}, cfg.RetailRPS)
	return &app{cfg: cfg, log: log, lim: lim}
}

func (a *app) bronzeRoot() string { return filepath.Join(a.cfg.DataDir, "bronze") }
func (a *app) stateDir() string   { return filepath.Join(a.cfg.DataDir, "state") }

func (a *app) httpClient(service, base string, limiters ...string) (*httpx.Client, error) {
	opt := httpx.Options{
		Service:   service,
		BaseURL:   base,
		Token:     a.cfg.Token,
		UserAgent: a.cfg.UserAgent,
		Logger:    a.log,
	}
	for _, n := range limiters {
		opt.Limiters = append(opt.Limiters, a.lim.Get(n))
	}
	return httpx.New(opt)
}

func (a *app) contest() (*contest.Client, error) {
	h, err := a.httpClient("contest", a.cfg.ContestBaseURL, ratelimit.Contest)
	if err != nil {
		return nil, err
	}
	return contest.New(h), nil
}

func (a *app) data() (*data.Client, error) {
	h, err := a.httpClient("data", a.cfg.DataBaseURL) // 버킷은 호출 종류별로 data 패키지가 고른다
	if err != nil {
		return nil, err
	}
	return data.New(h, data.Limiters{
		Hourly: a.lim.Get(ratelimit.DataHourly),
		Search: a.lim.Get(ratelimit.DataSearch),
		Free:   a.lim.Get(ratelimit.DataFree),
	}), nil
}

func (a *app) datasetHTTP() (*httpx.Client, error) {
	return a.httpClient("dataset", a.cfg.DatasetBaseURL, ratelimit.Dataset)
}
