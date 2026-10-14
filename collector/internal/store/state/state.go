// Package state는 수집 작업 큐와 체크포인트를 SQLite(WAL)에 둔다.
// 쓰기는 Go 수집기만 한다. Python은 읽기 전용으로 붙는다.
// 프로세스가 죽어도 done이 아닌 작업은 다음 실행에서 이어서 처리된다.
package state

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	_ "modernc.org/sqlite"
)

const (
	Pending = "pending"
	Running = "running"
	Done    = "done"
	Failed  = "failed"
	Skipped = "skipped"
)

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
  kind       TEXT NOT NULL,
  key        TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'pending',
  priority   REAL NOT NULL DEFAULT 0,
  attempts   INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (kind, key)
);
CREATE INDEX IF NOT EXISTS tasks_queue ON tasks(kind, status, priority DESC);
CREATE TABLE IF NOT EXISTS checkpoints (
  name       TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
`

type Store struct{ db *sql.DB }

type Task struct {
	Key      string
	Priority float64 // 높을수록 먼저(예: 후보 잠재 금액)
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// 단일 연결: 같은 프로세스의 워커들이 쓰기를 직렬화해 SQLITE_BUSY를 피한다.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("state schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Enqueue는 없는 작업만 추가한다(이미 있으면 상태를 건드리지 않는다). 추가된 개수를 돌려준다.
func (s *Store) Enqueue(ctx context.Context, kind string, tasks []Task) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO tasks(kind, key, priority, updated_at) VALUES(?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	added := 0
	ts := now()
	for _, t := range tasks {
		res, err := stmt.ExecContext(ctx, kind, t.Key, t.Priority, ts)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		added += int(n)
	}
	return added, tx.Commit()
}

// Claim은 pending 작업을 우선순위 순으로 최대 limit개 running으로 바꿔 돌려준다.
func (s *Store) Claim(ctx context.Context, kind string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
UPDATE tasks SET status='running', attempts=attempts+1, updated_at=?
WHERE rowid IN (
  SELECT rowid FROM tasks WHERE kind=? AND status='pending'
  ORDER BY priority DESC, rowid LIMIT ?)
RETURNING key, priority, rowid`, now(), kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type claimed struct {
		key   string
		prio  float64
		rowid int64
	}
	var got []claimed
	for rows.Next() {
		var c claimed
		if err := rows.Scan(&c.key, &c.prio, &c.rowid); err != nil {
			return nil, err
		}
		got = append(got, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// RETURNING 순서는 보장되지 않으므로 다시 정렬한다.
	slices.SortFunc(got, func(a, b claimed) int {
		if c := cmp.Compare(b.prio, a.prio); c != 0 {
			return c
		}
		return cmp.Compare(a.rowid, b.rowid)
	})
	keys := make([]string, len(got))
	for i, c := range got {
		keys[i] = c.key
	}
	return keys, nil
}

func (s *Store) Done(ctx context.Context, kind, key string) error {
	return s.set(ctx, kind, key, Done, "")
}

// Skip은 재시도해도 소용없는 작업(예: 404 = 없음)을 닫는다. 이유를 남긴다.
func (s *Store) Skip(ctx context.Context, kind, key, reason string) error {
	return s.set(ctx, kind, key, Skipped, reason)
}

// Fail은 시도 횟수가 maxAttempts에 이르면 failed, 아니면 pending으로 되돌린다.
func (s *Store) Fail(ctx context.Context, kind, key string, cause error, maxAttempts int) (final bool, err error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	var status string
	err = s.db.QueryRowContext(ctx, `
UPDATE tasks SET status = CASE WHEN attempts >= ? THEN 'failed' ELSE 'pending' END,
  last_error=?, updated_at=?
WHERE kind=? AND key=? RETURNING status`, maxAttempts, msg, now(), kind, key).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("작업 없음: %s/%s", kind, key)
	}
	return status == Failed, err
}

func (s *Store) set(ctx context.Context, kind, key, status, msg string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, last_error=NULLIF(?, ''), updated_at=? WHERE kind=? AND key=?`,
		status, msg, now(), kind, key)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("작업 없음: %s/%s", kind, key)
	}
	return nil
}

// RecoverRunning은 이전 실행이 죽으면서 running으로 남긴 작업을 pending으로 돌린다.
// 같은 kind를 처리하는 다른 프로세스가 없을 때만 부른다.
func (s *Store) RecoverRunning(ctx context.Context, kind string) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status='pending', updated_at=? WHERE kind=? AND status='running'`, now(), kind)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Retry는 failed 작업을 pending으로 되돌리고 시도 횟수를 0으로 만든다.
func (s *Store) Retry(ctx context.Context, kind string) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status='pending', attempts=0, updated_at=? WHERE kind=? AND status='failed'`, now(), kind)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) Counts(ctx context.Context, kind string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, count(*) FROM tasks WHERE kind=? GROUP BY status`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// Checkpoint는 커서·오프셋 같은 이어받기 지점이다(예: SSE Last-Event-ID).
func (s *Store) Checkpoint(ctx context.Context, name string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM checkpoints WHERE name=?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) SetCheckpoint(ctx context.Context, name, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO checkpoints(name, value, updated_at) VALUES(?,?,?)
ON CONFLICT(name) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, name, value, now())
	return err
}
