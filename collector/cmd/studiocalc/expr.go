package main

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"
)

// 식 문법(LLM이 쓴 글이라 믿지 않는다 — 숫자·연산자·괄호·ceil/floor만 받는다):
//
//	expr   := term (('+' | '-') term)*
//	term   := factor (('*' | '×' | 'x' | 'X' | '/' | '÷') factor)*
//	factor := ('+' | '-') factor | number | '(' expr ')' | ('ceil' | 'floor') '(' expr ')'
//	number := 숫자 [',' 숫자 3자리]* ['.' 숫자+]
const (
	maxExprLen   = 300
	maxExprDepth = 32
)

type parser struct {
	s     []rune
	pos   int
	depth int
}

func evalExpr(src string) (*big.Rat, error) {
	if strings.TrimSpace(src) == "" {
		return nil, errors.New("식이 비어 있다")
	}
	if len([]rune(src)) > maxExprLen {
		return nil, fmt.Errorf("식이 %d자를 넘는다", maxExprLen)
	}
	p := &parser{s: []rune(src)}
	v, err := p.expr()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos < len(p.s) {
		return nil, fmt.Errorf("%d번째 글자 %q를 읽지 못했다", p.pos+1, string(p.s[p.pos]))
	}
	return v, nil
}

func (p *parser) space() {
	for p.pos < len(p.s) && unicode.IsSpace(p.s[p.pos]) {
		p.pos++
	}
}

func (p *parser) peek() rune {
	p.space()
	if p.pos < len(p.s) {
		return p.s[p.pos]
	}
	return 0
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > maxExprDepth {
		return errors.New("괄호가 너무 깊다")
	}
	return nil
}

func (p *parser) expr() (*big.Rat, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	v, err := p.term()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek() {
		case '+':
			p.pos++
			r, err := p.term()
			if err != nil {
				return nil, err
			}
			v = new(big.Rat).Add(v, r)
		case '-', '−':
			p.pos++
			r, err := p.term()
			if err != nil {
				return nil, err
			}
			v = new(big.Rat).Sub(v, r)
		default:
			return v, nil
		}
	}
}

func (p *parser) term() (*big.Rat, error) {
	v, err := p.factor()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek() {
		case '*', '×', 'x', 'X', '·':
			p.pos++
			r, err := p.factor()
			if err != nil {
				return nil, err
			}
			v = new(big.Rat).Mul(v, r)
		case '/', '÷':
			p.pos++
			r, err := p.factor()
			if err != nil {
				return nil, err
			}
			if r.Sign() == 0 {
				return nil, errors.New("0으로 나눴다")
			}
			v = new(big.Rat).Quo(v, r)
		default:
			return v, nil
		}
	}
}

func (p *parser) factor() (*big.Rat, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	c := p.peek()
	switch {
	case c == '+':
		p.pos++
		return p.factor()
	case c == '-' || c == '−':
		p.pos++
		v, err := p.factor()
		if err != nil {
			return nil, err
		}
		return new(big.Rat).Neg(v), nil
	case c == '(':
		p.pos++
		v, err := p.expr()
		if err != nil {
			return nil, err
		}
		if p.peek() != ')' {
			return nil, errors.New("닫는 괄호가 없다")
		}
		p.pos++
		return v, nil
	case c >= '0' && c <= '9':
		return p.number()
	case unicode.IsLetter(c):
		return p.call()
	case c == 0:
		return nil, errors.New("식이 중간에 끝났다")
	}
	return nil, fmt.Errorf("%d번째 글자 %q를 읽지 못했다", p.pos+1, string(c))
}

func (p *parser) call() (*big.Rat, error) {
	start := p.pos
	for p.pos < len(p.s) && unicode.IsLetter(p.s[p.pos]) && p.s[p.pos] < 128 {
		p.pos++
	}
	name := strings.ToLower(string(p.s[start:p.pos]))
	if name != "ceil" && name != "floor" {
		return nil, fmt.Errorf("모르는 함수 %q(ceil, floor만 쓸 수 있다)", name)
	}
	if p.peek() != '(' {
		return nil, fmt.Errorf("%s 뒤에 여는 괄호가 없다", name)
	}
	p.pos++
	v, err := p.expr()
	if err != nil {
		return nil, err
	}
	if p.peek() != ')' {
		return nil, errors.New("닫는 괄호가 없다")
	}
	p.pos++
	if name == "ceil" {
		return ratCeil(v), nil
	}
	return ratFloor(v), nil
}

func (p *parser) number() (*big.Rat, error) {
	var b strings.Builder
	digits := func() int {
		n := 0
		for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
			b.WriteRune(p.s[p.pos])
			p.pos++
			n++
		}
		return n
	}
	digits()
	// 천 단위 쉼표(9,900): 쉼표 뒤가 정확히 숫자 3자리일 때만
	for p.pos+3 < len(p.s) && p.s[p.pos] == ',' && isDigits(p.s[p.pos+1:p.pos+4]) && (p.pos+4 == len(p.s) || !isDigit(p.s[p.pos+4])) {
		p.pos++
		digits()
	}
	if p.pos < len(p.s) && p.s[p.pos] == '.' {
		b.WriteRune('.')
		p.pos++
		if digits() == 0 {
			return nil, errors.New("소수점 뒤에 숫자가 없다")
		}
	}
	r, ok := new(big.Rat).SetString(b.String())
	if !ok {
		return nil, fmt.Errorf("숫자 %q를 읽지 못했다", b.String())
	}
	return r, nil
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isDigits(rs []rune) bool {
	for _, r := range rs {
		if !isDigit(r) {
			return false
		}
	}
	return len(rs) > 0
}

func ratFloor(r *big.Rat) *big.Rat {
	q := new(big.Int).Div(r.Num(), r.Denom()) // Div는 유클리드 나눗셈: 분모가 양수라 곧 내림
	return new(big.Rat).SetInt(q)
}

func ratCeil(r *big.Rat) *big.Rat {
	f := ratFloor(r)
	if f.Cmp(r) == 0 {
		return f
	}
	return new(big.Rat).Add(f, big.NewRat(1, 1))
}

// show는 결과를 사람이 읽는 십진 문자열로 낸다. 나누어떨어지지 않으면 소수 6자리에서 자르고 "…"를 붙인다.
func show(r *big.Rat) (decimal string, exact bool) {
	if r.IsInt() {
		return r.Num().String(), true
	}
	for places := 1; places <= 12; places++ {
		scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)))
		if scaled.IsInt() {
			return r.FloatString(places), true
		}
	}
	neg := r.Sign() < 0
	abs := new(big.Rat).Abs(r)
	s := truncString(abs, 6)
	if neg {
		s = "-" + s
	}
	return s + "…", false
}

func truncString(r *big.Rat, places int) string {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(places)), nil)
	n := new(big.Int).Mul(r.Num(), scale)
	n.Quo(n, r.Denom())
	s := n.String()
	for len(s) <= places {
		s = "0" + s
	}
	return s[:len(s)-places] + "." + s[len(s)-places:]
}

func opEval(expr string) (map[string]any, error) {
	v, err := evalExpr(expr)
	if err != nil {
		return nil, err
	}
	dec, exact := show(v)
	return map[string]any{"value": dec, "exact": exact, "fraction": v.RatString(), "integer": v.IsInt()}, nil
}
