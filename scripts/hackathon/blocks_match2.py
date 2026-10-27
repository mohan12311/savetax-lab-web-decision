"""보차도용콘크리트블록 ↔ 케이토목 동급: 종류(인조화강·대형컬러)·두께·투수·크기 일치만. 탐색용."""
import json, re, sys
import duckdb
data, kt_file, taken_file, out_file = sys.argv[1:5]
kt = json.load(open(kt_file))
mk = []
for k in kt:
    if not k["price_ex_vat"]: continue
    if "인조화강" in k["name"]: cl = "인조화강"
    elif "다칼라" in k["name"]: cl = "대형컬러"
    else: continue
    d = re.match(r"(\d+)\*(\d+)\*(\d+)", k["spec"])
    if not d: continue
    w, l, t = map(int, d.groups())
    perm = "불투수" if "불투수" in k["name"] else "투수"
    mk.append(dict(cl=cl, w=min(w, l), l=max(w, l), t=t, perm=perm, m=round(k["price_ex_vat"] * 1.1), k=k))
SPECIAL = r"공기정화|광촉매|차열|열차단|점토|자연석|화강석|현무암|야광|축광|발광|태양광|LED|잔디|식생|배수성|세라믹|타일|고무|우레탄|탄성|점자|장애인|경계|판석|대리석|코팅|항균|저소음|워싱|워씽|노출|앤틱|엔틱|샌딩|숏블|버너|연마|그라인|폴리싱|물갈기|석재|규사|마블|테라|천연"
taken = set(open(taken_file).read().split())
rows = duckdb.sql(f"""SELECT item_id, name, CAST(p_est AS BIGINT), c_est, unit_norm, coalesce(claim_state,'?')
   FROM read_parquet('{data}/gold/candidates.parquet')
   WHERE detail_category='보차도용콘크리트블록' AND c_est > 0 AND coalesce(claim_state,'?') NOT IN ('claimed','reform_found')""").fetchall()
res = []
for it, name, p, c, unit, st in rows:
    if it in taken or unit != "m2" or not p: continue
    if re.search(SPECIAL, name): continue
    if "인조화강" in name or re.search(r"(?<!천연)화강블록", name): cl = "인조화강"
    elif re.search(r"블랜딩|다칼라|멀티블록|메가블록", name): cl = "대형컬러"
    else: continue
    perm = "불투수" if "불투수" in name else ("투수" if "투수" in name else None)
    d = re.search(r"(\d{3,4})\s*×\s*(\d{3,4})\s*×\s*(\d{2,3})\s*mm", name)
    tm = re.search(r"[tT]\s*(\d{2,3})\s*mm", name)
    if d: w, l, t = map(int, d.groups()); w, l = min(w, l), max(w, l)
    elif tm: w = l = None; t = int(tm.group(1))
    else: continue
    def fit(x):
        if x["cl"] != cl or x["t"] != t: return False
        if perm and x["perm"] != perm: return False
        if w is None: return False  # 크기를 모르면 동급 판단 보류
        return abs(x["w"] - w) <= 0.05 * w and abs(x["l"] - l) <= 0.05 * l
    cands = [x for x in mk if fit(x)]
    if not cands: 
        res.append((0, None, it, name, p, None, float(c), cl, t, perm, None, st, "크기·투수 미상 또는 같은 규격 없음"))
        continue
    best = min(cands, key=lambda x: x["m"])
    r = p / best["m"]
    res.append((float(c) * (1 - 1 / r) if r >= 1.2 else 0, r, it, name, p, best["m"], float(c), cl, t, perm, best["k"], st, ""))
ok = sorted([x for x in res if x[1] and x[1] >= 1.3], key=lambda x: -x[0])
json.dump([dict(item_id=x[2], name=x[3], p_est=x[4], m_vat=x[5], ratio=round(x[1], 2), c_est=x[6], up=x[0], market=x[10]) for x in ok], open(out_file, "w"), ensure_ascii=False, indent=1)
print(f"같은 종류 후보 {len(res)} · 규격 일치 {sum(1 for x in res if x[1])} · 1.3배↑ {len(ok)} · 예상 상한 {sum(x[0] for x in ok)/1e8:.1f}억")
for up, r, it, name, p, m, c, cl, t, perm, k, st, note in ok[:40]:
    print(f"{it} x{r:4.2f} P={p:>7,} M={m:>6,} 납품={c/1e8:5.2f}억 up={up/1e8:5.2f}억 | {name[:62]} ⇄ {k['name']} {k['spec']}")
