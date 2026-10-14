-- silver.listings — 데이터셋 등재 1행 = 등재 1건 (키 listing_id)
-- 입력: src_listings(데이터셋 listings.parquet), 변수 as_of(DATE, 등재 관측일), release_id
-- 원값은 그대로 두고 정규화·플래그 컬럼만 더한다. 금액은 DECIMAL/BIGINT 그대로(float 금지), null은 null로 둔다.
WITH unit_alias(raw, norm) AS (
  -- 표기만 다른 단위. lower(trim(unit))에 대해 맞춘다(M·KG·SET·L 같은 대소문자는 lower가 처리).
  -- 포장 단위(50개·30부·4ℓ/통 등)는 다른 물건이라 합치지 않는다.
  VALUES ('ea', '개'), ('개(ea)', '개'), ('1개', '개'), ('개당', '개'),
         ('㎡', 'm2'), ('m²', 'm2'),
         ('㎥', 'm3'),
         ('㎏', 'kg'),
         ('㎜', 'mm'),
         ('세트', 'set'),
         ('ton', '톤'),
         ('리터', 'l')
)
SELECT
  s.listing_id,
  s.item_id,
  -- 계약번호는 등재번호 앞부분(README): '_' 형식은 앞 11자, 'R' 형식은 앞 13자. deliveries의 contractNo와 같은 값.
  CASE WHEN s.listing_id LIKE 'R%' THEN left(s.listing_id, 13)
       WHEN contains(s.listing_id, '_') THEN left(s.listing_id, 11) END AS contract_no,
  s.name, s.item_name, s.detail_category, s.detail_category_code,
  s.manufacturer, s.model, s.spec,
  s.unit,
  -- 비교용 단위. 제출 line의 unit은 이것이 아니라 대회 fact 값을 글자 그대로 쓴다.
  CASE WHEN s.unit IS NULL THEN NULL
       ELSE coalesce(a.norm, lower(trim(s.unit))) END AS unit_norm,
  s.price,
  s.discount_price,
  -- 원천 입력값 이상치(1원 등, 10억 이상). 거르지 않고 표시만 한다 — 10억 이상에는 정상 고가도 있다.
  CASE WHEN s.price < 10 THEN 'lt_10'
       WHEN s.price >= 1000000000 THEN 'ge_1e9' END AS price_flag,
  s.contract_begin,
  s.contract_end,
  -- 7027년 같은 입력 오류는 알 수 없음(null)으로 둔다.
  CASE WHEN s.contract_end < DATE '2100-01-01' THEN s.contract_end END AS contract_end_clean,
  -- as_of 시점 계약 유효. 종료일을 알 수 없으면 null(유효로도 무효로도 단정하지 않음).
  CASE WHEN s.contract_end >= DATE '2100-01-01' THEN NULL
       ELSE s.contract_begin <= getvariable('as_of') AND s.contract_end >= getvariable('as_of') END AS contract_active,
  s.contract_type,
  s.policy_tags,
  s.vendor_name, s.vendor_size,
  s.category_l_code, s.category_l_name, s.category_m_code, s.category_m_name,
  s.site_sales_qty,  -- 사이트 표시 지표. 판매량이 아니다 — 금액 계산에 쓰지 않는다.
  s.review_count,
  s.image_url, s.detail_url, s.source,
  s.dlv_key, s.dlv_shared_listings, s.dlv_lines, s.dlv_qty, s.dlv_unit, s.dlv_amount,
  s.dlv_first_date, s.dlv_last_date,
  getvariable('release_id') AS dataset_release_id
FROM src_listings s
LEFT JOIN unit_alias a ON a.raw = lower(trim(s.unit))
ORDER BY s.item_id, s.listing_id
