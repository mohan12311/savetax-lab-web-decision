// Package naver는 네이버 쇼핑 검색 API(https://openapi.naver.com/v1/search/shop.json)로 시중가 후보를 찾는다.
//
//   - 공식 오픈 API라 robots.txt 대상이 아니다. 쇼핑·스마트스토어 페이지는 robots가 막아 직접 받지 않는다(10/16 확인).
//   - 인증은 X-Naver-Client-Id·X-Naver-Client-Secret 헤더. 키는 환경변수(NAVER_CLIENT_ID·NAVER_CLIENT_SECRET)로만
//     받고 로그·파일·명령줄에 남기지 않는다. 참가 토큰은 보내지 않는다.
//   - 하루 25,000회. 인증 실패(401)·권한 없음(403)·한도 초과(429)는 계속해도 같으므로 호출자가 실행을 멈춘다.
//
// 결과는 탐색용이다. 링크(스마트스토어·쿠팡 등)는 대회 근거 검사가 못 읽을 수 있어, 근거는 정적 판매 페이지에서 따로 만든다.
package naver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"savetax-lab/collector/internal/httpx"
)

const (
	BaseURL    = "https://openapi.naver.com"
	Host       = "openapi.naver.com"
	SearchPath = "/v1/search/shop.json"
	MaxDisplay = 100
	// 중고·렌탈·해외직구는 같은 조건의 시중가가 아니라 뺀다.
	Exclude = "used:rental:cbshop"
)

// Keys는 애플리케이션 인증 정보다. String은 값을 숨긴다(실수로 출력돼도 새지 않게).
type Keys struct {
	ID     string
	Secret string
}

func (k Keys) String() string { return "naver.Keys{«redacted»}" }

type Client struct {
	h    *httpx.Client
	keys Keys
}

// New는 토큰 없는 httpx 클라이언트를 받는다. 참가 토큰이 든 클라이언트면 거부한다.
func New(h *httpx.Client, k Keys) (*Client, error) {
	if k.ID == "" || k.Secret == "" {
		return nil, errors.New("네이버 키가 비었다(NAVER_CLIENT_ID·NAVER_CLIENT_SECRET)")
	}
	if h.HasToken() {
		return nil, errors.New("네이버 클라이언트에 참가 토큰이 들어 있다")
	}
	return &Client{h: h, keys: k}, nil
}

// Params는 한 검색의 요청 인자다(bronze 봉투에 그대로 남긴다).
func Params(query string, display int) url.Values {
	return url.Values{
		"query":   {query},
		"display": {strconv.Itoa(display)},
		"start":   {"1"},
		"sort":    {"sim"},
		"exclude": {Exclude},
	}
}

// Search는 검색 결과 원문(JSON)과 해석한 결과를 돌려준다. 원문은 bronze에 그대로 보관한다.
func (c *Client) Search(ctx context.Context, query string, display int) (*httpx.Response, *SearchResult, error) {
	if display < 1 || display > MaxDisplay {
		return nil, nil, fmt.Errorf("display는 1~%d: %d", MaxDisplay, display)
	}
	resp, err := c.h.Do(ctx, &httpx.Request{
		Method: http.MethodGet,
		Path:   SearchPath,
		Query:  Params(query, display),
		Header: http.Header{
			"X-Naver-Client-Id":     {c.keys.ID},
			"X-Naver-Client-Secret": {c.keys.Secret},
		},
	})
	if err != nil {
		return nil, nil, err
	}
	res, err := Parse(resp.Body)
	return resp, res, err
}

// Fatal은 계속 보내도 같은 결과일 오류(인증·권한·하루 한도)인지 본다.
func Fatal(err error) bool {
	ae, ok := httpx.AsAPIError(err)
	if !ok {
		return false
	}
	switch ae.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return false
}

// Item은 상품 한 줄이다. 금액은 정수 원, 빈 값은 nil(0으로 바꾸지 않는다).
type Item struct {
	Title        string   `json:"title"` // <b> 강조 태그·HTML 엔티티를 푼 값
	Link         string   `json:"link"`
	LowPriceKRW  *int64   `json:"lprice_krw"`
	HighPriceKRW *int64   `json:"hprice_krw"`
	Mall         string   `json:"mall"`
	ProductID    string   `json:"product_id"`
	ProductType  string   `json:"product_type"`
	Brand        string   `json:"brand,omitempty"`
	Maker        string   `json:"maker,omitempty"`
	Category     []string `json:"category,omitempty"`
}

type SearchResult struct {
	Total   int64  `json:"total"`
	Start   int    `json:"start"`
	Display int    `json:"display"`
	Items   []Item `json:"items"`
}

type rawItem struct {
	Title       string `json:"title"`
	Link        string `json:"link"`
	LPrice      string `json:"lprice"`
	HPrice      string `json:"hprice"`
	MallName    string `json:"mallName"`
	ProductID   string `json:"productId"`
	ProductType string `json:"productType"`
	Brand       string `json:"brand"`
	Maker       string `json:"maker"`
	Category1   string `json:"category1"`
	Category2   string `json:"category2"`
	Category3   string `json:"category3"`
	Category4   string `json:"category4"`
}

var reTag = regexp.MustCompile(`</?b>`)

// Parse는 API 응답 JSON을 해석한다. 숫자가 아닌 가격은 nil로 둔다.
func Parse(body []byte) (*SearchResult, error) {
	var raw struct {
		Total   int64     `json:"total"`
		Start   int       `json:"start"`
		Display int       `json:"display"`
		Items   []rawItem `json:"items"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("네이버 응답 JSON 해석 실패: %w", err)
	}
	res := &SearchResult{Total: raw.Total, Start: raw.Start, Display: raw.Display, Items: make([]Item, 0, len(raw.Items))}
	for _, r := range raw.Items {
		it := Item{
			Title:        clean(r.Title),
			Link:         r.Link,
			LowPriceKRW:  price(r.LPrice),
			HighPriceKRW: price(r.HPrice),
			Mall:         clean(r.MallName),
			ProductID:    r.ProductID,
			ProductType:  r.ProductType,
			Brand:        clean(r.Brand),
			Maker:        clean(r.Maker),
		}
		for _, c := range []string{r.Category1, r.Category2, r.Category3, r.Category4} {
			if c = clean(c); c != "" {
				it.Category = append(it.Category, c)
			}
		}
		res.Items = append(res.Items, it)
	}
	return res, nil
}

func clean(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(reTag.ReplaceAllString(s, ""))), " ")
}

func price(s string) *int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || v <= 0 {
		return nil
	}
	return &v
}
