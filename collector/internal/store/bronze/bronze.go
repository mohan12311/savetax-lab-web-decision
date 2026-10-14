// Package bronze는 원본 응답을 불변 JSONL.gz로 쌓고, 실행(run)마다 manifest를 남긴다.
//
//	<root>/<source>/<kind>/dt=YYYY-MM-DD/part-<run_id>-<seq>.jsonl.gz
//	<root>/_runs/<run_id>.json
//
// 쓰는 동안은 .tmp 이름을 쓰고 닫을 때 rename하므로, glob(*.jsonl.gz)으로 읽는 쪽은
// 반쯤 쓴 파일을 보지 않는다. 이미 있는 파일은 덮어쓰지 않는다(append-only).
package bronze

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"savetax-lab/collector/internal/httpx"
)

// Envelope는 bronze 레코드 한 줄이다. Body는 서버 응답 원문 그대로다.
// 응답이 JSON이 아니면(시중 HTML) 원문은 Raw가 가리키는 파일에 두고 Body에는 추출 결과를 넣는다.
type Envelope struct {
	FetchedAt time.Time       `json:"fetched_at"`
	Source    string          `json:"source"`   // contest_api | data_api | dataset | danawa
	Endpoint  string          `json:"endpoint"` // 경로 템플릿, 예: /v1/events/current/items/{item_id}
	Params    map[string]any  `json:"params,omitempty"`
	Status    int             `json:"status"`
	RequestID *string         `json:"request_id"`
	Body      json.RawMessage `json:"body"`
	Raw       *RawRef         `json:"raw,omitempty"`
}

// RawRef는 WriteBlob으로 보관한 원문 파일이다. SHA256은 압축 전 원문 바이트의 해시라
// 나중에 인용문을 원문에서 다시 찾을 때 그대로 대조한다(manifest의 sha256은 디스크의 .gz 파일 해시).
type RawRef struct {
	Path        string `json:"path"` // root 기준 상대 경로
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	ContentType string `json:"content_type,omitempty"`
}

type FileEntry struct {
	Path   string `json:"path"` // root 기준 상대 경로
	SHA256 string `json:"sha256"`
	Rows   int64  `json:"rows"`
	Bytes  int64  `json:"bytes"`
}

type Manifest struct {
	RunID        string                         `json:"run_id"`
	Command      string                         `json:"command"`
	Args         []string                       `json:"args,omitempty"`
	StartedAt    time.Time                      `json:"started_at"`
	FinishedAt   time.Time                      `json:"finished_at"`
	Status       string                         `json:"status"` // ok | error
	Error        string                         `json:"error,omitempty"`
	Records      int64                          `json:"records"`
	Files        []FileEntry                    `json:"files"`
	APICalls     int                            `json:"api_calls"`
	ErrorsByCode map[string]int                 `json:"errors_by_code"`
	Services     map[string]httpx.StatsSnapshot `json:"services,omitempty"`
}

// MaxRowsPerFile을 넘으면 새 part 파일로 넘어간다.
const MaxRowsPerFile = 50_000

type Run struct {
	ID        string
	Command   string
	Args      []string
	StartedAt time.Time
	root      string

	mu      sync.Mutex
	writers []*Writer
	files   []FileEntry
	records int64
	stats   map[string]*httpx.Stats
	done    bool
}

func NewRun(root, command string, args []string) *Run {
	now := time.Now().UTC()
	return &Run{
		ID:        newRunID(now),
		Command:   command,
		Args:      args,
		StartedAt: now,
		root:      root,
		stats:     map[string]*httpx.Stats{},
	}
}

func newRunID(t time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return t.Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

func (r *Run) Root() string { return r.root }

// TrackStats는 manifest의 api_calls·errors_by_code에 넣을 클라이언트 통계를 등록한다.
func (r *Run) TrackStats(service string, s *httpx.Stats) {
	r.mu.Lock()
	r.stats[service] = s
	r.mu.Unlock()
}

// Writer는 source/kind 파티션의 JSONL.gz writer를 연다. 같은 (source, kind)는 하나만 만든다.
func (r *Run) Writer(source, kind string) (*Writer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return nil, errors.New("이미 끝난 run")
	}
	for _, w := range r.writers {
		if w.source == source && w.kind == kind {
			return w, nil
		}
	}
	w := &Writer{run: r, source: source, kind: kind}
	r.writers = append(r.writers, w)
	return w, nil
}

// AddFile은 JSONL이 아닌 산출물(예: 데이터셋 파일)을 manifest에 올린다. sha256을 다시 계산한다.
func (r *Run) AddFile(path string, rows int64) (FileEntry, error) {
	sum, n, err := HashFile(path)
	if err != nil {
		return FileEntry{}, err
	}
	rel, err := filepath.Rel(r.root, path)
	if err != nil {
		rel = path
	}
	fe := FileEntry{Path: filepath.ToSlash(rel), SHA256: sum, Rows: rows, Bytes: n}
	r.mu.Lock()
	r.files = append(r.files, fe)
	r.mu.Unlock()
	return fe, nil
}

// WriteBlob은 JSON이 아닌 원문(HTML 등)을 내용 주소 파일로 보관한다.
//
//	<root>/<source>/<kind>/dt=YYYY-MM-DD/<sha256>.<ext>.gz
//
// 같은 날 같은 내용이 이미 있으면 다시 쓰지 않고(append-only 그대로) 기존 파일을 가리킨다.
func (r *Run) WriteBlob(source, kind string, t time.Time, data []byte, ext, contentType string) (RawRef, error) {
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	path := filepath.Join(r.root, source, kind, "dt="+t.UTC().Format("2006-01-02"), hexSum+"."+ext+".gz")
	rel, err := filepath.Rel(r.root, path)
	if err != nil {
		return RawRef{}, err
	}
	ref := RawRef{Path: filepath.ToSlash(rel), SHA256: hexSum, Bytes: int64(len(data)), ContentType: contentType}
	if _, err := os.Stat(path); err == nil {
		return ref, nil
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return RawRef{}, err
	}
	if err := gz.Close(); err != nil {
		return RawRef{}, err
	}
	if err := WriteFileAtomic(path, buf.Bytes()); err != nil {
		return RawRef{}, err
	}
	if _, err := r.AddFile(path, 1); err != nil {
		return RawRef{}, err
	}
	return ref, nil
}

// Finish는 열린 writer를 모두 닫고 manifest를 쓴다. runErr가 있으면 status=error로 남긴다.
func (r *Run) Finish(runErr error) (string, error) {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return "", errors.New("이미 끝난 run")
	}
	r.done = true
	writers := r.writers
	r.mu.Unlock()

	var closeErrs []error
	for _, w := range writers {
		if err := w.close(); err != nil {
			closeErrs = append(closeErrs, err)
		}
	}
	m := Manifest{
		RunID:        r.ID,
		Command:      r.Command,
		Args:         r.Args,
		StartedAt:    r.StartedAt,
		FinishedAt:   time.Now().UTC(),
		Status:       "ok",
		ErrorsByCode: map[string]int{},
		Services:     map[string]httpx.StatsSnapshot{},
	}
	r.mu.Lock()
	m.Files = append([]FileEntry{}, r.files...)
	m.Records = r.records
	for name, s := range r.stats {
		snap := s.Snapshot()
		m.Services[name] = snap
		m.APICalls += snap.Calls
		for code, n := range snap.ByCode {
			m.ErrorsByCode[code] += n
		}
	}
	r.mu.Unlock()
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	if err := errors.Join(append([]error{runErr}, closeErrs...)...); err != nil {
		m.Status = "error"
		m.Error = err.Error()
	}
	path := filepath.Join(r.root, "_runs", r.ID+".json")
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	if err := WriteFileAtomic(path, append(b, '\n')); err != nil {
		return "", err
	}
	return path, errors.Join(closeErrs...)
}

func (r *Run) addFile(fe FileEntry) {
	r.mu.Lock()
	r.files = append(r.files, fe)
	r.records += fe.Rows
	r.mu.Unlock()
}

// Writer는 한 파티션의 롤링 JSONL.gz writer다. 동시 사용 가능.
type Writer struct {
	run    *Run
	source string
	kind   string

	mu    sync.Mutex
	seq   int
	f     *os.File
	gz    *gzip.Writer
	h     hash.Hash
	cw    *countWriter
	rows  int64
	final string
}

func (w *Writer) Write(env Envelope) error {
	if env.FetchedAt.IsZero() {
		env.FetchedAt = time.Now().UTC()
	}
	line, err := json.Marshal(env)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil && w.rows >= MaxRowsPerFile {
		if err := w.closeLocked(); err != nil {
			return err
		}
	}
	if w.f == nil {
		if err := w.openLocked(env.FetchedAt); err != nil {
			return err
		}
	}
	if _, err := w.gz.Write(append(line, '\n')); err != nil {
		return err
	}
	w.rows++
	return nil
}

func (w *Writer) openLocked(t time.Time) error {
	w.seq++
	dir := filepath.Join(w.run.root, w.source, w.kind, "dt="+t.UTC().Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	w.final = filepath.Join(dir, fmt.Sprintf("part-%s-%04d.jsonl.gz", w.run.ID, w.seq))
	if _, err := os.Stat(w.final); err == nil {
		return fmt.Errorf("bronze 파일이 이미 있다(덮어쓰기 금지): %s", w.final)
	}
	f, err := os.OpenFile(w.final+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w.f = f
	w.h = sha256.New()
	w.cw = &countWriter{w: io.MultiWriter(f, w.h)}
	w.gz = gzip.NewWriter(w.cw)
	w.rows = 0
	return nil
}

func (w *Writer) closeLocked() error {
	if w.f == nil {
		return nil
	}
	err := errors.Join(w.gz.Close(), w.f.Sync(), w.f.Close())
	f := w.final
	w.f, w.gz = nil, nil
	if err != nil {
		return err
	}
	if err := os.Rename(f+".tmp", f); err != nil {
		return err
	}
	rel, _ := filepath.Rel(w.run.root, f)
	w.run.addFile(FileEntry{Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(w.h.Sum(nil)), Rows: w.rows, Bytes: w.cw.n})
	return nil
}

func (w *Writer) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closeLocked()
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// HashFile은 파일의 sha256(hex)과 크기를 돌려준다.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// WriteFileAtomic은 같은 디렉터리의 임시 파일에 쓰고 rename한다.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // rename 성공 후에는 없는 파일이라 무해
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := errors.Join(tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
