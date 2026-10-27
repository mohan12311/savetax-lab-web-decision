"""외부 판매처 근거로 확정된 배율 2배↑ 카드의 (분야, 제조사) → 우리 미선점 형제 물품. 방향만 쓴다(남의 근거는 쓰지 않는다)."""
import gzip, json, glob, collections, statistics, sys, re
import duckdb
data = sys.argv[1]
cards = {}
for f in sorted(glob.glob(data + "/bronze/contest/found/**/*.jsonl.gz", recursive=True)):
    for l in gzip.open(f, "rt"):
        for c in json.loads(l)["body"].get("items") or []:
            cards[c["key"]] = c
INTERNAL = {"savetax.now", "shop.g2b.go.kr", "goods.go.kr", "hackathon.savetax.now", "api.savetax.now"}
taken = {c["item_id"] for c in cards.values() if c["status"] != "withdrawn"}
norm = lambda s: re.sub(r"\(주\)|㈜|주식회사|\s", "", s or "")
grp = collections.defaultdict(list)
for c in cards.values():
    if c["kind"] != "participant" or c["status"] != "confirmed": continue
    hs = {h.removeprefix("www.") for h in (c.get("evidence_summary") or {}).get("hosts") or []}
    if not hs or hs <= INTERNAL: continue
    r = float(((c.get("evidence_summary") or {}).get("price_gap") or {}).get("markup_ratio") or 0)
    if r >= 2:
        grp[(c.get("category"), norm(c.get("manufacturer")))].append((r, hs))
con = duckdb.connect()
cand = con.execute(f"""SELECT item_id, detail_category, manufacturer, CAST(p_est AS BIGINT), c_est, name, coalesce(claim_state,'?')
    FROM read_parquet('{data}/gold/candidates.parquet') WHERE c_est > 0""").fetchall()
pool = collections.defaultdict(list)
for it, cat, mk, p, c, name, st in cand:
    if it in taken or st in ("claimed", "reform_found"): continue
    pool[(cat, norm(mk))].append((float(c), it, p, name, st))
rows = []
for k, v in grp.items():
    sib = sorted(pool.get(k, []), reverse=True)
    if not sib: continue
    medr = statistics.median(r for r, _ in v)
    hosts = collections.Counter(h for _, hs in v for h in hs).most_common(2)
    est = sum(c * (1 - 1 / medr) for c, *_ in sib)
    rows.append((est, k, len(v), medr, hosts, sib))
rows.sort(key=lambda r: -r[0])
tot = sum(r[0] for r in rows)
print(f"그룹 {len(rows)} · 형제 물품 {sum(len(r[5]) for r in rows)} · 같은 배율 가정 상한 {tot/1e8:.0f}억\n")
for est, (cat, mk), n, medr, hosts, sib in rows[:30]:
    print(f"[{cat}] {mk} — 확정 {n}건 배율중앙 {medr:.1f} 출처 {hosts} → 형제 {len(sib)}개 납품 {sum(s[0] for s in sib)/1e8:.1f}억 (상한 {est/1e8:.1f}억)")
    for c, it, p, name, st in sib[:3]:
        print(f"     {it} {st:9} P={p:>11,} 납품 {c/1e8:5.2f}억 {name[:70]}")
