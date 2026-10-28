# collector — Go 수집·조회·제출 CLI

설계는 `docs/ARCHITECTURE.md` 3절. 여기는 사용법만 적는다. 설계에 있던 `crawl`(후보 큐 기반 대량 수집)·`watch`(SSE 피드 감시)는 구현하지 않았다.

## 명령 (저장소 루트에서 `make <명령>`)

| 명령 | 하는 일 | 토큰 |
|---|---|---|
| `make check` | `/v1/events/current` 상수(지난 실행 대비 변경 감지), 대회 `/me` 한도·remaining, 데이터 API `/v1/me` 사용량, 데이터셋 최신 버전 vs 로컬 | 없으면 공개 항목만 |
| `make dataset` | `latest.json` → `SHA256SUMS`와 교차 확인 → 공식 ZIP URL(`/latest/savetax-products.zip`, 토큰을 보내는 유일한 데이터셋 주소)에서 받기(sha256·크기 검증, 실패 시 처음부터 최대 3회) → 경로 검사 후 압축 해제 → 내부 파일 재검증 → `COMPLETE.json`·`current.json` | 필요 |
| `make lookup [ARGS='--limit N']` | `data/gold/lookup_ids.txt`를 대회 `items/lookup`으로 100개씩 조회 → `bronze/contest/lookup/`. 스냅샷을 만들지 않고 제출 횟수도 쓰지 않는다. 큐 없이 매번 목록 전체. 429면 65초 뒤 같은 묶음을 다시 묻는다. `--from <파일>`, `--id <번호>`도 받는다 | 필요 |
| `make facts ID=… [ARGS='--draft']` | 지정 물품의 `items/{item_id}`(스냅샷·납품 fact) → `bronze/contest/items/`, 비교 기준별 납품 단가 P와 gap 기준을 통과하는 최대 시중가. `--draft`는 fact 값을 글자 그대로 옮긴 본문 초안 | 필요 |
| `make found [ARGS='--max-pages N \| --detail <key> \| --counter <sid>']` | 공개 발견 카드 목록·상세, 제출별 공개 반증 목록 → `bronze/contest/{found,found_detail,counterevidence}/`. 출처 분포·검토 기준 파악용이고 남의 근거는 쓰지 않는다 | 필요 |
| `make listing ARGS='--id <등재번호> \| --from <파일>'` | 데이터 API 등재 상세 → `bronze/data/listings/`(동급 비교용 속성, 데이터 API 예산 사용) | 필요 |
| `make scrape ARGS='--query <모델명>'` | 다나와 검색 → 상위 `--top`(기본 3)개 상품 페이지 수집. robots.txt 준수(검색 Crawl-delay 10초), state 큐로 이어받기, 끝난 작업은 `--refresh`로만 다시, 403이면 중단. `--from <파일>`, `--pcode <코드>`, 파서 수정 뒤 `--reparse` | 보내지 않음(프로세스에서도 제거) |
| `make scrape SOURCE=naver ARGS='--from <파일>'` | 네이버 쇼핑 검색 API(공식, 탐색용) → `bronze/retail/naver/search/` | 참가 토큰 없음, 네이버 키만 |
| `make probe ARGS='--url <주소> --need <모델명>'` | 시중 페이지를 정적 HTML로 받아 본문 글자에 가격·문구가 있는지 판정(lint 가독성 미리보기). EUC-KR 해독, robots 준수, 403 도메인은 건너뜀. `--from <파일>`(`주소 \| 문구 \| 문구`) | 보내지 않음(프로세스에서도 제거) |
| `make preview ID=…` · `make submit ID=…` | 본문 게이트 검사(docs/ARCHITECTURE.md 6절) → preview(dry-run). `submit`(`--confirm`)은 사람의 명시적 승인 뒤에만 | 필요 |
| `make preview-revise ID=…` · `make revise ID=…` | 내 기존 제출의 수정(`If-Match: <revision>`, 전체 새 본문) | 필요 |
| `make submit-status KEY=…` | Idempotency-Key로 접수 결과·lint 다시 보기 | 필요 |

`go run ./cmd/savetax dataset [--verify | --force]`

데모 모드(2부, `studio/README.md`): `SAVETAX_DEMO=1`일 때만 `SAVETAX_CONTEST_BASE_URL`(경로 없는 루프백 https)과 `SAVETAX_DEMO_CA`(가짜 서버 인증서)를 받는다. 토큰은 `savetax_demo_` 접두어만 받고, HTTP 클라이언트는 그 인증서만 신뢰한다(실서버와 TLS가 맺어지지 않는다). 데모가 아닐 때 두 변수가 있으면 시작하지 않는다.

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
