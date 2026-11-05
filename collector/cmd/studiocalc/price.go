package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"

	"savetax-lab/collector/internal/api/contest"
	"savetax-lab/collector/internal/claim"
)

// 가격 차이 기준(CLAUDE.md 「2부에도 유효한 함정」, 가짜 서버 이벤트 상수와 같다): P ≥ 1.2×M 또는 P − M ≥ 100만 원.
const (
	gapMinRatio = "1.2"
	gapMinUnit  = "1000000"
)

var (
	intRe  = regexp.MustCompile(`^[0-9]+$`)
	fracRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(/[0-9]+(\.[0-9]+)?)?$`)
)

// parseRatio는 "20", "1/25", "0.04" 꼴의 양수를 읽는다.
func parseRatio(s string) (*big.Rat, error) {
	if !fracRe.MatchString(s) {
		return nil, fmt.Errorf("환산 값 %q를 읽지 못했다(예: 1, 20, 1/25)", s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() <= 0 {
		return nil, fmt.Errorf("환산 값 %q는 양수여야 한다", s)
	}
	return r, nil
}

// opBenchmark: 비교 단가 = 판매가 ÷ (판매 단위 1개가 조달 단위 몇 개인가) × (부가세 별도면 11/10), 마지막에 한 번 올림.
// 배송비·운반비는 더하지 않는다(대회 「자료와 방법」 2절). 부가세 표기가 없으면(not_stated) 호출하는 쪽이 두 가정을 따로 부른다.
func opBenchmark(price, vat, unitsPerSale string) (map[string]any, error) {
	if !intRe.MatchString(price) {
		return nil, fmt.Errorf("판매가 %q는 정수 원이어야 한다", price)
	}
	p, _ := new(big.Rat).SetString(price)
	if p.Sign() <= 0 {
		return nil, errors.New("판매가는 양수여야 한다")
	}
	per, err := parseRatio(unitsPerSale)
	if err != nil {
		return nil, err
	}
	v := new(big.Rat).Quo(p, per)
	switch vat {
	case "included":
	case "excluded":
		v.Mul(v, big.NewRat(11, 10))
	default:
		return nil, fmt.Errorf("vat는 included 또는 excluded다: %q", vat)
	}
	b := ratCeil(v)
	exact, _ := show(v)
	return map[string]any{"benchmark_krw": b.Num().String(), "before_ceil": exact}, nil
}

// opGap은 items.json의 facts와 비교 단가로 1부 claim.Evaluate를 그대로 돌린다.
func opGap(rawFacts json.RawMessage, benchmark string) (map[string]any, error) {
	if !intRe.MatchString(benchmark) {
		return nil, fmt.Errorf("비교 단가 %q는 정수 원이어야 한다", benchmark)
	}
	var facts []contest.Fact
	if err := json.Unmarshal(rawFacts, &facts); err != nil {
		return nil, fmt.Errorf("facts를 읽지 못했다: %w", err)
	}
	if len(facts) == 0 {
		return nil, errors.New("facts가 없다")
	}
	rule, err := claim.ParseGapRule("enforce", gapMinRatio, gapMinUnit)
	if err != nil {
		return nil, err
	}
	c := &claim.Claim{}
	for _, f := range facts {
		if !f.Scorable {
			continue
		}
		c.Lines = append(c.Lines, claim.Line{FactID: f.FactID, BenchmarkUnitPriceKRW: benchmark, ComparisonBasisID: f.ComparisonBasisID,
			Unit: f.Unit, TaxBasis: f.TaxBasis})
	}
	if len(c.Lines) == 0 {
		return nil, errors.New("점수에 쓸 수 있는 fact가 없다")
	}
	res, err := claim.Evaluate(c, facts, rule)
	if err != nil {
		return nil, err
	}
	groups, err := claim.FactGroups(facts, rule)
	if err != nil {
		return nil, err
	}
	if len(groups) != 1 {
		return nil, fmt.Errorf("비교 기준이 %d개다(지금은 한 기준만 다룬다)", len(groups))
	}
	pRat := groups[0].P
	m, _ := new(big.Rat).SetString(benchmark)
	if m.Sign() <= 0 {
		return nil, errors.New("비교 단가는 양수여야 한다")
	}
	// 배율은 소수 첫째 자리까지 내림(제목은 부풀리지 않는다)
	tenths := ratFloor(new(big.Rat).Quo(new(big.Rat).Mul(pRat, big.NewRat(10, 1)), m)).Num()
	ratio := fmt.Sprintf("%s.%s", new(big.Int).Quo(tenths, big.NewInt(10)), new(big.Int).Rem(tenths, big.NewInt(10)))
	ratioPass := pRat.Cmp(new(big.Rat).Mul(rule.MinRatio, m)) >= 0
	unitPass := new(big.Rat).Sub(pRat, m).Cmp(rule.MinUnit) >= 0
	return map[string]any{
		"gap_pass":     res.Pass,
		"ratio_pass":   ratioPass,
		"unit_pass":    unitPass,
		"p_krw":        claim.Won(pRat),
		"ratio":        ratio,
		"amount_krw":   res.Total.String(),
		"counted_rows": res.Counted,
	}, nil
}
