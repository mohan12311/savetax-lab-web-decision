# 아키텍처 — Go 수집 · Python 전처리 · AI 에이전트 · 대규모 분석

## 1. 설계 목표와 제약

| 구분 | 내용 | 설계에 주는 영향 |
|---|---|---|
| 데이터 규모 | 등재 1,052,815행 × 35컬럼(zip 48MB), 납품요구 원장 2,845,861행(최근 1년 최종 라인 1,907,120개), 대회 대상은 물품 전체(약 99만), 스냅샷은 조회·등록된 물품만 | 단일 노드 컬럼 저장(Parquet + DuckDB)으로 충분. 원장 라인 단위 분석까지 수백만 행 |
| 데이터 API 한도 | 3,600회/시간 공유(REST·MCP), 검색 120회/분 별도 | 하루 최대 86,400회. 남은 약 11일 동안 전체 등재 상세를 다 받을 수 없음 → **데이터셋 zip이 기준선, API는 후보 검증용** |
| 대회 API | 속도 제한 없음(새 물품 조회만 분당 30건), 제출 하루 50건·누적 100건 | 우리 쪽 초당 1회 이하. 제출 100건이 실제 병목 — 후보 품질이 가장 높은 것부터 |
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
 retail web  ──▶│ scrape    : 다나와 정적 HTML(net/http+goquery), robots.txt 준수             │──▶ data/bronze/retail/
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
  internal/scrape/              # Site(호스트별 robots·리미터·토큰 없는 클라이언트), danawa/(검색·상품 추출)
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
| `crawl --kind listing\|item\|deliveries` | state 큐(gold 후보에서 적재) | `bronze/data/<kind>/dt=YYYY-MM-DD/part-*.jsonl.gz` (`run.Writer("data", kind)`) | deliveries는 `final=1`, `limit≤1000`, offset 페이지. 봉투 `body`는 응답 전체(`{data:{id, contractNo, finalSummary, rows, page}}`) — silver `deliveries_final`이 이 경로를 읽는다 |
| `lookup --from <파일> [--limit N]` | `data/gold/lookup_ids.txt`(`make candidates`가 만든다) | `bronze/contest/lookup/dt=…/part-*.jsonl.gz` | 100개 배치, 스냅샷 안 만듦, 제출 횟수 미사용. 큐 없이 매번 목록 전체(끊기면 다시 실행). 응답에 빠진 번호는 missing으로 센다 |
| `facts` | 미선점 후보 | `bronze/contest/items/…` | 첫 호출 때 서버 스냅샷 고정 → 그 값이 채점 기준 |
| `watch` | `leaderboard`의 `stream_cursor` | `bronze/contest/events/…` | SSE `Last-Event-ID`, `resync_required` 시 순위표 재조회. 철회(delta<0) 감지 → lookup 재큐 |
| `scrape --source danawa` | `--query`·`--from 파일`·`--pcode` (state 큐 `scrape_danawa`) | `bronze/retail/danawa/{search,product}/dt=…/part-*.jsonl.gz` + `retail/danawa/html/dt=…/<sha256>.html.gz` | 도메인별 리미터, robots.txt 준수, 403이면 중단 |
| `probe --url <주소> [--need 문구] \| --from <파일>` | 시중 페이지 주소 | `bronze/retail/probe/{page,html}/…` | 정적 HTML의 본문 글자에 가격(N원)·문구가 있는지 판정(`static_ok`·`price_not_in_text`·`empty_shell`·`needle_missing`). EUC-KR 해독, robots 준수, 403이면 그 도메인 건너뜀, 토큰 없음 |
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

### 3.6 스크래퍼 (M5, 다나와)
다나와 MCP는 쓰지 않는다(2026-10-14 결정: IP 허용목록 403, 그리고 LLM이 HTML 원문을 읽지 않게 해 토큰을 아끼기 위함). 대신 Go가 다나와 정적 HTML을 직접 받아 추출하고, LLM에는 추출 결과만 넘긴다.

```
savetax scrape --source danawa --query <모델명> [--top 3] | --from <파일> | --pcode <코드> [--refresh]
  검색  search.danawa.com/dsearch.php?query=…   → 가격비교 상품(pcode·이름·스펙·최저가·하위 상품) + 쇼핑몰 개별 상품(참고용)
  상품  prod.danawa.com/info/?pcode=…           → 이름·스펙·제조사·최저가(배송비 별도/포함)·판매처 수 + 쇼핑몰별 최저가(정적 목록 상위 일부)
```
- `internal/scrape.Site`: 호스트마다 robots.txt를 먼저 읽어 Disallow는 보내기 전에 막고(`ErrDisallowed`), Crawl-delay를 리미터 하한으로 건다(검색 10초, 상품은 `SAVETAX_RETAIL_RPS` 기본 0.5). 클라이언트는 토큰 없이 만든다. 403은 `ErrBlocked` → 실행 중단(같은 도메인 반복 금지).
- 작업은 state 큐 `scrape_danawa`(`q:<검색어>`, `p:<pcode>`)로 돌아 끊겨도 이어진다. 검색이 찾은 상위 `--top`개 상품은 `p:` 작업으로 곧바로 이어 받는다. 끝난 작업은 `--refresh`로만 다시 받는다. 404는 skipped("없음"이지 가격 정상이 아님).
- 저장: 원문 HTML은 `WriteBlob`으로 내용 주소(`<sha256>.html.gz`, sha256은 압축 전 원문)에, 추출 결과는 봉투 `body`에, 원문 위치는 봉투 `raw`에 둔다. 추출에 실패한 페이지도 원문은 남긴다(`body: null`, 구조 변경 확인용).
- 추출 원칙: 금액은 정수 원, 못 읽은 값은 null. 페이지에 없는 값을 계산하지 않는다(배송비 포함가에서 상품가 역산 금지). 쇼핑몰 목록은 배송비 포함 표시 모드인지(`mall_prices_include_shipping`)를 함께 남긴다.
- 쓰지 않는 경로: `/bridge/`(쇼핑몰 이동 링크), `/info/ajax/`(전체 판매처 목록) — robots Disallow. 쇼핑몰 개별 상품은 근거 페이지로 쓰지 않는다(이동 링크가 bridge이고, 오픈마켓 페이지는 근거 검사가 못 읽는다).
- 약관: 자동 수집 명시 금지 조항은 없지만 "무단 복제·가공 금지"(콘텐츠산업 진흥법)라 저속으로만 받고, 원문은 `data/`(gitignore)에만 둔다. 테스트 픽스처는 구조만 흉내 낸 합성 HTML이다.
- 근거로 쓸 때: 상품 페이지 URL이 근거 후보이고, quote·관측가는 보관 원문에서 다시 확인한다(6절 게이트 5). 다른 쇼핑몰·제조사 페이지가 필요해지면 같은 `Site`로 소스를 더한다.
- 검색 페이지 구조 변경(2026-10-15 확인): 통합검색이 Next.js 앱이 되어 `ul.product_list`가 없다. 목록은 응답 HTML 안 `self.__next_f.push([1,"…"])` 조각의 react-query 상태(queryKey `["search","productList",…]`)에 JSON으로 실린다. `prodType` 1 = 가격비교 상품(숫자 pcode), 3 = 쇼핑몰 개별 상품. 파서는 옛 목록 → 새 앱 데이터 순서로 읽고 `layout`(`html_list`·`next_flight`)을 남긴다. 다나와가 검색어를 고치면 `searched_keyword`에 남긴다. 하위 상품에 `중고품`이 섞이므로 시중가 비교에서 뺀다. 상품 페이지(`/info/`)는 바뀌지 않았다.
- 파서를 고친 뒤에는 다시 받지 않고 `make scrape ARGS='--reparse'`로 보관 원문을 다시 추출한다(sha256 확인, 원래 봉투는 두고 `params.reparsed=true` 봉투를 덧붙임 — 읽는 쪽은 같은 원문의 나중 봉투를 쓴다).

## 4. Python pipeline (전처리·분석)

### 4.1 스택
- `uv` 프로젝트, `duckdb`, `polars`, `pyarrow`, `pydantic`(스키마), `pytest`.
- DuckDB가 `data/bronze/**/*.jsonl.gz`, `data/silver/**/*.parquet`을 glob으로 직접 읽는다. SQL 모델은 `sql/`에 두고 Python은 실행·검증·저장을 맡는다.
- 메모리 상한: `SET memory_limit='8GB'; SET threads=…;` 큰 조인은 DuckDB가 디스크로 스필한다.

### 4.2 silver 테이블 (M2, `make silver`)

`python -m savetax_pipeline.silver`가 `sql/staging_<table>.sql`을 차례로 실행해 `data/silver/<table>.parquet`과 `manifest.json`을 만든다. 매번 전체를 다시 만든다(약 3초). 차단 검사가 하나라도 실패하면 기존 silver를 그대로 두고 종료 코드 1.

| 테이블 | 키 | 원천 | 핵심 처리 |
|---|---|---|---|
| `listings` | listing_id | 데이터셋 parquet | 35컬럼 원값 보존(`price`는 DECIMAL(18,3) 그대로 — 소수 단가 68건). 추가: `contract_no`(등재번호 앞 11/13자), `unit_norm`(EA→개, ㎡·m²→m2, KG→kg 등 표기만 통일, 포장 단위는 그대로), `price_flag`(`lt_10`·`ge_1e9`, 거르지 않고 표시만), `contract_end_clean`(2100년 이후 입력 오류 → null), `contract_active`(as_of 기준, 종료일 불명은 null), `dataset_release_id` |
| `dlv_keys` | dlv_key | listings | 납품 키 1행. 납품 합계는 이 표에서 더한다. 키 → 물품·계약은 항상 1개(검사) |
| `items` | item_id | listings + dlv_keys | 물품 속성(물품 안에서 하나로 같음 — 검사), 등재·유효 등재·계약 수, `units`, 납품 합계(키 기준, 키 없으면 null). 수량은 모든 키의 단위가 하나일 때만 합산 |
| `deliveries_final` | (request_no, line_no) | `bronze/data/deliveries` 봉투 | `finalRequest='Y'` 라인만, 공유 등재·재수집 중복 제거(최신 `fetched_at`), 금액·수량 문자열 → DECIMAL(20,4), `listing_ids`·`item_id`·`is_zero`. 사업자번호는 읽지 않는다. crawl 전이라 지금은 빈 표 |
| `contest_facts` | fact_id | 대회 items/{item_id} | (M4) `unit`, `tax_basis`, `basis_id`, `unit_price_krw`, `quantity`, `comparable_amount_krw`, `scorable` 원문 그대로 |
| `retail_offers` | (url, observed_at) | 스크래퍼 | (M5) 가격·VAT·배송 조건, 원문 sha256 |
| `claim_state` | item_id | `bronze/contest/lookup` 봉투 | 물품별 가장 최근 lookup 결과(state·checked_at). item_id를 못 푼 행은 뺀다. 선점 판단에만 쓴다 |

- as_of = 데이터셋 manifest `source.stamp.listing_observed_max`의 KST 날짜(20261010-r5는 2026-10-04). 오늘 날짜가 아니라 등재 관측일이라 재실행해도 결과가 같다.
- `manifest.json`: 입력(데이터셋 sha256, deliveries 파일별 sha256), 코드(`git describe`, SQL 파일 sha256), 표별 행 수·sha256, 검사 결과, 리포트.

차단 검사(`checks.py`, 데이터셋 manifest `quality` 수치와 대조 — 그 릴리스 manifest에 없는 수치는 검사하지 않는다):
- 납품 합계: 키 기준 합 = `kept_amount`(17,566,373,107,109원), 등재별 단순 합 = `naive_amount`(21,760,228,811,978원, 23.87% 과대). 키 수·공유 등재 수·최대 공유·전량 취소 키·수량 null 수도 대조.
- 키 안에서 `dlv_*` 값이 갈리지 않음, 키 → 물품·계약 1개, `dlv_shared_listings` = 실제 공유 등재 수.
- null 보존: 원본과 silver.listings의 컬럼별 null 수가 같다(null → 0 변환 감시).
- float 금지: **쓴 Parquet 파일 자체**의 스키마에 FLOAT/DOUBLE이 없다. DuckDB `sum()`은 HUGEINT를 내고 Parquet은 이를 DOUBLE로 쓰므로, 합계는 SQL에서 BIGINT로 캐스트한다.
- 단가 이상치(`price_lt_10` 44, `price_ge_1e9` 287)·계약 종료일 오류(52)·할인(591) 건수.

리포트(차단 안 함): 이상 단가 비율·단위별 분포, 단순 합산 과대율, 계약 유효 분포, 단위 표기 수, deliveries 봉투·라인 수, 최종 표시 없는 라인 수, `finalSummary.amountSum` ≠ 라인 합인 등재 수(일부 페이지만 받았거나 원장 변경).

참고(10/14 실측): deliveries `finalSummary.lineCount`는 수량·금액이 모두 0인 라인을 포함하지만 데이터셋 `dlv_lines`는 뺀다(같은 등재에서 5 vs 4). 금액 합은 같다.

### 4.3 gold 후보 (M3, `make candidates`)

`python -m savetax_pipeline.candidates`가 `sql/mart_candidates.sql`로 `data/gold/candidates.parquet`(납품 있는 물품 1행), `lookup_ids.txt`, `candidates.manifest.json`을 만든다. **탐색용 추정**이다 — 시중가 근거를 찾을 순서를 정할 뿐, 제출 값은 대회 fact로 다시 계산한다.

- 키(계약×물품)마다 구매 단가 p = 키에 묶인 등재가의 중앙값(주문 평균 단가 ÷ 등재가의 중앙값이 1.0이라 대용으로 충분), 납품 a = `dlv_amount`.
- 기준가 m은 **조달 안에서 같은 것이 더 싸게 등재된 가격**(시중가 아님). 이상 단가 등재는 기준가에서 뺀다.
- 잠재액은 대회 산식 C − M×Q를 키마다 a×(p−m)÷p로 근사하고, gap 기준(P ≥ 1.2M 또는 P−M ≥ 100만)을 넘는 키만 더한다.

| 티어 | 기준가 | 조건 | 성격(10/14 첫 실행) |
|---|---|---|---|
| A | 같은 물품번호·단위의 최저 등재가 | 잠재액 ≥ 1천만 | 745개. 같은 물건이라는 근거는 가장 강하지만, 상위는 레미콘(지역별 가격)·금속제패널처럼 시중 판매 페이지가 없는 품목이 많다 |
| B | 같은 정규화 모델명(영문·숫자 5자 이상)·세부품명·단위인 다른 물품번호의 최저가 | 잠재액 ≥ 1천만 | 1,687개. 상위는 소프트웨어 라이선스 — 채널 수·에디션 차이일 수 있어 사람이 봐야 한다 |
| C | 세부품명·단위·포장 규격 중앙값(규격 없음 30건, 있음 10건 이상) | 중앙값의 3배 이상, 납품 ≥ 1억 | 1,715개. 포장 규격(상품명의 첫 kg·g·L·ml 표기)이 같은 것끼리만 비교해 제설제 1000kg 대 20kg 같은 잡음을 뺐다. 그래도 규격·사양 차이가 많아 가장 약하다 |

- 정렬: 티어 있는 것 먼저, 추정 잠재액순. `make candidates`는 티어별 상위 10을 따로 보여 준다(티어끼리 같은 눈금이 아니다).
- `lookup_ids.txt`: 티어가 있거나 납품 1억 원 이상, 이미 `claimed`·`reform_found`·`not_found`로 확인된 것 제외. 흐름은 `make candidates → make lookup → make silver → make candidates`.
- 시중가 범위(`retail_ids.txt`·`scrape_queries.txt`): 티어가 있고 `available`이거나 미확인인 물품 중 물품분류번호 앞 2자리 43(IT)·44(사무기기)·45(AV·사진)·52(가전), 소프트웨어(4323) 제외. 검색어 `retail_query`는 모델명에서 원산지 접두어(`(JP)` 등)만 뗀 값이고 같은 검색어는 한 번만 둔다. 이 범위만 다시 확인하려면 `make lookup IDS=retail_ids`, 다나와는 `make scrape ARGS='--from <절대경로>/data/gold/scrape_queries.txt --top 0'`(검색만 받고, 모델이 맞는 상품만 `--pcode`로 상품 페이지를 받는다). 10/15 182개 물품·검색어 176개.
- 10/15 시중가 범위 대조 결과: **gap 기준 통과 0개**. 다나와 가격비교 상품과 모델이 맞은 물품은 모두 조달가가 시중가와 비슷하거나 더 쌌다(EOS R1 1.03배, HC-X1600 0.96, XA65 0.92, 100QNED86BK 0.84, DR-1400K 0.80). 쇼핑몰 개별 상품으로 본 같은 제품도 0.48~0.92배였다. 내부 배수가 컸던 나머지는 시장에 같은 모델이 없는 조달 전용 장비(141개는 가격비교 상품 0개)였다. 모델명 부분 일치는 액세서리(EOS R1용 필름·케이지)나 다른 제품(망전환장치 G610 ↔ 모니터 27G610A)을 잘못 잡으므로, 가격비교 상품은 분류·이름을 사람이 확인한다.
- 내부 기준가만으로는 거짓 양성이 많다(10/14 확인: Windows Server 2025 2코어 티어 B는 조달 154,000원 < 시중 199,000원, EOS R1 티어 C는 조달 830만 원 대 시중 808만 원으로 1.03배). 그래서 시중가(다나와 등 정적 페이지)로 gap 기준을 다시 재는 단계가 필수다.
- 다음 단계: 시중가 근거 가능성(정적 판매 페이지가 있는 품목인가)을 점수에 넣고, 대회 fact로 C·Q·P를 확정해 다시 계산한다(M4).

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
