package scrape

import (
	"bufio"
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Robots는 robots.txt에서 우리에게 해당하는 그룹의 규칙이다(RFC 9309).
//   - 우리 제품 토큰(AgentToken)과 같은 user-agent 그룹이 있으면 그것을, 없으면 "*" 그룹을 쓴다.
//   - 가장 길게 맞는 규칙이 이기고, 길이가 같으면 Allow가 이긴다.
//   - `*`(아무 문자열)와 끝의 `$`(경로 끝)를 지원한다.
//   - Crawl-delay는 표준 밖이지만 지킨다(요청 간격의 하한).
type Robots struct {
	rules      []robotsRule
	CrawlDelay time.Duration
}

type robotsRule struct {
	allow   bool
	pattern string
	re      *regexp.Regexp // 와일드카드가 있을 때만
}

// AllowAll은 robots.txt가 없을 때(4xx)의 규칙이다.
func AllowAll() *Robots { return &Robots{} }

type robotsGroup struct {
	agents []string
	rules  []robotsRule
	delay  time.Duration
}

// ParseRobots는 robots.txt 본문을 읽어 agent에 해당하는 규칙만 남긴다.
func ParseRobots(body []byte, agent string) *Robots {
	agent = strings.ToLower(agent)
	var groups []*robotsGroup
	var cur *robotsGroup
	inAgents := false // 직전 줄이 user-agent였는가(연속된 user-agent는 한 그룹)
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "user-agent":
			if !inAgents {
				cur = &robotsGroup{}
				groups = append(groups, cur)
			}
			cur.agents = append(cur.agents, strings.ToLower(val))
			inAgents = true
		case "allow", "disallow":
			inAgents = false
			if cur == nil || val == "" { // 그룹 밖 규칙·빈 Disallow는 아무것도 막지 않는다
				continue
			}
			cur.rules = append(cur.rules, newRule(key == "allow", val))
		case "crawl-delay":
			inAgents = false
			if cur == nil {
				continue
			}
			if secs, err := strconv.ParseFloat(val, 64); err == nil && secs > 0 {
				cur.delay = time.Duration(secs * float64(time.Second))
			}
		default:
			inAgents = false // sitemap 등은 그룹을 끊지 않지만 user-agent 연속도 아니다
		}
	}

	pick := func(match func(string) bool) *Robots {
		var r *Robots
		for _, g := range groups {
			if !anyOf(g.agents, match) {
				continue
			}
			if r == nil {
				r = &Robots{}
			}
			r.rules = append(r.rules, g.rules...)
			r.CrawlDelay = max(r.CrawlDelay, g.delay)
		}
		return r
	}
	if r := pick(func(a string) bool { return agent != "" && a == agent }); r != nil {
		return r
	}
	if r := pick(func(a string) bool { return a == "*" }); r != nil {
		return r
	}
	return AllowAll()
}

func anyOf(xs []string, f func(string) bool) bool {
	for _, x := range xs {
		if f(x) {
			return true
		}
	}
	return false
}

func newRule(allow bool, pattern string) robotsRule {
	r := robotsRule{allow: allow, pattern: pattern}
	if strings.ContainsAny(pattern, "*$") {
		anchored := strings.HasSuffix(pattern, "$")
		p := strings.TrimSuffix(pattern, "$")
		parts := strings.Split(p, "*")
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		expr := "^" + strings.Join(parts, ".*")
		if anchored {
			expr += "$"
		}
		r.re = regexp.MustCompile(expr) // QuoteMeta로 만든 식이라 실패하지 않는다
	}
	return r
}

func (r robotsRule) matches(path string) bool {
	if r.re != nil {
		return r.re.MatchString(path)
	}
	return strings.HasPrefix(path, r.pattern)
}

// Allowed는 경로(+쿼리)를 가져와도 되는지 본다. /robots.txt 자체는 항상 허용이다.
func (r *Robots) Allowed(pathAndQuery string) bool {
	if pathAndQuery == "" {
		pathAndQuery = "/"
	}
	if pathAndQuery == "/robots.txt" {
		return true
	}
	best, allowed := -1, true
	for _, rule := range r.rules {
		if !rule.matches(pathAndQuery) {
			continue
		}
		n := len(rule.pattern)
		if n > best || (n == best && rule.allow) {
			best, allowed = n, rule.allow
		}
	}
	return allowed
}
