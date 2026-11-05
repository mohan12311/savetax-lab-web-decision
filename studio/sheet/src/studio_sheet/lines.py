"""원문 HTML → 줄 번호 원문(답안지 명세 3절). 페이지 내용을 이해하지 않는 전처리라 어떤 페이지에든 똑같이 적용한다.

규칙(lines-v1):
- <head>, <script>, <style>, <noscript>, <template>의 글은 뺀다.
- 블록 요소(p, div, h1~h6, li, tr, table, header, footer, nav, main, section, article, br 등)의 경계에서 줄을 나눈다.
- 표의 한 행(tr)은 한 줄이고 칸(th, td)은 " | "로 잇는다.
- 공백은 하나로 줄인다. 빈 줄은 버린다.
- 화면에 안 보이게 처리된 요소(style의 display:none·visibility:hidden, hidden 속성) 안의 글은 따로 줄을 만들고 hidden으로 표시한다.

규칙이 바뀌면 줄 번호도 바뀌므로 SPLIT_RULE을 올리고 원문 해시와 함께 기록한다.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from html.parser import HTMLParser

SPLIT_RULE = "lines-v1"

_BLOCK = {
    "address", "article", "aside", "blockquote", "body", "br", "dd", "details", "dialog", "div", "dl", "dt", "fieldset",
    "figcaption", "figure", "footer", "form", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hr", "li", "main", "nav",
    "ol", "p", "pre", "section", "summary", "table", "tbody", "thead", "tfoot", "tr", "ul", "caption",
}
_SKIP = {"head", "script", "style", "noscript", "template"}
_VOID = {"area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "track", "wbr"}
_HIDDEN_STYLE = re.compile(r"(display\s*:\s*none|visibility\s*:\s*hidden)", re.I)
_WS = re.compile(r"\s+")


@dataclass(frozen=True)
class Line:
    line: int
    text: str
    hidden: bool

    def as_dict(self) -> dict:
        return {"line": self.line, "text": self.text, "hidden": self.hidden}


class _Splitter(HTMLParser):
    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.stack: list[tuple[str, bool, bool]] = []  # (태그, 건너뜀, 숨김)
        self.out: list[tuple[str, bool]] = []
        self.buf: list[str] = []
        self.buf_hidden = False

    def _skipping(self) -> bool:
        return any(s for _, s, _ in self.stack)

    def _hidden(self) -> bool:
        return any(h for _, _, h in self.stack)

    def flush(self) -> None:
        text = _WS.sub(" ", "".join(self.buf)).strip(" |")
        if text:
            self.out.append((text, self.buf_hidden))
        self.buf = []

    def _write(self, s: str) -> None:
        hidden = self._hidden()
        if self.buf and hidden != self.buf_hidden and "".join(self.buf).strip():
            self.flush()
        if not "".join(self.buf).strip():
            self.buf_hidden = hidden
        self.buf.append(s)

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        a = dict(attrs)
        hidden = "hidden" in a or bool(_HIDDEN_STYLE.search(a.get("style") or ""))
        if tag in _BLOCK or hidden:
            self.flush()
        if tag in ("td", "th") and "".join(self.buf).strip():
            self._write(" | ")
        if tag not in _VOID:
            self.stack.append((tag, tag in _SKIP, hidden))

    def handle_startendtag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag in _BLOCK:
            self.flush()

    def handle_endtag(self, tag: str) -> None:
        if tag in _VOID:
            if tag in _BLOCK:
                self.flush()
            return
        # 짝이 맞지 않는 닫는 태그는 가장 가까운 같은 태그까지 닫는다
        for i in range(len(self.stack) - 1, -1, -1):
            if self.stack[i][0] == tag:
                was_hidden = self.stack[i][2]
                del self.stack[i:]
                if tag in _BLOCK or was_hidden:
                    self.flush()
                return

    def handle_data(self, data: str) -> None:
        if self._skipping():
            return
        self._write(data)


def split(html: str) -> list[Line]:
    p = _Splitter()
    p.feed(html)
    p.close()
    p.flush()
    return [Line(i, t, h) for i, (t, h) in enumerate(p.out, start=1)]
