package danawa

import (
	"bytes"
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// 2026-10-15부터 통합검색이 Next.js 앱으로 바뀌어(ul.product_list 없음) 상품 목록이 HTML 태그가 아니라
// self.__next_f.push([1,"…"]) 조각 안의 react-query 상태(queryKey ["search","productList",…])에 JSON으로 실린다.
// 응답 HTML 안에 있는 값이라 정적 페이지로 읽을 수 있다. 상품 페이지(prod.danawa.com/info)는 그대로다.

var (
	reFlightChunk = regexp.MustCompile(`self\.__next_f\.push\(\[1,("(?:[^"\\]|\\.)*")\]\)`)
	reTag         = regexp.MustCompile(`<[^>]*>`)
	reQueries     = regexp.MustCompile(`"queries"\s*:\s*\[`)
)

const (
	flightCatalog = 1 // 가격비교 상품(숫자 pcode)
	flightListing = 3 // 쇼핑몰 개별 상품(id "몰코드_상품번호")
	flightPriced  = "가격비교"
)

// flightInt는 숫자·숫자 문자열·null을 모두 받는다(앱 데이터라 타입이 섞여 올 수 있다).
type flightInt struct{ v *int64 }

func (f *flightInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		f.v = &v
	}
	return nil
}

// positive는 0 이하를 nil로 본다 — 앱은 가격이 없을 때(단종 등) 0을 싣는다.
func (f flightInt) positive() *int64 {
	if f.v == nil || *f.v <= 0 {
		return nil
	}
	return f.v
}

type flightQuery struct {
	QueryKey []json.RawMessage `json:"queryKey"`
	State    struct {
		Data struct {
			Data json.RawMessage `json:"data"`
		} `json:"data"`
	} `json:"state"`
}

type flightProduct struct {
	ProdType      int       `json:"prodType"`
	ProductSeq    string    `json:"productSeq"`
	ProductName   string    `json:"productName"`
	MakerName     string    `json:"makerName"`
	MinPrice      flightInt `json:"minPrice"`
	ShopCount     flightInt `json:"shopCount"`
	Status        string    `json:"priceCompareStatusType"`
	CategoryNames []string  `json:"categoryNameList"`
	Summary       *struct {
		Desc []string `json:"desc"`
	} `json:"summary"`
	Bundles []struct {
		ProductSeq string    `json:"productSeq"`
		BundleName string    `json:"bundleName"`
		MinPrice   flightInt `json:"minPrice"`
		ShopCount  flightInt `json:"shopCount"`
		Status     string    `json:"priceCompareStatusType"`
	} `json:"bundleList"`
	ShopName      string `json:"shopName"`
	PostPriceHTML string `json:"postPriceHtml"`
}

// plain은 앱이 검색어 강조로 넣은 <b> 태그와 HTML 엔티티를 지운다.
func plain(s string) string {
	return strings.TrimSpace(reSpace.ReplaceAllString(html.UnescapeString(reTag.ReplaceAllString(s, "")), " "))
}

// flightText는 페이지의 __next_f 조각을 이어 붙인 RSC 본문이다. 조각이 없으면 "".
func flightText(page []byte) string {
	var b strings.Builder
	for _, m := range reFlightChunk.FindAllSubmatch(page, -1) {
		var s string
		if json.Unmarshal(m[1], &s) == nil {
			b.WriteString(s)
		}
	}
	return b.String()
}

// parseSearchFlight는 새 검색 페이지를 읽는다. productList 쿼리가 없으면 ok=false(구조 변경 또는 다른 페이지).
// productList가 있는데 상품이 0개면 ok=true·빈 목록 — 다나와가 찾은 상품이 없다는 뜻이다.
func parseSearchFlight(page []byte) (res *SearchResult, ok bool) {
	flight := flightText(page)
	if flight == "" {
		return nil, false
	}
	for _, loc := range reQueries.FindAllStringIndex(flight, -1) {
		start := loc[1] - 1 // '['
		var qs []flightQuery
		if json.NewDecoder(strings.NewReader(flight[start:])).Decode(&qs) != nil {
			continue
		}
		for _, q := range qs {
			if len(q.QueryKey) < 3 || !rawIs(q.QueryKey[0], "search") || !rawIs(q.QueryKey[1], "productList") {
				continue
			}
			var data struct {
				Products []flightProduct `json:"products"`
			}
			if len(q.State.Data.Data) == 0 || json.Unmarshal(q.State.Data.Data, &data) != nil {
				continue
			}
			var key struct {
				Keyword  string `json:"keyword"`
				Original string `json:"originalKeyword"`
			}
			_ = json.Unmarshal(q.QueryKey[2], &key)
			res = &SearchResult{ListFound: true, Layout: LayoutFlight, Catalog: []CatalogItem{}, Listings: []Listing{}}
			if key.Keyword != "" && key.Keyword != key.Original {
				res.SearchedKeyword = key.Keyword
			}
			for n, p := range data.Products {
				order := n + 1
				switch p.ProdType {
				case flightCatalog:
					if ValidPCode(p.ProductSeq) {
						res.Catalog = append(res.Catalog, catalogFromFlight(p, order))
					}
				case flightListing:
					res.Listings = append(res.Listings, Listing{
						Order: order, ID: p.ProductSeq, Name: plain(p.ProductName), Mall: p.ShopName,
						PriceKRW: p.MinPrice.positive(), ShippingKRW: parseShipping(plain(p.PostPriceHTML)),
					})
				}
			}
			return res, true
		}
	}
	return nil, false
}

func catalogFromFlight(p flightProduct, order int) CatalogItem {
	it := CatalogItem{
		Order:       order,
		PCode:       p.ProductSeq,
		Name:        plain(p.ProductName),
		Maker:       p.MakerName,
		Category:    strings.Join(p.CategoryNames, "_"),
		MinPriceKRW: p.MinPrice.positive(),
		URL:         ProductURL(p.ProductSeq),
	}
	if p.Status != "" && p.Status != flightPriced {
		it.Status = p.Status
	}
	if p.Summary != nil {
		var parts []string
		for _, d := range p.Summary.Desc {
			if d = plain(d); d != "" {
				parts = append(parts, d)
			}
		}
		it.Spec = strings.Join(parts, " / ")
	}
	for _, b := range p.Bundles {
		if !ValidPCode(b.ProductSeq) {
			continue
		}
		v := Variant{PCode: b.ProductSeq, Label: plain(b.BundleName), MallCount: b.ShopCount.v, PriceKRW: b.MinPrice.positive()}
		if b.Status != "" && b.Status != flightPriced {
			v.Status = b.Status
		}
		it.Variants = append(it.Variants, v)
	}
	return it
}

func rawIs(raw json.RawMessage, want string) bool {
	var s string
	return json.Unmarshal(bytes.TrimSpace(raw), &s) == nil && s == want
}
