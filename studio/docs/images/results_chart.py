"""README 결과 그래프(SVG, 라이트·다크 두 벌)를 만든다. 표준 라이브러리만 쓴다.

숫자는 아래 실행들의 scorecard.md에서 옮겼다(studio/samples/scorecards/). 합성 데이터, 시드 7.
  v1: 20261105-141450-rule-baseline, 20261105-153104-gpt-6.1-terra, 20261105-153417-gpt-6.1-sol
  v2: 20261105-163820-rule-baseline, 20261105-163320-gpt-6.1-terra(+ 20261109-100556, 20261109-100939 같은 결과),
      20261109-101324-gpt-6.1-sol

    python3 studio/docs/images/results_chart.py
"""

from pathlib import Path

SERIES = ["규칙 기준선(LLM 없음)", "gpt-6.1-terra", "gpt-6.1-sol"]
# (패널 제목, 패널 설명, {묶음: [(맞힌 수, 전체), ...시리즈 순]})
PANELS = [
    ("라벨 정확도", "같은 모델 / 동급 / 다른 물건 / 근거 부족", {
        "v1 고른 표기 51건": [(48, 51), (50, 51), (48, 51)],
        "v2 다양한 표기 55건": [(13, 55), (55, 55), (54, 55)],
    }),
    ("결론 재현율", "정답이 \"N배\"인 사건을 놓치지 않기(보류 제외)", {
        "v1 고른 표기 51건": [(21, 22), (21, 21), (19, 19)],
        "v2 다양한 표기 55건": [(6, 25), (25, 25), (24, 24)],
    }),
]

THEMES = {
    "light": dict(surface="#fcfcfb", ink="#0b0b0b", ink2="#52514e", muted="#898781", grid="#e1e0d9", axis="#c3c2b7",
                  series=["#1baf7a", "#2a78d6", "#eb6834"]),
    "dark": dict(surface="#1a1a19", ink="#f0efec", ink2="#c3c2b7", muted="#898781", grid="#2c2c2a", axis="#383835",
                 series=["#199e70", "#3987e5", "#d95926"]),
}
FONT = "-apple-system, BlinkMacSystemFont, 'Apple SD Gothic Neo', 'Noto Sans KR', 'Malgun Gothic', sans-serif"

W, H = 900, 400
BAR, GAP = 24, 12  # 막대 두께 상한 24px, 숫자 라벨이 겹치지 않게 막대 사이를 띄운다
PLOT_TOP, PLOT_BOTTOM = 150, 340


def bar_path(x: float, y: float, w: float, h: float, r: float = 4) -> str:
    """위쪽만 4px 둥근 막대(바닥은 직각)."""
    r = min(r, h)
    return (f"M{x:.1f},{y + h:.1f} V{y + r:.1f} Q{x:.1f},{y:.1f} {x + r:.1f},{y:.1f} "
            f"H{x + w - r:.1f} Q{x + w:.1f},{y:.1f} {x + w:.1f},{y + r:.1f} V{y + h:.1f} Z")


def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;").replace('"', "&quot;")


def render(theme: str) -> str:
    t = THEMES[theme]
    out = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" '
           f'font-family="{esc(FONT)}" role="img" aria-labelledby="title desc">',
           '<title id="title">합성 데이터 v1·v2에서 규칙 기준선, gpt-6.1-terra, gpt-6.1-sol의 라벨 정확도와 결론 재현율</title>',
           '<desc id="desc">v1(고른 표기)에서는 셋 다 90% 이상이다. v2(다양한 표기)에서 규칙 기준선은 라벨 13/55, 재현율 6/25로 '
           '떨어지고 terra는 55/55, 25/25(3회 실행 모두 같음), sol은 54/55(보류 1), 24/24다.</desc>',
           f'<rect width="{W}" height="{H}" rx="12" fill="{t["surface"]}"/>',
           f'<text x="28" y="40" font-size="19" font-weight="700" fill="{t["ink"]}">표기를 다양하게 바꾸면 규칙은 무너지고 LLM 에이전트는 그대로</text>',
           f'<text x="28" y="64" font-size="13" fill="{t["ink2"]}">합성 데이터(시드 7). 정답은 생성기가 정한다. terra v2는 같은 설정 3회 실행이 모두 같은 점수, sol은 판단 보류 1건(hard-01)</text>']
    # 범례(시리즈가 셋이라 늘 둔다)
    lx = 28
    for i, name in enumerate(SERIES):
        out.append(f'<rect x="{lx}" y="84" width="12" height="12" rx="3" fill="{t["series"][i]}"/>')
        out.append(f'<text x="{lx + 18}" y="95" font-size="13" fill="{t["ink"]}">{esc(name)}</text>')
        lx += 18 + len(name) * 9.5 + 26
    panel_w = (W - 56) / len(PANELS)
    for p, (ptitle, pdesc, groups) in enumerate(PANELS):
        x0 = 28 + p * panel_w
        out.append(f'<text x="{x0:.1f}" y="128" font-size="15" font-weight="700" fill="{t["ink"]}">{esc(ptitle)}</text>')
        out.append(f'<text x="{x0 + len(ptitle) * 15 + 10:.1f}" y="128" font-size="12" fill="{t["muted"]}">{esc(pdesc)}</text>')
        axis_x = x0 + 34
        plot_h = PLOT_BOTTOM - PLOT_TOP
        for pct in (0, 50, 100):
            y = PLOT_BOTTOM - plot_h * pct / 100
            color = t["axis"] if pct == 0 else t["grid"]
            out.append(f'<line x1="{axis_x:.1f}" x2="{x0 + panel_w - 30:.1f}" y1="{y:.1f}" y2="{y:.1f}" stroke="{color}" stroke-width="1"/>')
            out.append(f'<text x="{axis_x - 8:.1f}" y="{y + 4:.1f}" font-size="11" text-anchor="end" fill="{t["muted"]}">{pct}%</text>')
        group_w = len(SERIES) * BAR + (len(SERIES) - 1) * GAP
        span = (x0 + panel_w - 30) - axis_x
        for g, (gname, vals) in enumerate(groups.items()):
            cx = axis_x + span * (g + 0.5) / len(groups)
            gx = cx - group_w / 2
            for i, (k, n) in enumerate(vals):
                h = plot_h * k / n
                bx = gx + i * (BAR + GAP)
                out.append(f'<path d="{bar_path(bx, PLOT_BOTTOM - h, BAR, h)}" fill="{t["series"][i]}">'
                           f'<title>{esc(SERIES[i])} · {esc(gname)}: {k}/{n}</title></path>')
                out.append(f'<text x="{bx + BAR / 2:.1f}" y="{PLOT_BOTTOM - h - 6:.1f}" font-size="11" text-anchor="middle" '
                           f'fill="{t["ink2"]}">{k}/{n}</text>')
            out.append(f'<text x="{cx:.1f}" y="{PLOT_BOTTOM + 22:.1f}" font-size="13" text-anchor="middle" fill="{t["ink"]}">{esc(gname)}</text>')
    out.append(f'<text x="28" y="{H - 12}" font-size="11" fill="{t["muted"]}">점수표 원본: studio/samples/scorecards/ · 그림 생성: studio/docs/images/results_chart.py</text>')
    out.append("</svg>")
    return "\n".join(out) + "\n"


if __name__ == "__main__":
    here = Path(__file__).resolve().parent
    for theme in THEMES:
        (here / f"results-{theme}.svg").write_text(render(theme), encoding="utf-8")
        print(here / f"results-{theme}.svg")
