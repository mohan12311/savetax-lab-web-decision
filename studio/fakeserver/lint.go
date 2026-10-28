package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// lint는 실서버의 1차 근거 검사(정적 HTML만 읽는다)를 단순하게 흉내 낸다. 데모 판매처 페이지만 읽고,
// 근거마다 ① 페이지가 있는지 ② 본문 글자가 있는지(JS 렌더링 껍데기면 unreadable) ③ 관측가(쉼표 표기)가
// 본문에 있는지 ④ 페이지의 부가세 표기와 근거의 vat_included가 같은지 ⑤ 인용에 페이지 상품명이 그대로 있는지 본다.
// 줄 판정은 근거 중 가장 나쁜 것, 전체는 suspect > unreadable > clean 순으로 가장 나쁜 것이다.
func (s *server) lint(b *claimBody) lintResult {
	res := lintResult{Status: "clean", Lines: []lintLine{}, Hints: []lintHint{}}
	rank := map[string]int{"clean": 0, "unreadable": 1, "suspect": 2}
	for _, l := range b.Lines {
		line := lintLine{FactID: l.FactID, Verdict: "clean", Reason: "근거 페이지에서 상품명·관측가를 확인했다"}
		for j, e := range l.PriceEvidence {
			verdict, code, msg := s.lintEvidence(e)
			if verdict == "clean" {
				continue
			}
			no := j + 1
			res.Hints = append(res.Hints, lintHint{FactID: l.FactID, Role: "price_evidence", EvidenceNo: &no, Code: code, Message: msg})
			if rank[verdict] > rank[line.Verdict] {
				line.Verdict, line.Reason = verdict, msg
			}
		}
		if rank[line.Verdict] > rank[res.Status] {
			res.Status = line.Verdict
		}
		res.Lines = append(res.Lines, line)
	}
	return res
}

func (s *server) lintEvidence(e evidence) (verdict, code, msg string) {
	u, err := url.Parse(e.SourceURL)
	if err != nil || s.shop == nil || u.Host != s.shop.host {
		return "unreadable", "EXTERNAL_PAGE", "데모 판정기는 데모 판매처 페이지만 읽는다"
	}
	p := s.shop.cat.Pages[u.Path]
	if p == nil {
		return "suspect", "PAGE_NOT_FOUND", "근거 주소에 페이지가 없다(404)"
	}
	if p.Render != "static" || !strings.Contains(p.text, "원") {
		return "unreadable", "EMPTY_SHELL", "본문에 상품·가격 글자가 없는 페이지(JS 렌더링)라 읽지 못했다"
	}
	if !strings.Contains(p.text, commas(e.ObservedPriceKRW)+"원") {
		return "suspect", "PRICE_NOT_IN_PAGE", "관측가 " + commas(e.ObservedPriceKRW) + "원이 페이지 본문에 없다"
	}
	if p.VATIncluded != nil && e.VATIncluded != nil && *p.VATIncluded != *e.VATIncluded {
		return "suspect", "VAT_MISMATCH", "페이지의 부가세 표기와 근거의 vat_included가 다르다"
	}
	if !strings.Contains(e.Quote, p.Title) {
		return "suspect", "QUOTE_MISSING_TITLE", "인용에 페이지의 상품명(" + p.Title + ")을 그대로 옮겨 적지 않았다"
	}
	return "clean", "", ""
}

// commas는 정수 문자열에 천 단위 쉼표를 넣는다("108900" → "108,900").
func commas(s string) string {
	if _, err := strconv.ParseUint(s, 10, 64); err != nil || len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// shopSite는 가짜 판매처다. robots.txt는 상품 페이지를 허용하고 장바구니만 막는다.
type shopSite struct {
	cat  *Catalog
	host string // host:port — 근거 주소가 이 판매처 것인지 가른다
}

func (sh *shopSite) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /cart\nAllow: /\n"))
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		p := sh.cat.Pages[r.URL.Path]
		if p == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(p.html)
	})
	return mux
}
