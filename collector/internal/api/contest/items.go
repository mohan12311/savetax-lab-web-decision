package contest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"savetax-lab/collector/internal/httpx"
)

// 경로에 넣는 번호·키는 미리 모양을 확인한다(경로 조작 방지, 서버 규칙과 같은 범위).
var (
	idRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	keyRe = regexp.MustCompile(`^[\x21-\x7e]{1,128}$`) // 공백 없는 인쇄 가능 ASCII
)

// Item은 GET …/items/{item_id}(물품 상세)다. 첫 호출 때 서버가 스냅샷을 만들어 고정한다.
// 금액·수량은 서버가 십진 문자열로 준다.
type Item struct {
	ItemID                string       `json:"item_id"`
	ResolvedFromListingID string       `json:"resolved_from_listing_id,omitempty"`
	Snapshot              ItemSnapshot `json:"snapshot"`
	Availability          struct {
		State   string `json:"state"`
		Message string `json:"message"`
	} `json:"availability"`
	Facts []Fact `json:"facts"`
}

type ItemSnapshot struct {
	Name           string `json:"name"`
	Model          string `json:"model"`
	Manufacturer   string `json:"manufacturer"`
	Category       string `json:"category"`
	SourceRelease  string `json:"source_release"`
	FetchedAt      string `json:"fetched_at"`
	FactsTotal     int    `json:"facts_total"`
	FactsTruncated bool   `json:"facts_truncated"`
	Listings       []struct {
		ListingID    string `json:"listing_id"`
		Vendor       string `json:"vendor"`
		UnitPriceKRW string `json:"unit_price_krw"`
	} `json:"listings"`
}

// Fact는 최종 차수 납품 라인 하나다. 제출 line의 unit·tax_basis·comparison_basis_id는
// 여기 Unit·TaxBasis·BasisID를 글자 그대로 쓴다.
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

// Q는 채점 수량이다(effective_quantity가 있으면 그것, 없으면 quantity).
func (f Fact) Q() string {
	if f.EffectiveQuantity != "" {
		return f.EffectiveQuantity
	}
	return f.Quantity
}

// Item은 물품 상세를 가져온다. 원문도 함께 돌려준다(bronze 저장용).
// 서버가 처음 보는 물품이면 스냅샷을 만들고 "새 물품 조회" 한도(429 RATE_LIMITED)에 걸릴 수 있다.
func (c *Client) Item(ctx context.Context, itemID string) (*Item, *httpx.Response, error) {
	if !idRe.MatchString(itemID) {
		return nil, nil, fmt.Errorf("물품번호 형식이 아니다: %q", itemID)
	}
	var it Item
	resp, err := c.h.GetJSON(ctx, "/v1/events/current/items/"+url.PathEscape(itemID), nil, true, &it)
	if err != nil {
		return nil, resp, err
	}
	return &it, resp, nil
}

// Preview는 POST …/submissions/preview 결과다. ok는 형식·참조·산식 통과일 뿐, 등록 가능 여부는 NextAction을 본다.
type Preview struct {
	OK             bool   `json:"ok"`
	TotalAmountKRW NumStr `json:"total_amount_krw"`
	Lines          []struct {
		FactID         string `json:"fact_id"`
		ScoreAmountKRW NumStr `json:"score_amount_krw"`
	} `json:"lines"`
	ProductAlreadyClaimed   bool            `json:"product_already_claimed"`
	ClaimedByMe             bool            `json:"claimed_by_me"`
	ExistingSubmissionID    string          `json:"existing_submission_id"`
	ExistingSubmissionState string          `json:"existing_submission_state"`
	CurrentRevision         *int            `json:"current_revision"`
	NextAction              string          `json:"next_action"` // submit | revise_existing | pick_another_product
	PriceGap                json.RawMessage `json:"price_gap"`
	LintNotice              json.RawMessage `json:"lint_notice"`
}

// Preview는 본문을 검사만 한다(선점·쓰기 없음, Idempotency-Key 불필요).
func (c *Client) Preview(ctx context.Context, body []byte) (*Preview, *httpx.Response, error) {
	resp, err := c.h.Do(ctx, &httpx.Request{Method: http.MethodPost, Path: "/v1/events/current/submissions/preview", Body: body, Auth: true})
	if err != nil {
		return nil, nil, err
	}
	var p Preview
	if err := json.Unmarshal(resp.Body, &p); err != nil {
		return nil, resp, fmt.Errorf("preview 응답 해석 실패: %w", err)
	}
	return &p, resp, nil
}

// Receipt는 제출 201 영수증이다(접수 시점의 모습).
type Receipt struct {
	SubmissionID       string `json:"submission_id"`
	ClaimID            string `json:"claim_id"`
	Revision           int    `json:"revision"`
	SubmissionState    string `json:"submission_state"`
	AdmittedAt         string `json:"admitted_at"`
	AmountKRW          NumStr `json:"amount_krw"`
	VerificationStatus string `json:"verification_status"`
	LintStatus         string `json:"lint_status"`
	APIPath            string `json:"api_path"`
	WebPath            string `json:"web_path"`
}

// Submit은 POST …/submissions다(접수되면 제출 횟수를 쓴다). key는 요청마다 새 UUID이고,
// 응답을 못 받았을 때만 같은 key·같은 본문으로 다시 보낸다(httpx가 네트워크 오류·5xx를 그렇게 재시도한다).
func (c *Client) Submit(ctx context.Context, body []byte, key string) (*Receipt, *httpx.Response, error) {
	if !keyRe.MatchString(key) {
		return nil, nil, fmt.Errorf("Idempotency-Key 형식이 아니다")
	}
	h := http.Header{}
	h.Set("Idempotency-Key", key)
	resp, err := c.h.Do(ctx, &httpx.Request{Method: http.MethodPost, Path: "/v1/events/current/submissions", Body: body, Header: h, Auth: true})
	if err != nil {
		return nil, nil, err
	}
	var r Receipt
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, resp, fmt.Errorf("영수증 해석 실패(접수는 됐을 수 있다 — submission-requests로 확인): %w", err)
	}
	return &r, resp, nil
}

// Revise는 POST …/submissions/{sid}/revisions다. 내 제출에 전체 새 본문을 올린다(물품은 바꿀 수 없고 제출 횟수는 쓰지 않는다).
// If-Match에는 지금 revision을 넣는다(다르면 409 REVISION_CONFLICT). 수정하면 자동 근거 검사가 다시 돈다.
func (c *Client) Revise(ctx context.Context, sid string, ifMatch int, body []byte, key string) (*Receipt, *httpx.Response, error) {
	if !idRe.MatchString(sid) {
		return nil, nil, fmt.Errorf("제출 번호 형식이 아니다: %q", sid)
	}
	if !keyRe.MatchString(key) {
		return nil, nil, fmt.Errorf("Idempotency-Key 형식이 아니다")
	}
	h := http.Header{}
	h.Set("Idempotency-Key", key)
	h.Set("If-Match", strconv.Itoa(ifMatch))
	resp, err := c.h.Do(ctx, &httpx.Request{Method: http.MethodPost, Path: "/v1/events/current/submissions/" + url.PathEscape(sid) + "/revisions",
		Body: body, Header: h, Auth: true})
	if err != nil {
		return nil, nil, err
	}
	var r Receipt
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, resp, fmt.Errorf("수정 영수증 해석 실패(반영됐을 수 있다 — submission-requests로 확인): %w", err)
	}
	return &r, resp, nil
}

// SubmissionRequest는 GET …/submission-requests/{key}다. 200 영수증, 202 확정 전, 404는 오류(접수된 것 없음).
func (c *Client) SubmissionRequest(ctx context.Context, key string) (*httpx.Response, error) {
	if !keyRe.MatchString(key) {
		return nil, fmt.Errorf("Idempotency-Key 형식이 아니다")
	}
	return c.h.Do(ctx, &httpx.Request{Path: "/v1/events/current/submission-requests/" + url.PathEscape(key), Auth: true})
}

// Lint는 제출 상세의 자동 근거 검사 결과다(제출자 본인에게만 붙는다).
type Lint struct {
	Status          string `json:"status"` // pending | clean | suspect | unreadable
	CheckedRevision *int   `json:"checked_revision"`
	CheckedAt       string `json:"checked_at"`
	Lines           []struct {
		FactID  string `json:"fact_id"`
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	} `json:"lines"`
	Hints []struct {
		FactID     string `json:"fact_id"`
		Role       string `json:"role"`
		EvidenceNo *int   `json:"evidence_no"`
		Code       string `json:"code"`
		Message    string `json:"message"`
	} `json:"hints"`
}

// Submission은 GET …/submissions/{sid}에서 쓰는 필드만 읽는다. 원문은 bronze에 남긴다.
type Submission struct {
	SubmissionID    string `json:"submission_id"`
	SubmissionState string `json:"submission_state"`
	CurrentRevision *int   `json:"current_revision"`
	AmountKRW       NumStr `json:"amount_krw"`
	LintStatus      string `json:"lint_status"`
	Lint            *Lint  `json:"lint"`
}

func (c *Client) Submission(ctx context.Context, sid string) (*Submission, *httpx.Response, error) {
	if !idRe.MatchString(sid) {
		return nil, nil, fmt.Errorf("제출 번호 형식이 아니다: %q", sid)
	}
	var s Submission
	resp, err := c.h.GetJSON(ctx, "/v1/events/current/submissions/"+url.PathEscape(sid), nil, true, &s)
	if err != nil {
		return nil, resp, err
	}
	return &s, resp, nil
}

// NumStr는 금액을 문자열로 받는다. 서버가 필드에 따라 "57000"(문자열)이나 57000(숫자)으로 주므로 둘 다 받는다
// (preview의 lines[].score_amount_krw는 숫자로 온다 — 10/16 실측). 숫자도 float을 거치지 않고 원문 그대로 옮긴다.
type NumStr string

func (n *NumStr) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case string(b) == "null":
		*n = ""
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*n = NumStr(s)
	default:
		var num json.Number
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if err := dec.Decode(&num); err != nil {
			return fmt.Errorf("금액이 문자열·숫자가 아니다: %s", b)
		}
		*n = NumStr(num.String())
	}
	return nil
}
