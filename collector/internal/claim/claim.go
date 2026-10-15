// Package claim은 대회 점수 등록 본문(submission-schema v2)과 제출 전 게이트 검사다.
// 네트워크를 쓰지 않는다 — 대회 값(이벤트 상수·물품 fact)은 호출자가 넘긴다.
// 금액은 십진 문자열을 big.Rat으로만 다룬다(float 금지).
package claim

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"savetax-lab/collector/internal/api/contest"
)

// Claim은 preview·제출·수정이 받는 본문이다. 서버는 모르는 필드를 거절하므로 여기 없는 필드는 보내지 않는다.
type Claim struct {
	DatasetReleaseID   string  `json:"dataset_release_id"`
	RulesetVersion     string  `json:"ruleset_version"`
	CanonicalProductID string  `json:"canonical_product_id"`
	Title              string  `json:"title"`
	Claim              *string `json:"claim,omitempty"`
	ComparisonScope    *string `json:"comparison_scope,omitempty"`
	ReproductionNotes  *string `json:"reproduction_notes,omitempty"`
	Lines              []Line  `json:"lines"`
}

type Line struct {
	FactID                string          `json:"fact_id"`
	BenchmarkUnitPriceKRW string          `json:"benchmark_unit_price_krw"`
	ComparisonBasisID     string          `json:"comparison_basis_id"`
	Unit                  string          `json:"unit"`
	TaxBasis              string          `json:"tax_basis"`
	Period                *string         `json:"period"` // 기간형이 아니면 null(키는 있어야 한다)
	PriceEvidence         []PriceEvidence `json:"price_evidence"`
	FulfillmentEvidence   []Fulfillment   `json:"fulfillment_evidence,omitempty"`
}

type PriceEvidence struct {
	Kind             string  `json:"kind"`
	SourceURL        string  `json:"source_url"`
	Quote            string  `json:"quote"`
	ObservedPriceKRW string  `json:"observed_price_krw"`
	ObservedAt       string  `json:"observed_at"`
	VATIncluded      *bool   `json:"vat_included"`
	ShippingIncluded *bool   `json:"shipping_included"`
	Note             *string `json:"note,omitempty"`
}

type Fulfillment struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Parse는 본문 파일을 읽는다. 모르는 필드·뒤에 붙은 값은 거절한다(서버로 새는 일을 막는다).
func Parse(data []byte) (*Claim, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Claim
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("본문 해석: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("본문 해석: JSON 값 뒤에 다른 내용이 있다")
	}
	return &c, nil
}

// Body는 서버에 보낼 바이트다. 같은 실행 안에서는 같은 바이트를 쓴다(멱등 재전송은 같은 본문이어야 한다).
func (c *Claim) Body() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// Env는 검사에 쓰는 대회 값이다.
type Env struct {
	DatasetReleaseID string
	RulesetVersion   string
	ItemID           string
	Today            string // KST YYYY-MM-DD
	Facts            []contest.Fact
}

// Problem은 게이트 검사 결과 하나다. Warn(주의)·OK(확인 기록)는 진행을 막지 않는다.
type Problem struct {
	Path string
	Msg  string
	Warn bool
	OK   bool
}

func (p Problem) String() string {
	tag := "막음"
	switch {
	case p.OK:
		tag = "확인"
	case p.Warn:
		tag = "주의"
	}
	return fmt.Sprintf("[%s] %s: %s", tag, p.Path, p.Msg)
}

// Blocking은 진행을 막는 문제가 있는지다.
func Blocking(ps []Problem) bool {
	for _, p := range ps {
		if !p.Warn && !p.OK {
			return true
		}
	}
	return false
}

var (
	decRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,6})?$`) // 우리가 보내는 비교 단가(소수 6자리까지)
	factRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)     // 서버가 준 금액·수량
	intRe  = regexp.MustCompile(`^[0-9]+$`)
	dateRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

const minObservedAt = "2015-01-01"

// Check는 서버 스키마(submission-schema v2)와 프로젝트 규칙으로 본문을 검사한다.
// 근거가 실제로 본 페이지인지(원문 보관)는 bronze를 읽는 호출자가 따로 확인한다.
func Check(c *Claim, env Env) []Problem {
	var ps []Problem
	add := func(path, format string, a ...any) {
		ps = append(ps, Problem{Path: path, Msg: fmt.Sprintf(format, a...)})
	}
	warn := func(path, format string, a ...any) {
		ps = append(ps, Problem{Path: path, Msg: fmt.Sprintf(format, a...), Warn: true})
	}

	if c.DatasetReleaseID != env.DatasetReleaseID || c.RulesetVersion != env.RulesetVersion {
		add("dataset_release_id/ruleset_version", "대회 현재 값(%s / %s)과 다르다(%s / %s)",
			env.DatasetReleaseID, env.RulesetVersion, c.DatasetReleaseID, c.RulesetVersion)
	}
	if c.CanonicalProductID != env.ItemID {
		add("canonical_product_id", "물품번호(item_id) %q가 아니다: %q", env.ItemID, c.CanonicalProductID)
	}
	checkText(&ps, "title", c.Title, 1, 200)
	if c.Claim != nil {
		checkText(&ps, "claim", *c.Claim, 0, 2000)
	}
	if c.ComparisonScope != nil {
		checkText(&ps, "comparison_scope", *c.ComparisonScope, 0, 1000)
	}
	if c.ReproductionNotes != nil {
		checkText(&ps, "reproduction_notes", *c.ReproductionNotes, 0, 4000)
	}

	facts := map[string]contest.Fact{}
	for _, f := range env.Facts {
		facts[f.FactID] = f
	}
	if len(c.Lines) == 0 {
		add("lines", "1개 이상이어야 한다")
	}
	seen := map[string]bool{}
	for i, l := range c.Lines {
		p := fmt.Sprintf("lines[%d]", i)
		if seen[l.FactID] {
			add(p+".fact_id", "같은 fact가 두 번 나온다: %s", l.FactID)
		}
		seen[l.FactID] = true
		f, ok := facts[l.FactID]
		switch {
		case !ok:
			add(p+".fact_id", "이 물품의 fact가 아니다: %q", l.FactID)
		case !f.Scorable:
			add(p+".fact_id", "점수 제외 fact다(%s)", strings.Join(f.ExclusionReasons, ", "))
		}
		if ok {
			// unit·tax_basis·basis_id는 글자까지 같아야 한다(422 COMPARISON_BASIS_MISMATCH)
			if l.Unit != f.Unit {
				add(p+".unit", "fact 값 %q와 다르다: %q", f.Unit, l.Unit)
			}
			if l.TaxBasis != f.TaxBasis {
				add(p+".tax_basis", "fact 값 %q와 다르다: %q", f.TaxBasis, l.TaxBasis)
			}
			if l.ComparisonBasisID != f.BasisID {
				add(p+".comparison_basis_id", "fact의 basis_id %q와 다르다: %q", f.BasisID, l.ComparisonBasisID)
			}
			if f.ComparisonBasisID != "" && f.ComparisonBasisID != f.BasisID {
				warn(p+".comparison_basis_id", "fact의 basis_id(%q)와 comparison_basis_id(%q)가 다르다 — 문서대로 basis_id를 썼다", f.BasisID, f.ComparisonBasisID)
			}
			if f.PeriodKind == "term" {
				if l.Period == nil || strings.TrimSpace(*l.Period) == "" {
					add(p+".period", "기간형 fact라 \"12개월\" 같은 기간이 필요하다")
				}
			} else if l.Period != nil {
				add(p+".period", "기간형이 아니라 null이어야 한다")
			}
		}
		b, bOK := posDecimal(l.BenchmarkUnitPriceKRW)
		if !bOK {
			add(p+".benchmark_unit_price_krw", "0보다 큰 십진 문자열(소수 6자리까지, 쉼표 없음)이어야 한다: %q", l.BenchmarkUnitPriceKRW)
		}
		ps = append(ps, checkEvidence(p, l, f, ok, b, bOK, env.Today)...)
		if len(l.FulfillmentEvidence) > 8 {
			add(p+".fulfillment_evidence", "8개 이하여야 한다: %d개", len(l.FulfillmentEvidence))
		}
		for j, fe := range l.FulfillmentEvidence {
			q := fmt.Sprintf("%s.fulfillment_evidence[%d]", p, j)
			if fe.Kind != "text" {
				add(q+".kind", "\"text\"여야 한다: %q", fe.Kind)
			}
			checkText(&ps, q+".text", fe.Text, 1, 2000)
		}
	}
	return ps
}

func checkEvidence(p string, l Line, f contest.Fact, factOK bool, b *big.Rat, bOK bool, today string) []Problem {
	var ps []Problem
	add := func(path, format string, a ...any) {
		ps = append(ps, Problem{Path: path, Msg: fmt.Sprintf(format, a...)})
	}
	warn := func(path, format string, a ...any) {
		ps = append(ps, Problem{Path: path, Msg: fmt.Sprintf(format, a...), Warn: true})
	}
	n := len(l.PriceEvidence)
	if n < 1 || n > 8 {
		add(p+".price_evidence", "1~8개여야 한다: %d개", n)
	}
	var minObs *big.Rat
	for j, e := range l.PriceEvidence {
		q := fmt.Sprintf("%s.price_evidence[%d]", p, j)
		if e.Kind != "price_page" {
			add(q+".kind", "\"price_page\"여야 한다: %q", e.Kind)
		}
		if u, err := url.Parse(e.SourceURL); err != nil || u.Scheme != "https" || u.Host == "" || len(e.SourceURL) > 2048 {
			add(q+".source_url", "https 주소(2048자 이하)여야 한다: %q", e.SourceURL)
		}
		checkText(&ps, q+".quote", e.Quote, 1, 2000)
		if !intRe.MatchString(e.ObservedPriceKRW) || strings.TrimLeft(e.ObservedPriceKRW, "0") == "" {
			add(q+".observed_price_krw", "0보다 큰 정수 문자열이어야 한다: %q", e.ObservedPriceKRW)
		} else {
			o, _ := new(big.Rat).SetString(e.ObservedPriceKRW)
			if minObs == nil || o.Cmp(minObs) < 0 {
				minObs = o
			}
		}
		switch {
		case !dateRe.MatchString(e.ObservedAt):
			add(q+".observed_at", "YYYY-MM-DD여야 한다: %q", e.ObservedAt)
		case e.ObservedAt < minObservedAt || e.ObservedAt > today:
			add(q+".observed_at", "%s~%s(KST 오늘) 사이여야 한다: %s", minObservedAt, today, e.ObservedAt)
		}
		if e.VATIncluded == nil {
			add(q+".vat_included", "필수다(true/false)")
		}
		if e.ShippingIncluded == nil {
			add(q+".shipping_included", "필수다(true/false)")
		}
		if e.Note != nil {
			checkText(&ps, q+".note", *e.Note, 0, 500)
		}
		if factOK && e.VATIncluded != nil {
			tb := strings.ToLower(f.TaxBasis)
			if strings.Contains(tb, "vat_included") && !*e.VATIncluded || strings.Contains(tb, "vat_excluded") && *e.VATIncluded {
				warn(q+".vat_included", "fact 세금 기준 %q와 관측가의 부가세 포함 여부가 다르다 — 같은 기준으로 비교해야 한다", f.TaxBasis)
			}
		}
	}
	// lint는 근거의 가장 싼 관측가로 시중가를 다시 잰다. B를 그보다 낮게 적으면 근거에 없는 가격이다.
	if bOK && minObs != nil {
		switch b.Cmp(minObs) {
		case -1:
			add(p+".benchmark_unit_price_krw", "근거의 가장 싼 관측가 %s보다 낮다 — 근거에서 확인한 값만 쓴다", minObs.FloatString(0))
		case 1:
			warn(p+".benchmark_unit_price_krw", "근거의 가장 싼 관측가 %s보다 높다(보수적이지만 lint는 관측가로 다시 잰다)", minObs.FloatString(0))
		}
	}
	return ps
}

// checkText는 길이(한글·이모지도 1자)와 공백·보이지 않는 문자뿐인지 본다. min이 0이면 빈 값을 허용한다.
func checkText(ps *[]Problem, path, s string, min, max int) {
	n := utf8.RuneCountInString(s)
	if n > max {
		*ps = append(*ps, Problem{Path: path, Msg: fmt.Sprintf("%d자 이하여야 한다: %d자", max, n)})
	}
	if min > 0 && strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) }) == "" {
		*ps = append(*ps, Problem{Path: path, Msg: "비어 있거나 공백·보이지 않는 문자뿐이다"})
	}
}

// posDecimal은 쉼표 없는 0보다 큰 십진 문자열(소수 6자리까지)을 읽는다.
func posDecimal(s string) (*big.Rat, bool) {
	if !decRe.MatchString(s) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() <= 0 {
		return nil, false
	}
	return r, true
}

// nonNegDecimal은 서버가 준 십진 문자열(0 허용)을 읽는다.
func nonNegDecimal(s string) (*big.Rat, bool) {
	if !factRe.MatchString(s) {
		return nil, false
	}
	return new(big.Rat).SetString(s)
}
