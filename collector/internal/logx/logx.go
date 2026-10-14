// Package logx는 토큰을 가리는 JSON slog 로거를 만든다.
package logx

import (
	"io"
	"log/slog"
	"regexp"
)

var secretRe = regexp.MustCompile(`savetax_[A-Za-z0-9_-]{4,}|(?i)bearer\s+\S+`)

// Mask는 문자열 안의 참가 토큰·Bearer 값을 가린다.
func Mask(s string) string {
	return secretRe.ReplaceAllString(s, "«redacted»")
}

// New는 문자열·오류 속성을 모두 Mask한 뒤 JSON 라인으로 쓰는 로거다.
func New(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			switch a.Value.Kind() {
			case slog.KindString:
				return slog.String(a.Key, Mask(a.Value.String()))
			case slog.KindAny:
				if err, ok := a.Value.Any().(error); ok {
					return slog.String(a.Key, Mask(err.Error()))
				}
			}
			return a
		},
	})
	return slog.New(h)
}
