// Package probe는 시중 판매 페이지가 서버 근거 검사(lint)에 읽힐지를 미리 가늠한다.
// lint는 공인 https의 정적 HTML만 읽는다. 그래서 스크립트를 실행하지 않고, 받은 원문에서
// 사람이 보는 본문 글자만 뽑아 상품명·가격이 들어 있는지 본다.
// 실제 lint 판정과 같다는 보장은 없다 — 서버의 위치·User-Agent가 다르고, 봇 차단은 IP마다 다르다.
package probe

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html/charset"
)

// 판정
const (
	StaticOK           = "static_ok"           // 본문 글자에 가격(N원)이 있고, 찾는 문구도 모두 본문에 있다
	NeedleMissing      = "needle_missing"      // 가격은 있지만 찾는 문구(상품명·모델) 일부가 본문에 없다
	PriceNotInText     = "price_not_in_text"   // 본문 글자에 가격 표기가 없다(스크립트·JSON에만 있거나 아예 없음)
	EmptyShell         = "empty_shell"         // 본문은 거의 비고 스크립트만 크다 — 브라우저가 그려야 보이는 페이지
	CharsetUnsupported = "charset_unsupported" // 선언된 문자셋을 풀지 못했다(사람이 본다)
)

// Hit은 찾는 문구 하나가 어디에 있었는지다. 비교는 대소문자·공백을 무시한다.
type Hit struct {
	InText bool `json:"in_text"` // 사람이 보는 본문 글자
	InRaw  bool `json:"in_raw"`  // 원문 어딘가(스크립트·속성 포함)
}

type Result struct {
	Title        string         `json:"title"`
	Charset      string         `json:"charset,omitempty"`
	TextChars    int            `json:"text_chars"`     // 스크립트·스타일을 뺀 본문 글자 수
	ScriptBytes  int            `json:"script_bytes"`   // <script> 안 바이트
	Prices       []int64        `json:"prices_in_text"` // 본문의 "N원" 표기, 나온 순서, 최대 10개(정수 원)
	Needles      map[string]Hit `json:"needles,omitempty"`
	JSONLDPrices []string       `json:"jsonld_prices,omitempty"` // schema.org Offer의 price·lowPrice(원문 문자열)
	MetaPrice    string         `json:"meta_price,omitempty"`    // <meta property="product:price:amount">
	AppShell     []string       `json:"app_shell,omitempty"`     // JS 앱 흔적(__NEXT_DATA__ 등)
	Verdict      string         `json:"verdict"`
}

var (
	priceRe    = regexp.MustCompile(`([0-9]{1,3}(?:,[0-9]{3})+|[0-9]{4,})\s*원`)
	charsetRe  = regexp.MustCompile(`(?i)charset\s*=\s*["']?([a-z0-9_-]+)`)
	shellMarks = []string{"__NEXT_DATA__", "__NUXT__", "window.__INITIAL_STATE__", "window.__PRELOADED_STATE__", "ng-version"}
)

const maxPrices = 10

// Analyze는 원문 HTML과 Content-Type으로 판정한다. needles는 본문에 있어야 할 문구(상품명·모델명 등)다.
func Analyze(raw []byte, contentType string, needles []string) Result {
	var r Result
	// 국내 쇼핑몰에는 EUC-KR 페이지가 아직 있다(예: 컴퓨존). UTF-8로 풀어서 본다.
	r.Charset = detectCharset(raw, contentType)
	if r.Charset != "" && r.Charset != "utf-8" && r.Charset != "utf8" {
		enc, name := charset.Lookup(r.Charset)
		if enc == nil {
			r.Verdict = CharsetUnsupported
			return r
		}
		dec, err := enc.NewDecoder().Bytes(raw)
		if err != nil {
			r.Verdict = CharsetUnsupported
			return r
		}
		raw, r.Charset = dec, name
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(raw))
	if err != nil {
		r.Verdict = EmptyShell
		return r
	}
	r.Title = squash(doc.Find("title").First().Text())
	r.MetaPrice = strings.TrimSpace(doc.Find(`meta[property="product:price:amount"]`).AttrOr("content", ""))
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		var v any
		if json.Unmarshal([]byte(s.Text()), &v) == nil {
			collectOfferPrices(v, &r.JSONLDPrices)
		}
	})
	doc.Find("script").Each(func(_ int, s *goquery.Selection) { r.ScriptBytes += len(s.Text()) })
	for _, m := range shellMarks {
		if bytes.Contains(raw, []byte(m)) {
			r.AppShell = append(r.AppShell, m)
		}
	}

	doc.Find("script, style, noscript, template, svg").Remove()
	text := squash(doc.Find("body").Text())
	r.TextChars = len([]rune(text))
	for _, m := range priceRe.FindAllStringSubmatch(text, -1) {
		n, err := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
		if err != nil || n <= 0 || slices.Contains(r.Prices, n) {
			continue
		}
		r.Prices = append(r.Prices, n)
		if len(r.Prices) == maxPrices {
			break
		}
	}

	allIn := true
	if len(needles) > 0 {
		r.Needles = map[string]Hit{}
		nt, nr := fold(text), fold(string(raw))
		for _, n := range needles {
			f := fold(n)
			if f == "" {
				continue
			}
			h := Hit{InText: strings.Contains(nt, f), InRaw: strings.Contains(nr, f)}
			r.Needles[n] = h
			allIn = allIn && h.InText
		}
	}
	switch {
	case r.TextChars < 200 && r.ScriptBytes > 20000:
		r.Verdict = EmptyShell
	case len(r.Prices) == 0:
		r.Verdict = PriceNotInText
	case !allIn:
		r.Verdict = NeedleMissing
	default:
		r.Verdict = StaticOK
	}
	return r
}

// detectCharset은 Content-Type, 없으면 앞부분의 meta charset을 본다. 모르면 "".
func detectCharset(raw []byte, contentType string) string {
	if m := charsetRe.FindStringSubmatch(contentType); m != nil {
		return strings.ToLower(m[1])
	}
	head := raw[:min(len(raw), 4096)]
	if m := charsetRe.FindSubmatch(head); m != nil {
		return strings.ToLower(string(m[1]))
	}
	return ""
}

func collectOfferPrices(v any, out *[]string) {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			collectOfferPrices(e, out)
		}
	case map[string]any:
		if typ, _ := t["@type"].(string); typ == "Offer" || typ == "AggregateOffer" {
			for _, k := range []string{"price", "lowPrice"} {
				switch p := t[k].(type) {
				case string:
					*out = append(*out, p)
				case float64:
					*out = append(*out, strconv.FormatFloat(p, 'f', -1, 64))
				}
			}
		}
		for _, e := range t {
			collectOfferPrices(e, out)
		}
	}
}

// squash는 공백을 한 칸으로 줄인다.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// fold는 비교용으로 공백을 지우고 소문자로 바꾼다.
func fold(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}
