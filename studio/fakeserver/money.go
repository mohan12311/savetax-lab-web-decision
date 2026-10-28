package main

import (
	"math/big"
	"regexp"
	"sort"
)

// 금액 계산은 대회 산식과 같다: 줄 예상액 = floor(max(C − B×Q, 0)), 가격 차이 기준은 예상액 1원 이상인 줄만
// 비교 기준(basis)별로 묶어 P(ΣC÷ΣQ) ≥ 1.2×M(ΣBQ÷ΣQ) 또는 P − M ≥ 100만 원. 기준이 여럿이면 전체 금액 1.2배로도 통과.
// 1부 collector/internal/claim/gap.go의 Evaluate와 같은 규칙이다(서버 쪽 재구현, float 금지).

var decRe = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,6})?$`)

func parseDec(s string) (*big.Rat, bool) {
	if !decRe.MatchString(s) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

type gapRule struct {
	MinRatio *big.Rat
	MinUnit  *big.Rat
}

var defaultGap = gapRule{MinRatio: big.NewRat(6, 5), MinUnit: big.NewRat(1_000_000, 1)}

type lineAmount struct {
	FactID string
	Amount *big.Int
}

type gapGroup struct {
	BasisID string
	Q, C    *big.Rat
	BQ      *big.Rat
	P, M    *big.Rat
	Pass    bool
}

type evaluation struct {
	Lines     []lineAmount
	Total     *big.Int
	Groups    []gapGroup
	Counted   int
	Pass      bool
	ByOverall bool
}

// evalLine은 검증을 마친 줄이다(fact와 짝지은 비교 단가).
type evalLine struct {
	Fact      Fact
	BasisID   string
	Period    *string
	Benchmark *big.Rat
}

func evaluate(lines []evalLine, rule gapRule) evaluation {
	res := evaluation{Total: new(big.Int)}
	idx := map[string]*gapGroup{}
	var keys []string
	for _, l := range lines {
		q, _ := parseDec(l.Fact.Q())
		c, _ := parseDec(l.Fact.ComparableAmountKRW)
		bq := new(big.Rat).Mul(l.Benchmark, q)
		diff := new(big.Rat).Sub(c, bq)
		amt := new(big.Int)
		if diff.Sign() > 0 {
			amt.Quo(diff.Num(), diff.Denom())
		}
		res.Lines = append(res.Lines, lineAmount{FactID: l.Fact.FactID, Amount: amt})
		res.Total.Add(res.Total, amt)
		if amt.Sign() <= 0 {
			continue
		}
		res.Counted++
		key := l.BasisID
		if l.Period != nil {
			key += "\x00" + *l.Period
		}
		g := idx[key]
		if g == nil {
			g = &gapGroup{BasisID: l.BasisID, Q: new(big.Rat), C: new(big.Rat), BQ: new(big.Rat)}
			idx[key] = g
			keys = append(keys, key)
		}
		g.Q.Add(g.Q, q)
		g.C.Add(g.C, c)
		g.BQ.Add(g.BQ, bq)
	}
	sort.Strings(keys)
	allPass := true
	sumC, sumBQ := new(big.Rat), new(big.Rat)
	for _, k := range keys {
		g := idx[k]
		g.P = new(big.Rat).Quo(g.C, g.Q)
		g.M = new(big.Rat).Quo(g.BQ, g.Q)
		ratioPass := g.P.Cmp(new(big.Rat).Mul(rule.MinRatio, g.M)) >= 0
		unitPass := new(big.Rat).Sub(g.P, g.M).Cmp(rule.MinUnit) >= 0
		g.Pass = ratioPass || unitPass
		allPass = allPass && g.Pass
		sumC.Add(sumC, g.C)
		sumBQ.Add(sumBQ, g.BQ)
		res.Groups = append(res.Groups, *g)
	}
	switch {
	case res.Counted == 0:
		res.Pass = false
	case allPass:
		res.Pass = true
	case len(res.Groups) > 1 && sumC.Cmp(new(big.Rat).Mul(rule.MinRatio, sumBQ)) >= 0:
		res.Pass, res.ByOverall = true, true
	}
	return res
}

// ratioString은 P÷M을 소수 셋째 자리까지 내림한 문자열이다.
func ratioString(p, m *big.Rat) string {
	if m.Sign() == 0 {
		return ""
	}
	r := new(big.Rat).Quo(p, m)
	r.Mul(r, big.NewRat(1000, 1))
	q := new(big.Int).Quo(r.Num(), r.Denom())
	return new(big.Rat).SetFrac(q, big.NewInt(1000)).FloatString(3)
}
