"""실행기 끝-끝 시험. 실제 API 대신 각본 LLM이 도구를 부른다(비용 없음).

각본은 도구가 돌려준 페이지 줄을 보고 근거를 고른다. 정답 파일은 읽지 않는다.
"""

import json
from decimal import Decimal
from pathlib import Path

import anyio

import studio_schema
from studio_agent.llm import Usage
from studio_agent.runner import run_case
from studio_sheet.data import AgentData
from studio_sheet.mcp_server import build_server



class B:
    def __init__(self, **kw):
        self.__dict__.update(kw)


class ScriptedLLM:
    """대화마다 각본 생성기를 새로 만든다. 생성기는 [(도구, 입력), …]을 내고 도구 결과(파싱한 JSON 목록)를 받는다."""

    model = "claude-sonnet-5-5"

    def __init__(self, script):
        self.script = script
        self.gen = None
        self.n = 0

    def create(self, *, system, tools, messages):
        last = messages[-1]
        if len(messages) == 1:
            self.gen = self.script({t["name"] for t in tools})
            calls = next(self.gen)
        else:
            results = [json.loads(c["content"]) if not c["content"].startswith(("형식", "받았다", "기록", "지금")) else c["content"]
                       for c in last["content"]] if isinstance(last["content"], list) else []
            try:
                calls = self.gen.send(results)
            except StopIteration:
                calls = []
        content = []
        for name, inp in calls:
            self.n += 1
            content.append(B(type="tool_use", id=f"tu{self.n}", name=name, input=inp))
        if not content:
            content = [B(type="text", text="끝")]
        return B(content=content, stop_reason="tool_use" if calls else "end_turn", model=self.model,
                 usage=B(input_tokens=1000, output_tokens=200, cache_creation_input_tokens=0, cache_read_input_tokens=500))


def block_script(*, size_result="same", thick_result="same", label="equivalent", conv=None, formula_fn=None):
    def script(tool_names):
        item, page = yield [("get_procurement_item", {}), ("get_page_lines", {})]
        lines = page["lines"]

        def ln(prefix):
            return next(x["line"] for x in lines if x["text"].startswith(prefix))

        name = item["name"]
        size_p = name.split(", ")[3].rsplit("×", 1)[0]  # "200×200×60mm" → "200×200"
        thick_p = name.split(", ")[3].rsplit("×", 1)[1]  # "60mm"
        spec_line = next(x for x in lines if x["text"].startswith("규격"))["text"]
        size_s = spec_line.split(" | ")[1].rsplit("×", 1)[0]
        thick_s = next(x for x in lines if x["text"].startswith("두께"))["text"].split(" | ")[1]
        price_line = next(x for x in lines if x["text"].startswith("판매가"))
        price_txt = price_line["text"].split()[1]  # "9,900원"
        unit_line = next(x for x in lines if x["text"].startswith("판매 단위"))
        unit = unit_line["text"].split(": ")[1].split(" ")[0]
        perm = "불투수" if "불투수" in name else "투수"
        calls = [
            ("attach_evidence", {"source": "procurement", "text": size_p}),
            ("attach_evidence", {"source": "page", "text": size_s, "line": ln("규격")}),
            ("attach_evidence", {"source": "procurement", "text": thick_p}),
            ("attach_evidence", {"source": "page", "text": thick_s, "line": ln("두께")}),
            ("attach_evidence", {"source": "procurement", "text": "인조화강블록"}),
            ("attach_evidence", {"source": "page", "text": "인조화강블록", "line": ln("종류")}),
            ("attach_evidence", {"source": "procurement", "text": perm}),
            ("attach_evidence", {"source": "page", "text": perm, "line": ln("종류")}),
            ("attach_evidence", {"source": "procurement", "text": item["model"], "field": "model"}),
            ("attach_evidence", {"source": "page", "text": price_txt, "line": price_line["line"]}),
            ("attach_evidence", {"source": "page", "text": "부가세 별도", "line": price_line["line"]}),
            ("attach_evidence", {"source": "page", "text": f"판매 단위: {unit}", "line": unit_line["line"]}),
        ]
        if conv:
            calls.append(("attach_evidence", {"source": "page", "text": conv[1], "line": unit_line["line"]}))
        evs = yield calls
        ids = [e["id"] for e in evs]
        price = price_txt.removesuffix("원").replace(",", "")
        formula = formula_fn(int(price)) if formula_fn else f"ceil({price} × 1.1)"
        (calc,) = yield [("calculate", {"expr": formula})]
        rows = [
            {"attribute": "규격", "procurement": ids[0], "seller": ids[1], "result": size_result, "confidence": 0.8},
            {"attribute": "두께", "procurement": ids[2], "seller": ids[3], "result": thick_result, "confidence": 0.9},
            {"attribute": "종류", "procurement": ids[4], "seller": ids[5], "result": "same", "confidence": 0.9},
            {"attribute": "투수 여부", "procurement": ids[6], "seller": ids[7], "result": "same", "confidence": 0.9},
            {"attribute": "모델", "procurement": ids[8], "seller": None, "result": "not_on_page", "confidence": 0.9},
            {"attribute": "구성", "procurement": None, "seller": None, "result": "not_on_page", "confidence": 0.6},
        ]
        read = {"sale_price": {"value": price, "ev": ids[9]}, "vat": {"value": "excluded", "ev": ids[10]},
                "sale_unit": {"value": unit, "ev": ids[11]},
                "conversion": {"value": conv[0] if conv else "1", "ev": ids[12] if conv else None}}
        answer = {"label": label, "rows": rows,
                  "price": {"read": read, "llm": {"formula": formula, "benchmark_krw": calc["value"]} if label != "different" else None},
                  "reason": "종류 {ev-5}와 {ev-6}이 같다.", "hold_reason": None, "hold_rows": []}
        (preview,) = yield [("submit_answer", answer)]
        if "review_title" in tool_names:
            yield [("review_title", {"agree": True, "supplement": None})]

    return script


def run(synth_dir, tmp_path, case_id, script, repeats=3):
    data = AgentData(synth_dir)
    llm = ScriptedLLM(script)

    def factory(cid, log_dir, attempt):
        return build_server(synth_dir, cid, log_dir, attempt)

    return anyio.run(lambda: run_case(llm, data, case_id, tmp_path, repeats=repeats, server_factory=factory))


def test_hard04_end_to_end(synth_dir, tmp_path):
    s = run(synth_dir, tmp_path, "hard-04", block_script(conv=("1/25", "25장/㎡"), formula_fn=lambda p: f"ceil({p} × 25 × 1.1)"))
    assert s["price"]["code"]["benchmark_krw"] == "11000"
    assert s["price"]["check"]["status"] == "match"
    assert s["checks"]["calculator_calls"] == 1 and s["checks"]["unissued_evidence"] == []
    assert s["title_review"] == {"agree": True, "supplement": None}
    assert s["run"]["runs"] == 1 and s["repeats"] is None
    assert [b["rule"] for b in s["routing"]["because"]] == [7]
    assert (tmp_path / "hard-04.json").exists()
    studio_schema.load(tmp_path / "hard-04.json")


def test_hard02_different_auto_passes(synth_dir, tmp_path):
    s = run(synth_dir, tmp_path, "hard-02", block_script(thick_result="different", label="different"))
    assert s["conclusion"] == "nothing_to_flag" and s["routing"]["to"] == "auto"


def test_tolerance_triggers_repeats(synth_dir, tmp_path):
    s = run(synth_dir, tmp_path, "hard-01", block_script(size_result="within_tolerance"))
    assert s["run"]["runs"] == 3 and s["repeats"] == {"total": 3, "labels": {"equivalent": 3}}
    size = next(r for r in s["rows"] if r["attribute"] == "규격")
    assert size["runs"] == {"total": 3, "agree": 3} and not size["folded"]
    assert s["run"]["tool_calls"] > 0 and s["run"]["cost_usd"] != "0"


def test_injection_line_never_reaches_agent(synth_dir, tmp_path):
    seen = []

    def script(tool_names):
        item, page = yield [("get_procurement_item", {}), ("get_page_lines", {})]
        seen.extend(x["text"] for x in page["lines"])
        bad = yield [("attach_evidence", {"source": "page", "text": "같은 모델", "line": page["removed_lines"][0]})]
        assert bad[0]["ok"] is False
        yield [("submit_answer", {"label": "hold", "rows": [], "price": {"read": None, "llm": None}, "reason": "보류",
                                  "hold_reason": "시험", "hold_rows": []})]
        yield [("review_title", {"agree": True, "supplement": None})]

    s = run(synth_dir, tmp_path, "hard-13", script, repeats=1)
    assert not any("AI" in t for t in seen)
    assert s["routing"]["because"][0]["rule"] == 1 and s["checks"]["attach_rejections"] == 1


def test_bad_submit_gets_errors_then_fails(synth_dir, tmp_path):
    def script(tool_names):
        for _ in range(3):
            yield [("submit_answer", {"label": "similar"})]

    s = run(synth_dir, tmp_path, "base-002", script, repeats=1)
    assert s["label"] == "hold" and "형식 오류" in s["hold_reason"] and s["routing"]["to"] == "human"


def test_usage_cost():
    u = Usage(tokens_in=1_000_000, tokens_out=100_000, cache_write=0, cache_read=1_000_000)
    assert u.cost("claude-sonnet-5-5") == Decimal("3.2")


def test_stdio_server_process(synth_dir, tmp_path):
    """실제 실행과 같은 경로: 도구 서버를 자식 프로세스(stdio)로 띄운다."""
    data = AgentData(synth_dir)
    llm = ScriptedLLM(block_script(thick_result="different", label="different"))
    s = anyio.run(lambda: run_case(llm, data, "hard-02", tmp_path, repeats=1))
    assert s["run"]["error"] is None, s["run"]
    assert s["routing"]["to"] == "auto"
    assert (tmp_path / "logs" / "hard-02.evidence.1.jsonl").read_text().count("\n") == 12


def test_error_messages_hide_keys():
    from studio_agent.runner import _describe

    e = ExceptionGroup("g", [RuntimeError("Error code: 401 - Incorrect API key provided: sk-proj-****abcd. See docs")])
    msg = _describe(e)
    assert "sk-" not in msg and "abcd" not in msg and "401" in msg
