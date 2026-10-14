package state

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state", "collector.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, p
}

func TestQueueLifecycle(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	n, err := s.Enqueue(ctx, "item", []Task{{"a", 1}, {"b", 9}, {"c", 5}})
	if err != nil || n != 3 {
		t.Fatalf("enqueue n=%d err=%v", n, err)
	}
	if n, _ := s.Enqueue(ctx, "item", []Task{{"a", 100}, {"d", 0}}); n != 1 {
		t.Errorf("중복 추가됨: %d", n)
	}

	keys, err := s.Claim(ctx, "item", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, []string{"b", "c"}) {
		t.Errorf("우선순위 순이 아니다: %v", keys)
	}
	if err := s.Done(ctx, "item", "b"); err != nil {
		t.Fatal(err)
	}
	if final, err := s.Fail(ctx, "item", "c", errors.New("503"), 2); err != nil || final {
		t.Errorf("1회 실패는 pending이어야: final=%v err=%v", final, err)
	}
	keys, _ = s.Claim(ctx, "item", 10)
	if !slices.Equal(keys, []string{"c", "a", "d"}) {
		t.Errorf("재claim = %v", keys)
	}
	if final, _ := s.Fail(ctx, "item", "c", errors.New("503"), 2); !final {
		t.Error("2회 실패인데 failed가 아니다")
	}
	if err := s.Skip(ctx, "item", "d", "404 PRODUCT_NOT_FOUND"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Counts(ctx, "item")
	want := map[string]int{Done: 1, Failed: 1, Running: 1, Skipped: 1}
	for k, v := range want {
		if c[k] != v {
			t.Errorf("counts = %v", c)
			break
		}
	}
	if err := s.Done(ctx, "item", "nope"); err == nil {
		t.Error("없는 작업을 done 처리했다")
	}
	if n, _ := s.Retry(ctx, "item"); n != 1 {
		t.Errorf("retry = %d", n)
	}
}

func TestResumeAfterCrash(t *testing.T) {
	s, p := open(t)
	ctx := context.Background()
	s.Enqueue(ctx, "listing", []Task{{Key: "x"}, {Key: "y"}})
	s.Claim(ctx, "listing", 2)
	s.Done(ctx, "listing", "x")
	s.Close() // y는 running으로 남은 채 "죽음"

	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if n, _ := s2.RecoverRunning(ctx, "listing"); n != 1 {
		t.Errorf("recover = %d", n)
	}
	keys, _ := s2.Claim(ctx, "listing", 10)
	if !slices.Equal(keys, []string{"y"}) {
		t.Errorf("이어받기 = %v (done인 x는 다시 나오면 안 된다)", keys)
	}
}

func TestCheckpoint(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	if _, ok, _ := s.Checkpoint(ctx, "stream_cursor"); ok {
		t.Error("빈 저장소에 값이 있다")
	}
	s.SetCheckpoint(ctx, "stream_cursor", "1")
	s.SetCheckpoint(ctx, "stream_cursor", "2")
	if v, ok, err := s.Checkpoint(ctx, "stream_cursor"); !ok || v != "2" || err != nil {
		t.Errorf("checkpoint = %q %v %v", v, ok, err)
	}
}

func TestRequeue(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	if _, err := s.Enqueue(ctx, "scrape", []Task{{"q:a", 2}, {"q:b", 1}}); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.Claim(ctx, "scrape", 2)
	if len(keys) != 2 {
		t.Fatalf("claim %v", keys)
	}
	if err := s.Done(ctx, "scrape", "q:a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fail(ctx, "scrape", "q:b", errors.New("boom"), 1); err != nil {
		t.Fatal(err)
	}
	n, err := s.Requeue(ctx, "scrape", []string{"q:a", "q:b", "q:none"})
	if err != nil || n != 2 {
		t.Fatalf("requeue n=%d err=%v", n, err)
	}
	c, _ := s.Counts(ctx, "scrape")
	if c[Pending] != 2 {
		t.Errorf("counts %v", c)
	}
	// 시도 횟수가 0으로 돌아가 maxAttempts=1에서도 한 번 더 시도할 수 있어야 한다.
	s.Claim(ctx, "scrape", 2)
	if final, _ := s.Fail(ctx, "scrape", "q:b", errors.New("boom"), 2); final {
		t.Error("attempts가 초기화되지 않았다")
	}
}
