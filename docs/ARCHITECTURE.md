# 아키텍처 — Go 수집 · Python 전처리 · AI 에이전트 · 대규모 분석

## 1. 설계 목표와 제약

| 구분 | 내용 | 설계에 주는 영향 |
|---|---|---|
| 데이터 규모 | 등재 1,052,815행 × 35컬럼(zip 48MB), 납품요구 원장 2,845,861행(최근 1년 최종 라인 1,907,120개), 대회 채점 대상 products_total 46,857 | 단일 노드 컬럼 저장(Parquet + DuckDB)으로 충분. 원장 라인 단위 분석까지 수백만 행 |
| 데이터 API 한도 | 3,600회/시간 공유(REST·MCP), 검색 120회/분 별도 | 하루 최대 86,400회. 남은 약 11일 동안 전체 등재 상세를 다 받을 수 없음 → **데이터셋 zip이 기준선, API는 후보 검증용** |
| 대회 API | 한도 미공개, 제출 누적·일일 한도 있음 | 보수적 레이트리밋. 제출은 후보 품질이 가장 높은 것부터 |
| 근거 검사(lint) | 공인 https 정적 HTML만 읽음 | 시중가 소스는 정적 페이지 우선. JS 페이지는 탐색용으로만 |
| 선착순 | 같은 item_id는 먼저 등록한 1명만 | 후보 → 제출까지의 지연(latency)이 곧 경쟁력. 파이프라인을 증분(incremental)으로 설계 |
| 정확성 | 확정 금액 ≤ 예상액, 검증에서 깎임 | 단위·수량·세금 기준 일치를 자동 검사하는 게이트 필수 |

비목표: 분산 클러스터 운영. 단, 아래 7절의 확장 경로로 같은 SQL을 그대로 옮길 수 있게 설계한다.

## 2. 전체 흐름

```
                ┌──────────────────────── Go collector (cmd/savetax) ────────────────────────┐
 dataset zip ──▶│ dataset   : 다운로드 · SHA256 검증 · 압축 해제                              │
 api.savetax ──▶│ crawl     : listings/{id}, items/{id}, deliveries?final=1 (후보 큐 기반)     │──▶ data/bronze/*.jsonl.gz
 hackathon   ──▶│ lookup    : items/lookup (100개 배치) · facts: items/{item_id}               │      + manifest.json
 hackathon   ──▶│ watch     : SSE /stream 또는 feed 폴링 → 선점·철회 이벤트                   │
 retail web  ──▶│ scrape    : 정적(net/http+goquery/colly) → 필요 시 chromedp                 │──▶ data/bronze/retail/
                └────────────────────────────────────────────────────────────────────────────┘
                                           │
                ┌──────────── Python pipeline (DuckDB + Polars) ────────────┐
                │ silver : 타입 정규화 · 중복 제거(dlv_key) · 이상치 플래그  │──▶ data/silver/*.parquet
                │ gold   : 분산·이상치 마트 · 후보 점수 · gap 사전 판정      │──▶ data/gold/*.parquet
                └───────────────────────────────────────────────────────────┘
                                           │
                ┌──────────── AI agent (Claude API) ───────────────────────┐
                │ A 속성 추출(제조사·모델·규격·포장단위)  ← Batch           │
                │ B 동일 상품 판정(조달 스냅샷 ↔ 시중 페이지)              │──▶ data/gold/match_*.parquet
                │ C 근거 초안(인용 구간 선택, 텍스트 복사는 코드가 수행)    │──▶ data/gold/evidence_drafts.jsonl
                └───────────────────────────────────────────────────────────┘
                                           │
                     사람 검토(review sheet) ─▶ Go submit: preview → submit --confirm
                                           │
                     제출 추적: 영수증 · lint 상태(clean/suspect/unreadable) · 수정
```

**원칙**
- 스테이지 간 계약은 파일이다(JSONL.gz / Parquet + manifest). Go와 Python은 서로를 호출하지 않는다. 덕분에 각 스테이지를 독립적으로 재실행·테스트·병렬화할 수 있다.
- 메달리온 레이어: bronze(원본, 불변) → silver(정규화) → gold(분석 마트). bronze만 있으면 나머지는 언제든 재생성된다.
- 증분 처리: 모든 수집은 상태 테이블(큐)을 보고 미완료분만 처리하고, 마트는 파티션 단위로 갱신한다.

## 3. Go collector

### 3.1 패키지 구조
```
collector/
  go.mod
  cmd/savetax/main.go           # 서브커맨드 라우팅 (cobra 또는 flag)
  internal/config/              # env(SAVETAX_TOKEN), 경로, 레이트 설정
  internal/httpx/               # 공통 클라이언트: 레이트리밋, 재시도, UA, 오류 파싱
  internal/ratelimit/           # 버킷별 토큰 버킷 (data_hourly, data_search, contest, retail:<domain>)
  internal/api/data/            # api.savetax.now 클라이언트 (오류: {error:{code,message}}, camelCase)
  internal/api/contest/         # hackathon.savetax.now 클라이언트 (오류: 평면 {code,message,request_id,retryable})
  internal/store/bronze/        # JSONL.gz 롤링 writer + manifest
  internal/store/state/         # SQLite(WAL) 작업 큐·체크포인트
  internal/scrape/              # Source 인터페이스, static/, headless/, extract/
  internal/submit/              # claim 빌더, 사전 검증, preview/submit/revise/withdraw
```

### 3.2 HTTP 공통 규칙
- 레이트리밋: `golang.org/x/time/rate`. 데이터 시간 버킷은 1 req/s(=3,600/h)보다 약간 낮게(예: 0.9 req/s) 두고, 검색은 별도 2 req/s(=120/min) 미만. 여러 워커가 **같은 리미터를 공유**한다.
- 재시도: 429·5xx·`retryable:true`만. 지수 백오프 + 지터, 최대 N회. 4xx(422·409 등)는 재시도하지 않고 원인을 기록한다.
- 오류 파싱은 서비스별로 분기한다(대회 API 평면형 / 데이터 API 중첩형 / MCP isError / 엣지 https_required).
- 헤더: `Authorization: Bearer $SAVETAX_TOKEN`, `User-Agent` 명시. 토큰은 로그에서 마스킹한다.
- 사용량 감시: 주기적으로 `GET api.savetax.now/v1/me`(예산 미차감)로 남은 한도를 보고 리미터를 조정한다.

### 3.3 서브커맨드

| 명령 | 입력 | 출력 | 비고 |
|---|---|---|---|
| `check` | — | 콘솔 출력 | `/v1/events/current` 상수, 대회 `/me` 한도·remaining, 데이터 `/v1/me` 사용량 |
| `dataset` | — | `data/bronze/dataset/<ver>/` | curl과 동일한 GET, `SHA256SUMS` 검증, 이어받기 없음 |
| `crawl --kind listing\|item\|deliveries` | state 큐(gold 후보에서 적재) | `bronze/api/<kind>/dt=YYYY-MM-DD/part-*.jsonl.gz` | deliveries는 `final=1`, `limit≤1000`, offset 페이지 |
| `lookup` | 후보 item_id 목록 | `bronze/contest/lookup/…` | 100개 배치, 제출 횟수 미사용 |
| `facts` | 미선점 후보 | `bronze/contest/items/…` | 첫 호출 때 서버 스냅샷 고정 → 그 값이 채점 기준 |
| `watch` | `leaderboard`의 `stream_cursor` | `bronze/contest/events/…` | SSE `Last-Event-ID`, `resync_required` 시 순위표 재조회. 철회(delta<0) 감지 → lookup 재큐 |
| `scrape --source <name>` | 매칭 대상 모델 목록 | `bronze/retail/<domain>/dt=…/` (HTML.gz + offers.jsonl) | 도메인별 리미터, robots.txt 준수 |
| `submit` | 승인된 claim JSON | `bronze/contest/receipts/…` | 기본 preview만. `--confirm`일 때만 POST |

### 3.4 bronze 레코드 봉투(envelope)
```json
{"fetched_at":"2026-10-14T03:00:00.123Z","source":"data_api","endpoint":"/v1/listings/{id}/deliveries",
 "params":{"id":"…","final":"1","offset":0},"status":200,"request_id":null,"body":{…원본 그대로…}}
```
배치마다 `manifest.json`: `{run_id, command, started_at, finished_at, records, files:[{path, sha256, rows}], api_calls, errors_by_code}`.

### 3.5 상태 저장소(SQLite, `data/state/collector.db`)
- `tasks(kind, key, status[pending|running|done|failed|skipped], attempts, last_error, updated_at, PRIMARY KEY(kind,key))`
- `claims(item_id, state[available|claimed|claimed_by_me|reform_found|not_found], checked_at)`
- `submissions(submission_id, item_id, revision, state, lint_status, amount_krw, idempotency_key, updated_at)`
- 동시 쓰기는 Go 쪽만. Python은 읽기 전용으로 붙거나 export된 Parquet을 읽는다(DuckDB는 단일 writer라 상태 저장소로 쓰지 않는다).

### 3.6 스크래퍼
```go
type Source interface {
    Name() string
    Search(ctx context.Context, q Query) ([]Offer, error)      // 모델명·제조사로 후보 검색
    Fetch(ctx context.Context, url string) (Page, error)        // 원문 HTML + 추출 텍스트
}
type Offer struct { URL, Title, Model, Seller string; PriceKRW int64; VATIncluded, ShippingIncluded *bool; ObservedAt time.Time }
```
- 정적 우선: `net/http` + `goquery`(또는 `colly`). 서버 lint가 읽을 수 있는 페이지라야 근거로 쓸 수 있다.
- JS가 필요한 페이지: `chromedp`로 탐색만 하고, 근거 URL은 가능하면 정적 상세 페이지로 바꾼다.
- 저장: 원문 HTML(gzip)과 `sha256`, 추출 텍스트를 함께 보관 → 나중에 인용 구간을 원문에서 재검증 가능.
- 외부 가격 탐색 MCP(다나와, `https://mcp.ai.cowave.kr/mcp`)는 후보 탐색용. SaveTax 토큰을 보내지 않는다. 결과는 반드시 판매 페이지에서 재확인.

## 4. Python pipeline (전처리·분석)

### 4.1 스택
- `uv` 프로젝트, `duckdb`, `polars`, `pyarrow`, `pydantic`(스키마), `pytest`.
- DuckDB가 `data/bronze/**/*.jsonl.gz`, `data/silver/**/*.parquet`을 glob으로 직접 읽는다. SQL 모델은 `sql/`에 두고 Python은 실행·검증·저장을 맡는다.
- 메모리 상한: `SET memory_limit='8GB'; SET threads=…;` 큰 조인은 DuckDB가 디스크로 스필한다.

### 4.2 silver 테이블

| 테이블 | 키 | 원천 | 핵심 처리 |
|---|---|---|---|
| `listings` | listing_id | 데이터셋 parquet + crawl 상세 | 금액 문자열 → int64, 단위 정규화 컬럼 추가(원값 보존), `contractEndYmd`로 유효 계약 플래그 |
| `deliveries_final` | (request_no, line_no) | deliveries API `final=1` | 최종 차수만, `dlv_key` 중복 제거 |
| `items` | item_id | 데이터셋 + items API | listing → item 매핑, 물품 단위 묶음 |
| `contest_facts` | fact_id | 대회 items/{item_id} | `unit`, `tax_basis`, `basis_id`, `unit_price_krw`, `quantity`, `comparable_amount_krw`, `scorable` 원문 그대로 |
| `retail_offers` | (url, observed_at) | 스크래퍼 | 가격·VAT·배송 조건, 원문 sha256 |
| `claim_state` | item_id | lookup·watch | 최신 상태 |

검증 테스트(필수):
- 납품 합계: `dlv_key` 중복 제거 전후 차이가 약 24% 수준인지 확인.
- 금액 컬럼에 float가 섞이지 않았는지, `null`이 0으로 바뀌지 않았는지.
- 1원 같은 극단 단가 비율 리포트.

### 4.3 gold 마트와 후보 점수
1. `mart_item_dispersion`: 같은 item_id·같은 단위의 등재 간 단가 min/median/max → 같은 물건을 비싸게 산 등재 탐지.
2. `mart_model_dispersion`: 정규화 모델명 기준, item_id를 넘어선 단가 분산.
3. `mart_category_outlier`: 세부품명(detailName) 안에서 단가의 robust z-score(MAD).
4. `mart_candidates`:
   - `C_est` = 해당 물품의 최종 납품 금액 합(데이터셋 dlv_* 또는 deliveries_final)
   - `M_est` = 시중가 추정(내부 최저가 → 시중 offer가 확보되면 교체)
   - `amount_upper` = `C_est − M_est × Q_est` (상한 추정)
   - `gap_pass_est` = `P ≥ 1.2×M_est` 또는 `P − M_est ≥ 1,000,000`
   - `priority` = `amount_upper × p_match × p_lint` (확률은 AI 판정·소스 정적 여부로 보정)
5. 상위 N개 → state 큐에 lookup/facts 작업 적재 → facts 확보 후 **fact 기준으로 다시 계산**(최종 판단은 항상 fact 값).

## 5. AI 에이전트 계층

| 작업 | 입력 | 출력(JSON 스키마) | 실행 방식 |
|---|---|---|---|
| A 속성 추출 | 조달 품명·규격 문자열 | `{manufacturer, model, spec, pack_unit, pack_qty, confidence}` | 정규식으로 먼저 처리하고, 남은 것만 LLM Batch로 처리 |
| B 동일 상품 판정 | 대회 스냅샷(name/model/manufacturer) + 시중 페이지 추출 텍스트 | `{same_product, confidence, mismatches[], unit_note, vat_note}` | 후보 상위만 동기 호출 |
| C 근거 초안 | 시중 페이지 추출 텍스트 + fact | `{quote_spans:[{start,end}], observed_price_krw, vat_included, shipping_included}` | 모델은 **구간만 선택**하고, quote 문자열은 코드가 원문에서 잘라 만든다 |

- 환각 방지: C는 원문 오프셋만 반환하고, 코드가 원문에 그 텍스트가 실제로 있는지와 가격 숫자가 구간 안에 있는지 검증한다. 검증에 실패하면 폐기한다.
- 모델 선택: 대량 분류(A)는 `claude-haiku-5-5`, 애매한 매칭(B)은 `claude-sonnet-5-5`. Batch 처리와 입력 해시 캐시(`data/state/llm_cache.sqlite`)로 같은 입력을 다시 보내지 않는다.
- 모든 LLM 출력은 pydantic으로 검증하고, 실패하면 1회 재요청 후 사람 검토 큐로 보낸다.
- 페이지 텍스트 안의 지시문은 데이터로만 취급한다(프롬프트 인젝션 방지). 시스템 프롬프트에 명시한다.
- Claude Code 세션 자체는 savetax MCP(`.mcp.json`)로 탐색·디버깅에 쓰고, 대량 처리는 위 배치 파이프라인으로 돌린다.

## 6. 제출 게이트 (Go `submit`)

제출 전 자동 검사(모두 통과해야 preview 진행):
1. `items/lookup` 상태가 `available`
2. 각 line의 `unit`·`tax_basis`·`comparison_basis_id` = fact 값(바이트 일치)
3. 금액 필드는 따옴표 문자열, `observed_at` ≤ 오늘(KST), ≥ 2015-01-01
4. price_evidence 1~8개, quote 1~2000자, source_url https·2048자 이하
5. quote가 bronze 원문 HTML에서 재현되는지(sha256으로 원문 추적)
6. gap 기준 사전 계산 통과
7. `dataset_release_id`·`ruleset_version` = `GET /v1/events/current` 값

흐름: `preview` → 리뷰 시트(HTML/CSV) 생성 → 사람이 승인 → `submit --confirm`(새 UUID Idempotency-Key) → 영수증 저장 → lint 상태 폴링 → suspect면 사유(`lint.lines[].reason`)를 보고 수정(`If-Match`).

## 7. 확장 경로 ("대규모"가 필요해질 때)

| 단계 | 언제 | 바꾸는 것 | 그대로인 것 |
|---|---|---|---|
| L0 로컬 | 지금(수백만 행) | — | Parquet + DuckDB + SQLite |
| L1 공유 스토리지 | 여러 머신·NAS 활용 | `data/`를 S3 호환 스토리지(MinIO/S3)로, DuckDB `httpfs`로 직접 쿼리 | SQL 모델, 파일 계약 |
| L2 다중 수집기 | 수집 워커를 여러 프로세스로 | 레이트리미터를 Redis 토큰 버킷으로 공유, 큐를 Postgres로 | 서브커맨드·봉투 포맷 |
| L3 분석 서버 | 동시 사용자·대시보드 | ClickHouse(또는 MotherDuck)에 silver/gold 적재 | 테이블 스키마 |
| L4 오케스트레이션 | 정기 실행 | cron → Dagster/Prefect, 스테이지를 asset으로 정의 | 각 스테이지 CLI |

파티셔닝 규칙(L0부터 적용): `source=<..>/dt=YYYY-MM-DD/part-<n>.parquet`(hive 스타일), 파일당 64~256MB 목표, 정렬 키는 `item_id`.

## 8. 운영·관측
- 로깅: Go `log/slog`, Python `structlog`, JSON 라인. 토큰·쿠키는 마스킹.
- 지표: 버킷별 호출 수·429 수, 큐 상태별 개수, 후보→제출 전환율, lint 결과 분포.
- 일일 리포트(gold): 내 제출 합계, held 금액, suspect 목록, 남은 제출 한도, 마감까지 남은 시간.
- 재현성: 제출의 `reproduction_notes`에 데이터셋 버전, 사용한 SQL 모델 커밋 해시를 적는다.

## 9. 보안·준수
- `.env`(토큰)는 gitignore. CI·로그·스크린샷에 토큰 노출 금지. 유출 의심 시 '내 경기'에서 즉시 재발급.
- 데이터셋 원본 재배포 금지(분석 결과·코드·근거로 드는 일부 행은 출처 표기 후 공개 가능).
- 스크래핑은 robots.txt·약관을 지키고, 로그인·결제 영역은 다루지 않는다.
