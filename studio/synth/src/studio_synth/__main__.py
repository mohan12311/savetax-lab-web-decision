"""python -m studio_synth --seed 7 --out ../demo/synth [--shop-base https://127.0.0.1:18444] [--observed-at YYYY-MM-DD]"""

from __future__ import annotations

import argparse
import datetime as dt
import json
from pathlib import Path

from .generate import generate

KST = dt.timezone(dt.timedelta(hours=9))


def main() -> None:
    ap = argparse.ArgumentParser(prog="studio_synth", description="2부 데모용 합성 데이터를 만든다(LLM 없음, 시드 고정)")
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--out", type=Path, required=True, help="출력 디렉터리(이전 studio-synth 출력이면 지우고 다시 만든다)")
    ap.add_argument("--shop-base", default="https://127.0.0.1:18444", help="가짜 판매처 주소(근거 URL에 들어간다)")
    ap.add_argument("--observed-at", default=dt.datetime.now(KST).date().isoformat(),
                    help="참조 본문의 관측일(KST). 1부 게이트는 이 날 받아 둔 원문만 인정하므로 데모 당일이어야 한다")
    ap.add_argument("--base-count", type=int, default=36, help="난수로 만드는 기본 사례 수(어려운 사례 15개는 따로 붙는다)")
    a = ap.parse_args()
    if not a.shop_base.startswith("https://"):
        ap.error("--shop-base는 https 주소여야 한다")
    m = generate(a.out, a.seed, a.shop_base.rstrip("/"), a.observed_at, a.base_count)
    print(json.dumps({k: m[k] for k in ("seed", "observed_at", "case_count", "reference_claims", "labels", "actions", "content_sha256")},
                     ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
