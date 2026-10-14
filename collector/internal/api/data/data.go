// Package data는 원시 데이터 REST(api.savetax.now) 클라이언트다.
// 응답은 {data, meta, page?} camelCase, 오류는 중첩 {error:{code, message}}.
//
// 한도: REST·MCP 공유 3,600회/시간, 검색 120회/분(별도). /v1/me는 예산 미차감.
// 호출 종류마다 기다릴 버킷을 고른다.
package data

import (
	"context"
	"encoding/json"
	"net/url"

	"golang.org/x/time/rate"

	"savetax-lab/collector/internal/httpx"
)

type Limiters struct {
	Hourly *rate.Limiter // 예산 차감 호출 전부
	Search *rate.Limiter // 검색은 시간 버킷과 함께 기다린다
	Free   *rate.Limiter // /v1/me 등 예산 미차감
}

type Client struct {
	h   *httpx.Client
	lim Limiters
}

// New의 httpx 클라이언트는 기본 리미터 없이 만든다(호출마다 여기서 고른다).
func New(h *httpx.Client, lim Limiters) *Client { return &Client{h: h, lim: lim} }

func (c *Client) HTTP() *httpx.Client { return c.h }

// Envelope는 데이터 API 공통 래퍼다.
type Envelope struct {
	Data json.RawMessage `json:"data"`
	Meta json.RawMessage `json:"meta,omitempty"`
	Page json.RawMessage `json:"page,omitempty"`
}

// Me는 GET /v1/me(한도·사용량). 예산에서 차감되지 않는다.
func (c *Client) Me(ctx context.Context) (*Envelope, *httpx.Response, error) {
	return c.get(ctx, "/v1/me", nil, c.lim.Free)
}

// Get은 예산 차감 일반 조회다(listings/{id}, items/{id}, deliveries 등).
func (c *Client) Get(ctx context.Context, path string, q url.Values) (*Envelope, *httpx.Response, error) {
	return c.get(ctx, path, q, c.lim.Hourly)
}

// Search는 GET /v1/listings 검색이다. 시간 버킷과 검색 버킷을 모두 기다린다.
func (c *Client) Search(ctx context.Context, q url.Values) (*Envelope, *httpx.Response, error) {
	return c.get(ctx, "/v1/listings", q, c.lim.Hourly, c.lim.Search)
}

func (c *Client) get(ctx context.Context, path string, q url.Values, lims ...*rate.Limiter) (*Envelope, *httpx.Response, error) {
	var ls []*rate.Limiter
	for _, l := range lims {
		if l != nil {
			ls = append(ls, l)
		}
	}
	resp, err := c.h.Do(ctx, &httpx.Request{Path: path, Query: q, Auth: true, Limiters: ls})
	if err != nil {
		return nil, nil, err
	}
	var env Envelope
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, resp, err
	}
	return &env, resp, nil
}
