"""채점기(채점 명세 studio/docs/scoring.md).

답안지(<실행 디렉터리>/<case_id>.json), 근거 발급 기록(logs/<case_id>.evidence.1.jsonl), 정답 파일(cases.jsonl)을
읽기만 한다. 에이전트와 같은 판단 로직(studio_sheet)을 쓰지 않고 ① 확인을 다시 돌리지 않는다. 생성기(studio_synth)도
가져다 쓰지 않는다. 정답은 cases.jsonl 파일로만 읽는다.

    python -m studio_grader <실행 디렉터리> --truth studio/demo/synth/cases.jsonl
"""

from __future__ import annotations

import json
import statistics
from collections import Counter, defaultdict
from decimal import Decimal
from fractions import Fraction
from pathlib import Path
from typing import Any

import studio_schema

POSITIVE = ("same_model", "equivalent")
TRUTH_LABELS = ("same_model", "equivalent", "different", "insufficient")
AGENT_LABELS = (*TRUTH_LABELS, "hold")
# 비교 단가 허용범위: 정답 대비 ±1%(채점 명세 3.4절, 2026-11-05 권장안 채택). 범위 안이어도 정답은 아니며 따로 센다.
TOLERANCE = Fraction(1, 100)
NOT_SHEETS = {"run.json", "scorecard.json"}


def frac(n: int, d: int) -> dict[str, Any]:
    """작은 표본은 건수로 적는다(채점 명세 1절)."""
    return {"n": n, "d": d, "text": f"{n}/{d}"}


def load_truth(path: Path) -> dict[str, dict[str, Any]]:
    return {r["case_id"]: r for r in (json.loads(x) for x in path.read_text(encoding="utf-8").splitlines() if x.strip())}


def load_sheets(run_dir: Path) -> dict[str, dict[str, Any]]:
    out = {}
    for p in sorted(run_dir.glob("*.json")):
        if p.name in NOT_SHEETS:
            continue
        s = studio_schema.load(p)
        out[s["case_id"]] = s
    return out


def issued_ids(run_dir: Path, case_id: str) -> set[str] | None:
    p = run_dir / "logs" / f"{case_id}.evidence.1.jsonl"
    if not p.exists():
        return None
    ids = set()
    for x in p.read_text(encoding="utf-8").splitlines():
        if x.strip():
            r = json.loads(x)
            if not r.get("rejected"):
                ids.add(r["id"])
    return ids


def referenced_ids(s: dict[str, Any]) -> set[str]:
    refs = set()
    for r in s["rows"]:
        refs.update(x for x in (r["procurement"], r["seller"]) if x)
    read = s["price"]["read"]
    if read:
        refs.update(v["ev"] for v in read.values() if v["ev"])
    return refs


def expected_conclusion(t: dict[str, Any]) -> str:
    """기대 결론: 정답 라벨이 같은 물건이고 gap_pass면 N배. 대회 선점 상태는 보지 않는다(채점 명세 2절)."""
    return "n_times" if t["label"] in POSITIVE and t["expected"]["gap_pass"] else "nothing_to_flag"


def group_of(case_id: str) -> str:
    return case_id.split("-")[0]


def _conclusion(pairs: list[tuple[dict, dict]]) -> dict[str, Any]:
    said_n = [(s, t) for s, t in pairs if s["conclusion"] == "n_times"]
    exp_n = [(s, t) for s, t in pairs if expected_conclusion(t) == "n_times"]
    exp_n_not_held = [(s, t) for s, t in exp_n if s["conclusion"] != "hold"]
    held = [(s, t) for s, t in pairs if s["conclusion"] == "hold"]
    wrong_label, wrong_gap = [], []
    for s, t in pairs:
        if s["conclusion"] == "hold" or s["conclusion"] == expected_conclusion(t):
            continue
        same_side = (s["label"] in POSITIVE) == (t["label"] in POSITIVE)
        (wrong_gap if same_side else wrong_label).append(s["case_id"])
    return {
        "precision": frac(sum(1 for s, t in said_n if expected_conclusion(t) == "n_times"), len(said_n)),
        "recall": frac(sum(1 for s, t in exp_n_not_held if s["conclusion"] == "n_times"), len(exp_n_not_held)),
        "held_n_times": sum(1 for s, t in exp_n if s["conclusion"] == "hold"),
        "hold_rate": frac(len(held), len(pairs)),
        "wrong_by_label": sorted(wrong_label),
        "wrong_by_gap": sorted(wrong_gap),
    }


def _labels(pairs: list[tuple[dict, dict]]) -> dict[str, Any]:
    m = {t: {a: 0 for a in AGENT_LABELS} for t in TRUTH_LABELS}
    for s, t in pairs:
        m[t["label"]][s["label"]] += 1
    return {
        "matrix": m,
        "accuracy": frac(sum(m[x][x] for x in TRUTH_LABELS), len(pairs)),
        "equivalent_as_same_model": [s["case_id"] for s, t in pairs if t["label"] == "equivalent" and s["label"] == "same_model"],
    }


def _hold_quality(pairs: list[tuple[dict, dict]]) -> dict[str, Any]:
    held = [(s, t) for s, t in pairs if s["label"] == "hold"]
    pointed = []
    for s, t in held:
        diffs = {d["attribute"] for d in (t["truth"]["diffs"] or [])}
        pointed.append(bool(diffs & set(s["hold_rows"])))
    return {"base_holds": sum(1 for s, _ in held if group_of(s["case_id"]) == "base"),
            "rows_match_truth_diffs": frac(sum(pointed), len(pointed))}


def _read_cause(s: dict[str, Any], t: dict[str, Any]) -> list[str]:
    read, truth = s["price"]["read"], t["truth"]["read"]
    if read is None or truth is None:
        return ["읽지 않음"]
    causes = []
    if read["sale_price"]["value"] != truth["sale_price_krw"]:
        causes.append("판매가")
    if read["vat"]["value"] != truth["vat"]:
        causes.append("부가세 표기")
    try:
        conv_ok = studio_schema.units(read["conversion"]["value"]) == studio_schema.units(truth["conversion"])
    except ValueError:
        conv_ok = False
    if read["sale_unit"]["value"] != truth["sale_unit"] or not conv_ok:
        causes.append("판매 단위·환산")
    return causes or ["계산 규칙"]


def _benchmark(pairs: list[tuple[dict, dict]]) -> dict[str, Any]:
    exact, within, outside, rows = 0, 0, 0, []
    target = [(s, t) for s, t in pairs
              if s["label"] in POSITIVE and t["label"] in POSITIVE and t["expected"]["benchmark_unit_price_krw"] is not None]
    for s, t in target:
        want = studio_schema.krw(t["expected"]["benchmark_unit_price_krw"])
        code = s["price"]["code"]
        if code is None:
            outside += 1
            rows.append({"case_id": s["case_id"], "kind": "outside", "got": None, "want": str(want), "direction": None,
                         "cause": _read_cause(s, t)})
            continue
        got = studio_schema.krw(code["benchmark_krw"])
        if got == want:
            exact += 1
            continue
        kind = "within" if abs(Fraction(got - want, want)) <= TOLERANCE else "outside"
        within += kind == "within"
        outside += kind == "outside"
        rows.append({"case_id": s["case_id"], "kind": kind, "got": str(got), "want": str(want),
                     "direction": "배율 부풂(비교 단가를 낮게)" if got < want else "배율 줄어듦(비교 단가를 높게)",
                     "cause": _read_cause(s, t)})
    return {"exact": frac(exact, len(target)), "within_tolerance": within, "outside": outside, "misses": rows,
            "tolerance": "±1%"}


def _reason(pairs: list[tuple[dict, dict]], label: str = "equivalent") -> dict[str, Any]:
    target = [(s, t) for s, t in pairs if t["label"] == label and s["label"] == label and t["truth"]["diffs"] is not None]
    ok, misses = 0, []
    for s, t in target:
        rows = {r["attribute"]: r["result"] for r in s["rows"]}
        missing = [f"{d['attribute']}:{d['kind']}" for d in t["truth"]["diffs"] if rows.get(d["attribute"]) != d["kind"]]
        if missing:
            misses.append({"case_id": s["case_id"], "missing": missing})
        else:
            ok += 1
    return {"scope": label, "score": frac(ok, len(target)), "misses": misses}


def _routing(pairs: list[tuple[dict, dict]]) -> dict[str, Any]:
    auto = [(s, t) for s, t in pairs if s["routing"]["to"] == "auto"]
    human = [(s, t) for s, t in pairs if s["routing"]["to"] == "human"]
    by_rule = Counter(b["rule"] for s, _ in human for b in s["routing"]["because"])
    return {
        "auto": len(auto),
        "auto_wrong": sorted(s["case_id"] for s, t in auto if s["conclusion"] != expected_conclusion(t)),
        "human": len(human),
        "human_by_rule": {str(k): v for k, v in sorted(by_rule.items())},
        "human_but_agent_right": sum(1 for s, t in human if s["conclusion"] == expected_conclusion(t)),
    }


def _diagnostics(sheets: list[dict[str, Any]]) -> dict[str, Any]:
    sig = Counter()
    for s in sheets:
        for r in s["rows"]:
            for x in r["check"]["signals"]:
                sig[x.split(":")[0]] += 1
        for x in s["price"]["check"]["signals"]:
            sig[x.split(":")[0]] += 1
    st = Counter(s["price"]["check"]["status"] for s in sheets)
    return {
        "attach_rejections": sum(s["checks"]["attach_rejections"] for s in sheets),
        "unissued_evidence": sum(len(s["checks"]["unissued_evidence"]) for s in sheets),
        "missing_required": sum(len(s["checks"]["missing_required"]) for s in sheets),
        "assertive": sum(len(s["checks"]["assertive"]) for s in sheets),
        "hidden_evidence": sum(len(s["checks"]["hidden_evidence"]) for s in sheets),
        "price_status": dict(sorted(st.items())),
        "signals": dict(sorted(sig.items())),
        "calculator_calls": sum(s["checks"]["calculator_calls"] for s in sheets),
    }


def _cost(sheets: list[dict[str, Any]]) -> dict[str, Any]:
    if not sheets:
        return {}
    cost = [Decimal(s["run"]["cost_usd"]) for s in sheets]
    ms = [s["run"]["elapsed_ms"] for s in sheets]
    calls = [s["run"]["tool_calls"] for s in sheets]
    return {
        "total_usd": format(sum(cost, Decimal(0)), "f"),
        "per_case_usd": {"median": format(statistics.median(cost), "f"), "max": format(max(cost), "f")},
        "elapsed_ms": {"median": int(statistics.median(ms)), "max": max(ms)},
        "tool_calls": {"median": statistics.median(calls), "max": max(calls)},
        "repeated": frac(sum(1 for s in sheets if s["run"]["runs"] > 1), len(sheets)),
        "errors": sorted(s["case_id"] for s in sheets if s["run"]["error"]),
    }


def _zero(run_dir: Path, sheets: list[dict[str, Any]]) -> dict[str, Any]:
    danger_auto = [s["case_id"] for s in sheets
                   if s["routing"]["to"] == "auto" and any(w["level"] == "danger" for w in s["warnings"])]
    unissued = []
    for s in sheets:
        log = issued_ids(run_dir, s["case_id"])
        refs = referenced_ids(s)
        if refs and (log is None or refs - log):
            unissued.append({"case_id": s["case_id"], "ids": sorted(refs - (log or set()))})
    assertive_auto = [s["case_id"] for s in sheets if s["routing"]["to"] == "auto" and s["checks"]["assertive"]]
    return {"pass": not (danger_auto or unissued or assertive_auto), "danger_auto_passed": danger_auto,
            "unissued_evidence": unissued, "assertive_auto_passed": assertive_auto}


def _hard_table(pairs: list[tuple[dict, dict]]) -> list[dict[str, Any]]:
    out = []
    for s, t in pairs:
        if group_of(s["case_id"]) != "hard":
            continue
        cells = []
        if s["label"] != t["label"]:
            cells.append(f"라벨 {s['label']}(정답 {t['label']})")
        if s["conclusion"] != expected_conclusion(t):
            cells.append(f"결론 {s['conclusion']}(기대 {expected_conclusion(t)})")
        want = t["expected"]["benchmark_unit_price_krw"]
        got = s["price"]["code"]["benchmark_krw"] if s["price"]["code"] else None
        if want and s["label"] in POSITIVE and got != want:
            cells.append(f"비교 단가 {got}(정답 {want})")
        out.append({"case_id": s["case_id"], "traps": t["traps"], "pass": not cells, "wrong": cells, "routing": s["routing"]["to"]})
    return out


def load_judgments(run_dir: Path) -> dict[str, dict[str, Any]]:
    d = run_dir / "judgments"
    return {p.stem: studio_schema.load(p, "judgment") for p in sorted(d.glob("*.json"))} if d.exists() else {}


def human_conclusion(s: dict[str, Any], j: dict[str, Any]) -> str:
    """사람 판정을 결론으로: 승인은 에이전트 결론, 반려는 지적할 것 없음, 라벨 수정은 코드 계산으로 다시 정한다."""
    if j["verdict"] == "approve":
        return "nothing_to_flag" if s["conclusion"] == "hold" else s["conclusion"]
    if j["verdict"] == "reject":
        return "nothing_to_flag"
    code = s["price"]["code"]
    return "n_times" if j["label"] in POSITIVE and code is not None and code["gap_pass"] else "nothing_to_flag"


def _human(pairs: list[tuple[dict, dict]], judgments: dict[str, dict[str, Any]]) -> dict[str, Any]:
    judged = [(s, t, judgments[s["case_id"]]) for s, t in pairs if s["case_id"] in judgments]
    return {
        "judged": len(judged),
        "agree_with_agent": frac(sum(1 for s, t, j in judged if human_conclusion(s, j) == s["conclusion"]), len(judged)),
        "agree_with_truth": frac(sum(1 for s, t, j in judged if human_conclusion(s, j) == expected_conclusion(t)), len(judged)),
    }


def relaxation_records(sheets: dict[str, dict[str, Any]], judgments: dict[str, dict[str, Any]]) -> list[dict[str, Any]]:
    """완화 기준 기록(채점 명세 6절): 펼친 줄마다 걸린 신호·일치도·버전·사람 판정, N배 결론마다 사람의 승인/수정/반려."""
    out = []
    for cid, j in sorted(judgments.items()):
        s = sheets.get(cid)
        if s is None:
            continue
        ver = {"model": s["run"]["model"], "prompt_version": s["run"]["prompt_version"], "category": s["run"]["category"]}
        for r in s["rows"]:
            if r["folded"] or r["attribute"] not in j["rows"]:
                continue
            out.append({"kind": "row", "case_id": cid, "attribute": r["attribute"],
                        "signals": sorted({x.split(":")[0] for x in r["check"]["signals"]}), "runs": r["runs"], **ver,
                        "human": j["rows"][r["attribute"]], "flipped": j["rows"][r["attribute"]] == "disagree"})
        if s["conclusion"] == "n_times":
            out.append({"kind": "n_times", "case_id": cid, **ver, "human": j["verdict"]})
    return out


def grade(run_dir: Path, truth_path: Path) -> dict[str, Any]:
    truth = load_truth(truth_path)
    sheets = load_sheets(run_dir)
    missing = sorted(set(sheets) - set(truth))
    if missing:
        raise ValueError(f"정답에 없는 사건: {missing}")
    judgments = load_judgments(run_dir)
    pairs_all = [(s, truth[c]) for c, s in sheets.items()]
    groups: dict[str, list] = defaultdict(list)
    for s, t in pairs_all:
        groups[group_of(s["case_id"])].append((s, t))
    groups["all"] = pairs_all

    def block(pairs):
        return {
            "cases": len(pairs),
            "conclusion": _conclusion(pairs),
            "labels": _labels(pairs),
            "hold": _hold_quality(pairs),
            "benchmark": _benchmark(pairs),
            "reason": _reason(pairs),
            "routing": _routing(pairs),
            "diagnostics": _diagnostics([s for s, _ in pairs]),
            "cost": _cost([s for s, _ in pairs]),
            "human": _human(pairs, judgments),
        }

    meta = {}
    if (run_dir / "run.json").exists():
        meta = json.loads((run_dir / "run.json").read_text(encoding="utf-8"))
    return {
        "run_id": meta.get("run_id", run_dir.name), "model": meta.get("model"), "prompt_version": meta.get("prompt_version"),
        "graded_cases": len(sheets), "truth_cases": len(truth),
        "zero_conditions": _zero(run_dir, list(sheets.values())),
        "groups": {k: block(v) for k, v in sorted(groups.items(), key=lambda kv: ("all", "base", "hard").index(kv[0])
                                                  if kv[0] in ("all", "base", "hard") else 9)},
        "hard_cases": _hard_table(pairs_all),
        "relaxation_records": relaxation_records(sheets, judgments),
    }
