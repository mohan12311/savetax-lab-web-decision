package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"savetax-lab/collector/internal/logx"
)

var (
	// ErrNoToken은 인증이 필요한 요청인데 토큰이 없을 때 보내기 전에 돌려준다.
	ErrNoToken = errors.New("SAVETAX_TOKEN이 없어 인증 요청을 보내지 않았다")
	// ErrRedirectRejected는 다른 출처로의 리다이렉트(또는 10회 초과)를 따라가지 않았다는 뜻이다. 재시도하지 않는다.
	ErrRedirectRejected = errors.New("리다이렉트 거부")
)

// APIError는 2xx가 아닌 응답이다. 세 서비스의 오류 형식을 하나로 묶는다.
//   - 대회 API: 평면 {code, message, request_id, retryable, context?}
//   - 데이터 API·https_required: 중첩 {error:{code, message, hint?}}
//   - 데이터셋 호스트: 평면 {code, message}
type APIError struct {
	Service   string
	Method    string
	Path      string
	Status    int
	Code      string
	Message   string
	Hint      string
	RequestID string
	Retryable *bool // 서버가 명시한 경우만 non-nil
	Context   json.RawMessage
	Raw       string // JSON이 아니거나 해석 못 한 본문(앞부분만)

	cf1010 bool
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s → %d", e.Service, e.Method, e.Path, e.Status)
	if e.Code != "" {
		fmt.Fprintf(&b, " %s", e.Code)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Hint != "" {
		fmt.Fprintf(&b, " (hint: %s)", e.Hint)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " [request_id=%s]", e.RequestID)
	}
	if e.Code == "" && e.Raw != "" {
		fmt.Fprintf(&b, " body=%q", e.Raw)
	}
	return logx.Mask(b.String())
}

// ShouldRetry: 서버가 retryable을 명시했으면 그 값을 따른다.
// (예: 429 PARTICIPANT_LIMIT은 제출 한도 소진이라 재시도 무의미 → retryable:false)
// 명시가 없으면 429·5xx만 재시도한다. 4xx(409·422 등)는 원인을 기록하고 멈춘다.
func (e *APIError) ShouldRetry() bool {
	if e.Retryable != nil {
		return *e.Retryable
	}
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Is1010은 Cloudflare가 User-Agent 때문에 막은 경우(403 + 비JSON 본문의 error code 1010)다.
func (e *APIError) Is1010() bool { return e.cf1010 }

// AsAPIError는 errors.As 축약형이다.
func AsAPIError(err error) (*APIError, bool) {
	var ae *APIError
	ok := errors.As(err, &ae)
	return ae, ok
}

const rawSnippet = 512

func parseError(service, method, path string, resp *http.Response, body []byte) *APIError {
	e := &APIError{Service: service, Method: method, Path: path, Status: resp.StatusCode}
	var flat struct {
		Code      string          `json:"code"`
		Message   string          `json:"message"`
		RequestID string          `json:"request_id"`
		Retryable *bool           `json:"retryable"`
		Context   json.RawMessage `json:"context"`
		Error     json.RawMessage `json:"error"`
	}
	trimmed := bytes.TrimSpace(body)
	if err := json.Unmarshal(trimmed, &flat); err == nil {
		e.Code, e.Message, e.RequestID, e.Retryable, e.Context = flat.Code, flat.Message, flat.RequestID, flat.Retryable, flat.Context
		if len(flat.Error) > 0 && flat.Error[0] == '{' {
			var nested struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Hint    string `json:"hint"`
			}
			if json.Unmarshal(flat.Error, &nested) == nil {
				e.Code, e.Message, e.Hint = nested.Code, nested.Message, nested.Hint
			}
		}
	}
	if e.Code == "" {
		// Cloudflare HTML 오류 페이지는 코드가 512바이트 뒤에 나올 수 있어 본문 전체를 본다.
		e.cf1010 = resp.StatusCode == http.StatusForbidden && bytes.Contains(trimmed, []byte("1010"))
		s := string(trimmed)
		if len(s) > rawSnippet {
			s = s[:rawSnippet]
		}
		e.Raw = s
	}
	if e.RequestID == "" {
		e.RequestID = resp.Header.Get("X-Request-Id")
	}
	return e
}
