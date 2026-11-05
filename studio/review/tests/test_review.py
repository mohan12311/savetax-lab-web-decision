import json
import shutil
import threading
import urllib.error
import urllib.request
from pathlib import Path

import pytest

import studio_schema
from studio_review import serve

EX = studio_schema.EXAMPLES_DIR / "answer-sheet" / "valid" / "hard-01.json"
STUDIO = Path(__file__).resolve().parents[2]


@pytest.fixture()
def server(tmp_path):
    import subprocess

    data = tmp_path / "synth"
    subprocess.run(["uv", "run", "--quiet", "--project", str(STUDIO / "synth"), "python", "-m", "studio_synth", "--seed", "7",
                    "--out", str(data), "--observed-at", "2026-10-28"], check=True, cwd=STUDIO / "synth")
    runs = tmp_path / "runs"
    run = runs / "r1"
    run.mkdir(parents=True)
    (run / "run.json").write_text(json.dumps({"run_id": "r1", "model": "test", "cases": ["hard-01"]}), encoding="utf-8")
    shutil.copy(EX, run / "hard-01.json")
    static = tmp_path / "dist"
    static.mkdir()
    (static / "index.html").write_text("<!doctype html><title>x</title>", encoding="utf-8")
    httpd = serve(runs, data, static, port=0)
    t = threading.Thread(target=httpd.serve_forever, daemon=True)
    t.start()
    yield f"http://127.0.0.1:{httpd.server_address[1]}", run
    httpd.shutdown()


def get(url):
    with urllib.request.urlopen(url) as r:
        return r.status, r.read()


def put(url, obj):
    req = urllib.request.Request(url, data=json.dumps(obj).encode(), method="PUT", headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req) as r:
        return r.status, json.loads(r.read())


def test_endpoints_and_judgment(server):
    base, run = server
    assert json.loads(get(f"{base}/api/runs")[1])[0]["run_id"] == "r1"
    cases = json.loads(get(f"{base}/api/runs/r1/cases")[1])
    assert cases[0]["case_id"] == "hard-01" and cases[0]["unfolded"] == 1
    detail = json.loads(get(f"{base}/api/runs/r1/cases/hard-01")[1])
    assert detail["page"]["lines"][3]["text"].startswith("규격") and detail["procurement"]["p_krw"] == "27000"
    st, j = put(f"{base}/api/runs/r1/cases/hard-01/judgment", {"verdict": "approve", "rows": {"규격": "agree"}, "note": "ok"})
    assert st == 200 and (run / "judgments" / "hard-01.json").exists()
    studio_schema.load(run / "judgments" / "hard-01.json", "judgment")
    with pytest.raises(urllib.error.HTTPError) as e:
        put(f"{base}/api/runs/r1/cases/hard-01/judgment", {"verdict": "edit"})
    assert e.value.code == 400


def test_no_traversal_or_answer_file(server):
    base, run = server
    (run.parent.parent / "synth" / "cases.jsonl").exists()
    for path in ["/api/runs/..%2Fsynth/cases", "/api/runs/r1/cases/..%2F..%2Fsynth%2Fcases", "/api/runs/r1/cases/run"]:
        with pytest.raises(urllib.error.HTTPError) as e:
            get(base + path)
        assert e.value.code == 404
    st, body = get(base + "/../../synth/cases.jsonl")
    assert b"<title>x</title>" in body  # 정적 경로 밖은 index.html로 돌린다
