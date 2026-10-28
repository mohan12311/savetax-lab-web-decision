package main

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 합성 데이터(studio/synth 출력)의 모양. 정답(cases.jsonl)은 읽지 않는다 — 가짜 서버가 정답을 알면
// 에이전트 평가가 서버 응답을 통해 정답을 엿볼 수 있기 때문이다.

// Fact는 대회 items/{item_id}의 납품 라인과 같은 모양이다. 금액·수량은 십진 문자열이다.
type Fact struct {
	FactID              string   `json:"fact_id"`
	ListingID           string   `json:"listing_id"`
	ContractNo          string   `json:"contract_no"`
	RequestNo           string   `json:"request_no"`
	LineNo              string   `json:"line_no"`
	RequestDate         string   `json:"request_date"`
	AgencyName          string   `json:"agency_name"`
	Unit                string   `json:"unit"`
	TaxBasis            string   `json:"tax_basis"`
	BasisID             string   `json:"basis_id"`
	ComparisonBasisID   string   `json:"comparison_basis_id"`
	PeriodKind          string   `json:"period_kind"`
	UnitPriceKRW        string   `json:"unit_price_krw"`
	Quantity            string   `json:"quantity"`
	EffectiveQuantity   string   `json:"effective_quantity,omitempty"`
	ComparableAmountKRW string   `json:"comparable_amount_krw"`
	Scorable            bool     `json:"scorable"`
	ExclusionReasons    []string `json:"exclusion_reasons"`
}

// Q는 채점 수량이다(effective_quantity가 있으면 그것).
func (f Fact) Q() string {
	if f.EffectiveQuantity != "" {
		return f.EffectiveQuantity
	}
	return f.Quantity
}

type Listing struct {
	ListingID    string `json:"listing_id"`
	Vendor       string `json:"vendor"`
	UnitPriceKRW string `json:"unit_price_krw"`
}

type Snapshot struct {
	Name           string    `json:"name"`
	Model          string    `json:"model"`
	Manufacturer   string    `json:"manufacturer"`
	Category       string    `json:"category"`
	SourceRelease  string    `json:"source_release"`
	FetchedAt      string    `json:"fetched_at"`
	FactsTotal     int       `json:"facts_total"`
	FactsTruncated bool      `json:"facts_truncated"`
	Listings       []Listing `json:"listings"`
}

// Item은 items.json 한 행이다. LookupState는 처음 상태(available | claimed | reform_found)다.
type Item struct {
	ItemID      string   `json:"item_id"`
	Snapshot    Snapshot `json:"snapshot"`
	Facts       []Fact   `json:"facts"`
	LookupState string   `json:"lookup_state"`
}

// Page는 가짜 판매처의 상품 페이지 하나다(shop/pages.json + shop/html/<sku>.html).
// VATIncluded가 nil이면 페이지에 가격이 없다(견적 문의 등).
type Page struct {
	SKU         string `json:"sku"`
	Path        string `json:"path"`
	Render      string `json:"render"` // static | spa
	Title       string `json:"title"`
	VATIncluded *bool  `json:"vat_included"`
	HTMLFile    string `json:"html_file"`

	html []byte
	text string // 태그를 걷어 낸 본문 글자(판정기가 읽는 것)
}

type Manifest struct {
	Generator       string `json:"generator"`
	Version         string `json:"version"`
	Seed            int64  `json:"seed"`
	ReleaseID       string `json:"dataset_release_id"`
	RulesetVersion  string `json:"ruleset_version"`
	ShopBaseURL     string `json:"shop_base_url"`
	ObservedAt      string `json:"observed_at"`
	ItemCount       int    `json:"item_count"`
	PageCount       int    `json:"page_count"`
	ReferenceClaims int    `json:"reference_claims"`
}

type Catalog struct {
	Manifest Manifest
	Items    map[string]*Item
	Order    []string
	Pages    map[string]*Page // path → page
}

func loadCatalog(dir string) (*Catalog, error) {
	c := &Catalog{Items: map[string]*Item{}, Pages: map[string]*Page{}}
	if err := readJSON(filepath.Join(dir, "manifest.json"), &c.Manifest); err != nil {
		return nil, err
	}
	var items []*Item
	if err := readJSON(filepath.Join(dir, "items.json"), &items); err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.ItemID == "" || c.Items[it.ItemID] != nil {
			return nil, fmt.Errorf("items.json: 물품번호가 비었거나 겹친다: %q", it.ItemID)
		}
		switch it.LookupState {
		case "available", "claimed", "reform_found":
		default:
			return nil, fmt.Errorf("items.json: %s의 lookup_state %q를 모른다", it.ItemID, it.LookupState)
		}
		c.Items[it.ItemID] = it
		c.Order = append(c.Order, it.ItemID)
	}
	var pages []*Page
	if err := readJSON(filepath.Join(dir, "shop", "pages.json"), &pages); err != nil {
		return nil, err
	}
	for _, p := range pages {
		if !strings.HasPrefix(p.Path, "/") || c.Pages[p.Path] != nil {
			return nil, fmt.Errorf("pages.json: 경로가 /로 시작하지 않거나 겹친다: %q", p.Path)
		}
		if strings.Contains(p.HTMLFile, "..") || filepath.IsAbs(p.HTMLFile) {
			return nil, fmt.Errorf("pages.json: html_file 경로가 이상하다: %q", p.HTMLFile)
		}
		b, err := os.ReadFile(filepath.Join(dir, "shop", p.HTMLFile))
		if err != nil {
			return nil, err
		}
		p.html = b
		p.text = visibleText(string(b))
		c.Pages[p.Path] = p
	}
	return c, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

var (
	scriptRe = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)>`)
	tagRe    = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

// visibleText는 판정기가 읽는 본문 글자다. 스크립트·스타일을 빼고 태그를 공백으로 바꾼다.
// display:none 같은 숨김은 따지지 않는다(정적 HTML 판정기는 CSS를 적용하지 않는다).
func visibleText(s string) string {
	s = scriptRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}
