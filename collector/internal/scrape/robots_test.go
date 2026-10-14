package scrape

import (
	"testing"
	"time"
)

func TestParseRobotsStarGroup(t *testing.T) {
	// search.danawa.com 형태(2026-10-14 실측)를 줄인 것
	r := ParseRobots([]byte(`# robots.txt
User-agent: *
Disallow: /api_ui/
Disallow: /tpl/
Crawl-delay: 10

Sitemap: https://search.danawa.com/sitemap.xml
`), AgentToken)
	if r.CrawlDelay != 10*time.Second {
		t.Errorf("crawl-delay %s", r.CrawlDelay)
	}
	cases := map[string]bool{
		"/dsearch.php?query=S24C310": true,
		"/api_ui/x":                  false,
		"/tpl/a.html":                false,
		"/robots.txt":                true,
		"":                           true,
	}
	for p, want := range cases {
		if got := r.Allowed(p); got != want {
			t.Errorf("Allowed(%q)=%v want %v", p, got, want)
		}
	}
}

func TestParseRobotsSpecificAgentWins(t *testing.T) {
	r := ParseRobots([]byte(`
User-agent: *
Disallow: /

User-agent: Savetax-Lab
User-agent: other
Disallow: /private/
Crawl-delay: 2
`), AgentToken)
	if !r.Allowed("/info/?pcode=1") || r.Allowed("/private/x") {
		t.Error("우리 토큰 그룹 대신 * 그룹을 썼다")
	}
	if r.CrawlDelay != 2*time.Second {
		t.Errorf("crawl-delay %s", r.CrawlDelay)
	}
}

func TestRobotsLongestMatchAndWildcards(t *testing.T) {
	r := ParseRobots([]byte(`
User-agent: *
Disallow: /info/
Allow: /info/?pcode=
Disallow: /*?iframe=*
Disallow: /*.php$
Allow: /same
Disallow: /same
`), AgentToken)
	cases := map[string]bool{
		"/info/ajax/x":        false,
		"/info/?pcode=123":    true, // 더 긴 Allow가 이긴다
		"/list/?iframe=1":     false,
		"/a.php":              false,
		"/a.php?x=1":          true, // $는 경로 끝
		"/same":               true, // 같은 길이면 Allow
		"/community/x":        true,
		"/bridge/loading.htm": true,
	}
	for p, want := range cases {
		if got := r.Allowed(p); got != want {
			t.Errorf("Allowed(%q)=%v want %v", p, got, want)
		}
	}
}

func TestParseRobotsEmptyAndNoGroup(t *testing.T) {
	if r := ParseRobots(nil, AgentToken); !r.Allowed("/anything") {
		t.Error("빈 robots.txt는 모두 허용")
	}
	r := ParseRobots([]byte("User-agent: googlebot\nDisallow: /\n"), AgentToken)
	if !r.Allowed("/x") {
		t.Error("해당 그룹이 없으면 모두 허용")
	}
	r = ParseRobots([]byte("User-agent: *\nDisallow:\n"), AgentToken)
	if !r.Allowed("/x") {
		t.Error("빈 Disallow는 아무것도 막지 않는다")
	}
}
