-- silver.deliveries_final — 최종 차수 납품요구 라인 1행 (키 request_no, line_no)
-- 입력: src_delivery_envelopes(bronze 봉투: fetched_at, endpoint, status, body = GET /v1/listings/{id}/deliveries 응답 전체), listings(silver)
-- 최종 차수 = 라인의 finalRequest='Y' ((requestNo, lineNo)당 최대 1개). summary(전 차수 단순합)는 쓰지 않는다.
-- 같은 (계약, 물품)을 공유하는 등재는 같은 라인을 돌려주므로 (request_no, line_no)로 한 번만 남긴다.
-- 같은 라인을 여러 번 받았으면 가장 최근에 받은 것을 쓴다. 금액·수량 문자열은 DECIMAL로(float 금지), null은 null로.
-- 공급업체 사업자번호(supplierBusinessNo)는 분석에 쓰지 않아 읽지 않는다.
WITH raw AS (
  SELECT
    e.fetched_at,
    e.body -> 'data' ->> 'id' AS listing_id,
    e.body -> 'data' ->> 'contractNo' AS contract_no,
    unnest(from_json(e.body -> 'data' -> 'rows', '[{
      "requestNo": "VARCHAR", "lineNo": "INTEGER", "revision": "VARCHAR", "finalRequest": "VARCHAR",
      "requestDate": "VARCHAR", "unit": "VARCHAR", "quantity": "VARCHAR", "unitPrice": "VARCHAR",
      "contractUnitPrice": "VARCHAR", "amount": "VARCHAR", "quantityAdjustment": "VARCHAR",
      "amountAdjustment": "VARCHAR", "agencyCode": "VARCHAR", "agencyName": "VARCHAR", "supplierName": "VARCHAR"
    }]')) AS r
  FROM src_delivery_envelopes e
  WHERE e.status = 200
),
typed AS (
  SELECT
    r.requestNo AS request_no,
    r.lineNo AS line_no,
    r.revision,
    contract_no,
    listing_id,
    fetched_at,
    CAST(r.requestDate AS DATE) AS request_date,
    r.unit,
    CAST(r.quantity AS DECIMAL(20, 4)) AS quantity,
    CAST(r.unitPrice AS DECIMAL(20, 4)) AS unit_price,
    CAST(r.contractUnitPrice AS DECIMAL(20, 4)) AS contract_unit_price,
    CAST(r.amount AS DECIMAL(20, 4)) AS amount,
    CAST(r.quantityAdjustment AS DECIMAL(20, 4)) AS quantity_adjustment,
    CAST(r.amountAdjustment AS DECIMAL(20, 4)) AS amount_adjustment,
    r.agencyCode AS agency_code,
    r.agencyName AS agency_name,
    r.supplierName AS supplier_name
  FROM raw
  WHERE r.finalRequest = 'Y'
),
latest AS (
  SELECT * FROM typed
  QUALIFY row_number() OVER (PARTITION BY request_no, line_no ORDER BY fetched_at DESC, listing_id) = 1
),
seen AS (
  SELECT request_no, line_no, list_sort(list_distinct(list(listing_id))) AS listing_ids
  FROM typed
  GROUP BY request_no, line_no
)
SELECT
  t.request_no, t.line_no, t.revision,
  t.contract_no,
  li.item_id,
  s.listing_ids,
  t.request_date, t.unit, t.quantity, t.unit_price, t.contract_unit_price, t.amount,
  t.quantity_adjustment, t.amount_adjustment,
  -- 수량·금액이 모두 0이면 전량 취소로 보인다(데이터셋 dlv_lines는 이런 라인을 세지 않는다).
  (t.quantity = 0 AND t.amount = 0) AS is_zero,
  t.agency_code, t.agency_name, t.supplier_name,
  t.fetched_at
FROM latest t
JOIN seen s USING (request_no, line_no)
LEFT JOIN listings li ON li.listing_id = t.listing_id
ORDER BY li.item_id, t.request_no, t.line_no
