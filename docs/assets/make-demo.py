#!/usr/bin/env python3
"""Generate the animated README demo (docs/assets/demo-*.svg).

The demo is one loop of the core story: Claude Code asks for a permission in
tmux on the computer, the request crosses the relay as ciphertext, it is
approved on the phone, the same session carries on, and the next message is
sent from the phone. Plain CSS animation, so GitHub plays it inside <img>.

    python3 docs/assets/make-demo.py            # write all four variants
    python3 docs/assets/make-demo.py --at 5.0   # also write frozen frames to /tmp for review
"""

import argparse
import pathlib
from xml.sax.saxutils import escape

T = 16.0      # loop length, seconds
END = 15.3    # everything fades out from here
FADE = 0.25

HERE = pathlib.Path(__file__).resolve().parent

THEMES = {
    "light": dict(page="#ffffff", fg="#16181c", fg2="#3a3f46", muted="#626970", line="#e6e8eb",
                  card="#ffffff", soft="#f5f5f7", chip="#eff0f2", frame="#1b1d21", edge="#d0d7de",
                  blue="#2563d9", blue_bg="#2f6feb", blue_soft="#edf3ff", orange="#e07a12",
                  orange_soft="#fff3e3", green="#22a05a", red="#c62f2f", relay="#f6f8fa"),
    "dark": dict(page="#0d1117", fg="#e8eaed", fg2="#c9ced4", muted="#a0a7af", line="#2a2d32",
                 card="#17181b", soft="#1f2125", chip="#272a2f", frame="#3a3d42", edge="#30363d",
                 blue="#7aa7ff", blue_bg="#2f6feb", blue_soft="rgba(90,145,255,.16)", orange="#f0a04b",
                 orange_soft="rgba(240,160,75,.14)", green="#3fbf73", red="#ff7b7b", relay="#161b22"),
}

TEXT = {
    "en": dict(
        alt="Animated demo: Claude Code running in tmux on a computer asks to run go test; the request crosses the relay as ciphertext; it is approved on the phone; the same session continues; the next message is sent from the phone.",
        computer="Your computer", relay="Your relay", relay_sub="sees ciphertext only", phone="Your phone",
        caps=["Start Codex or Claude Code on your computer, as usual",
              "The request is encrypted; the relay only forwards ciphertext",
              "Approve it on your phone",
              "The same tmux session carries on",
              "Send the next message from anywhere"],
        prompt1="Split the handlers into a package", prompt2="Also open a PR",
        title="API refactor", working="Working", waiting="Needs approval", idle="Idle",
        from_term="MacBook terminal", edited="Edited 5 files  +84 −61",
        run="Run command", deny="Deny", allow="Allow", allowed="Allowed",
        reply="All tests pass.", message="Message",
        t_reply="All tests pass.", t_next="Creating a branch and opening the PR…",
    ),
    "zh": dict(
        alt="动画演示：电脑上 tmux 里的 Claude Code 请求执行 go test；请求以密文经过中继；在手机上点允许；同一个会话接着运行；再从手机发出下一条消息。",
        computer="你的电脑", relay="你的中继", relay_sub="只看得到密文", phone="你的手机",
        caps=["照常在电脑上启动 Codex 或 Claude Code",
              "请求在电脑上加密，中继只转发密文",
              "在手机上点「允许」",
              "同一个 tmux 会话接着运行",
              "在哪儿都能发下一条消息"],
        prompt1="把 handler 拆到单独的包里", prompt2="再开个 PR",
        title="重构 API 层", working="运行中", waiting="等待审批", idle="空闲",
        from_term="MacBook 终端", edited="编辑 5 个文件  +84 −61",
        run="请求执行", deny="不允许", allow="允许", allowed="已允许",
        reply="测试全部通过。", message="消息",
        t_reply="测试全部通过。", t_next="正在新建分支并创建 PR…",
    ),
}


def width(text, size=13):
    """Rough rendered width: CJK glyphs are square, Latin about 0.53 em."""
    return sum(size if ord(ch) > 0x2e80 else size * 0.53 for ch in text)


def pct(t):
    return f"{max(0.0, min(100.0, t / T * 100)):.3f}%"


class Anim:
    """Collects one @keyframes rule per animated element."""

    def __init__(self):
        self.rules = []
        self.n = 0

    def _name(self):
        self.n += 1
        return f"k{self.n}"

    def show(self, *spans):
        """Visible during the given (on, off) spans; off=None means until END."""
        name = self._name()
        frames = [("0%", "opacity:0")]
        for on, off in spans:
            off = END if off is None else off
            frames += [(pct(on), "opacity:0"), (pct(on + FADE), "opacity:1"),
                       (pct(off), "opacity:1"), (pct(off + FADE), "opacity:0")]
        frames.append(("100%", "opacity:0"))
        self.rules.append((name, frames))
        return name

    def move(self, points):
        """points: (time, x) waypoints; visible only between the first and last."""
        name = self._name()
        t0, x0 = points[0]
        t1, x1 = points[-1]
        frames = [("0%", f"opacity:0;transform:translateX({x0}px)"),
                  (pct(t0), f"opacity:0;transform:translateX({x0}px)")]
        for i, (t, x) in enumerate(points):
            op = 1 if 0 < i < len(points) - 1 or len(points) == 1 else 1
            frames.append((pct(t + (0.05 if i == 0 else 0)), f"opacity:{op};transform:translateX({x}px)"))
        frames += [(pct(t1 + 0.1), f"opacity:0;transform:translateX({x1}px)"),
                   ("100%", f"opacity:0;transform:translateX({x1}px)")]
        self.rules.append((name, frames))
        return name

    def pulse(self, t):
        """A tap: a circle that grows and fades."""
        name = self._name()
        frames = [("0%", "opacity:0;transform:scale(.4)"), (pct(t), "opacity:0;transform:scale(.4)"),
                  (pct(t + 0.05), "opacity:.55;transform:scale(.5)"), (pct(t + 0.55), "opacity:0;transform:scale(1.6)"),
                  ("100%", "opacity:0;transform:scale(1.6)")]
        self.rules.append((name, frames))
        return name

    def wipe(self, on, off):
        """A cover that shrinks to the right, revealing typed text."""
        name = self._name()
        frames = [("0%", "transform:scaleX(1)"), (pct(on), "transform:scaleX(1)"),
                  (pct(off), "transform:scaleX(0)"), ("100%", "transform:scaleX(0)")]
        self.rules.append((name, frames))
        return name

    def css(self):
        out = []
        for name, frames in self.rules:
            body = "".join(f"{k}{{{v}}}" for k, v in frames)
            out.append(f"@keyframes {name}{{{body}}}")
        return "\n".join(out)


def build(lang, theme, at=None, scale=1.0):
    c = THEMES[theme]
    s = TEXT[lang]
    a = Anim()
    e = lambda x: escape(x)

    def g(anim, body, transient=True, extra=""):
        cls = "a tr" if transient else "a"
        return f'<g class="{cls}" style="animation-name:{anim}"{extra}>{body}</g>'

    parts = []
    add = parts.append

    # ---------- computer: a tmux pane running Claude Code ----------
    tx, ty, tw, th = 20, 40, 360, 330
    add(f'<text class="h" x="{tx}" y="26">{e(s["computer"])}</text>')
    add(f'<rect x="{tx}" y="{ty}" width="{tw}" height="{th}" rx="10" fill="#0d1117" stroke="{c["edge"]}"/>')
    add(f'<path d="M{tx} {ty+28}H{tx+tw}" stroke="#30363d"/>')
    for i, col in enumerate(["#ff5f57", "#febc2e", "#28c840"]):
        add(f'<circle cx="{tx+18+i*16}" cy="{ty+14}" r="5" fill="{col}"/>')
    add(f'<text class="m" x="{tx+tw/2}" y="{ty+18}" text-anchor="middle" fill="#8b949e" font-size="11">tmux · ~/api</text>')

    def tline(y, text, color="#c9d1d9", on=0.0, off=None, transient=False, weight=None):
        w = f' font-weight="{weight}"' if weight else ""
        body = f'<text class="m" x="{tx+14}" y="{y}" fill="{color}"{w}>{text}</text>'
        add(g(a.show((on, off)), body, transient))

    tline(92, '<tspan fill="#7ee787">$</tspan> remote-agent claude', on=0.3)
    tline(118, f'<tspan fill="#8b949e">&gt;</tspan> {e(s["prompt1"])}', color="#ffffff", on=0.8)
    tline(142, f'<tspan fill="#d2a8ff">●</tspan> {e(s["edited"])}', on=1.4)
    # Claude Code's own permission prompt, until it is answered from the phone
    box = (f'<rect x="{tx+10}" y="156" width="{tw-20}" height="80" rx="6" fill="none" stroke="{THEMES["dark"]["orange"]}"/>'
           f'<text class="m" x="{tx+22}" y="176" fill="{THEMES["dark"]["orange"]}" font-weight="600">Bash command</text>'
           f'<text class="m" x="{tx+22}" y="196" fill="#ffffff">go test ./... -count=1</text>'
           f'<text class="m" x="{tx+22}" y="222" fill="#c9d1d9">Do you want to proceed?  <tspan fill="#7ee787">❯ 1. Yes</tspan>  2. No</text>')
    add(g(a.show((2.2, 7.6)), box))
    tline(176, '<tspan fill="#7ee787">●</tspan> Bash(go test ./... -count=1)', on=7.75)
    tline(198, '<tspan fill="#8b949e">  ⎿</tspan>  ok   api/internal/handler  0.42s', color="#8b949e", on=8.2)
    tline(224, f'<tspan fill="#d2a8ff">●</tspan> {e(s["t_reply"])}', on=8.9)
    tline(254, f'<tspan fill="#8b949e">&gt;</tspan> {e(s["prompt2"])}', color="#ffffff", on=12.8)
    tline(278, f'<tspan fill="#d2a8ff">●</tspan> {e(s["t_next"])}', on=13.4)
    cursor = f'<rect x="{tx+14}" y="{th+ty-30}" width="8" height="14" fill="#c9d1d9"/>'
    add(f'<g class="blink tr">{cursor}</g>')

    rx0, ry0, rw, rh = 480, 150, 100, 120
    y_pipe = 210
    add(f'<path d="M{tx+tw+4} {y_pipe}H{rx0-4}M{rx0+rw+4} {y_pipe}H676" stroke="{c["muted"]}" stroke-dasharray="3 5" stroke-width="1.5"/>')

    def cipher(on, off, text):
        body = (f'<rect x="{rx0+8}" y="{ry0+rh+10}" width="{rw-16}" height="22" rx="6" fill="{c["chip"]}"/>'
                f'<text class="m" x="{rx0+rw/2}" y="{ry0+rh+25}" text-anchor="middle" fill="{c["muted"]}" font-size="11">{text}</text>')
        add(g(a.show((on, off)), body))

    lock = ('<rect x="-22" y="-11" width="44" height="22" rx="11" fill="{fill}"/>'
            '<path d="M-5 -3v-3a5 5 0 0 1 10 0v3" fill="none" stroke="#fff" stroke-width="1.8"/>'
            '<rect x="-8" y="-3" width="16" height="11" rx="2" fill="#fff"/>')

    def packet(points, fill):
        body = f'<g transform="translate(0 {y_pipe})">{lock.format(fill=fill)}</g>'
        add(f'<g class="a tr" style="animation-name:{a.move(points)}">{body}</g>')

    packet([(2.5, 384), (3.1, 530), (3.4, 530), (3.9, 676)], c["green"])
    cipher(2.9, 3.6, "7f3a·c91e…")
    packet([(6.4, 676), (6.9, 530), (7.1, 530), (7.6, 384)], c["green"])
    cipher(6.8, 7.3, "e15d·9a07…")
    packet([(11.5, 676), (12.0, 530), (12.2, 530), (12.7, 384)], c["green"])
    cipher(11.9, 12.4, "b62e·58d1…")

    # ---------- relay, drawn over the packets passing through it ----------
    add(f'<rect x="{rx0}" y="{ry0}" width="{rw}" height="{rh}" rx="12" fill="{c["relay"]}" stroke="{c["edge"]}" stroke-width="1.5"/>')
    add(f'<text class="h" x="{rx0+rw/2}" y="{ry0+28}" text-anchor="middle">{e(s["relay"])}</text>')
    add(f'<text class="s" x="{rx0+rw/2}" y="{ry0+48}" text-anchor="middle">{e(s["relay_sub"])}</text>')
    for i, y in enumerate((ry0 + 66, ry0 + 78, ry0 + 90)):
        add(f'<rect x="{rx0+18}" y="{y}" width="{rw-36}" height="6" rx="3" fill="{c["line"]}"/>')

    # ---------- phone ----------
    px, py, pw, ph = 680, 20, 260, 420
    cx = px + pw / 2
    add(f'<rect x="{px}" y="{py}" width="{pw}" height="{ph}" rx="34" fill="{c["card"]}" stroke="{c["frame"]}" stroke-width="5"/>')
    add(f'<text class="s" x="{px+30}" y="{py+28}" font-weight="600" fill="{c["fg"]}">9:41</text>')
    add(f'<rect x="{cx-36}" y="{py+12}" width="72" height="20" rx="10" fill="{c["frame"]}"/>')
    add(f'<text class="t" x="{cx}" y="{py+60}" text-anchor="middle" font-weight="600">{e(s["title"])}</text>')

    def status(on, off, color, word, transient=True):
        x0 = cx - (width(word, 12) + 11) / 2
        body = (f'<circle cx="{x0 + 3.5}" cy="{py+75}" r="3.5" fill="{color}"/>'
                f'<text class="s" x="{x0 + 11}" y="{py+79}">{e(word)}</text>')
        add(g(a.show((on, off)), body, transient))

    status(0.3, 4.0, c["blue"], s["working"])
    status(4.0, 6.3, c["orange"], s["waiting"])
    status(6.3, 9.3, c["blue"], s["working"])
    status(9.3, 11.5, c["green"], s["idle"])
    status(11.5, None, c["blue"], s["working"], transient=False)
    add(f'<path d="M{px+3} {py+92}H{px+pw-3}" stroke="{c["line"]}"/>')

    right = px + pw - 16

    def bubble(y, text, on, sub=None):
        w = 24 + width(text)
        w = min(w, pw - 50)
        body = (f'<rect x="{right-w}" y="{y}" width="{w}" height="30" rx="15" fill="{c["blue_soft"]}"/>'
                f'<text class="b" x="{right-11}" y="{y+20}" text-anchor="end">{e(text)}</text>')
        if sub:
            body += f'<text class="x" x="{right-6}" y="{y+45}" text-anchor="end">{e(sub)}</text>'
        add(g(a.show((on, None)), body, transient=False))

    left = px + 16
    bubble(py + 106, s["prompt1"], 1.2, s["from_term"])
    add(g(a.show((1.8, None)),
          f'<text class="m" x="{left}" y="{py+180}" fill="{c["fg2"]}"><tspan fill="{c["green"]}">✓</tspan> {e(s["edited"])}</text>',
          transient=False))
    add(g(a.show((6.3, None)),
          f'<text class="m" x="{left}" y="{py+206}" fill="{c["fg2"]}"><tspan fill="{c["green"]}">✓</tspan> go test ./...  '
          f'<tspan class="x" fill="{c["muted"]}">{e(s["allowed"])}</tspan></text>', transient=False))
    add(g(a.show((9.3, None)), f'<text class="b" x="{left}" y="{py+234}">{e(s["reply"])}</text>', transient=False))
    bubble(py + 256, s["prompt2"], 11.4)

    # approval docked above the composer
    dy = py + 248
    dock = (f'<rect x="{px+10}" y="{dy}" width="{pw-20}" height="114" rx="14" fill="{c["soft"]}" stroke="{c["line"]}"/>'
            f'<circle cx="{px+26}" cy="{dy+20}" r="4" fill="{c["orange"]}"/>'
            f'<text class="b" x="{px+36}" y="{dy+25}" font-weight="600">{e(s["run"])}</text>'
            f'<rect x="{px+20}" y="{dy+36}" width="{pw-40}" height="26" rx="6" fill="{c["card"]}"/>'
            f'<text class="m" x="{px+30}" y="{dy+53}" fill="{c["fg2"]}">go test ./... -count=1</text>'
            f'<rect x="{px+20}" y="{dy+72}" width="{(pw-50)/2}" height="32" rx="10" fill="{c["chip"]}"/>'
            f'<text class="b" x="{px+20+(pw-50)/4}" y="{dy+93}" text-anchor="middle" fill="{c["red"]}" font-weight="600">{e(s["deny"])}</text>'
            f'<rect x="{px+30+(pw-50)/2}" y="{dy+72}" width="{(pw-50)/2}" height="32" rx="10" fill="{c["blue_bg"]}"/>'
            f'<text class="b" x="{px+30+3*(pw-50)/4}" y="{dy+93}" text-anchor="middle" fill="#fff" font-weight="600">{e(s["allow"])}</text>')
    add(g(a.show((4.0, 6.3)), dock))
    tap_x, tap_y = px + 30 + 3 * (pw - 50) / 4, dy + 88
    add(f'<g transform="translate({tap_x} {tap_y})"><circle class="a tr p" r="22" fill="{c["blue"]}" style="animation-name:{a.pulse(5.7)}"/></g>')

    # composer
    cy0 = py + ph - 58
    add(f'<rect x="{px+12}" y="{cy0}" width="{pw-24}" height="38" rx="19" fill="{c["soft"]}" stroke="{c["line"]}"/>')
    add(g(a.show((0.0, 10.1), (11.4, None)), f'<text class="b" x="{px+28}" y="{cy0+24}" fill="{c["muted"]}">{e(s["message"])}</text>',
          transient=False))
    typed = (f'<text class="b" x="{px+28}" y="{cy0+24}">{e(s["prompt2"])}</text>'
             f'<rect class="a cover" x="{px+24}" y="{cy0+6}" width="{pw-90}" height="26" fill="{c["soft"]}" '
             f'style="animation-name:{a.wipe(10.2, 10.9)}"/>')
    add(g(a.show((10.1, 11.4)), typed))
    sx, sy = px + pw - 32, cy0 + 19
    add(f'<circle cx="{sx}" cy="{sy}" r="14" fill="{c["blue_bg"]}"/>'
        f'<path d="M{sx} {sy+6}V{sy-6}M{sx-5} {sy-1}L{sx} {sy-6}L{sx+5} {sy-1}" stroke="#fff" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"/>')
    add(f'<g transform="translate({sx} {sy})"><circle class="a tr p" r="20" fill="{c["blue"]}" style="animation-name:{a.pulse(11.1)}"/></g>')

    # ---------- caption under the computer ----------
    spans = [(0.0, 2.2), (2.2, 4.4), (4.4, 7.4), (7.4, 10.0), (10.0, None)]
    for i, (cap, span) in enumerate(zip(s["caps"], spans)):
        body = (f'<circle cx="{tx+12}" cy="{406}" r="12" fill="{c["blue_bg"]}"/>'
                f'<text class="b" x="{tx+12}" y="{411}" text-anchor="middle" fill="#fff" font-weight="600">{i+1}</text>'
                f'<text class="c" x="{tx+34}" y="{412}">{e(cap)}</text>')
        add(g(a.show(span), body, transient=i < 4))
    add(f'<text class="h" x="{px+pw/2}" y="{py+ph+18}" text-anchor="middle">{e(s["phone"])}</text>')

    frozen = ""
    if at is not None:
        frozen = f".a{{animation-play-state:paused!important;animation-delay:-{at}s!important}}"

    fonts = '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", Helvetica, Arial, sans-serif'
    mono = 'ui-monospace, SFMono-Regular, Menlo, Consolas, "PingFang SC", monospace'
    style = f"""
svg{{fill:{c["fg"]}}}
text{{font-family:{fonts}}}
.h{{font-size:15px;font-weight:600}}
.t{{font-size:15px}}
.b{{font-size:13px}}
.s{{font-size:12px;fill:{c["muted"]}}}
.x{{font-size:11px;fill:{c["muted"]}}}
.c{{font-size:16px;font-weight:500}}
.m{{font-family:{mono};font-size:12.5px}}
.a{{animation-duration:{T}s;animation-iteration-count:infinite;animation-timing-function:linear}}
.p{{transform-box:fill-box;transform-origin:center}}
.cover{{transform-box:fill-box;transform-origin:right}}
.blink{{animation:blink 1s steps(1) infinite}}
@keyframes blink{{50%{{opacity:0}}}}
{a.css()}
@media (prefers-reduced-motion:reduce){{.a,.blink{{animation:none!important}}.tr{{display:none}}}}
{frozen}
"""
    W, H = 960, 470
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W*scale:g}" height="{H*scale:g}" role="img" aria-label="{e(s["alt"])}">\n'
            f'<title>{e(s["alt"])}</title>\n<style>{style}</style>\n'
            f'<rect width="{W}" height="{H}" rx="16" fill="{c["page"]}"/>\n'
            + "\n".join(parts) + "\n</svg>\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--at", type=float, action="append", help="also write frames frozen at these seconds to /tmp")
    args = ap.parse_args()
    for lang in TEXT:
        for theme in THEMES:
            suffix = "" if lang == "en" else ".zh-CN"
            (HERE / f"demo-{theme}{suffix}.svg").write_text(build(lang, theme))
            for t in args.at or []:
                pathlib.Path(f"/tmp/ra-demo-{lang}-{theme}-{t}.svg").write_text(build(lang, theme, at=t, scale=0.75))


if __name__ == "__main__":
    main()
