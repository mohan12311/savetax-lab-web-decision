-- silver.dlv_keys — 납품 집계 키 1행 (키 dlv_key)
-- 입력: listings(silver)
-- dlv_* 값은 (계약번호, 물품) 키 단위라 키를 공유하는 등재끼리 같다. 납품 합계는 이 표에서 더한다
-- (등재별로 그냥 더하면 약 24% 과대). 키 안에서 값이 갈리는지는 n_variants로 검사한다.
SELECT
  dlv_key,
  min(item_id) AS item_id,
  count(DISTINCT item_id) AS n_items,
  min(contract_no) AS contract_no,
  count(DISTINCT contract_no) AS n_contracts,
  count(*) AS n_listings,
  list(listing_id ORDER BY listing_id) AS listing_ids,
  count(DISTINCT (dlv_shared_listings, dlv_lines, dlv_qty, dlv_unit, dlv_amount, dlv_first_date, dlv_last_date)) AS n_variants,
  any_value(dlv_shared_listings) AS dlv_shared_listings,
  any_value(dlv_lines) AS dlv_lines,
  any_value(dlv_qty) AS dlv_qty,
  any_value(dlv_unit) AS dlv_unit,
  any_value(dlv_amount) AS dlv_amount,
  any_value(dlv_first_date) AS dlv_first_date,
  any_value(dlv_last_date) AS dlv_last_date
FROM listings
WHERE dlv_key IS NOT NULL
GROUP BY dlv_key
ORDER BY item_id, dlv_key
