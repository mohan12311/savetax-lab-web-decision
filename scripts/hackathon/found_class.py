import gzip, json, glob, collections, sys
cards = {}
for f in sorted(glob.glob(sys.argv[1] + "/bronze/contest/found/**/*.jsonl.gz", recursive=True)):
    for l in gzip.open(f, "rt"):
        for c in json.loads(l)["body"].get("items") or []:
            cards[c["key"]] = c
INTERNAL = {"savetax.now", "shop.g2b.go.kr", "goods.go.kr", "hackathon.savetax.now", "g2b.go.kr"}
COMPARE = {"prod.danawa.com", "search.danawa.com", "price.enuri.com", "enuri.com", "danawa.com"}
def cls(c):
    hs = {h[4:] if h.startswith("www.") else h for h in ((c.get("evidence_summary") or {}).get("hosts") or [])}
    if not hs: return "근거 없음"
    if hs <= INTERNAL: return "조달 내부(savetax.now·나라장터)만"
    if hs & INTERNAL: return "조달 내부 + 외부 섞임"
    if hs <= COMPARE: return "가격비교(다나와·에누리)"
    if any(h.endswith(".com") and not h.endswith(".co.kr") and h not in COMPARE and not h.endswith("coupang.com") for h in hs) and not any(h.endswith(".kr") for h in hs):
        return "해외·.com 판매처"
    return "국내 판매처(.kr 등)"
amt = lambda c: int(c.get("amount_krw") or 0)
T = collections.defaultdict(lambda: collections.Counter())
for c in cards.values():
    if c["kind"] != "participant": continue
    k = cls(c)
    T[k][c["status"] + "_n"] += 1; T[k][c["status"]] += amt(c)
print(f"{'근거 출처 유형':28} {'확정 개수':>7} {'확정(억)':>8} {'보류(억)':>8} {'유보(억)':>8} {'철회(억)':>8}")
for k, v in sorted(T.items(), key=lambda x: -x[1]["confirmed"]):
    print(f"{k:28} {v['confirmed_n']:7} {v['confirmed']/1e8:8.0f} {v['held']/1e8:8.0f} {v['unconfirmed']/1e8:8.0f} {v['withdrawn']/1e8:8.0f}")
