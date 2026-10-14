package probe

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/korean"
)

// 픽스처는 모두 합성 HTML이다(실제 사이트 원문을 저장소에 두지 않는다).

func TestStaticPageWithPriceAndNeedles(t *testing.T) {
	html := `<html><head><meta charset="utf-8"><title>테스트 카메라 X1 | 상점</title>
<meta property="product:price:amount" content="1990000">
<script type="application/ld+json">{"@type":"Product","name":"X1","offers":{"@type":"Offer","price":"1990000","priceCurrency":"KRW"}}</script>
</head><body><h1>테스트 카메라 X1 바디</h1><p>판매가 1,990,000원 · 배송비 3,000원</p><p>` + strings.Repeat("설명 ", 80) + `</p></body></html>`
	r := Analyze([]byte(html), "text/html; charset=UTF-8", []string{"x1 바디", "테스트카메라"})
	if r.Verdict != StaticOK {
		t.Fatalf("verdict = %s (%+v)", r.Verdict, r)
	}
	if len(r.Prices) != 2 || r.Prices[0] != 1990000 || r.Prices[1] != 3000 {
		t.Errorf("prices = %v", r.Prices)
	}
	if r.MetaPrice != "1990000" || len(r.JSONLDPrices) != 1 || r.JSONLDPrices[0] != "1990000" {
		t.Errorf("meta=%q jsonld=%v", r.MetaPrice, r.JSONLDPrices)
	}
	if r.Title != "테스트 카메라 X1 | 상점" {
		t.Errorf("title = %q", r.Title)
	}
}

func TestPriceOnlyInScriptIsNotReadable(t *testing.T) {
	html := `<html><body><div id="app">` + strings.Repeat("메뉴 ", 60) + `</div>
<script>window.__INITIAL_STATE__={"price":"1,990,000원","name":"X1"}</script></body></html>`
	r := Analyze([]byte(html), "text/html", []string{"X1"})
	if r.Verdict != PriceNotInText {
		t.Fatalf("verdict = %s", r.Verdict)
	}
	if h := r.Needles["X1"]; h.InText || !h.InRaw {
		t.Errorf("needle = %+v", h)
	}
	if len(r.AppShell) != 1 {
		t.Errorf("app shell = %v", r.AppShell)
	}
}

func TestEmptyShellAndMissingNeedle(t *testing.T) {
	shell := `<html><body><div id="root"></div><script>` + strings.Repeat("x", 30000) + `</script></body></html>`
	if r := Analyze([]byte(shell), "text/html", nil); r.Verdict != EmptyShell {
		t.Errorf("shell verdict = %s", r.Verdict)
	}
	page := `<html><body><p>다른 상품 Y2 · 15,000원</p>` + strings.Repeat("글 ", 100) + `</body></html>`
	if r := Analyze([]byte(page), "text/html", []string{"X1"}); r.Verdict != NeedleMissing {
		t.Errorf("needle verdict = %s", r.Verdict)
	}
}

func TestEUCKRIsDecoded(t *testing.T) {
	page := `<html><head><meta http-equiv="Content-Type" content="text/html; charset=euc-kr"><title>윈도우 서버</title></head>` +
		`<body><p>Windows Server 2025 판매가 199,000원</p>` + strings.Repeat("설명 ", 100) + `</body></html>`
	raw, err := korean.EUCKR.NewEncoder().Bytes([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	r := Analyze(raw, "text/html", []string{"windows server 2025"})
	if r.Verdict != StaticOK || r.Charset != "euc-kr" || r.Title != "윈도우 서버" || r.Prices[0] != 199000 {
		t.Errorf("%+v", r)
	}
}

func TestUnknownCharsetIsFlagged(t *testing.T) {
	r := Analyze([]byte(`<html><head><meta charset="x-unknown-cs"></head><body>...</body></html>`), "text/html", nil)
	if r.Verdict != CharsetUnsupported {
		t.Errorf("%+v", r)
	}
}
