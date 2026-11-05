import json

import anyio
from mcp import Client

from studio_sheet.mcp_server import build_server


def structured(r):
    if r.structured_content is not None:
        sc = r.structured_content
        return sc.get("result", sc) if isinstance(sc, dict) and set(sc) == {"result"} else sc
    return json.loads(r.content[0].text)


def test_tools_over_mcp(synth_dir, tmp_path):
    server = build_server(synth_dir, "hard-13", tmp_path, attempt=1)

    async def go():
        async with Client(server) as c:
            names = sorted(t.name for t in (await c.list_tools()).tools)
            assert names == ["attach_evidence", "calculate", "get_page_lines", "get_procurement_item", "get_site_policy"]
            page = structured(await c.call_tool("get_page_lines", {}))
            assert page["removed_lines"] == [10]
            assert all("AI" not in ln["text"] for ln in page["lines"])
            item = structured(await c.call_tool("get_procurement_item", {}))
            assert "p_krw" not in item and "unit_price_krw" not in json.dumps(item)
            assert "27000" not in json.dumps(item, ensure_ascii=False)
            assert [a["name"] for a in item["required_attributes"]][:2] == ["규격", "두께"]
            ev = structured(await c.call_tool("attach_evidence", {"source": "page", "text": "80T", "line": 6}))
            assert ev["id"] == "ev-1"
            bad = structured(await c.call_tool("attach_evidence", {"source": "page", "text": "60T", "line": 6}))
            assert bad["ok"] is False
            r = structured(await c.call_tool("calculate", {"expr": "ceil(11800 × 1.1)"}))
            assert r["value"] == "12980"

    anyio.run(go)
    log = (tmp_path / "hard-13.evidence.1.jsonl").read_text().splitlines()
    assert len(log) == 2
    assert json.loads((tmp_path / "hard-13.calc.1.jsonl").read_text())["value"] == "12980"
