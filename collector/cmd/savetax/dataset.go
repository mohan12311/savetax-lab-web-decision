package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"savetax-lab/collector/internal/dataset"
	"savetax-lab/collector/internal/httpx"
	"savetax-lab/collector/internal/store/bronze"
)

func runDataset(ctx context.Context, a *app, args []string) error {
	fs := flag.NewFlagSet("dataset", flag.ContinueOnError)
	force := fs.Bool("force", false, "이미 검증된 버전도 다시 받는다")
	verify := fs.Bool("verify", false, "이미 받은 버전의 파일 해시를 다시 확인한다")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	h, err := a.datasetHTTP()
	if err != nil {
		return err
	}
	run := bronze.NewRun(a.bronzeRoot(), "dataset", args)
	run.TrackStats("dataset", h.Stats())

	res, err := dataset.Sync(ctx, h, dataset.Options{
		Root:   filepath.Join(a.bronzeRoot(), "dataset"),
		Force:  *force,
		Verify: *verify,
		Run:    run,
		Log:    a.log,
	})
	mpath, ferr := run.Finish(err)
	if err != nil {
		if errors.Is(err, httpx.ErrNoToken) {
			return fmt.Errorf("%w — 데이터셋 zip은 참가 토큰이 필요하다(.env의 SAVETAX_TOKEN)", err)
		}
		hintAuth(os.Stderr, err)
		return errors.Join(err, ferr)
	}

	out := os.Stdout
	if res.Skipped {
		state := "이미 검증됨 — 건너뜀"
		if *verify {
			state = "이미 받음 — 해시 재검증 통과"
		}
		fmt.Fprintf(out, "dataset %s: %s\n", res.ReleaseID, state)
	} else {
		fmt.Fprintf(out, "dataset %s: 받기·SHA256 검증·압축 해제 완료\n", res.ReleaseID)
	}
	fmt.Fprintf(out, "  dir: %s\n", res.Dir)
	names := make([]string, 0, len(res.Files))
	for n := range res.Files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintf(out, "  %-40s sha256 %s…\n", n, res.Files[n][:16])
	}
	fmt.Fprintf(out, "run manifest: %s\n", mpath)
	return ferr
}
