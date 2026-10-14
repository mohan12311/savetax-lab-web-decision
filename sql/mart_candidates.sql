-- gold.candidates — 납품이 있는 물품 1행. 시중가 근거를 찾을 순서를 정하는 탐색용 추정이다(제출 값이 아니다).
-- 입력: listings, dlv_keys, items, claim_state(silver)
-- 변수: min_u(티어 A·B 잠재액 하한), min_c_cat(티어 C 납품 하한), min_x_cat(티어 C 중앙값 배수 하한)
--
-- 기준가 m은 조달 안에서 같은 것이 더 싸게 등재된 가격이다. 시중가가 아니다 — 근거는 따로 찾는다.
--   item  : 같은 물품번호·같은 단위의 최저 등재가(다른 업체·계약). 같은 물건이라는 근거가 가장 강하다
--   model : 같은 정규화 모델명·세부품명·단위인 다른 물품번호의 최저 등재가. 모델명이 겹친 다른 물건일 수 있다
--   cat   : 세부품명·단위 중앙값(30건 이상). 규격 차이가 대부분이라 가장 약하다 — 순위만 매기고 금액 근거로 쓰지 않는다
-- 키(계약×물품)마다 구매 단가 p는 키에 묶인 등재가의 중앙값이다(주문 평균 단가 ÷ 등재가의 중앙값은 1.0).
-- 잠재액은 대회 산식 C − M × Q를 키마다 a × (p − m) ÷ p로 근사하고, gap 기준(P ≥ 1.2M 또는 P − M ≥ 100만)을
-- 넘는 키만 더한다(대회는 기준 미달 라인을 빼고 낼 수 있다).
WITH sane AS (
  -- 기준가로 쓸 등재: 이상 단가(10원 미만·10억 이상)와 단위 없는 등재 제외
  SELECT
    listing_id, item_id, dlv_key, unit_norm, price, contract_active, detail_category_code,
    CASE WHEN length(mn) >= 5 AND regexp_matches(mn, '[0-9]') AND regexp_matches(mn, '[A-Z]') THEN mn END AS model_norm
  FROM (SELECT *, upper(regexp_replace(coalesce(model, ''), '[^0-9A-Za-z]', '', 'g')) AS mn FROM listings)
  WHERE price_flag IS NULL AND unit_norm IS NOT NULL
),
key_price AS (
  SELECT
    k.dlv_key, k.item_id, k.dlv_amount AS a,
    CAST(median(s.price) AS DECIMAL(18, 3)) AS p,
    min(s.unit_norm) AS unit_norm,
    min(s.detail_category_code) AS dcc,
    min(s.model_norm) AS model_norm
  FROM dlv_keys k
  JOIN sane s ON s.dlv_key = k.dlv_key
  WHERE k.dlv_lines > 0 AND k.dlv_amount > 0
  GROUP BY k.dlv_key, k.item_id, k.dlv_amount
  HAVING count(DISTINCT s.unit_norm) = 1
),
item_ref AS (
  SELECT item_id, unit_norm, min(price) AS m,
         arg_min(listing_id, price) AS m_listing_id, arg_min(contract_active, price) AS m_active
  FROM sane
  GROUP BY item_id, unit_norm
),
model_ref AS (
  SELECT kp.dlv_key, min(s.price) AS m, arg_min(s.item_id, s.price) AS m_item_id, count(DISTINCT s.item_id) AS n_items
  FROM key_price kp
  JOIN sane s
    ON s.model_norm = kp.model_norm AND s.detail_category_code = kp.dcc
   AND s.unit_norm = kp.unit_norm AND s.item_id <> kp.item_id
  WHERE kp.model_norm IS NOT NULL
  GROUP BY kp.dlv_key
),
cat_ref AS (
  SELECT detail_category_code, unit_norm, median(price) AS med, count(*) AS n
  FROM sane
  GROUP BY detail_category_code, unit_norm
  HAVING count(*) >= 30
),
keyed AS (
  SELECT
    kp.*,
    ir.m AS m_item, ir.m_listing_id, ir.m_active,
    mr.m AS m_model, mr.m_item_id, mr.n_items AS n_model_items,
    cr.med AS cat_median, cr.n AS cat_n,
    CASE WHEN kp.p >= 1.2 * ir.m OR kp.p - ir.m >= 1000000 THEN kp.a * (kp.p - ir.m) / kp.p END AS u_item_k,
    CASE WHEN kp.p >= 1.2 * mr.m OR kp.p - mr.m >= 1000000 THEN kp.a * (kp.p - mr.m) / kp.p END AS u_model_k,
    kp.a / kp.p AS q_k
  FROM key_price kp
  LEFT JOIN item_ref ir ON ir.item_id = kp.item_id AND ir.unit_norm = kp.unit_norm
  LEFT JOIN model_ref mr ON mr.dlv_key = kp.dlv_key
  LEFT JOIN cat_ref cr ON cr.detail_category_code = kp.dcc AND cr.unit_norm = kp.unit_norm
),
by_item AS (
  -- 단위·기준가는 납품 금액이 가장 큰 키의 것을 대표로 쓴다
  SELECT
    item_id,
    arg_max(unit_norm, a) AS unit_norm,
    sum(a) / sum(q_k) AS p_est,
    arg_max(m_item, a) AS m_item, arg_max(m_listing_id, a) AS m_item_listing_id, arg_max(m_active, a) AS m_item_active,
    sum(u_item_k) AS u_item,
    arg_max(m_model, a) AS m_model, arg_max(m_item_id, a) AS m_model_item_id, arg_max(n_model_items, a) AS n_model_items,
    sum(u_model_k) AS u_model,
    arg_max(cat_median, a) AS cat_median, arg_max(cat_n, a) AS cat_n
  FROM keyed
  GROUP BY item_id
),
tiered AS (
  SELECT
    b.*,
    i.dlv_amount AS c_est,
    b.p_est / b.cat_median AS x_median,
    CASE
      WHEN b.u_item >= getvariable('min_u') THEN 'A'
      WHEN b.u_model >= getvariable('min_u') THEN 'B'
      WHEN b.p_est >= getvariable('min_x_cat') * b.cat_median AND i.dlv_amount >= getvariable('min_c_cat') THEN 'C'
    END AS tier
  FROM by_item b
  JOIN items i USING (item_id)
)
SELECT
  t.item_id,
  t.tier,
  CASE t.tier
    WHEN 'A' THEN CAST(floor(greatest(t.u_item, coalesce(t.u_model, 0))) AS BIGINT)
    WHEN 'B' THEN CAST(floor(t.u_model) AS BIGINT)
    WHEN 'C' THEN CAST(floor(t.c_est * (1 - t.cat_median / t.p_est)) AS BIGINT)
  END AS u_est,
  CASE t.tier WHEN 'A' THEN 'item_min' WHEN 'B' THEN 'model_min' WHEN 'C' THEN 'category_median' END AS u_basis,
  -- 티어를 정한 기준가 대비 구매 단가 배수
  CAST(CASE t.tier WHEN 'A' THEN t.p_est / t.m_item WHEN 'B' THEN t.p_est / t.m_model WHEN 'C' THEN t.x_median END
       AS DECIMAL(12, 2)) AS r_tier,
  cs.state AS claim_state,
  cs.checked_at AS claim_checked_at,
  i.name, i.detail_category, i.detail_category_code, i.manufacturer, i.model, i.source,
  t.unit_norm,
  t.c_est,
  i.n_dlv_keys, i.dlv_last_date, i.n_listings, i.n_listings_active,
  CAST(t.p_est AS DECIMAL(18, 2)) AS p_est,
  CAST(t.m_item AS DECIMAL(18, 3)) AS m_item, t.m_item_listing_id, t.m_item_active,
  CAST(floor(t.u_item) AS BIGINT) AS u_item,
  CAST(t.p_est / t.m_item AS DECIMAL(12, 2)) AS r_item,
  CAST(t.m_model AS DECIMAL(18, 3)) AS m_model, t.m_model_item_id, t.n_model_items,
  CAST(floor(t.u_model) AS BIGINT) AS u_model,
  CAST(t.p_est / t.m_model AS DECIMAL(12, 2)) AS r_model,
  CAST(t.cat_median AS DECIMAL(18, 3)) AS cat_median, t.cat_n,
  CAST(t.x_median AS DECIMAL(12, 2)) AS x_median
FROM tiered t
JOIN items i USING (item_id)
LEFT JOIN claim_state cs USING (item_id)
ORDER BY (t.tier IS NULL), u_est DESC NULLS LAST, t.c_est DESC, t.item_id
