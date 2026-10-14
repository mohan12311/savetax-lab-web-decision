-- silver.items — 물품(item_id) 1행
-- 입력: listings(silver), dlv_keys(silver)
-- 물품 속성은 물품 안에서 하나로 같다(검사: checks.item_attrs_vary). 그래도 섞이지 않게 listing_id가 가장 작은 등재 한 행에서 가져온다.
-- 납품 합계는 dlv_keys(중복 제거된 키)를 물품별로 더한다. 키가 없으면 null — 0이 아니다.
-- sum()은 HUGEINT를 내고 Parquet은 이를 DOUBLE로 쓰므로 BIGINT로 바꿔 둔다(넘치면 오류로 멈춘다).
WITH l AS (
  SELECT
    item_id,
    arg_min(name, listing_id) AS name,
    arg_min(item_name, listing_id) AS item_name,
    arg_min(detail_category, listing_id) AS detail_category,
    arg_min(detail_category_code, listing_id) AS detail_category_code,
    arg_min(manufacturer, listing_id) AS manufacturer,
    arg_min(model, listing_id) AS model,
    arg_min(spec, listing_id) AS spec,
    arg_min(source, listing_id) AS source,
    count(*) AS n_listings,
    count(*) FILTER (WHERE contract_active) AS n_listings_active,
    count(DISTINCT contract_no) AS n_contracts,
    count(DISTINCT vendor_name) AS n_vendor_names,  -- 업체명 표기가 섞여 있어 업체 수의 근사치다
    list_sort(list_distinct(list(unit_norm))) AS units
  FROM listings
  GROUP BY item_id
),
d AS (
  SELECT
    item_id,
    count(*) AS n_dlv_keys,
    CAST(sum(dlv_amount) AS BIGINT) AS dlv_amount,
    CAST(sum(dlv_lines) AS BIGINT) AS dlv_lines,
    min(dlv_first_date) AS dlv_first_date,
    max(dlv_last_date) AS dlv_last_date,
    -- 수량은 모든 키의 수량이 있고 단위가 하나일 때만 더한다(단위가 다른 수량은 더할 수 없다).
    CASE WHEN count(dlv_qty) = count(*) AND count(DISTINCT dlv_unit) = 1 THEN CAST(sum(dlv_qty) AS BIGINT) END AS dlv_qty,
    CASE WHEN count(dlv_qty) = count(*) AND count(DISTINCT dlv_unit) = 1 THEN min(dlv_unit) END AS dlv_unit
  FROM dlv_keys
  GROUP BY item_id
)
SELECT
  l.*,
  coalesce(d.n_dlv_keys, 0) AS n_dlv_keys,
  d.dlv_amount, d.dlv_lines, d.dlv_qty, d.dlv_unit, d.dlv_first_date, d.dlv_last_date
FROM l
LEFT JOIN d USING (item_id)
ORDER BY item_id
