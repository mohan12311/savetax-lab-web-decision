package main

import (
	"encoding/json"
	"testing"
)

func call(t *testing.T, req string) map[string]any {
	t.Helper()
	out := handle([]byte(req))
	b, _ := json.Marshal(out)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEval(t *testing.T) {
	cases := []struct{ expr, want string }{
		{"400 × 25 × 1.1", "11000"},
		{"ceil(400 × 25 × 1.1)", "11000"},
		{"198000 ÷ 20 × 1.1", "10890"},
		{"9,900 * 1.1", "10890"},
		{"9900x1.1", "10890"},
		{"ceil(10 / 3)", "4"},
		{"floor(10 / 3)", "3"},
		{"10 / 4", "2.5"},
		{"-(3 - 5) * 2", "4"},
		{"1,000,000 + 1", "1000001"},
	}
	for _, c := range cases {
		m := call(t, `{"op":"eval","expr":`+jsonStr(c.expr)+`}`)
		if m["ok"] != true || m["value"] != c.want {
			t.Errorf("%s = %v, want %s", c.expr, m, c.want)
		}
	}
	m := call(t, `{"op":"eval","expr":"10 / 3"}`)
	if m["value"] != "3.333333…" || m["exact"] != false || m["fraction"] != "10/3" {
		t.Errorf("10/3 = %v", m)
	}
}

func TestEvalRejects(t *testing.T) {
	for _, expr := range []string{"", "1 +", "2 ** 3", "import os", "pow(2, 3)", "1 / 0", "(1 + 2", "1.", "__import__('os')", "1;2"} {
		m := call(t, `{"op":"eval","expr":`+jsonStr(expr)+`}`)
		if m["ok"] != false {
			t.Errorf("%q는 거절해야 한다: %v", expr, m)
		}
	}
}

// 생성기(studio_synth.model.market_unit_price)와 같은 규칙인지: 분수로 계산하고 마지막에 한 번 올림한다(채점 명세 3.4절).
func TestBenchmarkMatchesGeneratorRule(t *testing.T) {
	cases := []struct{ price, vat, per, want string }{
		{"400", "excluded", "1/25", "11000"},   // hard-04
		{"198000", "excluded", "20", "10890"},  // hard-05
		{"12000", "excluded", "1", "13200"},    // hard-06
		{"9800", "excluded", "1", "10780"},     // hard-07(운반비는 더하지 않는다)
		{"6000000", "included", "1", "6000000"}, // hard-15
		{"9499", "excluded", "1", "10449"},     // 10448.9 → 올림
	}
	for _, c := range cases {
		m := call(t, `{"op":"benchmark","sale_price_krw":"`+c.price+`","vat":"`+c.vat+`","procurement_units_per_sale_unit":"`+c.per+`"}`)
		if m["ok"] != true || m["benchmark_krw"] != c.want {
			t.Errorf("%s %s %s = %v, want %s", c.price, c.vat, c.per, m, c.want)
		}
	}
	for _, bad := range []string{
		`{"op":"benchmark","sale_price_krw":"9900.5","vat":"excluded","procurement_units_per_sale_unit":"1"}`,
		`{"op":"benchmark","sale_price_krw":"9900","vat":"not_stated","procurement_units_per_sale_unit":"1"}`,
		`{"op":"benchmark","sale_price_krw":"9900","vat":"excluded","procurement_units_per_sale_unit":"0"}`,
	} {
		if m := call(t, bad); m["ok"] != false {
			t.Errorf("거절해야 한다: %s → %v", bad, m)
		}
	}
}

func facts(p string, qs ...string) string {
	s := "["
	for i, q := range qs {
		if i > 0 {
			s += ","
		}
		s += `{"fact_id":"f` + q + `","unit":"㎡","tax_basis":"vat_included","basis_id":"b","comparison_basis_id":"b","unit_price_krw":"` + p +
			`","quantity":"` + q + `","comparable_amount_krw":"` + mul(p, q) + `","scorable":true}`
	}
	return s + "]"
}

func mul(a, b string) string {
	var x, y int64
	json.Unmarshal([]byte(a), &x)
	json.Unmarshal([]byte(b), &y)
	out, _ := json.Marshal(x * y)
	return string(out)
}

func TestGap(t *testing.T) {
	cases := []struct {
		name, facts, b string
		pass           bool
		ratio          string
	}{
		{"hard-14 1.196배 미달", facts("12500", "3000"), "10450", false, "1.1"},
		{"hard-15 차이 100만 원", facts("7000000", "12"), "6000000", true, "1.1"},
		{"hard-01 2.4배", facts("27000", "1200", "800"), "10890", true, "2.4"},
		{"경계 정확히 1.2배", facts("12000", "10"), "10000", true, "1.2"},
		{"비교 단가가 더 높다", facts("10000", "10"), "12000", false, "0.8"},
	}
	for _, c := range cases {
		m := call(t, `{"op":"gap","facts":`+c.facts+`,"benchmark_krw":"`+c.b+`"}`)
		if m["ok"] != true || m["gap_pass"] != c.pass || m["ratio"] != c.ratio {
			t.Errorf("%s: %v", c.name, m)
		}
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
