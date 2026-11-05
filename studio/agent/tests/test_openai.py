"""OpenAI 어댑터 시험. 실제 API 대신 가짜 Responses 클라이언트가 각본(test_runner.block_script)대로 함수를 부른다."""

import json
from decimal import Decimal

import anyio

from studio_agent.llm import PRICES
from studio_agent.openai_llm import OpenAILLM, to_openai_tools
from studio_agent.runner import SUBMIT_TOOL, run_case
from studio_sheet.data import AgentData
from studio_sheet.mcp_server import build_server
from test_runner import block_script


class Item:
    def __init__(self, **kw):
        self.__dict__.update(kw)

    def model_dump(self, exclude_none=False, exclude=()):
        d = {k: v for k, v in self.__dict__.items() if k not in exclude and not (exclude_none and v is None)}
        if "content" in d and isinstance(d["content"], list):
            d["content"] = [c.__dict__ for c in d["content"]]
        return d


class FakeResponses:
    """입력 끝의 function_call_output을 각본에 넘기고, 각본이 낸 호출을 function_call 항목으로 돌려준다."""

    def __init__(self, script):
        self.script = script
        self.calls = []
        self.gen = None
        self.n = 0

    def create(self, **kw):
        self.calls.append(json.loads(json.dumps(kw, ensure_ascii=False, default=str)))
        items = kw["input"]
        if len(items) == 1:
            self.gen = self.script({t["name"] for t in kw["tools"]})
            calls = next(self.gen)
        else:
            outs = []
            for it in reversed(items):
                if it.get("type") != "function_call_output":
                    break
                outs.insert(0, it["output"])
            results = []
            for o in outs:
                try:
                    results.append(json.loads(o))
                except ValueError:
                    results.append(o)
            try:
                calls = self.gen.send(results)
            except StopIteration:
                calls = []
        output = [Item(type="reasoning", id=f"rs_{self.n}", summary=[], encrypted_content="enc", status=None)]
        for name, inp in calls:
            self.n += 1
            output.append(Item(type="function_call", id=f"fc_{self.n}", call_id=f"call_{self.n}", name=name,
                               arguments=json.dumps(inp, ensure_ascii=False), status="completed"))
        if not calls:
            output.append(Item(type="message", id="msg", role="assistant", status="completed",
                               content=[Item(type="output_text", text="끝", annotations=[])]))
        usage = Item(input_tokens=2000, output_tokens=300, input_tokens_details=Item(cached_tokens=1500))
        return Item(output=output, status="completed", incomplete_details=None, usage=usage, model="gpt-5.4-mini-2026-03-17")


class FakeClient:
    def __init__(self, script):
        self.responses = FakeResponses(script)


def test_tools_are_function_tools_without_oneof():
    t = to_openai_tools([SUBMIT_TOOL])[0]
    assert t["type"] == "function" and t["name"] == "submit_answer" and t["strict"] is False
    assert "oneOf" not in json.dumps(t["parameters"]) and "anyOf" in json.dumps(t["parameters"])


def test_openai_end_to_end(synth_dir, tmp_path):
    client = FakeClient(block_script(conv=("1/25", "25장/㎡"), formula_fn=lambda p: f"ceil({p} × 25 × 1.1)"))
    llm = OpenAILLM(model="gpt-5.4-mini", effort="low", client=client)
    data = AgentData(synth_dir)

    def factory(cid, log_dir, attempt):
        return build_server(synth_dir, cid, log_dir, attempt)

    s = anyio.run(lambda: run_case(llm, data, "hard-04", tmp_path, repeats=1, server_factory=factory))
    assert s["run"]["error"] is None, s["run"]
    assert s["price"]["code"]["benchmark_krw"] == "11000" and s["price"]["check"]["status"] == "match"
    assert s["title_review"] == {"agree": True, "supplement": None}
    assert s["run"]["model"] == "gpt-5.4-mini"  # 날짜 붙은 스냅숏은 같은 모델로 본다
    calls = client.responses.calls
    first, second = calls[0], calls[1]
    assert first["store"] is False and first["reasoning"] == {"effort": "low"}
    assert "검토 도우미" in first["instructions"]
    # 두 번째 요청: 첫 응답의 추론·함수 호출 항목을 그대로 다시 보내고, 결과를 call_id로 잇는다
    kinds = [i.get("type", i.get("role")) for i in second["input"]]
    assert kinds[:4] == ["user", "reasoning", "function_call", "function_call"] and kinds[-1] == "function_call_output"
    fc_ids = {i["call_id"] for i in second["input"] if i.get("type") == "function_call"}
    out_ids = {i["call_id"] for i in second["input"] if i.get("type") == "function_call_output"}
    assert fc_ids == out_ids
    # 비용: 캐시 아닌 입력 500 × 0.75 + 캐시 1500 × 0.075 + 출력 300 × 4.5 (1M당), 요청 수만큼
    per = (Decimal(500) * PRICES["gpt-5.4-mini"]["in"] + Decimal(1500) * PRICES["gpt-5.4-mini"]["cache_read"]
           + Decimal(300) * PRICES["gpt-5.4-mini"]["out"]) / Decimal(1_000_000)
    assert Decimal(s["run"]["cost_usd"]) == (per * len(calls)).quantize(Decimal("0.000001"))


def test_error_results_are_marked(synth_dir, tmp_path):
    def script(tool_names):
        bad = yield [("submit_answer", {"label": "similar"})]
        assert isinstance(bad[0], str) and bad[0].startswith("[오류] 형식 오류")
        yield [("submit_answer", {"label": "hold", "rows": [], "price": {"read": None, "llm": None}, "reason": "보류",
                                  "hold_reason": "시험", "hold_rows": []})]
        yield [("review_title", {"agree": True, "supplement": None})]

    llm = OpenAILLM(model="gpt-5.4-mini", client=FakeClient(script))
    s = anyio.run(lambda: run_case(llm, AgentData(synth_dir), "base-002", tmp_path, repeats=1,
                                   server_factory=lambda c, d, n: build_server(synth_dir, c, d, n)))
    assert s["label"] == "hold" and s["run"]["error"] is None


def test_gpt6_cache_write_is_billed():
    """GPT-6 계열은 캐시 쓰기가 따로 과금된다. input_tokens는 읽기·쓰기를 포함한 전체다."""
    from studio_agent.llm import Usage
    from studio_agent.openai_llm import from_openai

    resp = Item(output=[], status="completed", incomplete_details=None, model="gpt-6.1-terra",
                usage=Item(input_tokens=10_000, output_tokens=1_000,
                           input_tokens_details=Item(cached_tokens=6_000, cache_write_tokens=3_000)))
    r = from_openai(resp, "gpt-6.1-terra")
    assert (r.usage.input_tokens, r.usage.cache_read_input_tokens, r.usage.cache_creation_input_tokens) == (1_000, 6_000, 3_000)
    u = Usage()
    u.add(r.usage)
    # 보통 입력 1,000 × $1 + 캐시 읽기 6,000 × $0.05 + 캐시 쓰기 3,000 × $1.25 + 출력 1,000 × $5 (1M당)
    assert u.cost("gpt-6.1-terra") == Decimal("0.01005")
    assert u.cost("gpt-6.1-sol") == Decimal("0.0201")


def test_only_project_specific_key_is_used(monkeypatch):
    """전역 OPENAI_API_KEY가 있어도 쓰지 않는다. 전용 이름(SAVETAX_STUDIO_OPENAI_KEY)이 없으면 시작하지 않는다."""
    import pytest

    monkeypatch.delenv("SAVETAX_STUDIO_OPENAI_KEY", raising=False)
    monkeypatch.setenv("OPENAI_API_KEY", "sk-global-should-not-be-used")
    with pytest.raises(RuntimeError, match="SAVETAX_STUDIO_OPENAI_KEY"):
        OpenAILLM(model="gpt-6.1-terra")
    monkeypatch.setenv("SAVETAX_STUDIO_OPENAI_KEY", "sk-project-test")
    llm = OpenAILLM(model="gpt-6.1-terra")
    assert llm.client.api_key == "sk-project-test"
