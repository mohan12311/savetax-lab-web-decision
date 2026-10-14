// Package contest는 대회 API(hackathon.savetax.now) 클라이언트다.
// 응답은 래퍼 없는 snake_case, 오류는 평면 {code, message, request_id, retryable}.
package contest

import (
	"context"
	"encoding/json"
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
