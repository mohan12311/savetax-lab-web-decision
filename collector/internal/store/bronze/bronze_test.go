package bronze

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []Envelope {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var out []Envelope
	sc := bufio.NewScanner(gz)
	for sc.Scan() {
		var e Envelope
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestRunWritesPartsAndManifest(t *testing.T) {
	root := t.TempDir()
	run := NewRun(root, "check", []string{"--x"})
	w, err := run.Writer("contest", "event")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	for i := range 3 {
		if err := w.Write(Envelope{FetchedAt: at, Source: "contest_api", Endpoint: "/v1/events/current", Status: 200,
			Body: json.RawMessage(`{"i":` + string(rune('0'+i)) + `}`)}); err != nil {
			t.Fatal(err)
		}
	}
	// 쓰는 중에는 .jsonl.gz가 보이지 않아야 한다.
	if m, _ := filepath.Glob(filepath.Join(root, "contest/event/dt=2026-10-14/*.jsonl.gz")); len(m) != 0 {
		t.Fatalf("닫기 전에 최종 파일이 보인다: %v", m)
	}
	mpath, err := run.Finish(nil)
	if err != nil {
		t.Fatal(err)
	}
	parts, _ := filepath.Glob(filepath.Join(root, "contest/event/dt=2026-10-14/*.jsonl.gz"))
	if len(parts) != 1 {
		t.Fatalf("parts = %v", parts)
	}
	if got := readLines(t, parts[0]); len(got) != 3 || string(got[2].Body) != `{"i":2}` {
		t.Errorf("lines = %+v", got)
	}

	var m Manifest
	b, _ := os.ReadFile(mpath)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Status != "ok" || m.Records != 3 || len(m.Files) != 1 || m.Files[0].Rows != 3 {
		t.Fatalf("manifest = %+v", m)
	}
	sum, n, _ := HashFile(parts[0])
	if m.Files[0].SHA256 != sum || m.Files[0].Bytes != n {
		t.Errorf("manifest 해시 불일치: %+v vs %s/%d", m.Files[0], sum, n)
	}
	if _, err := run.Writer("x", "y"); err == nil {
		t.Error("끝난 run에서 writer를 열었다")
	}
}

func TestRunRollsOverAndRecordsError(t *testing.T) {
	root := t.TempDir()
	run := NewRun(root, "crawl", nil)
	w, _ := run.Writer("data", "listing")
	for range MaxRowsPerFile + 1 {
		if err := w.Write(Envelope{Source: "data_api", Body: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	mpath, err := run.Finish(errors.New("중단됨"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	b, _ := os.ReadFile(mpath)
	_ = json.Unmarshal(b, &m)
	if len(m.Files) != 2 || m.Records != MaxRowsPerFile+1 || m.Status != "error" || m.Error != "중단됨" {
		t.Errorf("manifest = files:%d records:%d status:%s err:%q", len(m.Files), m.Records, m.Status, m.Error)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a", "b.json")
	if err := WriteFileAtomic(p, []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("2")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	ents, _ := os.ReadDir(filepath.Dir(p))
	if string(b) != "2" || len(ents) != 1 {
		t.Errorf("content=%s entries=%d", b, len(ents))
	}
}
