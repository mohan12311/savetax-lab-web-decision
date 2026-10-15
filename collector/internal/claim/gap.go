package claim

import (
	"fmt"
	"math/big"
	"sort"

	"savetax-lab/collector/internal/api/contest"
)

// GapRule은 이벤트의 price_gap이다. 켜져 있으면 납품 단가 P와 시중가 M이
// P ≥ MinRatio×M 또는 P − M ≥ MinUnit(경계 포함)이어야 등록된다(api.md 5절).
type GapRule struct {
	Enforce  bool
	MinRatio *big.Rat
	MinUnit  *big.Rat
}

// ParseGapRule은 이벤트 응답의 문자열을 읽는다. 꺼져 있으면 비율·단가는 무시한다.
func ParseGapRule(rule, minRatio, minUnit string) (GapRule, error) {
	if rule != "enforce" {
		return GapRule{}, nil
	}
	r, ok1 := posDecimal(minRatio)
	u, ok2 := nonNegDecimal(minUnit)
	if !ok1 || !ok2 {
		return GapRule{}, fmt.Errorf("price_gap 값을 읽지 못했다: min_ratio=%q min_unit_krw=%q", minRatio, minUnit)
	}
	return GapRule{Enforce: true, MinRatio: r, MinUnit: u}, nil
}

// MaxPassing은 납품 단가 p에서 기준을 통과하는 가장 높은 시중가다: max(p ÷ 비율, p − 단가 차이).
func (g GapRule) MaxPassing(p *big.Rat) *big.Rat {
	if !g.Enforce {
		return nil
	}
	a := new(big.Rat).Quo(p, g.MinRatio)
	if b := new(big.Rat).Sub(p, g.MinUnit); b.Cmp(a) > 0 {
		return b
	}
	return a
}

// Group은 같은 비교 기준(basis_id, 기간형이면 기간까지)으로 묶은 줄들이다.
type Group struct {
	BasisID     string
	Unit        string
	TaxBasis    string
	Lines       int
	Q, C        *big.Rat
	BQ          *big.Rat // 비교 단가 × 수량 합(본문 평가에서만)
	P           *big.Rat // 납품 단가 ΣC ÷ ΣQ
	M           *big.Rat // 시중가 ΣBQ ÷ ΣQ(본문 평가에서만)
	RatioPass   bool
	UnitPass    bool
	Pass        bool
	MaxPassingM *big.Rat
}

// FactGroups는 점수에 쓸 수 있는 fact를 비교 기준별로 모아 납품 단가와 통과 가능한 최대 시중가를 낸다.
func FactGroups(facts []contest.Fact, rule GapRule) ([]Group, error) {
	idx := map[string]*Group{}
	var keys []string
	for _, f := range facts {
		if !f.Scorable {
			continue
		}
		q, ok1 := nonNegDecimal(f.Q())
		c, ok2 := nonNegDecimal(f.ComparableAmountKRW)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("fact %s의 수량·금액을 읽지 못했다: %q %q", f.FactID, f.Q(), f.ComparableAmountKRW)
		}
		key := f.BasisID + "\x00" + f.PeriodKind
		g := idx[key]
		if g == nil {
			g = &Group{BasisID: f.BasisID, Unit: f.Unit, TaxBasis: f.TaxBasis, Q: new(big.Rat), C: new(big.Rat)}
			idx[key] = g
			keys = append(keys, key)
		}
		g.Lines++
		g.Q.Add(g.Q, q)
		g.C.Add(g.C, c)
	}
	sort.Strings(keys)
	out := make([]Group, 0, len(keys))
	for _, k := range keys {
		g := idx[k]
		if g.Q.Sign() > 0 {
			g.P = new(big.Rat).Quo(g.C, g.Q)
			g.MaxPassingM = rule.MaxPassing(g.P)
		}
		out = append(out, *g)
	}
	return out, nil
}

// LineAmount는 줄 하나의 예상액 floor(max(C − B×Q, 0))다.
type LineAmount struct {
	FactID string
	Amount *big.Int
}

// Result는 본문을 서버 산식으로 미리 계산한 값이다. 최종 판단은 서버(preview)가 한다.
type Result struct {
	Lines     []LineAmount
	Total     *big.Int
	Groups    []Group
	Counted   int  // 예상액 1원 이상인 줄(가격 차이 계산에 넣는 줄)
	Pass      bool // 가격 차이 기준 통과(꺼져 있으면 true)
	ByOverall bool // 단위가 여럿이라 전체 금액 비율로 통과
}

// Evaluate는 본문의 줄마다 예상액을 내고 가격 차이 기준을 미리 잰다. Check를 통과한 본문을 가정한다.
func Evaluate(c *Claim, facts []contest.Fact, rule GapRule) (Result, error) {
	byID := map[string]contest.Fact{}
	for _, f := range facts {
		byID[f.FactID] = f
	}
	res := Result{Total: new(big.Int)}
	idx := map[string]*Group{}
	var keys []string
	for _, l := range c.Lines {
		f, ok := byID[l.FactID]
		if !ok {
			return res, fmt.Errorf("fact가 없다: %s", l.FactID)
		}
		q, ok1 := nonNegDecimal(f.Q())
		cc, ok2 := nonNegDecimal(f.ComparableAmountKRW)
		b, ok3 := posDecimal(l.BenchmarkUnitPriceKRW)
		if !ok1 || !ok2 || !ok3 {
			return res, fmt.Errorf("줄 %s의 금액·수량·비교 단가를 읽지 못했다", l.FactID)
		}
		bq := new(big.Rat).Mul(b, q)
		diff := new(big.Rat).Sub(cc, bq)
		amt := new(big.Int)
		if diff.Sign() > 0 {
			amt.Quo(diff.Num(), diff.Denom()) // 양수라 몫이 곧 내림
		}
		res.Lines = append(res.Lines, LineAmount{FactID: l.FactID, Amount: amt})
		res.Total.Add(res.Total, amt)
		if amt.Sign() <= 0 {
			continue // 예상액 0원인 줄은 가격 차이 계산에서 빠진다
		}
		res.Counted++
		key := l.ComparisonBasisID
		if l.Period != nil {
			key += "\x00" + *l.Period
		}
		g := idx[key]
		if g == nil {
			g = &Group{BasisID: l.ComparisonBasisID, Unit: f.Unit, TaxBasis: f.TaxBasis, Q: new(big.Rat), C: new(big.Rat), BQ: new(big.Rat)}
			idx[key] = g
			keys = append(keys, key)
		}
		g.Lines++
		g.Q.Add(g.Q, q)
		g.C.Add(g.C, cc)
		g.BQ.Add(g.BQ, bq)
	}
	sort.Strings(keys)
	allPass := true
	sumC, sumBQ := new(big.Rat), new(big.Rat)
	for _, k := range keys {
		g := idx[k]
		if g.Q.Sign() <= 0 {
			return res, fmt.Errorf("비교 기준 %s의 수량 합이 0이다", g.BasisID)
		}
		g.P = new(big.Rat).Quo(g.C, g.Q)
		g.M = new(big.Rat).Quo(g.BQ, g.Q)
		g.MaxPassingM = rule.MaxPassing(g.P)
		if rule.Enforce {
			g.RatioPass = g.P.Cmp(new(big.Rat).Mul(rule.MinRatio, g.M)) >= 0
			g.UnitPass = new(big.Rat).Sub(g.P, g.M).Cmp(rule.MinUnit) >= 0
			g.Pass = g.RatioPass || g.UnitPass
		} else {
			g.Pass = true
		}
		allPass = allPass && g.Pass
		sumC.Add(sumC, g.C)
		sumBQ.Add(sumBQ, g.BQ)
		res.Groups = append(res.Groups, *g)
	}
	switch {
	case res.Counted == 0:
		res.Pass = false // 422 NO_POSITIVE_AMOUNT
	case !rule.Enforce || allPass:
		res.Pass = true
	case len(res.Groups) > 1 && sumC.Cmp(new(big.Rat).Mul(rule.MinRatio, sumBQ)) >= 0:
		// 단위가 여럿이면 전체 금액이 min_ratio배 이상이어도 통과한다
		res.Pass, res.ByOverall = true, true
	}
	return res, nil
}

// Won은 원 단위 표시용 문자열이다(내림, 정수).
func Won(r *big.Rat) string {
	if r == nil {
		return "-"
	}
	q := new(big.Int).Quo(r.Num(), r.Denom())
	if r.Sign() < 0 && new(big.Int).Mul(q, r.Denom()).Cmp(r.Num()) != 0 {
		q.Sub(q, big.NewInt(1))
	}
	return q.String()
}
