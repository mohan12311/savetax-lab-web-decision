# collector — Go 수집·조회·제출 CLI

설계는 `docs/ARCHITECTURE.md` 3절. 여기는 사용법과 구현 현황만 적는다.

## 명령 (저장소 루트에서 `make <명령>`)

| 명령 | 상태 | 하는 일 | 토큰 |
|---|---|---|---|
| `make check` | M1 ✅ | `/v1/events/current` 상수(지난 실행 대비 변경 감지), 대회 `/me` 한도·remaining, 데이터 API `/v1/me` 사용량, 데이터셋 최신 버전 vs 로컬 | 없으면 공개 항목만 |
| `make dataset` | M1 ✅ | `latest.json` → `SHA256SUMS`와 교차 확인 → 공식 ZIP URL(`/latest/savetax-products.zip`, 토큰을 보내는 유일한 데이터셋 주소)에서 받기(sha256·크기 검증, 실패 시 처음부터 최대 3회) → 경로 검사 후 압축 해제 → 내부 파일 재검증 → `COMPLETE.json`·`current.json` | 필요 |
| `make scrape ARGS='--query <모델명>'` | M5 ✅ | 다나와 검색 → 상위 `--top`(기본 3)개 상품 페이지 수집. robots.txt 준수(검색 Crawl-delay 10초), state 큐로 이어받기, 끝난 작업은 `--refresh`로만 다시, 403이면 중단. `--from <파일>`(한 줄 하나), `--pcode <코드>`도 받는다 | 보내지 않음(프로세스에서도 제거) |
| `crawl` `lookup` `facts` `watch` `submit` | M4~M6 | — | — |

`go run ./cmd/savetax dataset [--verify | --force]`

## 산출물

```
data/bronze/_runs/<run_id>.json                       실행별 manifest(파일·sha256·행 수·api_calls·errors_by_code)
data/bronze/contest/{event,me}/dt=YYYY-MM-DD/part-*.jsonl.gz   응답 봉투(원문 그대로)
data/bronze/data/me/dt=…/part-*.jsonl.gz
data/bronze/retail/danawa/{search,product}/dt=…/part-*.jsonl.gz  추출 결과(body) + 원문 위치(raw: path·sha256)
data/bronze/retail/danawa/html/dt=…/<sha256>.html.gz  원문 HTML(sha256은 압축 전 기준, 같은 날 같은 내용은 한 번만)
data/bronze/dataset/current.json                      silver가 읽을 기준 버전({release_id, dir, parquet})
data/bronze/dataset/<ver>/listings.parquet …          + COMPLETE.json, _meta/{latest.json,SHA256SUMS,manifest.json}
data/state/event_current.json                         check 변경 감지 기준
data/state/collector.db                               작업 큐·체크포인트(SQLite WAL) — scrape_danawa 큐 사용 중
```

## 패키지

| 패키지 | 역할 |
|---|---|
| `internal/config` | 환경변수 로드. 토큰은 `SAVETAX_TOKEN`만, 형식 검사(오류에 원문 미포함), make가 남기는 따옴표·공백 제거 |
| `internal/httpx` | https 강제, 시도마다 리미터 대기, 429·5xx·`retryable:true`만 지수 백오프+지터(Retry-After 준수, 상한 초과 시 즉시 실패), 서버가 `retryable:false`를 주면 429여도 멈춤(PARTICIPANT_LIMIT), 네트워크 오류 재시도는 GET/HEAD·Idempotency-Key 요청만, 같은 출처(scheme+host+port) 리다이렉트만 따라가고 cross-origin은 거부해 `ErrRedirectRejected`로 기록(재시도 없음, 오류에 Location 쿼리 미포함), Cloudflare 1010이면 UA를 `Mozilla/5.0`으로 1회 전환, 평면·중첩 오류 형식 통합(`APIError`), 호출 통계 |
| `internal/ratelimit` | 이름별 공유 토큰 버킷(burst 1): `data_hourly` `data_search` `data_free` `contest` `dataset` `retail:<domain>` |
| `internal/api/contest`, `internal/api/data` | 엔드포인트 래퍼. 데이터 API는 호출 종류별로 버킷을 고른다(검색 = 시간+검색 버킷, `/v1/me` = 무료 버킷) |
| `internal/dataset` | 데이터셋 동기화(위 절차) |
| `internal/store/bronze` | JSONL.gz 롤링 writer(쓰는 중 `.tmp` → 닫을 때 rename, 덮어쓰기 금지) + run manifest, `WriteBlob`(HTML 등 원문 내용 주소 보관) |
| `internal/store/state` | `tasks`(우선순위 큐, 죽은 running 복구, 재시도 한도, `Requeue`) · `checkpoints`(커서) |
| `internal/scrape` | 시중 사이트 공통: 호스트별 `Site`(robots.txt RFC 9309 파서, Crawl-delay → 리미터, 토큰 없는 클라이언트, 403 → `ErrBlocked`) |
| `internal/scrape/danawa` | 다나와 검색·상품 페이지 추출(goquery). 금액은 정수 원, 못 읽으면 null, 역산 금지 |
| `internal/logx` | JSON slog, 토큰·Bearer 값 마스킹 |

테스트: `make test` (모두 로컬 httptest TLS 서버, 실서버 호출 없음)
