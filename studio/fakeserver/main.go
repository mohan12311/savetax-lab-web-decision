// fakeserver는 2부 데모용 가짜 대회 API와 가짜 판매처다. 합성 데이터(studio/synth 출력)만 읽고,
// 루프백 주소에서 자체 서명 인증서로 https만 연다. 실제 대회·판매 사이트에는 아무것도 보내지 않는다.
//
//	fakeserver -data studio/demo/synth -tls-dir studio/demo/tls
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DemoToken은 데모 토큰이다(비밀이 아니다). 1부 CLI는 데모 모드에서 savetax_demo_ 접두어 토큰만 보낸다.
var DemoToken = "savetax_demo_" + strings.Repeat("0", 56-len("savetax_demo_"))

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakeserver:", err)
		os.Exit(1)
	}
}

func run() error {
	dataDir := flag.String("data", "", "합성 데이터 디렉터리(studio/synth 출력)")
	contestAddr := flag.String("contest-addr", "127.0.0.1:18443", "가짜 대회 API 주소(루프백만)")
	shopAddr := flag.String("shop-addr", "127.0.0.1:18444", "가짜 판매처 주소(루프백만)")
	tlsDir := flag.String("tls-dir", "", "인증서(ca.pem)와 ready.json을 쓸 디렉터리")
	token := flag.String("token", DemoToken, "받을 데모 토큰")
	flag.Parse()
	if *dataDir == "" || *tlsDir == "" {
		return errors.New("사용법: fakeserver -data <합성 데이터> -tls-dir <디렉터리>")
	}
	if !strings.HasPrefix(*token, "savetax_demo_") {
		return errors.New("-token은 savetax_demo_로 시작해야 한다(실제 참가 토큰을 받지 않는다)")
	}
	for _, a := range []string{*contestAddr, *shopAddr} {
		if err := requireLoopback(a); err != nil {
			return err
		}
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cat, err := loadCatalog(*dataDir)
	if err != nil {
		return err
	}
	cert, certPEM, err := selfSigned([]string{"127.0.0.1", "::1", "localhost"}, 7*24*time.Hour)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*tlsDir, 0o755); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(*tlsDir, "ready.json"))
	caPath := filepath.Join(*tlsDir, "ca.pem")
	if err := os.WriteFile(caPath, certPEM, 0o644); err != nil {
		return err
	}
	tcfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}

	shop := &shopSite{cat: cat, host: *shopAddr}
	api := newServer(cat, *token, shop, log)
	servers := []*http.Server{
		{Addr: *contestAddr, Handler: api.handler(), ReadHeaderTimeout: 5 * time.Second},
		{Addr: *shopAddr, Handler: shop.handler(), ReadHeaderTimeout: 5 * time.Second},
	}
	errc := make(chan error, len(servers))
	for _, srv := range servers {
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			return err
		}
		go func(srv *http.Server, ln net.Listener) {
			if err := srv.Serve(tls.NewListener(ln, tcfg)); !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}(srv, ln)
	}
	ready := map[string]any{
		"contest_url": "https://" + *contestAddr, "shop_url": "https://" + *shopAddr, "ca_pem": caPath,
		"pid": os.Getpid(), "started_at": time.Now().UTC().Format(time.RFC3339),
		"dataset_release_id": cat.Manifest.ReleaseID, "items": len(cat.Items), "pages": len(cat.Pages),
	}
	b, _ := json.MarshalIndent(ready, "", "  ")
	if err := os.WriteFile(filepath.Join(*tlsDir, "ready.json"), b, 0o644); err != nil {
		return err
	}
	log.Info("fakeserver 시작", "contest", ready["contest_url"], "shop", ready["shop_url"], "items", len(cat.Items), "pages", len(cat.Pages))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(sctx)
	}
	_ = os.Remove(filepath.Join(*tlsDir, "ready.json"))
	return err
}

// requireLoopback은 가짜 서버를 네트워크에 열지 않게 한다.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("주소 %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("루프백 주소만 연다: %q", addr)
	}
	return nil
}
