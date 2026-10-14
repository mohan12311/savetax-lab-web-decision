package dataset

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"savetax-lab/collector/internal/httpx"
)

const ver = "20991231-r1"

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fakeHost struct {
	zip        []byte
	sums       string
	latestSum  string // latest.json에 적을 zip 해시(기본: 실제 해시)
	zipStatus  int
	zipHits    atomic.Int32
	authHeader atomic.Value
}

func newFakeHost(t *testing.T, fh *fakeHost) *httptest.Server {
	t.Helper()
	pkg := PackageName(ver)
	if fh.latestSum == "" {
		fh.latestSum = sha(fh.zip)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"release_id": ver,
			"files":      []map[string]any{{"name": pkg, "bytes": len(fh.zip), "sha256": fh.latestSum, "access": "token"}},
		})
	})
	mux.HandleFunc("/releases/"+ver+"/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, fh.sums) })
	mux.HandleFunc("/releases/"+ver+"/manifest.json", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"rows":2}`) })
	mux.HandleFunc(LatestZipPath, func(w http.ResponseWriter, r *http.Request) {
		fh.zipHits.Add(1)
		fh.authHeader.Store(r.Header.Get("Authorization"))
		if fh.zipStatus != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(fh.zipStatus)
			fmt.Fprint(w, `{"code":"unauthorized","message":"토큰 필요"}`)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Write(fh.zip)
	})
	// 토큰은 공식 ZIP 경로에만 가야 한다(공개 메타데이터 요청에는 붙이지 않는다).
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != LatestZipPath && r.Header.Get("Authorization") != "" {
			t.Errorf("공식 ZIP 경로가 아닌 %s 에 토큰을 보냈다", r.URL.Path)
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client(t *testing.T, srv *httptest.Server, token string) *httpx.Client {
	t.Helper()
	h, err := httpx.New(httpx.Options{Service: "dataset", BaseURL: srv.URL, Token: token, HTTPClient: srv.Client(), MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func goodPackage(t *testing.T) (*fakeHost, map[string]string) {
	inner := map[string]string{
		"README.md":        "# readme",
		"listings.parquet": "PAR1fakePAR1",
		"columns.csv":      "a,b",
	}
	files := map[string]string{}
	for n, b := range inner {
		files["savetax-products-"+ver+"/"+n] = b
	}
	z := makeZip(t, files)
	sums := fmt.Sprintf("%s  %s\n%s  README.md\n%s  listings.parquet\n%s  columns.csv\n",
		sha(z), PackageName(ver), sha([]byte(inner["README.md"])), sha([]byte(inner["listings.parquet"])), sha([]byte(inner["columns.csv"])))
	return &fakeHost{zip: z, sums: sums}, inner
}

// 가짜 토큰은 런타임에 조립한다(저장소에 토큰처럼 보이는 리터럴을 두지 않는다).
var tok = "savetax" + "_" + strings.Repeat("T", 48)

func TestSyncHappyPathAndSkip(t *testing.T) {
	fh, inner := goodPackage(t)
	srv := newFakeHost(t, fh)
	root := t.TempDir()
	ctx := context.Background()

	res, err := Sync(ctx, client(t, srv, tok), Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped || res.ReleaseID != ver {
		t.Fatalf("res = %+v", res)
	}
	if got := fh.authHeader.Load(); got != "Bearer "+tok {
		t.Errorf("zip 요청 Authorization = %v", got)
	}
	for name, body := range inner {
		b, err := os.ReadFile(filepath.Join(root, ver, name))
		if err != nil || string(b) != body {
			t.Errorf("%s: %q %v (최상위 폴더를 벗겨야 한다)", name, b, err)
		}
	}
	for _, p := range []string{completeFile, "_meta/SHA256SUMS", "_meta/latest.json", "_meta/manifest.json", PackageName(ver)} {
		if _, err := os.Stat(filepath.Join(root, ver, p)); err != nil {
			t.Errorf("없음: %s", p)
		}
	}
	cur, err := ReadCurrent(root)
	if err != nil || cur.ReleaseID != ver || cur.Parquet != ver+"/listings.parquet" {
		t.Errorf("current = %+v %v", cur, err)
	}

	res, err = Sync(ctx, client(t, srv, tok), Options{Root: root, Verify: true})
	if err != nil || !res.Skipped {
		t.Fatalf("두 번째 실행은 건너뛰어야 한다: %+v %v", res, err)
	}
	if fh.zipHits.Load() != 1 {
		t.Errorf("zip을 %d번 받았다", fh.zipHits.Load())
	}

	// 받은 뒤 파일이 바뀌면 --verify가 잡아낸다.
	os.WriteFile(filepath.Join(root, ver, "listings.parquet"), []byte("tampered"), 0o644)
	if _, err := Sync(ctx, client(t, srv, tok), Options{Root: root, Verify: true}); err == nil {
		t.Error("변조를 못 잡았다")
	}
}

func TestSyncRejectsChecksumMismatch(t *testing.T) {
	fh, _ := goodPackage(t)
	fh.sums = strings.Replace(fh.sums, sha(fh.zip), strings.Repeat("0", 64), 1)
	fh.latestSum = strings.Repeat("0", 64) // 두 출처가 같은 (틀린) 값을 말하는 경우
	srv := newFakeHost(t, fh)
	root := t.TempDir()
	_, err := Sync(context.Background(), client(t, srv, tok), Options{Root: root})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("err = %v", err)
	}
	if fh.zipHits.Load() != downloadTries {
		t.Errorf("해시 불일치 재시도 = %d", fh.zipHits.Load())
	}
	for _, p := range []string{completeFile, PackageName(ver), PackageName(ver) + ".part"} {
		if _, err := os.Stat(filepath.Join(root, ver, p)); err == nil {
			t.Errorf("남으면 안 된다: %s", p)
		}
	}
	if cur, _ := ReadCurrent(root); cur != nil {
		t.Error("검증 실패인데 current.json이 생겼다")
	}
}

func TestSyncRejectsSourceDisagreement(t *testing.T) {
	fh, _ := goodPackage(t)
	fh.latestSum = strings.Repeat("a", 64)
	srv := newFakeHost(t, fh)
	_, err := Sync(context.Background(), client(t, srv, tok), Options{Root: t.TempDir()})
	if err == nil || fh.zipHits.Load() != 0 {
		t.Fatalf("err=%v hits=%d", err, fh.zipHits.Load())
	}
}

func TestSyncAuthErrorNotRetried(t *testing.T) {
	fh, _ := goodPackage(t)
	fh.zipStatus = 401
	srv := newFakeHost(t, fh)
	_, err := Sync(context.Background(), client(t, srv, tok), Options{Root: t.TempDir()})
	if ae, ok := httpx.AsAPIError(err); !ok || ae.Status != 401 {
		t.Fatalf("err = %v", err)
	}
	if fh.zipHits.Load() != 1 {
		t.Errorf("401을 %d번 보냈다", fh.zipHits.Load())
	}
	if _, err := Sync(context.Background(), client(t, srv, ""), Options{Root: t.TempDir()}); err == nil {
		t.Error("토큰 없이 성공했다")
	}
}

func TestExtractRejectsUnsafePaths(t *testing.T) {
	for _, name := range []string{"../evil.txt", "a/../../evil.txt", "/abs.txt", `a\..\evil.txt`, "a//b.txt"} {
		dir := t.TempDir()
		zp := filepath.Join(dir, "x.zip")
		os.WriteFile(zp, makeZip(t, map[string]string{name: "x", "ok/listings.parquet": "p"}), 0o644)
		if _, err := extract(zp, filepath.Join(dir, "out")); err == nil {
			t.Errorf("%q를 허용했다", name)
		}
	}
}

func TestParseSums(t *testing.T) {
	h := strings.Repeat("ab", 32)
	m, err := ParseSums([]byte(h + "  a.zip\n\n" + strings.ToUpper(h) + " *b.bin\n"))
	if err != nil || m["a.zip"] != h || m["b.bin"] != h {
		t.Errorf("m=%v err=%v", m, err)
	}
	if _, err := ParseSums([]byte("nothex  a\n")); err == nil {
		t.Error("잘못된 줄을 받아들였다")
	}
}
