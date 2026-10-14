package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCommas(t *testing.T) {
	for in, want := range map[string]string{
		"0": "0", "999": "999", "1000": "1,000", "1000000": "1,000,000",
		"-1234567": "-1,234,567", "1.2": "1.2", "": "",
	} {
		if got := commas(in); got != want {
			t.Errorf("commas(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPadCountsHangulAsWide(t *testing.T) {
	if got := pad("마감", 6); got != "마감  " {
		t.Errorf("%q", got)
	}
	if got := pad("phase", 6); got != "phase " {
		t.Errorf("%q", got)
	}
}

func TestDiffSnapshot(t *testing.T) {
	p := filepath.Join(t.TempDir(), "event.json")
	if _, first, err := diffSnapshot(p, []byte(`{}`)); !first || err != nil {
		t.Fatalf("first=%v err=%v", first, err)
	}
	os.WriteFile(p, []byte(`{"ruleset_version":"rules-v1","price_gap":{"min_ratio":"1.2"},"server_time":"a","submission_count":1}`), 0o644)
	changes, first, err := diffSnapshot(p, []byte(`{"ruleset_version":"rules-v2","price_gap":{"min_ratio":"1.2"},"server_time":"b","submission_count":2,"new_key":true}`))
	if err != nil || first {
		t.Fatal(err)
	}
	want := []string{`new_key: (없음) → true`, `ruleset_version: "rules-v1" → "rules-v2"`}
	if !slices.Equal(changes, want) {
		t.Errorf("changes = %q", changes)
	}
}
