package logx

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// 가짜 토큰은 런타임에 조립한다(저장소에 토큰처럼 보이는 리터럴을 두지 않는다).
var tok = "savetax" + "_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ-_"

func TestMask(t *testing.T) {
	for _, in := range []string{
		"token=" + tok,
		"Authorization: Bearer " + tok,
		"authorization: bearer xyz.abc",
	} {
		out := Mask(in)
		if strings.Contains(out, tok) || strings.Contains(out, "xyz.abc") {
			t.Errorf("가려지지 않았다: %q", out)
		}
	}
}

func TestLoggerMasksStringsAndErrors(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelDebug)
	l.Info("msg "+tok, "s", "x "+tok, "err", errors.New("bad "+tok))
	if strings.Contains(buf.String(), tok) {
		t.Fatalf("로그에 토큰이 남았다: %s", buf.String())
	}
}
