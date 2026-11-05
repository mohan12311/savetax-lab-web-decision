"""python -m studio_review --runs studio/demo/runs --data studio/demo/synth [--port 18480]"""

from __future__ import annotations

import argparse
from pathlib import Path

from . import serve

WEB_DIST = Path(__file__).resolve().parents[3] / "web" / "dist"


def main() -> None:
    ap = argparse.ArgumentParser(prog="studio_review")
    ap.add_argument("--runs", type=Path, required=True)
    ap.add_argument("--data", type=Path, required=True)
    ap.add_argument("--port", type=int, default=18480)
    ap.add_argument("--static", type=Path, default=WEB_DIST)
    a = ap.parse_args()
    httpd = serve(a.runs, a.data, a.static, a.port)
    print(f"검토 화면: http://127.0.0.1:{a.port}  (Ctrl-C로 끝)", flush=True)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
