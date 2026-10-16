# API 레퍼런스 (2026-10-14 사이트 문서 요약)

원문: https://hackathon.savetax.now/#/data/api , #/data/auth , #/data/mcp , #/data/dataset
불확실하거나 바뀌었을 수 있는 값은 실제 응답을 기준으로 한다.

## 호스트와 인증

| 용도 | 호스트 | 오류 형식 | 응답 필드 |
|---|---|---|---|
| 대회 API | `https://hackathon.savetax.now/v1/*` | 평면 `{code, message, request_id, retryable, context?}`, code는 UPPER_SNAKE | 래퍼 없음, snake_case |
| 원시 데이터 REST | `https://api.savetax.now/v1/*` | 중첩 `{error:{code, message}}`, 소문자 | `{data, meta, page?}`, camelCase |
| MCP | `https://mcp.savetax.now/mcp` (Streamable HTTP) | 도구 오류는 HTTP 200 + `isError` + `{error:{code,message,hint}}` | 도구 결과 텍스트(JSON) |
| 데이터셋 | `https://savetax-dataset.reform-dev-infra.workers.dev` | 평면 `{code, message}` | 파일 |
| 평문 http | 모든 호스트 | 400 `{"error":{"code":"https_required"}}` | 토큰 재발급 권장 |

- 참가 토큰 하나(`savetax_…`)로 전부 사용. 헤더 `Authorization: Bearer <토큰>`만 쓰고, URL 쿼리에 넣지 않는다.
- 토큰 발급·폐기는 웹 '내 경기'(로그인 세션)에서만. 원문은 1회만 표시된다. 만료는 대회 마감 48시간 뒤.
- 참가 등록이 먼저: `POST /v1/events/current/registration {handle, ruleset_version, agree_ruleset_version:true}`.
- 권한: read-own, upload, submit, 데이터(rest·mcp·dataset).
- HEAD 403은 GET 실패가 아니다. GET 403 + Cloudflare 1010이면 User-Agent 문제(`Mozilla/5.0` 지정 후 1회만 재시도).

## 한도
- 데이터 REST·MCP 공유 3,600회/시간, 검색 120회/분(별도 버킷), 요청 단위로 센다.
- `GET api.savetax.now/v1/me`, MCP `savetax_get_me`는 예산에서 차감되지 않는다(분당 120회 별도 상한).
- MCP `initialize`·`tools/list`·`savetax_get_guide` 비용은 0.
- 대회 API: 호출 속도 제한·Retry-After 없음. 예외는 새 물품 조회(`items/{item_id}` 첫 호출 = 스냅샷 생성)로 참가자당 분당 30건 + 서버 전체 상한, 넘으면 429 `RATE_LIMITED`(1분쯤 대기, 횟수 안 셈). 우리 쪽 운영은 초당 1회 이하.
- 제출 누적·일일 한도: `GET /v1/events/current/me`의 `limits`(null=무제한)·`remaining`. 10/14 기준 `max_claims_per_day` 50, `max_claims_total` 100, 하루 경계 KST 자정. 거절(409 REFORM_ALREADY_FOUND, 409 PRODUCT_ALREADY_CLAIMED, 422 PRICE_GAP_TOO_SMALL, 503 CATALOG_UNAVAILABLE)은 횟수에 세지 않는다. 철회는 횟수가 돌아오지 않고, 운영진 반려는 1회를 돌려준다.

## 대회 API 주요 엔드포인트

### 공개(인증 없음)
- `GET /v1/events/current`: 일정, `ruleset_version`, `dataset_release_id`, `price_gap{rule,min_ratio,min_unit_krw}`, `server_time`, `submission_count`
  - 10/14 값: `dataset_release_id=release-live-20261012`, `ruleset_version=rules-v1`, `price_gap={enforce, 1.2, 1000000}`, `evidence_format=text`, `release_claim_on_withdraw=true`
  - (실측 10/14) `price_gap.min_ratio`·`min_unit_krw`는 **문자열**("1.2", "1000000"). 그 밖의 필드: `phase`, `admission_state`, `contest_mode`, `opens_at`, `closes_at`, `scoring_version`, `identity_version`, `item_catalog`, `event_epoch`, `schedule_version`
  - (실측 10/14 04:31Z, 로컬 시계는 NTP 대비 +0.001s) 엣지 캐시된다: `x-edge-cache: HIT|STALE`만 붙고 `Age`·`CF-Cache-Status`·`Cache-Control` 헤더는 없다. 캐시되는 것은 **본문**(`server_time`·`submission_count`)이고, STALE 응답의 `server_time`은 약 20초, 첫 관측에서는 약 2분 늦었다. **`Date` 헤더는 캐시되지 않는다** — HIT/STALE에서도 매 응답 새로 붙고 로컬 시각과 ±1s(초 단위 해상도) 안에서 일치. Bearer를 붙인 `/v1/events/current/me`(HTTP 200)는 캐시 헤더가 없고 같은 순간의 `Date`가 같다. → 시계 보정은 `events/current`의 `Date`로 충분(`check`가 그렇게 한다). 응답에 `x-request-id` 헤더가 붙는다.
- `GET /v1/events/current/insights`: headline(참가자, 선점 수, products_total, 예상 합계, 보류액), 상위 랭커, 최근 제보
- `GET /v1/events/current/insights/products?sort=&state=&limit=&cursor=`: 상품별 예상 차액 집계
- `GET /v1/events/current/leaderboard`: 순위 snapshot + `stream_cursor`

### 참가 인증 필요
- `GET /v1/events/current/me`: 한도, 사용량, remaining, 제출 목록
- `GET /v1/events/current/items/lookup?ids=a,b,…`(최대 100): 행별 `state ∈ available|claimed|claimed_by_me|reform_found|not_found`. (실측 10/16) 문서에 없는 `on_hold`도 온다 — 철회된 물품 268개 중 64개(풀리기 전 대기로 추정, 등록하지 않는다). 등재번호도 받아 item_id로 변환해 준다. 횟수 미사용. (실측 10/14) 서버가 처음 보는 물품이 섞이면 lookup도 429 `RATE_LIMITED`("새 물품을 너무 빠르게 조회하고 있습니다 — 1분쯤 뒤")가 난다. 약 2,400개 연속 조회 뒤 걸렸다
- `GET /v1/events/current/items/{item_id}`: `snapshot{name, model, manufacturer, category, listings[], source_release, fetched_at}`, `facts[{fact_id, listing_id, contract_no, request_no, line_no, unit, tax_basis, basis_id, unit_price_krw, quantity, comparable_amount_krw, scorable}]`. 첫 호출 때 서버가 물품 요약·등재 목록·최종 차수 납품 라인을 가져와 스냅샷으로 고정한다(이후 모두에게 같은 값). fact 2,000개 초과면 금액순으로 자르고 `facts_truncated: true`. 데이터 API 예산을 쓰지 않는다
- `GET /v1/events/current/found?q=&who=&status=&sort=&cursor=&limit=`: 발견 상품(참가자 등록 + 개혁신당 공개분). (실측 10/16) `limit` 1~48, 응답 `{counts, items[], next_cursor, reform_amount_note}`, 카드에 `evidence_summary{hosts[], price_gap{markup_ratio, market_unit_krw, procurement_unit_krw…}}`·`status`·`finder.handle`. `status=withdrawn`은 따로 읽는다. `found/{key}` 상세에는 `price_evidence[]{source_url, quote, observed_price_krw…}`·`lines[]`·`lint.public_note`가 있다 — 남의 근거라 우리 근거로 쓰지 않는다(`make found`)
- `GET /v1/events/current/feed?limit=1..100`: 최근 접수·수정·철회. `delta_krw` 음수 = 철회
- `GET /v1/events/current/stream`: SSE. `Last-Event-ID: <stream_cursor>`, 15초 heartbeat `:hb`. `stream.resync_required`면 순위표를 다시 읽고 재구독
- `GET /v1/events/current/stats`, `/viewer-config`
- `GET /v1/events/current/submissions/{sid}`(본인이면 `lint` 객체 포함), `/revisions`, `/counterevidence`
- `GET /v1/events/current/submission-requests/{key}`: Idempotency-Key 결과(200 영수증, 202 확정 전, 404)

### 제출·변경 (Idempotency-Key 필수, submit 권한)
- `POST /v1/events/current/submissions/preview`: 형식·산식 검사만. 응답 `ok, total_amount_krw, lines[], price_gap, product_already_claimed, claimed_by_me, next_action(submit|revise_existing|pick_another_product)`
- `POST /v1/events/current/submissions` → 201 영수증 `{submission_id, claim_id, revision, submission_state, admitted_at, amount_krw, lint_status, api_path, web_path}`
- `POST /v1/events/current/submissions/{sid}/revisions` + `If-Match: <revision>`: 전체 새 본문(상품 변경 불가, 0원 불가)
- `POST /v1/events/current/submissions/{sid}/withdraw`: 물품은 풀리고 횟수는 그대로
- `POST /v1/events/current/submissions/{sid}/counterevidence`: 남의 accepted 제출에 반증
- `POST /v1/events/current/reports`: 일반 제보(점수 없음, 제출 한도는 공유)

Idempotency-Key: 1~128자 인쇄 가능 ASCII, UUID 권장. 같은 key + 다른 본문은 409 IDEMPOTENCY_KEY_REUSED. 요청마다 새 UUID를 쓴다.

## claim 본문

```json
{
  "dataset_release_id": "release-live-20261012",
  "ruleset_version": "rules-v1",
  "canonical_product_id": "<item_id>",
  "title": "같은 모델의 시중가 대비 3.5배",
  "claim": "동일 모델·동일 보증 조건의 판매 페이지와 대조",
  "comparison_scope": "동일 모델·동일 보증·동일 설치조건",
  "reproduction_notes": "dataset 20261010-r5, sql/mart_candidates.sql@<commit>",
  "lines": [{
    "fact_id": "<facts[].fact_id, scorable만>",
    "benchmark_unit_price_krw": "57000",
    "comparison_basis_id": "<fact.basis_id 그대로>",
    "unit": "<fact.unit 그대로>",
    "tax_basis": "<fact.tax_basis 그대로>",
    "period": null,
    "price_evidence": [{
      "kind": "price_page",
      "source_url": "https://…",
      "quote": "상품명·모델·판매가·VAT·배송·판매처·확인일을 페이지에서 옮겨 적은 글",
      "observed_price_krw": "57000",
      "observed_at": "2026-10-14",
      "vat_included": true,
      "shipping_included": false,
      "note": "판정에는 안 쓰이는 메모"
    }],
    "fulfillment_evidence": []
  }]
}
```

필드 제약:
- 추가 필드는 거절(422 SCHEMA_INVALID). 금액은 모두 따옴표 문자열.
- `title` ≤ 200자, `claim` ≤ 2000자, `comparison_scope` ≤ 1000자, `reproduction_notes` ≤ 4000자(최상위에만).
- `lines` ≥ 1, fact마다 line 하나. `period`는 기간형(term)일 때만 "12개월" 같은 문자열.
- `price_evidence` 1~8개(필수), `fulfillment_evidence` 0~8개(서버 스냅샷에 납품 사실이 있으면 생략 가능). `asset_id`(파일)는 실전에서 422.
- `quote` 1~2000자, `source_url` https·2048자 이하, `observed_at`은 KST 오늘까지·2015-01-01 이후, `note` ≤ 500자.
- 예상액 = `floor(max(C − B×Q, 0))`. 모든 line이 0원이면 422 NO_POSITIVE_AMOUNT(메시지에 line별 B 상한이 있다).
- gap 미달 422 PRICE_GAP_TOO_SMALL의 `context.price_gap.max_passing_market_unit_krw`가 통과 가능한 최대 시중가다. 근거보다 낮게 적어 맞추면 lint에서 보류된다.

## lint(서버 1차 근거 검사)
- 접수 뒤 비동기 `pending → clean | suspect | unreadable`.
- suspect(+accepted) = 순위 합계에서 보류(물품은 계속 내 것). 수정하면 재검사한다.
- unreadable은 보류가 아니다(순위 유지). 하지만 마감 후 검증에서 불리할 수 있다.
- 공인 https 정적 HTML만 읽는다. JS 렌더링·봇 차단·로그인·PDF는 읽지 못한다.
- `title`·`claim`·`note`는 판정기에 보내지 않는다. `quote`만 읽을 자료로 전달된다.

## 원시 데이터 REST (api.savetax.now)
- `GET /v1/me`: 한도·사용량(예산 미차감)
- `GET /v1/releases/current`: 데이터 버전(대회 dataset_release_id와 다른 축, 제출에 쓰지 않음)
- `GET /v1/listings?q=&dtls=&cate=&vendor=&price_min=&price_max=&sort=&limit(1..50)=&cursor=&has_activity=1`
  - 기본 정렬은 관련도가 아니다(`sort=relevance` 명시). `dtls`(세부품명)로 범위를 먼저 고정한다. cursor는 같은 조건에서만 유효하다.
- `GET /v1/listings/{id}`: 등재 상세(카드·계약·첨부·deliveryCases=공급후기)
- `GET /v1/listings/{id}/deliveries?limit(≤1000)&offset&final=1`: `data.rows`, `data.finalSummary`(최종 차수 합계, 이것을 쓴다), `data.page`
- `GET /v1/listings/resolve?ids=`(≤100): 등재번호 → item_id
- `GET /v1/items/{id}`: 물품 상세 + 연결 등재(최대 100)
- `GET /v1/categories`, `GET /v1/stats`, `GET /v1/publications[/{kind}/{key}]`
- 공개 상세분석 사례로 등록된 등재는 검색에서 빠지고 상세 조회 시 404(의도된 제외).

## MCP 도구 (11개)
`savetax_get_guide`(먼저 1회), `savetax_get_releases`, `savetax_get_me`, `savetax_search_listings`(인자 q, source, categoryCode, vendorId, vendor, cert, enterpriseType, deliveryTerms, region, detailName, priceMin, priceMax, sort, limit, cursor, includeInactive, includeFacets, hasContracts, hasActivity), `savetax_get_listing`(id), `savetax_get_item`(id), `savetax_list_categories`, `savetax_get_stats`, `savetax_list_deliveries`(id, limit, offset, final), `savetax_list_publications`, `savetax_get_publication`(kind, key).
- 인자 이름은 정확히 써야 한다(`listingId`가 아니라 `id`). 스키마에 없는 인자는 bad_arguments.
- `stale_cursor` = 데이터 버전이 바뀌었으니 첫 페이지부터 다시. `_mcpTruncated` = limit를 줄여 나눠 조회.

## 데이터셋
- `GET /latest/savetax-products.zip`(Bearer 필요, 48MB, 이어받기 없음). 버전 고정은 `/releases/<ver>/`.
- 구성: `README.md`, `columns.csv`(35컬럼 사전), `quickstart.sql`, `quickstart.py`, `listings.parquet`(1,052,815행: 종합쇼핑몰 1,043,081 · 디지털서비스몰 9,734).
- `listings.csv.gz`(59MB)는 릴리스 폴더에 별도로 있다. 샘플 CSV 1,000행은 토큰 없이 받을 수 있다: `/sample/listings-sample.csv`.
- 컬럼 묶음: 식별(listing_id, item_id), 상품, 가격·계약, 업체·표시, 분류, 사이트 지표, 링크·출처, 납품요구 합계 `dlv_*`(2025-10-10~2026-09-27 주문, 최종 차수).
- 체크섬: `releases/<ver>/SHA256SUMS`(리눅스는 `sha256sum -c -`).
- (실측 10/14) 토큰 없이 열리는 메타: `/latest.json`(현재 버전 `release_id=20261010-r5`, 파일별 bytes·sha256·rows), `/releases/<ver>/manifest.json`(컬럼 사전 + `quality`: `naive_amount`/`kept_amount` = dlv_key 중복 제거 전후 납품 합계 → silver 검증 기준값), `/releases/<ver>/SHA256SUMS`, 루트 `/`(README). 없는 파일·토큰 오류는 모두 401로 보인다.
- **토큰을 보내는 데이터셋 주소는 공식 ZIP URL `https://savetax-dataset.reform-dev-infra.workers.dev/latest/savetax-products.zip` 하나뿐**(대회 에이전트 지침·docs/index.md). README에 있는 버전 고정 주소 `/releases/<ver>/…zip`에는 토큰을 보내지 않는다. 버전 고정은 받은 뒤 `releases/<ver>/SHA256SUMS`(공개) 대조로 대신한다. ZIP Worker는 `api.savetax.now/v1/me`로 토큰을 검증한다. ZIP은 HEAD가 아니라 인증된 GET으로 받는다(HEAD 403 ≠ GET 실패).
- 이 데이터셋 버전(`20261010-r5`)은 대회 `dataset_release_id`(`release-live-20261012`)와 다른 축이다. 제출에는 대회 값만 쓴다.
- 이용 조건: 대회 참가·심사용 분석에 한정. 원본 재배포·공개 게시 금지.

## 대회 API 오류 코드 요약
- 400 IDEMPOTENCY_REQUIRED / IDEMPOTENCY_KEY_INVALID / IF_MATCH_REQUIRED / IF_MATCH_INVALID / SCHEMA_INVALID
- 401 AUTH_REQUIRED(헤더 없음 / 형식 오류 / 폐기·만료를 message로 구분)
- 403 NOT_REGISTERED / SCOPE_DENIED / SELF_COUNTEREVIDENCE / PARTICIPANT_SUSPENDED … (재시도 무의미)
- 404 PRODUCT_NOT_FOUND / FACT_NOT_FOUND / SUBMISSION_NOT_FOUND / IDEMPOTENCY_KEY_NOT_FOUND
- 409 REFORM_ALREADY_FOUND / PRODUCT_ALREADY_CLAIMED / FACT_ALREADY_CLAIMED / REVISION_CONFLICT / SUBMISSION_NOT_EDITABLE / IDEMPOTENCY_KEY_REUSED / EVENT_CLOSED …
- 413 BODY_TOO_LARGE(JSON 본문 1 MiB 초과)
- 422 SCHEMA_INVALID / COMPARISON_BASIS_MISMATCH / NO_POSITIVE_AMOUNT / PRICE_GAP_TOO_SMALL / DATASET_VERSION_MISMATCH / FACT_NOT_SCORABLE / PRICE_INVALID …
- 429 PARTICIPANT_LIMIT / REVISION_LIMIT
- 503 CATALOG_UNAVAILABLE(같은 요청 재시도, 횟수 안 셈) / ADMISSION_UNAVAILABLE(같은 key로 재시도 또는 submission-requests 확인)
