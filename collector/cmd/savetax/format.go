package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"savetax-lab/collector/internal/logx"
)

var kst = time.FixedZone("KST", 9*60*60)

// commas는 정수 문자열에 천 단위 쉼표를 넣는다. 정수가 아니면 그대로 둔다.
func commas(s string) string {
	neg := strings.HasPrefix(s, "-")
	d := strings.TrimPrefix(s, "-")
	if d == "" || strings.Trim(d, "0123456789") != "" {
		return s
	}
	var b strings.Builder
	for i, r := range d {
		if i > 0 && (len(d)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// pad는 터미널 표시 폭 기준으로 오른쪽을 채운다(한글·한자는 2칸).
func pad(s string, width int) string {
	w := 0
	for _, r := range s {
		if unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Han, r) {
			w += 2
		} else {
			w++
		}
	}
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "마감됨"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	return fmt.Sprintf("%d일 %d시간 %d분", days, hours, mins)
}

// decodeJSON은 숫자를 json.Number로 보존한다(큰 금액을 float로 바꾸지 않는다).
func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	return v, dec.Decode(&v)
}

// printTree는 스키마를 모르는 응답(/me 등)을 키 순서대로 요약 출력한다.
// 스칼라는 그대로, 짧은 객체는 한 줄 JSON, 배열은 개수만, 긴 객체는 한 단계 더 들어간다.
func printTree(w io.Writer, v any, indent string, depth int) {
	m, ok := v.(map[string]any)
	if !ok {
		fmt.Fprintf(w, "%s%s\n", indent, scalar(v))
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		switch x := m[k].(type) {
		case map[string]any:
			b, _ := json.Marshal(x)
			if len(b) <= 160 || depth >= 2 {
				fmt.Fprintf(w, "%s%-22s %s\n", indent, k, logx.Mask(clip(string(b), 400)))
				continue
			}
			fmt.Fprintf(w, "%s%s:\n", indent, k)
			printTree(w, x, indent+"  ", depth+1)
		case []any:
			fmt.Fprintf(w, "%s%-22s [%d개]\n", indent, k, len(x))
		default:
			fmt.Fprintf(w, "%s%-22s %s\n", indent, k, scalar(x))
		}
	}
}

func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return logx.Mask(strconv.Quote(clip(x, 200)))
	case json.Number:
		return commas(x.String())
	default:
		return fmt.Sprint(x)
	}
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// flatten은 중첩 객체를 "a.b" 키의 문자열 맵으로 편다(변경 감지용).
func flatten(prefix string, v any, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, vv := range x {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, vv, out)
		}
	default:
		b, _ := json.Marshal(x)
		out[prefix] = string(b)
	}
}
