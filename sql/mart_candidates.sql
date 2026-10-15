-- gold.candidates — 납품이 있는 물품 1행. 시중가 근거를 찾을 순서를 정하는 탐색용 추정이다(제출 값이 아니다).
-- 입력: listings, dlv_keys, items, claim_state(silver)
-- 변수: min_u(티어 A·B 잠재액 하한), min_c_cat(티어 C 납품 하한), min_x_cat(티어 C 중앙값 배수 하한)
--
-- 기준가 m은 조달 안에서 같은 것이 더 싸게 등재된 가격이다. 시중가가 아니다 — 근거는 따로 찾는다.
--   item  : 같은 물품번호·같은 단위의 최저 등재가(다른 업체·계약). 같은 물건이라는 근거가 가장 강하다.
--           주문한 키 자신의 등재가는 그 키의 중앙값보다 쌀 때만 기준가가 된다 — 납품 단가가 제 등재가보다 높은 것
--           (설치·옵션 포함 납품)을 "더 싼 등재"로 세지 않는다
--   model : 같은 정규화 모델명·세부품명·단위인 다른 물품번호의 최저 등재가. 모델명이 겹친 다른 물건일 수 있다
--   cat   : 세부품명·단위·포장 규격 중앙값(규격 없음 30건, 규격 있음 10건 이상). 규격 차이가 대부분이라 가장 약하다
-- 포장 규격(pack_key)은 상품명의 첫 중량·용량 표기(kg·g·L·ml)를 kg·l로 맞춘 값이다(예: 1000kg 포대 ≠ 20kg 포대).
-- 단가를 용량으로 나누지 않고 같은 표기끼리만 비교한다 — 배낭 60L처럼 용량이 포장량이 아니라 사양인 경우가 있다.
-- 키(계약×물품)마다 구매 단가 p는 납품 금액 ÷ 납품 수량이다. 데이터셋 납품 금액·수량은 대회 fact의 C·Q와 같았다
-- (10/15 실측: 선점 물품 1건 금액·수량 일치). 수량이 없거나, 납품 단위가 등재 단위와 다르거나, 납품 단가가 등재가의
-- 0.5~2배 밖이면 키에 묶인 등재가의 중앙값으로 대신한다(p_src). 키의 98%는 0.9~1.1배이고, 2배 넘는 키는 거의
-- 분전반·제어반·배전반·졸업앨범처럼 등재가가 기본형이고 납품이 구성·옵션을 담은 맞춤 제작품이다 — 등재가 기준가와
-- 비교하면 안 되는 납품이라 거른다. 거르기 전 값은 p_dlv, 등재가 기반 옛 추정은 p_list로 남긴다.
-- 잠재액은 대회 산식 C − M × Q를 키마다 a − m × q로 계산하고, gap 기준(P ≥ 1.2M 또는 P − M ≥ 100만)을
-- 넘는 키만 더한다(대회는 기준 미달 라인을 빼고 낼 수 있다).
WITH parsed AS (
  SELECT
    *,
    upper(regexp_replace(coalesce(model, ''), '[^0-9A-Za-z]', '', 'g')) AS mn,
    -- 천 단위 쉼표(1,000kg)를 지우고 첫 중량·용량 표기를 읽는다. RE2라 전방탐색 대신 뒤 글자를 소비한다
    regexp_extract(regexp_replace(name, '([0-9]),([0-9]{3})', '\1\2', 'g'),
                   '(?i)([0-9]+(?:\.[0-9]+)?)\s*(kg|㎏|g|ℓ|l|ml|㎖|리터)([^a-z]|$)', ['qty', 'u']) AS pm
  FROM listings
),
sane AS (
  -- 기준가로 쓸 등재: 이상 단가(10원 미만·10억 이상)와 단위 없는 등재 제외
  SELECT
    listing_id, item_id, dlv_key, unit, unit_norm, price, contract_active, detail_category_code,
    CASE WHEN length(mn) >= 5 AND regexp_matches(mn, '[0-9]') AND regexp_matches(mn, '[A-Z]') THEN mn END AS model_norm,
    CASE
      WHEN pm.qty = '' THEN NULL
      WHEN lower(pm.u) IN ('kg', '㎏') THEN CAST(CAST(nullif(pm.qty, '') AS DECIMAL(18, 4)) AS VARCHAR) || 'kg'
      WHEN lower(pm.u) = 'g' THEN CAST(CAST(CAST(nullif(pm.qty, '') AS DECIMAL(18, 4)) / 1000 AS DECIMAL(18, 4)) AS VARCHAR) || 'kg'
      WHEN lower(pm.u) IN ('l', 'ℓ', '리터') THEN CAST(CAST(nullif(pm.qty, '') AS DECIMAL(18, 4)) AS VARCHAR) || 'l'
      ELSE CAST(CAST(CAST(nullif(pm.qty, '') AS DECIMAL(18, 4)) / 1000 AS DECIMAL(18, 4)) AS VARCHAR) || 'l'
    END AS pack_key
  FROM parsed
  WHERE price_flag IS NULL AND unit_norm IS NOT NULL
),
key_list AS (
  SELECT
    k.dlv_key, k.item_id, k.dlv_amount AS a,
    CAST(median(s.price) AS DECIMAL(18, 3)) AS p_list,
    -- 납품 수량은 납품 단위가 등재 단위 표기와 같을 때만 쓴다(EA ≠ 대처럼 다른 단위면 단가를 잴 수 없다)
    CASE WHEN k.dlv_qty > 0 AND bool_or(s.unit = k.dlv_unit OR s.unit_norm = lower(trim(k.dlv_unit)))
         THEN k.dlv_qty END AS q_dlv,
    min(s.unit_norm) AS unit_norm,
    min(s.detail_category_code) AS dcc,
    min(s.model_norm) AS model_norm,
    min(s.pack_key) AS pack_key
  FROM dlv_keys k
  JOIN sane s ON s.dlv_key = k.dlv_key
  WHERE k.dlv_lines > 0 AND k.dlv_amount > 0
  GROUP BY k.dlv_key, k.item_id, k.dlv_amount, k.dlv_qty, k.dlv_unit
  HAVING count(DISTINCT s.unit_norm) = 1
),
key_price AS (
  SELECT *, CAST(a / q AS DECIMAL(18, 3)) AS p
  FROM (
    SELECT *, CASE WHEN use_dlv THEN q_dlv ELSE a / p_list END AS q
    FROM (SELECT *, coalesce(a / q_dlv BETWEEN 0.5 * p_list AND 2 * p_list, false) AS use_dlv FROM key_list)
  )
),
item_ref AS (
  SELECT kp.dlv_key, min(s.price) AS m,
         arg_min(s.listing_id, s.price) AS m_listing_id, arg_min(s.contract_active, s.price) AS m_active
  FROM key_price kp
  JOIN sane s
    ON s.item_id = kp.item_id AND s.unit_norm = kp.unit_norm
   AND (s.dlv_key IS DISTINCT FROM kp.dlv_key OR s.price < kp.p_list)
  GROUP BY kp.dlv_key
),
model_ref AS (
  SELECT kp.dlv_key, min(s.price) AS m, arg_min(s.item_id, s.price) AS m_item_id, count(DISTINCT s.item_id) AS n_items
  FROM key_price kp
  JOIN sane s
    ON s.model_norm = kp.model_norm AND s.detail_category_code = kp.dcc
   AND s.unit_norm = kp.unit_norm AND s.pack_key IS NOT DISTINCT FROM kp.pack_key AND s.item_id <> kp.item_id
  WHERE kp.model_norm IS NOT NULL
  GROUP BY kp.dlv_key
),
cat_ref AS (
  SELECT detail_category_code, unit_norm, pack_key, median(price) AS med, count(*) AS n
  FROM sane
  GROUP BY detail_category_code, unit_norm, pack_key
  HAVING count(*) >= CASE WHEN pack_key IS NULL THEN 30 ELSE 10 END
),
keyed AS (
  SELECT
    kp.*,
    ir.m AS m_item, ir.m_listing_id, ir.m_active,
    mr.m AS m_model, mr.m_item_id, mr.n_items AS n_model_items,
    cr.med AS cat_median, cr.n AS cat_n,
    CASE WHEN kp.p >= 1.2 * ir.m OR kp.p - ir.m >= 1000000 THEN kp.a - ir.m * kp.q END AS u_item_k,
    CASE WHEN kp.p >= 1.2 * mr.m OR kp.p - mr.m >= 1000000 THEN kp.a - mr.m * kp.q END AS u_model_k,
    kp.q AS q_k
  FROM key_price kp
  LEFT JOIN item_ref ir ON ir.dlv_key = kp.dlv_key
  LEFT JOIN model_ref mr ON mr.dlv_key = kp.dlv_key
  LEFT JOIN cat_ref cr ON cr.detail_category_code = kp.dcc AND cr.unit_norm = kp.unit_norm
                     AND cr.pack_key IS NOT DISTINCT FROM kp.pack_key
),
by_item AS (
  -- 단위·기준가는 납품 금액이 가장 큰 키의 것을 대표로 쓴다
  SELECT
    item_id,
    arg_max(unit_norm, a) AS unit_norm,
    sum(a) / sum(q_k) AS p_est,
    sum(a) / sum(a / p_list) AS p_list,
    CASE count(*) FILTER (WHERE use_dlv) WHEN count(*) THEN 'dlv' WHEN 0 THEN 'listing' ELSE 'mixed' END AS p_src,
    sum(a) FILTER (WHERE q_dlv IS NOT NULL) / sum(q_dlv) AS p_dlv,
    arg_max(m_item, a) AS m_item, arg_max(m_listing_id, a) AS m_item_listing_id, arg_max(m_active, a) AS m_item_active,
    sum(u_item_k) AS u_item,
    arg_max(m_model, a) AS m_model, arg_max(m_item_id, a) AS m_model_item_id, arg_max(n_model_items, a) AS n_model_items,
    sum(u_model_k) AS u_model,
    arg_max(cat_median, a) AS cat_median, arg_max(cat_n, a) AS cat_n, arg_max(pack_key, a) AS pack_key
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
  -- 시중 검색어: 모델명에서 원산지 접두어((JP)·(CN) 등)만 뗀다. 하이픈 등은 판매 페이지 상품명에 그대로 있어 남긴다
  nullif(trim(regexp_replace(coalesce(i.model, ''), '^\s*\([A-Za-z]{2}\)\s*', '')), '') AS retail_query,
  t.unit_norm,
  t.pack_key,
  t.c_est,
  i.n_dlv_keys, i.dlv_last_date, i.n_listings, i.n_listings_active,
  -- 납품 단가 추정(가능하면 납품 금액 ÷ 수량, p_src). p_list는 등재가 기반 옛 추정, p_dlv는 거르기 전 납품 단가(참고)
  CAST(t.p_est AS DECIMAL(18, 2)) AS p_est,
  t.p_src,
  CAST(t.p_list AS DECIMAL(18, 2)) AS p_list,
  CAST(t.p_dlv AS DECIMAL(18, 2)) AS p_dlv,
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
