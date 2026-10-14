-- silver.claim_state — 물품 1행, 가장 최근 lookup 결과 (키 item_id)
-- 입력: src_lookup_envelopes(bronze/contest/lookup 봉투, body = GET /v1/events/current/items/lookup 응답 전체)
-- 선점 여부 판단에만 쓴다(프로젝트 규칙 2). 상태는 금방 바뀌므로 제출 직전에 다시 묻는다.
-- item_id를 풀지 못한 행(kind = unknown)은 키가 없어 뺀다.
WITH r AS (
  SELECT
    e.fetched_at,
    unnest(from_json(e.body -> 'items', '[{
      "input": "VARCHAR", "kind": "VARCHAR", "item_id": "VARCHAR", "state": "VARCHAR",
      "message": "VARCHAR", "my_submission_id": "VARCHAR", "current_revision": "BIGINT"
    }]')) AS it
  FROM src_lookup_envelopes e
  WHERE e.status = 200
)
SELECT
  it.item_id,
  it.state,
  it.kind,
  it.input,
  it.message,
  it.my_submission_id,
  it.current_revision,
  fetched_at AS checked_at
FROM r
WHERE it.item_id IS NOT NULL AND it.item_id <> ''
QUALIFY row_number() OVER (PARTITION BY it.item_id ORDER BY fetched_at DESC, it.input) = 1
ORDER BY it.item_id
