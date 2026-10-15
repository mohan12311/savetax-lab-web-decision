package claim

import (
	"math/big"
	"strings"
	"testing"

	"savetax-lab/collector/internal/api/contest"
)

func fact(id, basis, q, c string) contest.Fact {
	return contest.Fact{FactID: id, Unit: "대", TaxBasis: "vat_included", BasisID: basis, PeriodKind: "none",
		Quantity: q, ComparableAmountKRW: c, Scorable: true}
}

func yes() *bool { t := true; return &t }
func no() *bool  { f := false; return &f }

func goodClaim() *Claim {
	return &Claim{
		DatasetReleaseID: "rel-1", RulesetVersion: "rules-v1", CanonicalProductID: "24883583",
		Title: "같은 모델의 시중가 대비 1.5배",
		Lines: []Line{{
			FactID: "dl:1", BenchmarkUnitPriceKRW: "100000", ComparisonBasisID: "basis:대:vat_included",
			Unit: "대", TaxBasis: "vat_included",
			PriceEvidence: []PriceEvidence{{
				Kind: "price_page", SourceURL: "https://prod.danawa.com/info/?pcode=1",
				Quote:            "가상전자 TX-100 최저가 100,000원 부가세 포함, 배송비 별도, 다나와, 2026-10-15 확인",
				ObservedPriceKRW: "100000", ObservedAt: "2026-10-15", VATIncluded: yes(), ShippingIncluded: no(),
			}},
		}},
	}
}

func env() Env {
	return Env{DatasetReleaseID: "rel-1", RulesetVersion: "rules-v1", ItemID: "24883583", Today: "2026-10-15",
		Facts: []contest.Fact{fact("dl:1", "basis:대:vat_included", "10", "1500000")}}
}

func rule(t *testing.T) GapRule {
	t.Helper()
	g, err := ParseGapRule("enforce", "1.2", "1000000")
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestParseRejectsUnknownFieldsAndTrailingData(t *testing.T) {
	if _, err := Parse([]byte(`{"title":"x","total_score":"1"}`)); err == nil {
		t.Error("모르는 필드(total_score)를 받았다")
	}
	if _, err := Parse([]byte(`{"title":"x"} {"title":"y"}`)); err == nil {
		t.Error("뒤에 붙은 값을 받았다")
	}
	c, err := Parse([]byte(`{"title":"x","lines":[{"fact_id":"a","period":null,"price_evidence":[]}]}`))
	if err != nil || c.Lines[0].Period != nil {
		t.Fatalf("정상 본문: %v %+v", err, c)
	}
	// period는 null이어도 키가 남아야 한다(필수 필드)
	b, _ := c.Body()
	if !strings.Contains(string(b), `"period":null`) {
		t.Errorf("본문에 period:null이 없다: %s", b)
	}
}

func TestCheckGoodClaimHasNoProblems(t *testing.T) {
	if ps := Check(goodClaim(), env()); len(ps) != 0 {
		t.Fatalf("문제 없어야 한다: %v", ps)
	}
}

func TestCheckCatchesSchemaAndProjectRules(t *testing.T) {
	c := goodClaim()
	c.RulesetVersion = "rules-v0"
	c.Title = " ​ "
	l := &c.Lines[0]
	l.Unit = "EA"                      // fact와 다른 단위
	l.ComparisonBasisID = "basis:EA"   // fact basis_id와 다름
	l.BenchmarkUnitPriceKRW = "90,000" // 쉼표
	e := &l.PriceEvidence[0]
	e.SourceURL = "http://example.com/a"
	e.ObservedAt = "2026-10-16" // 내일
	e.ShippingIncluded = nil
	ps := Check(c, env())
	want := []string{"ruleset_version", "title", "lines[0].unit", "lines[0].comparison_basis_id",
		"lines[0].benchmark_unit_price_krw", "source_url", "observed_at", "shipping_included"}
	got := ""
	for _, p := range ps {
		got += p.String() + "\n"
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s 문제를 못 찾았다:\n%s", w, got)
		}
	}
	if !Blocking(ps) {
		t.Error("막는 문제가 있어야 한다")
	}
}

func TestCheckBenchmarkAgainstObservedPrices(t *testing.T) {
	c := goodClaim()
	c.Lines[0].BenchmarkUnitPriceKRW = "90000" // 근거(100,000)보다 낮게 — 근거에 없는 가격
	ps := Check(c, env())
	if !Blocking(ps) || !strings.Contains(ps[0].Msg, "가장 싼 관측가 100000보다 낮다") {
		t.Errorf("B < 관측가를 막아야 한다: %v", ps)
	}
	c.Lines[0].BenchmarkUnitPriceKRW = "110000" // 근거보다 높게 — 보수적이라 주의만
	ps = Check(c, env())
	if Blocking(ps) || len(ps) != 1 {
		t.Errorf("B > 관측가는 주의만: %v", ps)
	}
}

func TestCheckFactScorableAndPeriod(t *testing.T) {
	e := env()
	e.Facts[0].Scorable = false
	e.Facts[0].ExclusionReasons = []string{"zero_amount"}
	if ps := Check(goodClaim(), e); !strings.Contains(ps[0].String(), "점수 제외") {
		t.Errorf("점수 제외 fact: %v", ps)
	}
	e = env()
	e.Facts[0].PeriodKind = "term"
	if ps := Check(goodClaim(), e); !strings.Contains(ps[0].String(), "기간형") {
		t.Errorf("기간형인데 period 없음: %v", ps)
	}
	c := goodClaim()
	p := "12개월"
	c.Lines[0].Period = &p
	if ps := Check(c, env()); !strings.Contains(ps[0].String(), "null이어야") {
		t.Errorf("기간형 아닌데 period 있음: %v", ps)
	}
}

func TestCheckVATMismatchWarns(t *testing.T) {
	c := goodClaim()
	c.Lines[0].PriceEvidence[0].VATIncluded = no()
	ps := Check(c, env())
	if Blocking(ps) || len(ps) != 1 || !strings.Contains(ps[0].Msg, "부가세") {
		t.Errorf("세금 기준 불일치는 주의: %v", ps)
	}
}

func TestEvaluateAmountsAndGap(t *testing.T) {
	// 공식 문서 예: 납품 80,770원 · 시중 80,000원(1.00배) → 거절, 통과 가능한 최대 시중가 67,308.33원
	facts := []contest.Fact{fact("dl:1", "b1", "550", "44423500")} // 80,770 × 550
	c := goodClaim()
	c.Lines[0].ComparisonBasisID = "b1"
	c.Lines[0].BenchmarkUnitPriceKRW = "80000"
	r, err := Evaluate(c, facts, rule(t))
	if err != nil {
		t.Fatal(err)
	}
	g := r.Groups[0]
	if r.Pass || g.RatioPass || g.UnitPass || Won(g.P) != "80770" || g.MaxPassingM.FloatString(2) != "67308.33" {
		t.Errorf("거절이어야 한다: pass=%v P=%s maxM=%s", r.Pass, Won(g.P), g.MaxPassingM.FloatString(2))
	}
	if r.Total.String() != "423500" { // 44,423,500 − 80,000×550
		t.Errorf("예상액 = %s", r.Total)
	}

	// 경계: P = 1.2 × M 이면 통과(경계 포함)
	facts = []contest.Fact{fact("dl:1", "b1", "10", "1200000")} // P = 120,000
	c.Lines[0].BenchmarkUnitPriceKRW = "100000"                 // M = 100,000 → 정확히 1.2배
	if r, _ = Evaluate(c, facts, rule(t)); !r.Pass || !r.Groups[0].RatioPass {
		t.Errorf("1.2배 경계는 통과: %+v", r.Groups[0])
	}

	// 단가 차이 100만 원 이상이면 비율이 낮아도 통과
	facts = []contest.Fact{fact("dl:1", "b1", "1", "11000000")}
	c.Lines[0].BenchmarkUnitPriceKRW = "10000000"
	if r, _ = Evaluate(c, facts, rule(t)); !r.Pass || r.Groups[0].RatioPass || !r.Groups[0].UnitPass {
		t.Errorf("단가 차이로 통과: %+v", r.Groups[0])
	}
}

func TestEvaluateFloorsAndSkipsZeroLines(t *testing.T) {
	facts := []contest.Fact{fact("dl:1", "b1", "3", "1000"), fact("dl:2", "b1", "1", "100")}
	c := goodClaim()
	c.Lines = []Line{
		{FactID: "dl:1", BenchmarkUnitPriceKRW: "100.5", ComparisonBasisID: "b1"}, // 1000 − 301.5 = 698.5 → 698
		{FactID: "dl:2", BenchmarkUnitPriceKRW: "200", ComparisonBasisID: "b1"},   // 음수 → 0원, 계산에서 빠짐
	}
	r, err := Evaluate(c, facts, rule(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.Lines[0].Amount.String() != "698" || r.Lines[1].Amount.Sign() != 0 || r.Counted != 1 || r.Groups[0].Lines != 1 {
		t.Errorf("예상액 내림·0원 줄 제외: %+v counted=%d", r.Lines, r.Counted)
	}
	c.Lines = c.Lines[1:]
	if r, _ = Evaluate(c, facts, rule(t)); r.Pass || r.Counted != 0 {
		t.Error("모든 줄이 0원이면 통과가 아니다(NO_POSITIVE_AMOUNT)")
	}
}

func TestEvaluateMultiUnitOverallRatio(t *testing.T) {
	// 단위 b1은 1.5배로 통과, b2는 1.1배로 미달. 전체 금액 비율 (1500+1100)/(1000+1000)=1.3 ≥ 1.2 → 통과
	facts := []contest.Fact{fact("dl:1", "b1", "1", "1500"), fact("dl:2", "b2", "1", "1100")}
	c := goodClaim()
	c.Lines = []Line{
		{FactID: "dl:1", BenchmarkUnitPriceKRW: "1000", ComparisonBasisID: "b1"},
		{FactID: "dl:2", BenchmarkUnitPriceKRW: "1000", ComparisonBasisID: "b2"},
	}
	r, err := Evaluate(c, facts, rule(t))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Pass || !r.ByOverall || r.Groups[1].Pass {
		t.Errorf("전체 금액 비율로 통과: pass=%v overall=%v", r.Pass, r.ByOverall)
	}
}

func TestFactGroupsAndRuleOff(t *testing.T) {
	facts := []contest.Fact{fact("dl:1", "b1", "2", "300000"), fact("dl:2", "b1", "1", "150000"), fact("dl:3", "b2", "1", "5")}
	facts[2].Scorable = false
	gs, err := FactGroups(facts, rule(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || Won(gs[0].P) != "150000" || Won(gs[0].MaxPassingM) != "125000" || gs[0].Lines != 2 {
		t.Errorf("groups = %+v", gs)
	}
	off, _ := ParseGapRule("off", "", "")
	if off.Enforce || off.MaxPassing(big.NewRat(1, 1)) != nil {
		t.Error("꺼진 규칙")
	}
}
