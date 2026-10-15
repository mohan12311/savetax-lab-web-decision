package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"savetax-lab/collector/internal/scrape/danawa"
	"savetax-lab/collector/internal/store/bronze"
)

// reparseSearch는 보관한 다나와 검색 원문을 지금 파서로 다시 추출한다. 다나와에 요청하지 않는다.
// bronze는 덧붙이기만 한다: 원래 봉투는 두고, 같은 fetched_at·raw에 params.reparsed=true를 단 봉투를 더한다.
// 읽는 쪽은 같은 원문(raw.sha256)의 봉투가 여럿이면 나중 파일(run이 늦은 쪽)의 것을 쓴다.
func reparseSearch(root string, args []string, out io.Writer) error {
	files, err := filepath.Glob(filepath.Join(root, "retail", "danawa", "search", "dt=*", "part-*.jsonl.gz"))
	if err != nil {
		return err
	}
	sort.Strings(files) // 같은 dt 안에서 part-<run 시작 시각>순
	latest := map[string]bronze.Envelope{}
	var order []string
	for _, f := range files {
		envs, err := readEnvelopeFile(f)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		for _, e := range envs {
			if e.Raw == nil {
				continue
			}
			if _, seen := latest[e.Raw.SHA256]; !seen {
				order = append(order, e.Raw.SHA256)
			}
			latest[e.Raw.SHA256] = e
		}
	}
	if len(order) == 0 {
		fmt.Fprintln(out, "다시 읽을 검색 원문이 없다")
		return nil
	}
	fmt.Fprintf(out, "다나와 검색 원문 %d개를 다시 추출한다(네트워크 없음)\n", len(order))

	run := bronze.NewRun(root, "scrape", args)
	w, err := run.Writer("retail/danawa", "search")
	layouts := map[string]int{}
	for _, sha := range order {
		if err != nil {
			break
		}
		e := latest[sha]
		var page []byte
		if page, err = readBlob(filepath.Join(root, e.Raw.Path), sha); err != nil {
			break
		}
		body := json.RawMessage("null")
		res, perr := danawa.ParseSearch(page)
		label := "추출 실패: "
		if perr == nil {
			if body, err = json.Marshal(res); err != nil {
				break
			}
			label = res.Layout
			if !res.ListFound {
				label = "목록 없음"
			}
			layouts[label]++
		} else {
			layouts["추출 실패"]++
			label += perr.Error()
		}
		params := map[string]any{"reparsed": true}
		for k, v := range e.Params {
			params[k] = v
		}
		err = w.Write(bronze.Envelope{
			FetchedAt: e.FetchedAt, Source: e.Source, Endpoint: e.Endpoint, Params: params,
			Status: e.Status, RequestID: e.RequestID, Body: body, Raw: e.Raw,
		})
		q, _ := e.Params["query"].(string)
		if perr == nil {
			fmt.Fprintf(out, "  %-12s 가격비교 %2d · 개별 %2d  %s\n", label, len(res.Catalog), len(res.Listings), clip(q, 50))
		} else {
			fmt.Fprintf(out, "  %s  %s\n", label, clip(q, 50))
		}
	}
	mpath, ferr := run.Finish(err)
	fmt.Fprintf(out, "구조별: %v\n", layouts)
	if mpath != "" {
		fmt.Fprintf(out, "run manifest: %s\n", mpath)
	}
	return errors.Join(err, ferr)
}

func readEnvelopeFile(path string) ([]bronze.Envelope, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	var out []bronze.Envelope
	dec := json.NewDecoder(gz)
	for {
		var e bronze.Envelope
		if err := dec.Decode(&e); err == io.EOF {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
}

// readBlob은 WriteBlob이 남긴 원문(.gz)을 풀고, 압축 전 sha256이 봉투의 값과 같은지 확인한다.
func readBlob(path, wantSHA string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	b, err := io.ReadAll(gz)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != wantSHA {
		return nil, fmt.Errorf("원문 sha256 불일치: %s", path)
	}
	return b, nil
}
