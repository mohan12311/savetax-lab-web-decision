package danawa

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
)

// 추출 결과의 금액은 정수 원(int64)이다. 페이지에서 못 읽은 값은 nil(0으로 바꾸지 않는다).
// 페이지에 없는 값을 계산해 채우지 않는다 — 예: 배송비 포함가에서 상품가를 역산하지 않는다.

// SearchResult는 통합검색 결과 한 페이지다.
type SearchResult struct {
	ListFound bool          `json:"list_found"` // 상품 목록 영역이 있었는가(false면 결과 없음 또는 구조 변경)
	Catalog   []CatalogItem `json:"catalog"`    // 가격비교 상품(pcode) — 근거 페이지 후보
	Listings  []Listing     `json:"listings"`   // 오픈마켓 개별 상품 — 탐색 참고용(근거 페이지로 쓰지 않는다)
}

// CatalogItem은 다나와가 묶은 가격비교 상품이다. Variants는 같은 상품의 용량·크기 등 하위 상품(각자 pcode)이다.
type CatalogItem struct {
	Order       int       `json:"order"`
	PCode       string    `json:"pcode"`
	Name        string    `json:"name"`
	Maker       string    `json:"maker,omitempty"`
	Category    string    `json:"category,omitempty"`
	Spec        string    `json:"spec,omitempty"`
	MinPriceKRW *int64    `json:"min_price_krw"`
	URL         string    `json:"url"`
	Variants    []Variant `json:"variants,omitempty"`
}

type Variant struct {
	PCode     string `json:"pcode"`
	Label     string `json:"label,omitempty"`
	MallCount *int64 `json:"mall_count"`
	PriceKRW  *int64 `json:"price_krw"`
	Status    string `json:"status,omitempty"` // 가격 대신 적힌 글(예: 단종·일시품절)
}

// Listing은 가격비교에 묶이지 않은 쇼핑몰 개별 상품이다. 링크는 다나와 bridge(robots Disallow)라 따라가지 않는다.
type Listing struct {
	Order       int    `json:"order"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Mall        string `json:"mall,omitempty"`
	PriceKRW    *int64 `json:"price_krw"`
	ShippingKRW *int64 `json:"shipping_krw"`
}

// Product는 가격비교 상품 페이지(prod.danawa.com/info/?pcode=…)의 추출 결과다.
type Product struct {
	PCode           string `json:"pcode"`
	URL             string `json:"url"` // canonical
	Name            string `json:"name"`
	Brand           string `json:"brand,omitempty"`
	Maker           string `json:"maker,omitempty"`
	Spec            string `json:"spec,omitempty"`
	RegisteredMonth string `json:"registered_month,omitempty"`
	OfferCount      *int64 `json:"offer_count"`

	// 최저가 요약. LowPrice(구조화 데이터)와 MinPrice는 배송비 별도, MinPriceWithShipping은 배송비 포함.
	LowPriceKRW             *int64 `json:"low_price_krw"`
	MinPriceKRW             *int64 `json:"min_price_krw"`
	MinPriceWithShippingKRW *int64 `json:"min_price_with_shipping_krw"`
	MinPriceNote            string `json:"min_price_note,omitempty"` // 예: "133,990+3,000원"
	Summary                 string `json:"summary,omitempty"`        // og:description 예: "최저가 133,990원, 현금최저가: 156,000원"

	// Malls는 정적 HTML에 실린 '쇼핑몰별 최저가' 목록이다(전체 판매처가 아니라 상위 일부).
	// MallPricesIncludeShipping이 true면 PriceKRW는 배송비 포함 표시가다.
	MallPricesIncludeShipping bool        `json:"mall_prices_include_shipping"`
	Malls                     []MallOffer `json:"malls"`
}

type MallOffer struct {
	Rank         int    `json:"rank"`
	Mall         string `json:"mall"`
	PriceKRW     *int64 `json:"price_krw"`      // 화면 표시가
	BasePriceKRW *int64 `json:"base_price_krw"` // 페이지가 상품가를 따로 실은 경우만
	ShippingKRW  *int64 `json:"shipping_krw"`   // "무료배송"=0, "3,000원"=3000, 그 밖에는 nil
	ShippingText string `json:"shipping_text,omitempty"`
	Lowest       bool   `json:"lowest"`
	Link         string `json:"link,omitempty"` // 다나와 bridge 링크(수집기는 따라가지 않는다)
}

var (
	errNotUTF8   = errors.New("UTF-8이 아닌 페이지")
	reSpace      = regexp.MustCompile(`\s+`)
	reSlash      = regexp.MustCompile(`\s*/\s*`)
	rePCode      = regexp.MustCompile(`[?&]pcode=(\d+)`)
	reCatalogID  = regexp.MustCompile(`^productItem(\d+)$`)
	reListingID  = regexp.MustCompile(`^productItem([A-Za-z0-9]+_[A-Za-z0-9_-]+)$`)
	reMallCount  = regexp.MustCompile(`(\d[\d,]*)\s*몰`)
	reRegistered = regexp.MustCompile(`등록월\s*:\s*(\S+)`)
)

func load(html []byte) (*goquery.Document, error) {
	if !utf8.Valid(html) {
		return nil, errNotUTF8
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(html))
	if err != nil {
		return nil, err
	}
	if cs := charset(doc); cs != "" && cs != "utf-8" && cs != "utf8" {
		return nil, fmt.Errorf("%w: charset=%s", errNotUTF8, cs)
	}
	return doc, nil
}

func charset(doc *goquery.Document) string {
	if v, ok := doc.Find("meta[charset]").First().Attr("charset"); ok {
		return strings.ToLower(strings.TrimSpace(v))
	}
	v, _ := doc.Find(`meta[http-equiv="Content-Type"]`).First().Attr("content")
	if _, cs, ok := strings.Cut(strings.ToLower(v), "charset="); ok {
		return strings.TrimSpace(cs)
	}
	return ""
}

// text는 공백을 한 칸으로 줄인 텍스트다.
func text(s *goquery.Selection) string {
	return strings.TrimSpace(reSpace.ReplaceAllString(s.Text(), " "))
}

// specText는 "모니터/ 60.4cm…" 같은 스펙 나열을 "모니터 / 60.4cm …"로 고른다.
func specText(s *goquery.Selection) string {
	return reSlash.ReplaceAllString(text(s), " / ")
}

// parseKRW는 "133,990원"·"133990" 같은 표기를 원 단위 정수로 읽는다. 숫자만 남지 않으면 nil.
func parseKRW(s string) *int64 {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "원")
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

// parseShipping: "무료배송"=0, "배송비 3,000원"·"3,000원"=3000, 그 밖(착불·조건부 등)은 nil.
func parseShipping(s string) *int64 {
	s = strings.TrimSpace(s)
	if s == "무료배송" || s == "무료" {
		zero := int64(0)
		return &zero
	}
	return parseKRW(strings.TrimSpace(strings.TrimPrefix(s, "배송비")))
}

func pcodeOf(href string) string {
	if m := rePCode.FindStringSubmatch(href); m != nil {
		return m[1]
	}
	return ""
}

// ParseSearch는 search.danawa.com/dsearch.php 결과를 읽는다. 광고 영역(adSmartArea…)은 건너뛴다.
func ParseSearch(html []byte) (*SearchResult, error) {
	doc, err := load(html)
	if err != nil {
		return nil, err
	}
	res := &SearchResult{Catalog: []CatalogItem{}, Listings: []Listing{}}
	list := doc.Find("ul.product_list").First()
	res.ListFound = list.Length() > 0
	list.Find("li.prod_item").Each(func(_ int, li *goquery.Selection) {
		id, _ := li.Attr("id")
		order, _ := strconv.Atoi(li.AttrOr("data-product-order", ""))
		if m := reCatalogID.FindStringSubmatch(id); m != nil {
			res.Catalog = append(res.Catalog, parseCatalogItem(li, m[1], order))
		} else if m := reListingID.FindStringSubmatch(id); m != nil {
			res.Listings = append(res.Listings, parseListing(li, m[1], order))
		}
	})
	return res, nil
}

func parseCatalogItem(li *goquery.Selection, pcode string, order int) CatalogItem {
	it := CatalogItem{
		Order: order,
		PCode: pcode,
		Name:  text(li.Find("p.prod_name a").First()),
		URL:   ProductURL(pcode),
	}
	it.Maker = li.Find("[data-maker-name]").First().AttrOr("data-maker-name", "")
	// id에 쓰인 pcode는 숫자뿐이라 선택자에 그대로 넣어도 안전하다.
	it.Category = li.Find("#productItem_categoryInfo_"+pcode).AttrOr("value", "")
	it.MinPriceKRW = parseKRW(li.Find("#min_price_"+pcode).AttrOr("value", ""))
	spec := li.Find(".spec-box--full .spec_list").First()
	if spec.Length() == 0 {
		spec = li.Find(".spec_list").First()
	}
	it.Spec = specText(spec)
	li.Find(".prod_pricelist li").Each(func(_ int, row *goquery.Selection) {
		v := Variant{PCode: strings.TrimPrefix(row.AttrOr("id", ""), "productInfoDetail_")}
		if v.PCode == "" || strings.Trim(v.PCode, "0123456789") != "" {
			v.PCode = pcodeOf(row.Find(".price_sect a").AttrOr("href", ""))
		}
		v.Label = text(row.Find(".memory_sect .text"))
		if m := reMallCount.FindStringSubmatch(text(row.Find(".chk_sect"))); m != nil {
			v.MallCount = parseKRW(m[1])
		}
		priceText := text(row.Find(".price_sect strong").First())
		if v.PriceKRW = parseKRW(priceText); v.PriceKRW == nil {
			v.Status = priceText
		}
		if v.PCode != "" {
			it.Variants = append(it.Variants, v)
		}
	})
	return it
}

func parseListing(li *goquery.Selection, id string, order int) Listing {
	l := Listing{Order: order, ID: id, Name: text(li.Find("p.prod_name a").First())}
	row := li.Find(".prod_pricelist li").First()
	l.Mall = strings.TrimSpace(row.Find(".mall_icon img").AttrOr("alt", ""))
	if l.Mall == "" {
		l.Mall = text(row.Find(".mall_icon"))
	}
	l.PriceKRW = parseKRW(text(row.Find(".price_sect strong").First()))
	if ship := row.Find(".ship_sect"); ship.Length() > 0 {
		l.ShippingKRW = parseShipping(text(ship))
	}
	return l
}

// ldProduct는 schema.org Product 구조화 데이터 중 쓰는 필드다(숫자·문자열이 섞여 와도 받는다).
type ldProduct struct {
	Type        any    `json:"@type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SKU         any    `json:"sku"`
	Brand       struct {
		Name string `json:"name"`
	} `json:"brand"`
	Offers struct {
		OfferCount any    `json:"offerCount"`
		LowPrice   any    `json:"lowPrice"`
		URL        string `json:"url"`
	} `json:"offers"`
}

func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return ""
	}
}

// ParseProduct는 prod.danawa.com/info/?pcode=… 상품 페이지를 읽는다.
// 상품명과 pcode를 둘 다 못 찾으면 구조가 바뀐 것으로 보고 오류를 돌려준다.
func ParseProduct(html []byte) (*Product, error) {
	doc, err := load(html)
	if err != nil {
		return nil, err
	}
	p := &Product{Malls: []MallOffer{}}

	doc.Find(`script[type="application/ld+json"]`).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		dec := json.NewDecoder(strings.NewReader(s.Text()))
		dec.UseNumber()
		var ld ldProduct
		if dec.Decode(&ld) != nil || anyString(ld.Type) != "Product" {
			return true
		}
		p.Name = strings.TrimSpace(ld.Name)
		p.Spec = reSlash.ReplaceAllString(strings.TrimSpace(ld.Description), " / ")
		p.PCode = anyString(ld.SKU)
		p.Brand = strings.TrimSpace(ld.Brand.Name)
		p.OfferCount = parseKRW(anyString(ld.Offers.OfferCount))
		p.LowPriceKRW = parseKRW(anyString(ld.Offers.LowPrice))
		return false
	})

	if href, ok := doc.Find(`link[rel="canonical"]`).First().Attr("href"); ok {
		p.URL = strings.TrimSpace(href)
		if c := pcodeOf(href); c != "" {
			p.PCode = c
		}
	}
	if p.Name == "" {
		og := doc.Find(`meta[property="og:title"]`).AttrOr("content", "")
		p.Name = strings.TrimSpace(strings.TrimPrefix(og, "[다나와]"))
	}
	if p.PCode == "" && p.Name == "" {
		return nil, errors.New("상품 페이지에서 상품명·pcode를 못 찾았다(페이지 구조 변경 가능)")
	}
	if p.URL == "" && p.PCode != "" {
		p.URL = ProductURL(p.PCode)
	}
	p.Summary = strings.TrimSpace(doc.Find(`meta[property="og:description"]`).AttrOr("content", ""))
	p.Maker = text(doc.Find("#makerTxtArea a").First())
	doc.Find(".made_info .txt").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if m := reRegistered.FindStringSubmatch(text(s)); m != nil {
			p.RegisteredMonth = m[1]
			return false
		}
		return true
	})

	sum := doc.Find(".price-summary").First()
	p.MinPriceKRW = parseKRW(sum.Find("#intMinFee").AttrOr("value", ""))
	p.MinPriceWithShippingKRW = parseKRW(sum.Find("#intIncludeDeliveryFee").AttrOr("value", ""))
	p.MinPriceNote = strings.TrimSpace(sum.Find("#strDeliveryFee").AttrOr("value", ""))

	box := doc.Find(".box__mall-price").First()
	p.MallPricesIncludeShipping = box.HasClass("include-delivery")
	box.Find("ul.list__mall-price > li.list-item").Each(func(i int, li *goquery.Selection) {
		m := MallOffer{Rank: i + 1}
		logo := li.Find(".box__logo").First()
		m.Mall = strings.TrimSpace(logo.Find("img").AttrOr("alt", ""))
		if m.Mall == "" {
			m.Mall = text(logo.Find(".text__logo"))
		}
		price := li.Find(".box__price").First()
		m.Lowest = price.HasClass("lowest")
		sell := price.Find(".sell-price").First()
		m.PriceKRW = parseKRW(text(sell.Find(".text__num")))
		m.BasePriceKRW = parseKRW(sell.AttrOr("data-base-price", ""))
		// 배송 칸에는 '빠른배송' 안내 툴팁이 함께 있어 그 부분을 빼고 읽는다.
		del := li.Find(".box__delivery").First().Clone()
		del.Find(".box__fast-notice").Remove()
		m.ShippingText = text(del)
		if v, ok := sell.Attr("data-delivery-price"); ok {
			m.ShippingKRW = parseKRW(v)
		} else {
			m.ShippingKRW = parseShipping(m.ShippingText)
		}
		m.Link = li.Find("a.link__full-cover").AttrOr("href", "")
		p.Malls = append(p.Malls, m)
	})
	return p, nil
}
