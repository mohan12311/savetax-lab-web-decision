"""케이토목 목록 표(probe 보관 원문) → 상품(it_id, 품명, 규격, 단위, 판매가 VAT별도, 운반비예상). 탐색용."""
import gzip, json, glob, re, html, sys
data = sys.argv[1]
pages = {}
for f in sorted(glob.glob(data + "/bronze/retail/probe/page/**/*.jsonl.gz", recursive=True)):
    for l in gzip.open(f, "rt"):
        d = json.loads(l)
        u = d["params"].get("url", "")
        if "ktomok.com/shop/list_search.php" in u and d.get("raw"):
            pages[u] = d["raw"]["path"]
cell = lambda c: re.sub(r"\s+", " ", html.unescape(re.sub(r"<[^>]+>", " ", c))).strip()
out = {}
for u, p in pages.items():
    s = gzip.open(f"{data}/bronze/{p}", "rt").read()
    for row in re.findall(r"<tr\s*>(.*?)</tr>", s, re.S):
        tds = re.findall(r"<td[^>]*>(.*?)</td>", row, re.S)
        m = re.search(r"it_id=(\d+)", row)
        if not m or len(tds) < 8: continue
        c = [cell(x) for x in tds]
        num = lambda x: int(x.replace(",", "")) if re.fullmatch(r"[0-9,]+", x or "") else None
        out[m.group(1)] = {"it_id": m.group(1), "name": c[1], "spec": c[2], "unit": c[3], "kg_m2": c[4],
                           "price_ex_vat": num(c[5]), "freight": num(c[7]), "note": c[10][:100] if len(c) > 10 else "",
                           "url": f"https://ktomok.com/shop/item.php?it_id={m.group(1)}"}
json.dump(list(out.values()), open(sys.argv[2], "w"), ensure_ascii=False, indent=0)
for o in out.values(): print(o["it_id"], o["name"], o["spec"], o["unit"], o["price_ex_vat"], "운반", o["freight"])
