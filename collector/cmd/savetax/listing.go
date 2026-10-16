package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/store/bronze"
)

var reListingID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

// 우수조달물품 지정 문구: "우수조달물품(지정번호: 2021114, 제품명: 보강체결구조 책상)"
var reExcellent = regexp.MustCompile(`우수조달물품\(지정번호:\s*([0-9]+),\s*제품명:\s*([^)]+)\)`)

// runListing은 데이터 API 등재 상세(GET /v1/listings/{id})를 받아 bronze/data/listings에 원문을 남기고,
// 동급 비교에 쓰는 속성(synonyms·납품 조건·우수조달 지정 기술)을 한 줄씩 보여 준다. 예산(3,600/h)을 쓴다.
func runListing(ctx context.Context, a *app, args []string) error {
	fs := flag.NewFlagSet("listing", flag.ContinueOnError)
	var ids stringsFlag
	fs.Var(&ids, "id", "등재번호(listing_id). 여러 번 줄 수 있다")
	from := fs.String("from", "", "등재번호 목록 파일(한 줄에 하나)")
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
	if len(ids) == 0 {
		return usageError{"사용법: savetax listing --id <등재번호> [--id …] | --from <파일>"}
	}
	for _, id := range ids {
		if !reListingID.MatchString(id) {
			return usageError{fmt.Sprintf("등재번호 형식이 아니다: %q", id)}
		}
	}
	dc, err := a.data()
	if err != nil {
		return err
	}
	if !dc.HTTP().HasToken() {
		return httpx.ErrNoToken
	}
	out := os.Stdout
	run := bronze.NewRun(a.bronzeRoot(), "listing", args)
	run.TrackStats(dc.HTTP().Service(), dc.HTTP().Stats())
	w, err := run.Writer("data", "listings")
	for i := 0; err == nil && i < len(ids); i++ {
		id := ids[i]
		env, resp, gerr := dc.Get(ctx, "/v1/listings/"+url.PathEscape(id), nil)
		if ae, ok := httpx.AsAPIError(gerr); ok && ae.Status == 404 {
			fmt.Fprintf(out, "%s 없음(404) — 가격 정상이라는 뜻이 아니다\n", id)
			continue
		}
		if gerr != nil {
			err = gerr
			break
		}
		if err = w.Write(bronze.Envelope{FetchedAt: time.Now().UTC(), Source: "data_api", Endpoint: "/v1/listings/{id}",
			Params: map[string]any{"id": id}, Status: resp.Status, Body: json.RawMessage(resp.Body)}); err != nil {
			break
		}
		printListing(out, env.Data)
	}
	mpath, ferr := run.Finish(err)
	if mpath != "" {
		fmt.Fprintf(out, "run manifest: %s\n", mpath)
	}
	return errors.Join(err, ferr)
}

func printListing(out *os.File, data json.RawMessage) {
	var l struct {
		ID            string `json:"id"`
		ItemID        string `json:"itemId"`
		Name          string `json:"name"`
		Price         string `json:"price"`
		Unit          string `json:"unit"`
		Synonyms      string `json:"synonyms"`
		DeliveryTerms string `json:"deliveryTerms"`
		ContractTerms struct {
			SpecialTerms string `json:"specialTerms"`
		} `json:"contractTerms"`
	}
	if json.Unmarshal(data, &l) != nil {
		fmt.Fprintln(out, "  (해석 실패 — 원문은 bronze에 있다)")
		return
	}
	fmt.Fprintf(out, "%s 물품 %s %s원/%s · %s · 납품 %s\n", l.ID, l.ItemID, commas(l.Price), l.Unit, l.Name, orNone(l.DeliveryTerms))
	fmt.Fprintf(out, "  속성 %s\n", strings.ReplaceAll(l.Synonyms, "||", " | "))
	if m := reExcellent.FindStringSubmatch(l.ContractTerms.SpecialTerms); m != nil {
		fmt.Fprintf(out, "  우수조달물품 지정 %s: %s\n", m[1], m[2])
	}
}
