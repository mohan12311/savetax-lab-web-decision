// Package danawa는 다나와 가격비교의 정적 HTML(통합검색·상품 페이지)을 가져와 추출한다.
//
//   - 검색: https://search.danawa.com/dsearch.php?query=… (robots Crawl-delay 10초)
//   - 상품: https://prod.danawa.com/info/?pcode=… — 대회 근거 검사가 읽을 수 있는 정적 페이지라 근거 URL 후보다.
//   - /bridge/(쇼핑몰 이동)·/info/ajax/(전체 판매처 목록)는 robots Disallow라 쓰지 않는다.
//
// 추출 결과는 탐색·대조용이다. 제출 근거의 가격·인용은 보관한 원문에서 다시 확인한다.
package danawa

import (
	"context"
	"net/url"
	"regexp"

	"savetax-lab/collector/internal/scrape"
)

const (
	SearchBaseURL = "https://search.danawa.com"
	ProdBaseURL   = "https://prod.danawa.com"
)

var rePCodeOnly = regexp.MustCompile(`^\d{1,12}$`)

// ValidPCode는 pcode가 숫자만으로 된 상품 코드인지 본다(경로에 그대로 넣기 전 검사).
func ValidPCode(pcode string) bool { return rePCodeOnly.MatchString(pcode) }

func SearchPath(query string) string  { return "/dsearch.php?query=" + url.QueryEscape(query) }
func ProductPath(pcode string) string { return "/info/?pcode=" + pcode }
func ProductURL(pcode string) string  { return ProdBaseURL + ProductPath(pcode) }

// Client는 검색·상품 두 호스트를 묶는다. 각 Site가 자기 robots·리미터를 가진다.
type Client struct {
	search *scrape.Site
	prod   *scrape.Site
}

func New(search, prod *scrape.Site) *Client { return &Client{search: search, prod: prod} }

// Search는 검색 결과 페이지를 가져와 추출한다. 원문 페이지도 함께 돌려준다(bronze 보관용).
func (c *Client) Search(ctx context.Context, query string) (*scrape.Page, *SearchResult, error) {
	pg, err := c.search.Get(ctx, SearchPath(query))
	if err != nil {
		return nil, nil, err
	}
	res, err := ParseSearch(pg.Body)
	return pg, res, err
}

// Product는 상품 페이지를 가져와 추출한다.
func (c *Client) Product(ctx context.Context, pcode string) (*scrape.Page, *Product, error) {
	pg, err := c.prod.Get(ctx, ProductPath(pcode))
	if err != nil {
		return nil, nil, err
	}
	p, err := ParseProduct(pg.Body)
	return pg, p, err
}
