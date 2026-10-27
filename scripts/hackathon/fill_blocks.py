"""블록 초안(data/gold/claims/<id>.json)에 동급 근거를 채운다. 인용은 보관 원문 본문에서 그대로 잘라 온다(규칙 1).
사용: python fill_blocks.py <data_dir> <item_id> <두께60|80> <조달 규격 메모> <납품 단가 P(facts)> [불투수만=1]"""
import gzip, json, glob, re, html, sys
from decimal import Decimal, ROUND_CEILING
data, item, thick, spec_note, P = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4], Decimal(sys.argv[5])
PAGES = {"60": ["1581994489", "1581994495"], "80": ["1581994490", "1581994496"]}[thick]
if len(sys.argv) > 6 and sys.argv[6] == "1":
    PAGES = PAGES[1:]  # 불투수(휨강도 5.0MPa 내외)만
latest = {}
for f in sorted(glob.glob(f"{data}/bronze/retail/probe/page/**/*.jsonl.gz", recursive=True)):
    for l in gzip.open(f, "rt"):
        d = json.loads(l)
        m = re.search(r"it_id=(\d+)$", d["params"].get("url", ""))
        if m and m.group(1) in PAGES and d.get("raw") and d["fetched_at"] >= "2026-10-15T15:00:00Z":
            latest[m.group(1)] = (d["fetched_at"], d["params"]["url"], d["raw"]["path"], d["raw"]["sha256"])
def text(path):
    s = gzip.open(f"{data}/bronze/{path}", "rt").read()
    t = re.sub(r"<script.*?</script>|<style.*?</style>", "", s, flags=re.S)
    t = re.sub(r"m<sup>2</sup>", "m2", t)
    t = html.unescape(re.sub(r"<[^>]+>", "\n", t)); t = re.sub(r"[ \t]+", " ", t)
    return [x.strip() for x in t.split("\n") if x.strip()]
def grab(lines, start, n):
    i = lines.index(start); return " ".join(lines[i:i + n])
evs, prices, freight, flex = [], [], [], []
for pid in PAGES:
    at, url, path, sha = latest[pid]
    L = text(path)
    title = next(x for x in L if x.startswith("인조화강블록(") and ">" in x)
    i = L.index("상세설명"); j = L.index("판매단가")
    detail = " ".join(L[i:j])
    price = L[j + 1]                        # 예: 9,900원
    k = L.index("운반비예상"); fr = " ".join(L[k:k + 4])
    m = re.search(r"당 ([0-9,]+)원 예상", fr)
    g = next(x for x in L if x.startswith("직접생산아님"))
    weight = " ".join(L[L.index("중량/단위"):L.index("중량/단위") + 2])
    quote = (f"케이토목(ktomok.com, 토목자재도매단가쇼핑몰) 상품 페이지 — {title}. {weight}. {detail}. "
             f"판매단가 {price} (운반비/부가세 별도). {fr}. 상품정보제공고시: 제조국 국내산(대한민국), {g}. 2026-10-16 확인.")
    obs = int(price.replace(",", "").replace("원", ""))
    flex.append(f"{title.split('>')[0].strip()} {re.search(r'휨강도 ([0-9.]+MPa 내외)', g).group(1)}")
    prices.append(obs); freight.append(int(m.group(1).replace(",", "")))
    evs.append({"kind": "price_page", "source_url": url, "quote": quote[:2000], "observed_price_krw": str(obs),
                "observed_at": "2026-10-16", "vat_included": False, "shipping_included": False,
                "note": f"판매단가 {price}(부가세·운반비 별도), 운반비 예상 ㎡당 {m.group(1)}원. 원문 sha256 {sha[:16]}"})
assert len(set(prices)) == 1, prices
B = ((Decimal(prices[0]) + Decimal(max(freight))) * Decimal("1.1")).quantize(Decimal("1"), rounding=ROUND_CEILING)
p = f"{data}/gold/claims/{item}.json"
c = json.load(open(p))
for ln in c["lines"]:
    ln["benchmark_unit_price_krw"] = str(B)
    ln["price_evidence"] = evs
c["claim"] = ("같은 모델은 시중에서 팔지 않는 조달 등재 모델이라, 종류(인조화강블록)·크기·두께가 같은 시중 제품의 판매가와 비교했다(동급 비교). "
              f"시중 판매단가 {prices[0]:,}원/㎡은 부가세·운반비 별도라, 부가세 10%와 판매처가 적은 현장 운반비 예상액(㎡당 {max(freight):,}원)을 더해 "
              f"조달 조건(부가세 포함, 납품장소차상도)에 맞춘 {B:,}원/㎡을 비교 단가로 썼다.")
c["comparison_scope"] = (f"동급(판정 2): {spec_note} ⇄ 시중 인조화강블록 200×200×{thick}mm, 판매단가 {prices[0]:,}원/㎡({' · '.join(flex)}). "
                         "시중 제품: 국내산, 별도 인증 표기 없음. 판매처 케이토목은 조달 공급사·제조사와 다른 유통사. 파렛트(약 17~22㎡) 단위 판매.")
c["title"] = f"동급 인조화강블록 시중가 대비 {(P / B).quantize(Decimal('0.1'))}배"
json.dump(c, open(p, "w"), ensure_ascii=False, indent=2)
print(item, "B =", B, "관측", prices[0], "운반", freight[0])
