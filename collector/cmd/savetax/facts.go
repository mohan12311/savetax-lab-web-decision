package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"savetax-lab/collector/internal/api/contest"
	"savetax-lab/collector/internal/claim"
	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/store/bronze"
)

// runFacts는 지정한 물품의 상세(items/{item_id})를 받아 bronze/contest/items에 쌓고,
// 비교 기준별 납품 단가 P와 가격 차이 기준을 통과하는 최대 시중가를 보여 준다.
// 첫 호출이 서버 스냅샷을 만들어 남에게도 보이는 신호가 될 수 있으므로 일괄 조회는 하지 않는다 — 제출 직전 후보에만 쓴다.
func runFacts(ctx context.Context, a *app, args []string) error {
	fs := flag.NewFlagSet("facts", flag.ContinueOnError)
	var ids stringsFlag
	fs.Var(&ids, "id", "물품번호(item_id). 여러 번 줄 수 있다")
	draft := fs.Bool("draft", false, "data/gold/claims/<item_id>.json에 근거가 빈 본문 초안을 쓴다(이미 있으면 두지 않는다)")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	list, err := cleanIDs(ids)
	if err != nil {
		return usageError{err.Error()}
	}
	if len(list) == 0 {
		return usageError{"사용법: savetax facts --id <item_id> [--id …] [--draft]"}
	}
	c, err := a.contest()
	if err != nil {
		return err
	}
	if !c.HTTP().HasToken() {
		return httpx.ErrNoToken
	}
	out := os.Stdout
	ev, _, err := c.Event(ctx)
	if err != nil {
		return err
	}
	rule, err := claim.ParseGapRule(ev.PriceGap.Rule, ev.PriceGap.MinRatio, ev.PriceGap.MinUnitKRW)
	if err != nil {
		return err
	}

	run := bronze.NewRun(a.bronzeRoot(), "facts", args)
	run.TrackStats(c.HTTP().Service(), c.HTTP().Stats())
	w, err := run.Writer("contest", "items")
	if err == nil {
		for _, id := range list {
			var it *contest.Item
			if it, err = fetchItem(ctx, c, w, id, out); err != nil {
				break
			}
			printItem(out, it, rule)
			if *draft {
				if err = writeDraft(a, ev, it, out); err != nil {
					break
				}
			}
		}
	}
	mpath, ferr := run.Finish(err)
	if mpath != "" {
		fmt.Fprintf(out, "run manifest: %s\n", mpath)
	}
	return errors.Join(err, ferr)
}

// fetchItem은 물품 상세를 받아 봉투로 남긴다. 새 물품 조회 한도(429)면 1분쯤 기다렸다 다시 묻는다.
func fetchItem(ctx context.Context, c *contest.Client, w *bronze.Writer, id string, out io.Writer) (*contest.Item, error) {
	it, resp, err := c.Item(ctx, id)
	for streak := 1; isRateLimited(err) && streak <= lookupMaxRateWaits; streak++ {
		fmt.Fprintf(out, "  %s: 새 물품 조회 한도(429) — %s 뒤 다시 묻는다 (연속 %d/%d)\n", id, lookupRateWait, streak, lookupMaxRateWaits)
		if serr := lookupSleep(ctx, lookupRateWait); serr != nil {
			return nil, serr
		}
		it, resp, err = c.Item(ctx, id)
	}
	if err != nil {
		return nil, fmt.Errorf("물품 %s: %w", id, err)
	}
	var rid *string
	if v := resp.Header.Get("X-Request-Id"); v != "" {
		rid = &v
	}
	if err := w.Write(bronze.Envelope{
		FetchedAt: time.Now().UTC(),
		Source:    "contest_api",
		Endpoint:  "/v1/events/current/items/{item_id}",
		Params:    map[string]any{"item_id": id},
		Status:    resp.Status,
		RequestID: rid,
		Body:      json.RawMessage(resp.Body),
	}); err != nil {
		return nil, err
	}
	return it, nil
}

func printItem(out io.Writer, it *contest.Item, rule claim.GapRule) {
	s := it.Snapshot
	fmt.Fprintf(out, "\n물품 %s  [%s]  %s\n", it.ItemID, orNone(it.Availability.State), s.Name)
	if m := it.Availability.Message; m != "" && it.Availability.State != "available" {
		fmt.Fprintf(out, "  %s\n", m)
	}
	fmt.Fprintf(out, "  모델 %s · 제조 %s · 분류 %s · 스냅샷 %s\n", orNone(s.Model), orNone(s.Manufacturer), orNone(s.Category), orNone(s.FetchedAt))
	scorable := 0
	reasons := map[string]int{}
	for _, f := range it.Facts {
		if f.Scorable {
			scorable++
			continue
		}
		for _, r := range f.ExclusionReasons {
			reasons[r]++
		}
	}
	trunc := ""
	if s.FactsTruncated {
		trunc = fmt.Sprintf(" (전체 %d개 중 금액순으로 잘림)", s.FactsTotal)
	}
	fmt.Fprintf(out, "  fact %d개, 점수에 쓸 수 있는 것 %d개%s\n", len(it.Facts), scorable, trunc)
	if len(reasons) > 0 {
		var parts []string
		for r, n := range reasons {
			parts = append(parts, fmt.Sprintf("%s %d", r, n))
		}
		fmt.Fprintf(out, "  제외 사유: %s\n", strings.Join(parts, " · "))
	}
	groups, err := claim.FactGroups(it.Facts, rule)
	if err != nil {
		fmt.Fprintln(out, "  ", err)
		return
	}
	for _, g := range groups {
		fmt.Fprintf(out, "  기준 %s (단위 %s, 세금 %s) · 줄 %d · 수량 %s · 금액 %s원 · 납품 단가 P %s원",
			g.BasisID, g.Unit, g.TaxBasis, g.Lines, g.Q.FloatString(2), commas(claim.Won(g.C)), commas(claim.Won(g.P)))
		if g.MaxPassingM != nil {
			fmt.Fprintf(out, " → 시중가가 %s원 이하여야 등록된다", commas(claim.Won(g.MaxPassingM)))
		}
		fmt.Fprintln(out)
	}
}

// writeDraft는 fact 값을 글자 그대로 옮긴 본문 초안을 쓴다. 비교 단가·근거는 비워 둔다 —
// 실제로 본 판매 페이지에서만 채운다(근거를 지어내지 않는다). 비어 있으면 submit 게이트가 막는다.
func writeDraft(a *app, ev *contest.Event, it *contest.Item, out io.Writer) error {
	path := filepath.Join(a.cfg.DataDir, "gold", "claims", it.ItemID+".json")
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(out, "  초안이 이미 있어 두지 않았다: %s\n", path)
		return nil
	}
	notes := fmt.Sprintf("items/%s 스냅샷 %s의 fact 값(unit·tax_basis·basis_id)을 그대로 썼다. 비교 단가는 근거의 관측가.", it.ItemID, it.Snapshot.FetchedAt)
	c := claim.Claim{
		DatasetReleaseID:   ev.DatasetReleaseID,
		RulesetVersion:     ev.RulesetVersion,
		CanonicalProductID: it.ItemID,
		Title:              "",
		ReproductionNotes:  &notes,
	}
	for _, f := range it.Facts {
		if !f.Scorable {
			continue
		}
		c.Lines = append(c.Lines, claim.Line{
			FactID: f.FactID, ComparisonBasisID: f.BasisID, Unit: f.Unit, TaxBasis: f.TaxBasis,
			PriceEvidence: []claim.PriceEvidence{},
		})
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := bronze.WriteFileAtomic(path, append(b, '\n')); err != nil {
		return err
	}
	fmt.Fprintf(out, "  초안: %s (줄 %d개 — title·비교 단가·근거를 채운 뒤 make preview ID=%s)\n", path, len(c.Lines), it.ItemID)
	return nil
}
