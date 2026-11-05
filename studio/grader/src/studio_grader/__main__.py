"""채점: python -m studio_grader <실행 디렉터리> --truth <cases.jsonl> → scorecard.json, scorecard.md"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from . import grade
from .report import markdown


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="studio_grader")
    ap.add_argument("run_dir", type=Path)
    ap.add_argument("--truth", type=Path, required=True, help="정답 파일(cases.jsonl). 평가 단계만 읽는다")
    a = ap.parse_args(argv)
    card = grade(a.run_dir, a.truth)
    (a.run_dir / "scorecard.json").write_text(json.dumps(card, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    recs = card["relaxation_records"]
    (a.run_dir / "relaxation-records.jsonl").write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in recs), encoding="utf-8")
    md = markdown(card)
    (a.run_dir / "scorecard.md").write_text(md, encoding="utf-8")
    print(md)
    return 0 if card["zero_conditions"]["pass"] else 1


if __name__ == "__main__":
    sys.exit(main())
