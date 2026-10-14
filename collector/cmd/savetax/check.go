package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"savetax-lab/collector/internal/api/contest"
	"savetax-lab/collector/internal/dataset"
	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/store/bronze"
)

// 변경 감지에서 빼는 값(매번 바뀌는 것).
var volatileEventKeys = map[string]bool{"server_time": true, "submission_count": true}

func runCheck(ctx context.Context, a *app, args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	out := os.Stdout
	cc, err := a.contest()
	if err != nil {
		return err
	}
	dc, err := a.data()
	if err != nil {
		return err
	}
	dh, err := a.datasetHTTP()
	if err != nil {
		return err
	}
	run := bronze.NewRun(a.bronzeRoot(), "check", args)
	run.TrackStats("contest", cc.HTTP().Stats())
	run.TrackStats("data", dc.HTTP().Stats())
	run.TrackStats("dataset", dh.Stats())
	var errs []error
	fail := func(err error) {
		errs = append(errs, err)
		fmt.Fprintln(out, "  실패:", err)
		hintAuth(out, err)
	}

	// 1) 대회 상수 — 공개, 토큰 불필요
	fmt.Fprintln(out, "== 대회 /v1/events/current ==")
	if err := checkEvent(ctx, a, cc, run, out); err != nil {
		fail(err)
	}

	// 2) 인증 필요 항목
	if a.cfg.Token == "" {
		fmt.Fprintln(out, "\n(SAVETAX_TOKEN 미설정 — /me·데이터 API 사용량 확인을 건너뜀)")
	} else {
		fmt.Fprintln(out, "\n== 내 대회 한도 /v1/events/current/me ==")
		if raw, resp, err := cc.Me(ctx); err != nil {
			fail(err)
		} else {
			saveEnvelope(run, "contest", "me", "contest_api", "/v1/events/current/me", resp, &errs)
			printJSON(out, raw)
		}

		fmt.Fprintln(out, "\n== 데이터 API 사용량 api.savetax.now/v1/me (예산 미차감) ==")
		if env, resp, err := dc.Me(ctx); err != nil {
			fail(err)
		} else {
			saveEnvelope(run, "data", "me", "data_api", "/v1/me", resp, &errs)
			printJSON(out, env.Data)
		}
	}

	// 3) 데이터셋 버전
	fmt.Fprintln(out, "\n== 데이터셋 ==")
	if err := checkDataset(ctx, a, dh, out); err != nil {
		fail(err)
	}

	err = errors.Join(errs...)
	mpath, ferr := run.Finish(err)
	if mpath != "" {
		fmt.Fprintf(out, "\nrun manifest: %s\n", mpath)
	}
	return errors.Join(err, ferr)
}

func checkEvent(ctx context.Context, a *app, cc *contest.Client, run *bronze.Run, out io.Writer) error {
	localBefore := time.Now()
	ev, resp, err := cc.Event(ctx)
	if err != nil {
		return err
	}
	localAfter := time.Now()
	var errs []error
	saveEnvelope(run, "contest", "event", "contest_api", "/v1/events/current", resp, &errs)

	pg := ev.PriceGap
	row := func(k, v string) { fmt.Fprintf(out, "  %s %s\n", pad(k, 20), v) }
	row("phase", fmt.Sprintf("%s (admission=%s, mode=%s)", ev.Phase, ev.AdmissionState, ev.ContestMode))
	row("마감", fmt.Sprintf("%s (closes_at %s) — 남은 시간 %s",
		ev.ClosesAt.In(kst).Format("2006-01-02 15:04 MST"), ev.ClosesAt.UTC().Format(time.RFC3339), humanDuration(time.Until(ev.ClosesAt))))
	row("ruleset_version", ev.RulesetVersion)
	row("dataset_release_id", ev.DatasetReleaseID)
	row("price_gap", fmt.Sprintf("%s: P ≥ %s×M 또는 P−M ≥ %s원", pg.Rule, pg.MinRatio, commas(pg.MinUnitKRW)))
	row("evidence_format", fmt.Sprintf("%s · release_claim_on_withdraw=%v · %s · %s", ev.EvidenceFormat, ev.ReleaseClaimOnWithdraw, ev.ScoringVersion, ev.IdentityVersion))
	row("submission_count", commas(fmt.Sprint(ev.SubmissionCount)))
	// 이 응답은 엣지 캐시(x-edge-cache: HIT/STALE)라 server_time은 '응답 생성 시각'이다.
	// 시계 차이는 매번 새로 붙는 Date 헤더(초 단위)로 본다.
	cache := resp.Header.Get("X-Edge-Cache")
	row("server_time", fmt.Sprintf("%s (응답 생성 시각, edge-cache=%s)", ev.ServerTime.UTC().Format(time.RFC3339), orNone(cache)))
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		mid := localBefore.Add(localAfter.Sub(localBefore) / 2)
		skew := mid.Sub(d).Seconds()
		note := ""
		if skew > 5 || skew < -5 {
			note = " ⚠ 로컬 시계를 맞출 것(observed_at·마감 판단에 영향)"
		}
		row("로컬 시계 차이", fmt.Sprintf("%+.1fs (Date 헤더 기준, ±1s 오차)%s", skew, note))
	}

	// 지난 실행과 비교해 규칙·한도 변경을 알린다.
	snap := filepath.Join(a.stateDir(), "event_current.json")
	changes, first, err := diffSnapshot(snap, resp.Body)
	switch {
	case err != nil:
		errs = append(errs, err)
	case first:
		row("변경 감지", "첫 실행 — 기준값 저장")
	case len(changes) == 0:
		row("변경 감지", "지난 실행과 같음")
	default:
		row("변경 감지", fmt.Sprintf("⚠ %d개 항목이 바뀌었다 — docs/·제출 기본값을 확인할 것", len(changes)))
		for _, c := range changes {
			fmt.Fprintf(out, "    %s\n", c)
		}
	}
	if err == nil {
		if werr := bronze.WriteFileAtomic(snap, resp.Body); werr != nil {
			errs = append(errs, werr)
		}
	}
	return errors.Join(errs...)
}

// diffSnapshot은 이전 스냅샷과 비교해 바뀐 항목을 "key: old → new"로 돌려준다.
func diffSnapshot(path string, cur []byte) (changes []string, first bool, err error) {
	prevRaw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	pv, err := decodeJSON(prevRaw)
	if err != nil {
		return nil, true, nil // 깨진 스냅샷은 새 기준으로 덮어쓴다
	}
	cv, err := decodeJSON(cur)
	if err != nil {
		return nil, false, err
	}
	pm, cm := map[string]string{}, map[string]string{}
	flatten("", pv, pm)
	flatten("", cv, cm)
	keys := map[string]bool{}
	for k := range pm {
		keys[k] = true
	}
	for k := range cm {
		keys[k] = true
	}
	for k := range keys {
		if volatileEventKeys[k] || pm[k] == cm[k] {
			continue
		}
		changes = append(changes, fmt.Sprintf("%s: %s → %s", k, orNone(pm[k]), orNone(cm[k])))
	}
	slices.Sort(changes)
	return changes, false, nil
}

func orNone(s string) string {
	if s == "" {
		return "(없음)"
	}
	return s
}

func checkDataset(ctx context.Context, a *app, h *httpx.Client, out io.Writer) error {
	latest, _, err := dataset.FetchLatest(ctx, h)
	if err != nil {
		return err
	}
	row := func(k, v string) { fmt.Fprintf(out, "  %s %s\n", pad(k, 20), v) }
	row("최신 release", fmt.Sprintf("%s (published %s)", latest.ReleaseID, latest.PublishedAt))
	cur, err := dataset.ReadCurrent(filepath.Join(a.bronzeRoot(), "dataset"))
	switch {
	case err != nil:
		return err
	case cur == nil:
		row("로컬", "없음 → make dataset")
	case cur.ReleaseID == latest.ReleaseID:
		row("로컬", cur.ReleaseID+" (최신)")
	default:
		row("로컬", cur.ReleaseID+" — 새 버전 있음 → make dataset")
	}
	return nil
}

func saveEnvelope(run *bronze.Run, source, kind, srcName, endpoint string, resp *httpx.Response, errs *[]error) {
	w, err := run.Writer(source, kind)
	if err == nil {
		var rid *string
		if v := resp.Header.Get("X-Request-Id"); v != "" {
			rid = &v
		}
		err = w.Write(bronze.Envelope{
			FetchedAt: time.Now().UTC(),
			Source:    srcName,
			Endpoint:  endpoint,
			Status:    resp.Status,
			RequestID: rid,
			Body:      json.RawMessage(resp.Body),
		})
	}
	if err != nil {
		*errs = append(*errs, fmt.Errorf("bronze 저장 %s/%s: %w", source, kind, err))
	}
}

func printJSON(out io.Writer, raw []byte) {
	v, err := decodeJSON(raw)
	if err != nil {
		fmt.Fprintf(out, "  (JSON 해석 실패: %v)\n", err)
		return
	}
	printTree(out, v, "  ", 0)
}

func hintAuth(out io.Writer, err error) {
	if errors.Is(err, httpx.ErrNoToken) {
		fmt.Fprintln(out, "  → SAVETAX_TOKEN을 .env에 넣는다('내 경기'에서 발급)")
		return
	}
	ae, ok := httpx.AsAPIError(err)
	if !ok {
		return
	}
	switch {
	case ae.Status == 401:
		fmt.Fprintln(out, "  → 토큰이 없거나·틀렸거나·폐기/만료됐다. '내 경기'에서 재발급 후 .env 갱신")
	case ae.Code == "NOT_REGISTERED":
		fmt.Fprintln(out, "  → 참가 등록이 먼저다: POST /v1/events/current/registration (규칙 동의 필요 — 사람이 직접)")
	}
}
