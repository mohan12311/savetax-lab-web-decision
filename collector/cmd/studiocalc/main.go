// studiocalc는 2부(studio/)가 부르는 작은 계산기다. 표준 입력으로 JSON 요청 하나를 받아 표준 출력으로 JSON 하나를 낸다.
// 네트워크·토큰·파일을 쓰지 않는다. 금액은 십진 문자열을 big.Rat으로만 다룬다(float 금지).
//
//	{"op":"eval","expr":"ceil(400 × 25 × 1.1)"}                     에이전트의 계산기 도구(MCP로 노출)
//	{"op":"benchmark","sale_price_krw":"400","vat":"excluded",
//	 "procurement_units_per_sale_unit":"1/25"}                       읽은 값 → 비교 단가(코드 계산, 노출하지 않음)
//	{"op":"gap","facts":[…items.json의 facts…],"benchmark_krw":"11000"} 1부 claim.Evaluate로 가격 차이 기준(노출하지 않음)
//
// 실패해도 종료 코드는 0이고 {"ok":false,"error":"…"}를 낸다(호출하는 쪽이 오류를 글로 받게).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const maxInput = 1 << 20

func main() {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxInput+1))
	var out any
	switch {
	case err != nil:
		out = fail(fmt.Errorf("입력을 읽지 못했다: %w", err))
	case len(data) > maxInput:
		out = fail(fmt.Errorf("입력이 %d바이트를 넘는다", maxInput))
	default:
		out = handle(data)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		os.Exit(1)
	}
}

type request struct {
	Op string `json:"op"`
	// eval
	Expr string `json:"expr"`
	// benchmark
	SalePriceKRW string `json:"sale_price_krw"`
	VAT          string `json:"vat"`
	UnitsPerSale string `json:"procurement_units_per_sale_unit"`
	// gap
	Facts        json.RawMessage `json:"facts"`
	BenchmarkKRW string          `json:"benchmark_krw"`
}

func fail(err error) map[string]any { return map[string]any{"ok": false, "error": err.Error()} }

func handle(data []byte) any {
	var req request
	if err := json.Unmarshal(data, &req); err != nil {
		return fail(fmt.Errorf("요청 JSON을 읽지 못했다: %w", err))
	}
	var (
		res map[string]any
		err error
	)
	switch req.Op {
	case "eval":
		res, err = opEval(req.Expr)
	case "benchmark":
		res, err = opBenchmark(req.SalePriceKRW, req.VAT, req.UnitsPerSale)
	case "gap":
		res, err = opGap(req.Facts, req.BenchmarkKRW)
	default:
		err = fmt.Errorf("모르는 op: %q", req.Op)
	}
	if err != nil {
		return fail(err)
	}
	res["ok"] = true
	return res
}
