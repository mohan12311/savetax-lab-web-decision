// Package dataset은 나라장터 상품 데이터셋 패키지를 받아 검증·압축 해제한다.
//
// 절차: latest.json(공개) → releases/<ver>/SHA256SUMS(공개)와 교차 확인 →
// 공식 ZIP URL(LatestZipPath, 토큰) 스트리밍 + sha256 →
// 압축 해제(경로 검사) → 들어 있는 파일을 SHA256SUMS로 다시 검증 → COMPLETE 기록.
//
// 대회 지침상 토큰을 보내도 되는 데이터셋 주소는 공식 ZIP URL 하나뿐이라 버전 고정 주소
// (releases/<ver>/…zip)는 쓰지 않는다. 메타데이터를 읽은 뒤 latest가 바뀌면 sha256이 맞지 않아 실패한다.
// 서버가 이어받기를 지원하지 않으므로 끊기면 처음부터 다시 받는다.
package dataset

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/store/bronze"
)

const (
	// LatestZipPath는 토큰을 보내는 유일한 데이터셋 경로다(공식 ZIP URL).
	LatestZipPath = "/latest/savetax-products.zip"

	completeFile    = "COMPLETE.json"
	currentFile     = "current.json"
	maxExtractBytes = 4 << 30 // 압축 폭탄 방지(정상 패키지는 수십 MB)
	downloadTries   = 3
)

// 서버가 준 버전 문자열을 경로에 쓰므로 형식을 제한한다.
var releaseRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

type FileInfo struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	Rows   *int64 `json:"rows"`
	Access string `json:"access"`
}

type Latest struct {
	ReleaseID   string     `json:"release_id"`
	PublishedAt string     `json:"published_at"`
	Files       []FileInfo `json:"files"`
	Docs        []FileInfo `json:"docs"`
}

func (l *Latest) file(name string) (FileInfo, bool) {
	for _, f := range append(append([]FileInfo{}, l.Files...), l.Docs...) {
		if f.Name == name {
			return f, true
		}
	}
	return FileInfo{}, false
}

// Complete는 검증을 마친 버전 디렉터리에 남기는 표식이다.
type Complete struct {
	ReleaseID  string            `json:"release_id"`
	VerifiedAt time.Time         `json:"verified_at"`
	Package    string            `json:"package"`
	Files      map[string]string `json:"files"` // 상대 경로 → sha256
}

// Current는 <root>/current.json — silver가 읽을 기준 버전.
type Current struct {
	ReleaseID string `json:"release_id"`
	Dir       string `json:"dir"` // root 기준 상대 경로
	Parquet   string `json:"parquet"`
}

type Options struct {
	Root   string // data/bronze/dataset
	Force  bool   // 이미 COMPLETE여도 다시 받는다
	Verify bool   // COMPLETE면 받지 않고 파일 해시만 다시 확인
	Run    *bronze.Run
	Log    *slog.Logger
}

type Result struct {
	ReleaseID string
	Dir       string
	Skipped   bool // 이미 받아 둔 버전
	Files     map[string]string
}

func FetchLatest(ctx context.Context, h *httpx.Client) (*Latest, []byte, error) {
	resp, err := h.Do(ctx, &httpx.Request{Path: "/latest.json"})
	if err != nil {
		return nil, nil, err
	}
	var l Latest
	if err := json.Unmarshal(resp.Body, &l); err != nil {
		return nil, nil, fmt.Errorf("latest.json: %w", err)
	}
	if !releaseRe.MatchString(l.ReleaseID) {
		return nil, nil, fmt.Errorf("latest.json release_id 형식이 이상하다: %q", l.ReleaseID)
	}
	return &l, resp.Body, nil
}

// ParseSums는 `sha256  name` 형식(sha256sum 출력)을 읽는다.
func ParseSums(b []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || len(f[0]) != 64 {
			return nil, fmt.Errorf("SHA256SUMS 줄 형식 오류: %q", line)
		}
		if _, err := hex.DecodeString(f[0]); err != nil {
			return nil, fmt.Errorf("SHA256SUMS 해시 형식 오류: %q", line)
		}
		out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	if len(out) == 0 {
		return nil, errors.New("SHA256SUMS가 비어 있다")
	}
	return out, sc.Err()
}

func PackageName(release string) string { return "savetax-products-" + release + ".zip" }

// ReadCurrent는 마지막으로 검증된 버전을 읽는다(없으면 nil).
func ReadCurrent(root string) (*Current, error) {
	b, err := os.ReadFile(filepath.Join(root, currentFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Current
	return &c, json.Unmarshal(b, &c)
}

// Sync는 latest 버전을 받아 검증한다. 이미 검증된 버전이면 건너뛴다.
func Sync(ctx context.Context, h *httpx.Client, opt Options) (*Result, error) {
	log := opt.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	latest, latestRaw, err := FetchLatest(ctx, h)
	if err != nil {
		return nil, err
	}
	ver := latest.ReleaseID
	dir := filepath.Join(opt.Root, ver)
	pkg := PackageName(ver)

	if c, err := readComplete(dir); err == nil && !opt.Force {
		if opt.Verify {
			if err := verifyFiles(dir, c.Files); err != nil {
				return nil, fmt.Errorf("%s 재검증 실패(--force로 다시 받는다): %w", ver, err)
			}
			log.Info("dataset re-verified", "release", ver, "files", len(c.Files))
		}
		if err := writeCurrent(opt.Root, ver); err != nil {
			return nil, err
		}
		return &Result{ReleaseID: ver, Dir: dir, Skipped: true, Files: c.Files}, nil
	}
	if !h.HasToken() {
		return nil, httpx.ErrNoToken // zip은 토큰이 필요하다. 메타데이터만 받아 두지 않는다
	}

	sumsResp, err := h.Do(ctx, &httpx.Request{Path: "/releases/" + ver + "/SHA256SUMS"})
	if err != nil {
		return nil, fmt.Errorf("SHA256SUMS: %w", err)
	}
	sums, err := ParseSums(sumsResp.Body)
	if err != nil {
		return nil, err
	}
	wantSum, ok := sums[pkg]
	if !ok {
		return nil, fmt.Errorf("SHA256SUMS에 %s가 없다", pkg)
	}
	// 두 공개 출처(latest.json·SHA256SUMS)가 서로 맞는지 본다.
	var wantBytes int64 = -1
	if fi, ok := latest.file(pkg); ok {
		if !strings.EqualFold(fi.SHA256, wantSum) {
			return nil, fmt.Errorf("latest.json과 SHA256SUMS의 %s 해시가 다르다", pkg)
		}
		wantBytes = fi.Bytes
	}
	manifestResp, err := h.Do(ctx, &httpx.Request{Path: "/releases/" + ver + "/manifest.json"})
	if err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// 공개 메타데이터도 함께 보관(검증 근거·행 수·quality 통계 — silver 테스트가 쓴다).
	for name, b := range map[string][]byte{"latest.json": latestRaw, "SHA256SUMS": sumsResp.Body, "manifest.json": manifestResp.Body} {
		if err := bronze.WriteFileAtomic(filepath.Join(dir, "_meta", name), b); err != nil {
			return nil, err
		}
	}

	zipPath := filepath.Join(dir, pkg)
	if err := download(ctx, h, LatestZipPath, zipPath, wantSum, wantBytes, log); err != nil {
		return nil, err
	}
	files, err := extract(zipPath, dir)
	if err != nil {
		return nil, fmt.Errorf("압축 해제: %w", err)
	}
	// 압축 안 파일 중 SHA256SUMS에 있는 것은 모두 맞아야 하고, listings.parquet은 반드시 있어야 한다.
	checked := map[string]string{pkg: wantSum}
	for rel, got := range files {
		if want, ok := sums[path.Base(rel)]; ok {
			if got != want {
				return nil, fmt.Errorf("%s 해시 불일치: got %s want %s", rel, got, want)
			}
			checked[rel] = got
		}
	}
	if _, ok := checked["listings.parquet"]; !ok {
		return nil, errors.New("패키지에 검증된 listings.parquet이 없다")
	}

	if opt.Run != nil {
		for rel := range checked {
			if _, err := opt.Run.AddFile(filepath.Join(dir, rel), 0); err != nil {
				return nil, err
			}
		}
	}
	c := Complete{ReleaseID: ver, VerifiedAt: time.Now().UTC(), Package: pkg, Files: checked}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := bronze.WriteFileAtomic(filepath.Join(dir, completeFile), append(b, '\n')); err != nil {
		return nil, err
	}
	if err := writeCurrent(opt.Root, ver); err != nil {
		return nil, err
	}
	return &Result{ReleaseID: ver, Dir: dir, Files: checked}, nil
}

func download(ctx context.Context, h *httpx.Client, urlPath, dst, wantSum string, wantBytes int64, log *slog.Logger) error {
	var lastErr error
	for try := 1; try <= downloadTries; try++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = downloadOnce(ctx, h, urlPath, dst, wantSum, wantBytes)
		if lastErr == nil {
			return nil
		}
		if _, isAPI := httpx.AsAPIError(lastErr); isAPI || errors.Is(lastErr, httpx.ErrNoToken) {
			return lastErr // 401 등: 다시 받아도 같다(httpx가 이미 상태 코드 재시도를 했다)
		}
		log.Warn("dataset download failed, restarting from zero", "try", try, "err", lastErr)
	}
	return lastErr
}

func downloadOnce(ctx context.Context, h *httpx.Client, urlPath, dst, wantSum string, wantBytes int64) error {
	resp, err := h.Open(ctx, &httpx.Request{Path: urlPath, Auth: true, Header: map[string][]string{"Accept": {"application/zip, */*"}}})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		return fmt.Errorf("zip 대신 JSON 응답(Content-Type=%s)", ct)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) // 성공 시 rename돼 없음
	hsh := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hsh), resp.Body)
	if cerr := errors.Join(f.Sync(), f.Close()); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("받는 중 끊김(%d바이트): %w", n, err)
	}
	if wantBytes >= 0 && n != wantBytes {
		return fmt.Errorf("크기 불일치: got %d want %d", n, wantBytes)
	}
	if got := hex.EncodeToString(hsh.Sum(nil)); got != wantSum {
		return fmt.Errorf("sha256 불일치: got %s want %s", got, wantSum)
	}
	return os.Rename(tmp, dst)
}

// extract는 zip을 dir에 푼다. 공통 최상위 폴더(savetax-products-<ver>/)는 벗긴다.
// 절대 경로·..·심볼릭 링크는 거부한다(내려받은 압축 파일은 신뢰하지 않는다).
// 돌려주는 값: dir 기준 상대 경로(슬래시) → sha256.
func extract(zipPath, dir string) (map[string]string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var entries []*zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if f.Mode()&os.ModeType != 0 {
			return nil, fmt.Errorf("일반 파일이 아닌 항목: %s", f.Name)
		}
		if err := safeName(f.Name); err != nil {
			return nil, err
		}
		entries = append(entries, f)
	}
	if len(entries) == 0 {
		return nil, errors.New("빈 zip")
	}
	strip := commonTopDir(entries)

	out := map[string]string{}
	var total uint64
	for _, f := range entries {
		rel := strings.TrimPrefix(f.Name, strip)
		if rel == "" || rel == completeFile || strings.HasPrefix(rel, "_meta/") {
			return nil, fmt.Errorf("예약된 이름과 겹친다: %s", f.Name)
		}
		total += f.UncompressedSize64
		if total > maxExtractBytes {
			return nil, errors.New("압축 해제 크기 상한 초과")
		}
		sum, err := extractOne(f, filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		out[rel] = sum
	}
	return out, nil
}

func safeName(name string) error {
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || filepath.IsAbs(name) {
		return fmt.Errorf("허용하지 않는 경로: %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return fmt.Errorf("허용하지 않는 경로: %q", name)
		}
	}
	if path.Clean(name) != name {
		return fmt.Errorf("정규화되지 않은 경로: %q", name)
	}
	return nil
}

func commonTopDir(entries []*zip.File) string {
	first := entries[0].Name
	i := strings.Index(first, "/")
	if i < 0 {
		return ""
	}
	prefix := first[:i+1]
	for _, f := range entries {
		if !strings.HasPrefix(f.Name, prefix) {
			return ""
		}
	}
	return prefix
}

func extractOne(f *zip.File, dst string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	tmp := dst + ".tmp"
	w, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	hsh := sha256.New()
	// 헤더의 크기를 믿지 않고 실제로 읽은 양도 제한한다.
	n, err := io.Copy(io.MultiWriter(w, hsh), io.LimitReader(rc, int64(f.UncompressedSize64)+1))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if uint64(n) != f.UncompressedSize64 {
		return "", fmt.Errorf("크기 불일치: got %d header %d", n, f.UncompressedSize64)
	}
	return hex.EncodeToString(hsh.Sum(nil)), os.Rename(tmp, dst)
}

func readComplete(dir string) (*Complete, error) {
	b, err := os.ReadFile(filepath.Join(dir, completeFile))
	if err != nil {
		return nil, err
	}
	var c Complete
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if len(c.Files) == 0 {
		return nil, errors.New("빈 COMPLETE")
	}
	return &c, nil
}

func verifyFiles(dir string, files map[string]string) error {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, rel := range names {
		got, _, err := bronze.HashFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if got != files[rel] {
			return fmt.Errorf("%s 해시 불일치", rel)
		}
	}
	return nil
}

func writeCurrent(root, ver string) error {
	c := Current{ReleaseID: ver, Dir: ver, Parquet: ver + "/listings.parquet"}
	b, _ := json.MarshalIndent(c, "", "  ")
	return bronze.WriteFileAtomic(filepath.Join(root, currentFile), append(b, '\n'))
}
