package naver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"savetax-lab/collector/internal/httpx"
)

// 합성 응답(실제 API 응답을 옮기지 않는다).
const fixture = `{"lastBuildDate":"Fri, 16 Oct 2026 14:00:00 +0900","total":2,"start":1,"display":2,"items":[
 {"title":"테스트 <b>TX-100</b> 허브랙 &amp; 선반","link":"https://example.com/p/1","image":"","lprice":"450000","hprice":"",
  "mallName":"테스트몰","productId":"111","productType":"2","brand":"테스트","maker":"테스트산업",
  "category1":"디지털/가전","category2":"네트워크장비","category3":"허브랙","category4":""},
 {"title":"가격 없는 <b>TX-100</b>","link":"https://example.com/p/2","lprice":"","hprice":"0","mallName":"네이버",
  "productId":"222","productType":"1","brand":"","maker":"","category1":"","category2":"","category3":"","category4":""}]}`

func TestParse(t *testing.T) {
	r, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 2 || len(r.Items) != 2 {
		t.Fatalf("%+v", r)
	}
	a := r.Items[0]
	if a.Title != "테스트 TX-100 허브랙 & 선반" || a.Mall != "테스트몰" || a.LowPriceKRW == nil || *a.LowPriceKRW != 450000 {
		t.Errorf("item0 %+v", a)
	}
	if a.HighPriceKRW != nil || strings.Join(a.Category, ">") != "디지털/가전>네트워크장비>허브랙" {
		t.Errorf("item0 hprice/category %+v", a)
	}
	if b := r.Items[1]; b.LowPriceKRW != nil || b.HighPriceKRW != nil || len(b.Category) != 0 {
		t.Errorf("빈 가격은 nil이어야 한다: %+v", b)
	}
}

func newClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	hc, err := httpx.New(httpx.Options{Service: "retail:naver", BaseURL: srv.URL, HTTPClient: srv.Client(), MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(hc, Keys{ID: "id-x", Secret: "secret-y"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSearchSendsKeysAndParams(t *testing.T) {
	var got *http.Request
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		io.WriteString(w, fixture)
	})
	resp, res, err := c.Search(context.Background(), "TX-100", 50)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != fixture || len(res.Items) != 2 {
		t.Errorf("원문을 그대로 돌려줘야 한다")
	}
	if got.URL.Path != SearchPath || got.Header.Get("X-Naver-Client-Id") != "id-x" || got.Header.Get("X-Naver-Client-Secret") != "secret-y" {
		t.Errorf("요청 %s %v", got.URL, got.Header)
	}
	if got.Header.Get("Authorization") != "" {
		t.Error("Authorization 헤더가 나갔다")
	}
	q := got.URL.Query()
	if q.Get("query") != "TX-100" || q.Get("display") != "50" || q.Get("sort") != "sim" || q.Get("exclude") != Exclude {
		t.Errorf("query %v", q)
	}
	if strings.Contains(got.URL.RawQuery, "secret") {
		t.Error("키가 URL에 들어갔다")
	}
}

func TestFatalErrors(t *testing.T) {
	for _, code := range []int{401, 403, 429} {
		c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			io.WriteString(w, `{"errorMessage":"테스트 오류","errorCode":"024"}`)
		})
		_, _, err := c.Search(context.Background(), "x", 10)
		if err == nil || !Fatal(err) {
			t.Errorf("%d: Fatal이어야 한다 (%v)", code, err)
		}
	}
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })
	if _, _, err := c.Search(context.Background(), "x", 10); err == nil || Fatal(err) {
		t.Errorf("400은 Fatal이 아니다 (%v)", err)
	}
}

func TestNewRejectsTokenAndEmptyKeys(t *testing.T) {
	tok := "savetax" + "_" + strings.Repeat("T", 48)
	hc, _ := httpx.New(httpx.Options{BaseURL: BaseURL, Token: tok})
	if _, err := New(hc, Keys{ID: "a", Secret: "b"}); err == nil {
		t.Error("토큰 든 클라이언트를 받아들였다")
	}
	plain, _ := httpx.New(httpx.Options{BaseURL: BaseURL})
	if _, err := New(plain, Keys{ID: "a"}); err == nil {
		t.Error("빈 Secret을 받아들였다")
	}
	if s := fmt.Sprint(Keys{ID: "id-x", Secret: "secret-y"}); strings.Contains(s, "id-x") || strings.Contains(s, "secret-y") {
		t.Errorf("Keys 출력이 값을 드러낸다: %s", s)
	}
}
