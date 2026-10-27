"""발견 상품 카드 집계(출처 도메인·분야·배율·상태). 남의 근거(링크·인용·가격)는 우리 근거로 쓰지 않는다."""
import gzip, json, sys, collections
from decimal import Decimal
from pathlib import Path
data = Path(sys.argv[1])
cards = {}
for f in sorted((data / "bronze/contest/found").rglob("*.jsonl.gz")):
    for l in gzip.open(f, "rt"):
        d = json.loads(l)
        for c in d["body"].get("items") or []:
            cards[c["key"]] = c  # 나중 읽은 값이 이긴다
part = [c for c in cards.values() if c["kind"] == "participant"]
reform = [c for c in cards.values() if c["kind"] != "participant"]
print(f"카드 {len(cards)} · 참가자 {len(part)} · 개혁신당 {len(reform)}")
st = collections.Counter(c["status"] for c in part)
print("참가자 상태:", dict(st))
amt = lambda c: int(c.get("amount_krw") or 0)
print("참가자 금액 합(억):", {s: round(sum(amt(c) for c in part if c["status"] == s) / 1e8) for s in st})

def hosts(c):
    es = c.get("evidence_summary") or {}
    return es.get("hosts") or []
H = collections.defaultdict(collections.Counter)
HA = collections.Counter()
for c in part:
    for h in set(hosts(c)) or {"(없음)"}:
        h2 = h[4:] if h.startswith("www.") else h
        H[h2][c["status"]] += 1
        HA[h2] += amt(c)
print("\n== 근거 도메인(카드 수 상위 40) — 상태별")
print(f"{'도메인':32} {'전체':>5} {'확정':>5} {'보류':>5} {'유보':>5} {'철회':>5} {'금액(억)':>8}")
for h, cnt in sorted(H.items(), key=lambda x: -sum(x[1].values()))[:40]:
    print(f"{h[:32]:32} {sum(cnt.values()):5} {cnt['confirmed']:5} {cnt['held']:5} {cnt['unconfirmed']:5} {cnt['withdrawn']:5} {HA[h]/1e8:8.0f}")

def ratio(c):
    pg = ((c.get("evidence_summary") or {}).get("price_gap")) or {}
    try: return Decimal(pg.get("markup_ratio"))
    except Exception: return None
print("\n== 배율 분포(참가자, 확정만)")
bins = [(1.2, 1.5), (1.5, 2), (2, 3), (3, 5), (5, 10), (10, 1e9)]
conf = [c for c in part if c["status"] == "confirmed"]
for lo, hi in bins:
    sel = [c for c in conf if ratio(c) is not None and lo <= ratio(c) < hi]
    print(f"  {lo:>4}~{hi if hi < 1e9 else '':<4} {len(sel):5}개  금액 {sum(amt(c) for c in sel)/1e8:7.0f}억")
print("\n== 분야(세부품명) 상위 30 — 참가자 확정, 금액순")
cat = collections.defaultdict(lambda: [0, 0])
for c in conf:
    cat[c.get("category")][0] += 1; cat[c.get("category")][1] += amt(c)
for k, (n, a) in sorted(cat.items(), key=lambda x: -x[1][1])[:30]:
    print(f"  {str(k)[:24]:24} {n:4}개 {a/1e8:7.0f}억")
