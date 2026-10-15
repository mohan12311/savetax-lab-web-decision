package danawa

import (
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func eq(t *testing.T, field string, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %d", field, deref(got), want)
	}
}

func isNil(t *testing.T, field string, got *int64) {
	t.Helper()
	if got != nil {
		t.Errorf("%s = %d, want nil(없는 값을 0이나 추정값으로 채우지 않는다)", field, *got)
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestParseSearch(t *testing.T) {
	r, err := ParseSearch(fixture(t, "search.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.ListFound || r.Layout != LayoutHTML {
		t.Errorf("list_found=%v layout=%q", r.ListFound, r.Layout)
	}
	if len(r.Catalog) != 2 || len(r.Listings) != 1 {
		t.Fatalf("catalog=%d listings=%d (광고 영역은 빠져야 한다)", len(r.Catalog), len(r.Listings))
	}

	c := r.Catalog[0]
	if c.PCode != "1001" || c.Order != 1 || c.Name != "가상전자 TX-100" || c.Maker != "가상전자" || c.Category != "가전_테스트기기" {
		t.Errorf("catalog[0] = %+v", c)
	}
	if c.URL != "https://prod.danawa.com/info/?pcode=1001" {
		t.Errorf("url은 검색어 꼬리 없는 정규 주소여야 한다: %s", c.URL)
	}
	if c.Spec != "테스트기기 / 10kg / 220V" {
		t.Errorf("spec은 전체 스펙 칸에서: %q", c.Spec)
	}
	eq(t, "min_price", c.MinPriceKRW, 120000)
	if len(c.Variants) != 1 || c.Variants[0].PCode != "1001" {
		t.Fatalf("variants %+v", c.Variants)
	}
	eq(t, "variant mall_count", c.Variants[0].MallCount, 12)

	c2 := r.Catalog[1]
	if c2.PCode != "2002" || c2.Order != 3 || c2.Spec != "테스트기기 / 20kg" {
		t.Errorf("catalog[1] = %+v", c2)
	}
	isNil(t, "min_price(숨은 칸 없음)", c2.MinPriceKRW)
	if len(c2.Variants) != 2 {
		t.Fatalf("variants %+v", c2.Variants)
	}
	v0, v1 := c2.Variants[0], c2.Variants[1]
	if v0.Label != "1TB" || v1.Label != "2TB" || v1.PCode != "2003" {
		t.Errorf("variant label·pcode: %+v %+v", v0, v1)
	}
	eq(t, "variant0 price", v0.PriceKRW, 1500000)
	isNil(t, "단종 variant price", v1.PriceKRW)
	if v1.Status != "단종" {
		t.Errorf("단종 상태: %q", v1.Status)
	}
	eq(t, "단종 mall_count", v1.MallCount, 0)

	l := r.Listings[0]
	if l.ID != "MK1_555" || l.Order != 2 || l.Mall != "테스트몰" || l.Name != "가상전자 TX-100 (호환)" {
		t.Errorf("listing %+v", l)
	}
	eq(t, "listing price", l.PriceKRW, 99000)
	eq(t, "listing shipping", l.ShippingKRW, 2500)
}

func TestParseSearchNoList(t *testing.T) {
	r, err := ParseSearch([]byte(`<html><head><meta charset="utf-8"></head><body><p>검색결과가 없습니다</p></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if r.ListFound || len(r.Catalog) != 0 || r.Catalog == nil {
		t.Errorf("%+v", r)
	}
}

func TestParseProduct(t *testing.T) {
	p, err := ParseProduct(fixture(t, "product.html"))
	if err != nil {
		t.Fatal(err)
	}
	if p.PCode != "1001" || p.Name != "가상전자 TX-100" || p.Brand != "가상전자" || p.Maker != "가상전자" {
		t.Errorf("ident %+v", p)
	}
	if p.URL != "https://prod.danawa.com/info/?pcode=1001" || p.RegisteredMonth != "2024.05." {
		t.Errorf("url/registered %q %q", p.URL, p.RegisteredMonth)
	}
	if p.Spec != "테스트기기 / 10kg / 220V" {
		t.Errorf("spec %q", p.Spec)
	}
	if p.Summary != "최저가 120,000원, 현금최저가: 118,000원" || p.MinPriceNote != "120,000+2,500원" {
		t.Errorf("summary %q note %q", p.Summary, p.MinPriceNote)
	}
	eq(t, "offer_count", p.OfferCount, 12)
	eq(t, "low_price", p.LowPriceKRW, 120000)
	eq(t, "min_price", p.MinPriceKRW, 120000)
	eq(t, "min_price_with_shipping", p.MinPriceWithShippingKRW, 122500)
	if !p.MallPricesIncludeShipping {
		t.Error("배송비 포함 표시 모드를 못 읽었다")
	}
	if len(p.Malls) != 3 {
		t.Fatalf("malls %d", len(p.Malls))
	}

	a := p.Malls[0]
	if a.Rank != 1 || a.Mall != "가몰" || !a.Lowest || a.ShippingText != "2,500원" || !strings.Contains(a.Link, "cmpnyc=A") {
		t.Errorf("mall[0] %+v (배송 칸의 빠른배송 안내문은 빠져야 한다)", a)
	}
	eq(t, "mall[0] price", a.PriceKRW, 122500)
	eq(t, "mall[0] base", a.BasePriceKRW, 120000)
	eq(t, "mall[0] shipping", a.ShippingKRW, 2500)

	b := p.Malls[1]
	if b.Mall != "나몰" || b.Lowest || b.ShippingText != "무료배송" {
		t.Errorf("mall[1] %+v", b)
	}
	eq(t, "mall[1] price", b.PriceKRW, 125000)
	isNil(t, "mall[1] base(역산 금지)", b.BasePriceKRW)
	eq(t, "mall[1] shipping", b.ShippingKRW, 0)

	c := p.Malls[2]
	if c.Mall != "다몰" || c.ShippingText != "착불" || c.Link != "" {
		t.Errorf("mall[2] %+v", c)
	}
	isNil(t, "착불 shipping", c.ShippingKRW)
}

func TestParseProductRejectsUnknownPage(t *testing.T) {
	if _, err := ParseProduct([]byte(`<html><head><meta charset="utf-8"></head><body>점검 중</body></html>`)); err == nil {
		t.Error("상품명·pcode 없는 페이지를 받아들였다")
	}
}

func TestParseRejectsNonUTF8(t *testing.T) {
	euckr := []byte("<html><head><meta charset=\"euc-kr\"></head><body>x</body></html>")
	if _, err := ParseSearch(euckr); err == nil {
		t.Error("euc-kr 선언을 받아들였다")
	}
	if _, err := ParseProduct([]byte{0xff, 0xfe, 'a'}); err == nil {
		t.Error("UTF-8이 아닌 바이트를 받아들였다")
	}
}

func TestParseKRW(t *testing.T) {
	cases := map[string]any{
		"133,990원": int64(133990), " 133990 ": int64(133990), "0": int64(0),
		"": nil, "단종": nil, "1.5": nil, "-100": nil, "가격비교예정": nil,
	}
	for in, want := range cases {
		if got := deref(parseKRW(in)); got != want {
			t.Errorf("parseKRW(%q)=%v want %v", in, got, want)
		}
	}
	ship := map[string]any{"무료배송": int64(0), "배송비 3,000원": int64(3000), "3,000원": int64(3000), "착불": nil, "조건부 무료": nil}
	for in, want := range ship {
		if got := deref(parseShipping(in)); got != want {
			t.Errorf("parseShipping(%q)=%v want %v", in, got, want)
		}
	}
}

func TestPaths(t *testing.T) {
	if got := SearchPath("TX-100 24인치&x"); got != "/dsearch.php?query=TX-100+24%EC%9D%B8%EC%B9%98%26x" {
		t.Errorf("search path %q", got)
	}
	if !ValidPCode("19211177") || ValidPCode("1001&x=1") || ValidPCode("") {
		t.Error("ValidPCode")
	}
}

func TestParseSearchFlight(t *testing.T) {
	r, err := ParseSearch(fixture(t, "search_flight.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.ListFound || r.Layout != LayoutFlight {
		t.Fatalf("list_found=%v layout=%q", r.ListFound, r.Layout)
	}
	if r.SearchedKeyword != "TX-100 본체" {
		t.Errorf("searched_keyword=%q (다나와가 고친 검색어)", r.SearchedKeyword)
	}
	// 숫자 아닌 pcode는 가격비교 상품으로 받지 않는다(adLeader 등 다른 쿼리도 무시)
	if len(r.Catalog) != 2 || len(r.Listings) != 2 {
		t.Fatalf("catalog=%d listings=%d", len(r.Catalog), len(r.Listings))
	}
	c := r.Catalog[0]
	if c.Order != 1 || c.PCode != "1001" || c.Name != "가상전자 TX-100 본체 & 받침" || c.Maker != "가상전자" || c.Category != "가전_테스트기기" {
		t.Errorf("catalog[0] = %+v", c)
	}
	if c.Spec != "테스트기기 / 100W / 무게:2kg" || c.URL != ProductURL("1001") || c.Status != "" {
		t.Errorf("spec=%q url=%q status=%q", c.Spec, c.URL, c.Status)
	}
	eq(t, "catalog[0].min_price", c.MinPriceKRW, 1500000)
	if len(c.Variants) != 3 || c.Variants[1].Label != "중고품" || c.Variants[2].Status != "단종" {
		t.Fatalf("variants = %+v", c.Variants)
	}
	eq(t, "variants[1].price(문자열 숫자)", c.Variants[1].PriceKRW, 1400000)
	isNil(t, "variants[2].price(단종 0원)", c.Variants[2].PriceKRW)
	d := r.Catalog[1]
	if d.Order != 3 || d.Status != "단종" || d.Spec != "" || len(d.Variants) != 0 {
		t.Errorf("catalog[1] = %+v", d)
	}
	isNil(t, "catalog[1].min_price(단종 0원)", d.MinPriceKRW)
	l := r.Listings[0]
	if l.Order != 2 || l.ID != "MALL1_778899" || l.Mall != "가상몰" || l.Name != "[해외] TX-100 호환 커버" {
		t.Errorf("listing[0] = %+v", l)
	}
	eq(t, "listing[0].price", l.PriceKRW, 9900)
	eq(t, "listing[0].shipping(무료배송)", l.ShippingKRW, 0)
	eq(t, "listing[1].shipping", r.Listings[1].ShippingKRW, 3000)
}

func TestParseSearchFlightEmptyAndUnknown(t *testing.T) {
	r, err := ParseSearch(fixture(t, "search_flight_empty.html"))
	if err != nil {
		t.Fatal(err)
	}
	// 목록 쿼리는 있는데 상품이 없다 = 다나와 검색 결과 없음(구조 변경과 구분)
	if !r.ListFound || r.Layout != LayoutFlight || len(r.Catalog) != 0 || r.SearchedKeyword != "" {
		t.Errorf("empty: %+v", r)
	}
	r, err = ParseSearch([]byte(`<html><head><meta charset="utf-8"></head><body><div class="dnw-x">x</div></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if r.ListFound || r.Layout != "" {
		t.Errorf("unknown: list_found=%v layout=%q (모르는 구조는 list_found=false)", r.ListFound, r.Layout)
	}
}
