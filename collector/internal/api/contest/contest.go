// Package contest는 대회 API(hackathon.savetax.now) 클라이언트다.
// 응답은 래퍼 없는 snake_case, 오류는 평면 {code, message, request_id, retryable}.
package contest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"savetax-lab/collector/internal/httpx"
)

// Event는 GET /v1/events/current. 금액·비율은 서버가 문자열로 주므로 문자열로 둔다(float 금지).
type Event struct {
	EventID                string    `json:"event_id"`
	Slug                   string    `json:"slug"`
	Phase                  string    `json:"phase"`
	AdmissionState         string    `json:"admission_state"`
	ContestMode            string    `json:"contest_mode"`
	OpensAt                time.Time `json:"opens_at"`
	ClosesAt               time.Time `json:"closes_at"`
	ServerTime             time.Time `json:"server_time"`
	RulesetVersion         string    `json:"ruleset_version"`
	DatasetReleaseID       string    `json:"dataset_release_id"`
	ScoringVersion         string    `json:"scoring_version"`
	IdentityVersion        string    `json:"identity_version"`
	ScheduleVersion        int       `json:"schedule_version"`
	EventEpoch             int       `json:"event_epoch"`
	EvidenceFormat         string    `json:"evidence_format"`
	ItemCatalog            string    `json:"item_catalog"`
	ReleaseClaimOnWithdraw bool      `json:"release_claim_on_withdraw"`
	PriceGap               PriceGap  `json:"price_gap"`
	SubmissionCount        int64     `json:"submission_count"`
}

type PriceGap struct {
	Rule       string `json:"rule"`
	MinRatio   string `json:"min_ratio"`
	MinUnitKRW string `json:"min_unit_krw"`
}

type Client struct{ h *httpx.Client }

func New(h *httpx.Client) *Client { return &Client{h: h} }

func (c *Client) HTTP() *httpx.Client { return c.h }

// Event는 공개 엔드포인트라 토큰 없이 부른다. 원문도 함께 돌려준다(bronze 저장용).
func (c *Client) Event(ctx context.Context) (*Event, *httpx.Response, error) {
	var ev Event
	resp, err := c.h.GetJSON(ctx, "/v1/events/current", nil, false, &ev)
	if err != nil {
		return nil, resp, err
	}
	return &ev, resp, nil
}

// Me는 GET /v1/events/current/me(한도·사용량·remaining·제출 목록). 스키마가 공개되지 않아 원문으로 돌려준다.
func (c *Client) Me(ctx context.Context) (json.RawMessage, *httpx.Response, error) {
	resp, err := c.h.Do(ctx, &httpx.Request{Path: "/v1/events/current/me", Auth: true})
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, resp, nil
}

// LookupMax는 items/lookup 한 번에 물을 수 있는 번호 수다.
const LookupMax = 100

// LookupItem은 items/lookup 결과 한 행이다. State는 available | claimed | claimed_by_me | reform_found | not_found.
type LookupItem struct {
	Input           string `json:"input"`
	Kind            string `json:"kind"` // item | listing | unknown
	ItemID          string `json:"item_id"`
	State           string `json:"state"`
	Message         string `json:"message"`
	MySubmissionID  string `json:"my_submission_id,omitempty"`
	CurrentRevision *int   `json:"current_revision,omitempty"`
}

type LookupResult struct {
	Items []LookupItem `json:"items"`
}

// Lookup은 GET /v1/events/current/items/lookup이다(물품번호·등재번호 최대 100개).
// 스냅샷을 만들지 않고 제출 횟수도 쓰지 않는다. 원문도 함께 돌려준다(bronze 저장용).
func (c *Client) Lookup(ctx context.Context, ids []string) (*LookupResult, *httpx.Response, error) {
	if len(ids) == 0 || len(ids) > LookupMax {
		return nil, nil, fmt.Errorf("lookup은 번호 1~%d개: %d개를 받았다", LookupMax, len(ids))
	}
	var res LookupResult
	q := url.Values{"ids": {strings.Join(ids, ",")}}
	resp, err := c.h.GetJSON(ctx, "/v1/events/current/items/lookup", q, true, &res)
	if err != nil {
		return nil, resp, err
	}
	return &res, resp, nil
}

// Found는 GET /v1/events/current/found(발견 상품 카드 목록: 참가자 등록분 + 개혁신당 공개분).
// q는 q·who·status·sort·cursor·limit. 스키마가 공개되지 않아 원문으로 돌려준다.
func (c *Client) Found(ctx context.Context, q url.Values) (json.RawMessage, *httpx.Response, error) {
	resp, err := c.h.Do(ctx, &httpx.Request{Path: "/v1/events/current/found", Query: q, Auth: true})
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, resp, nil
}

// FoundDetail은 GET /v1/events/current/found/{key}(카드 상세).
func (c *Client) FoundDetail(ctx context.Context, key string) (json.RawMessage, *httpx.Response, error) {
	resp, err := c.h.Do(ctx, &httpx.Request{Path: "/v1/events/current/found/" + url.PathEscape(key), Auth: true})
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, resp, nil
}

// Counterevidence는 GET /v1/events/current/submissions/{sid}/counterevidence(제출에 달린 공개 반증 목록, 읽기 전용).
func (c *Client) Counterevidence(ctx context.Context, sid string) (json.RawMessage, *httpx.Response, error) {
	resp, err := c.h.Do(ctx, &httpx.Request{Path: "/v1/events/current/submissions/" + url.PathEscape(sid) + "/counterevidence", Auth: true})
	if err != nil {
		return nil, nil, err
	}
	return resp.Body, resp, nil
}
