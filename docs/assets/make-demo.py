#!/usr/bin/env python3
"""Generate the animated README demo (docs/assets/demo-*.svg).

Pictures, not prose: the computer, the relay and the phone, with almost no
labels. The loop opens on a pending permission request, so every frame is a
complete picture. Motion only changes state: the request crosses the relay as ciphertext, it
is approved on the phone, the same session carries on, and the next message
comes from the phone. Plain CSS animation, so GitHub plays it inside <img>;
with reduced motion the opening poster is shown.

    python3 docs/assets/make-demo.py            # write all four variants
    python3 docs/assets/make-demo.py --at 1.0   # also write frozen frames to /tmp for review
"""

import argparse
import pathlib
from xml.sax.saxutils import escape

T = 12.0      # loop length, seconds
RESET = 10.6  # what the loop added fades out and the pending request returns
FADE = 0.2
W, H = 880, 348

HERE = pathlib.Path(__file__).resolve().parent

THEMES = {
    "light": dict(page="#ffffff", fg="#16181c", fg2="#3a3f46", muted="#626970", line="#e6e8eb",
                  card="#ffffff", soft="#f5f5f7", chip="#eff0f2", frame="#1b1d21", edge="#d0d7de",
                  blue="#2563d9", blue_bg="#2f6feb", blue_soft="#edf3ff", orange="#e07a12",
                  green="#22a05a", red="#c62f2f", relay="#f6f8fa"),
    "dark": dict(page="#0d1117", fg="#e8eaed", fg2="#c9ced4", muted="#a0a7af", line="#2a2d32",
                 card="#17181b", soft="#1f2125", chip="#272a2f", frame="#3a3d42", edge="#30363d",
                 blue="#7aa7ff", blue_bg="#2f6feb", blue_soft="rgba(90,145,255,.16)", orange="#f0a04b",
                 green="#3fbf73", red="#ff7b7b", relay="#161b22"),
}

TEXT = {
    "en": dict(
        alt=("Animated overview: Claude Code runs natively in tmux on your computer and asks to run go test; "
             "the request is encrypted and crosses a relay you host, which sees only IDs, timestamps and sizes; "
             "you approve it on your phone; the same session carries on; the next message is sent from the phone. "),
        tmux="Your computer — tmux · ~/api",
        prompt1="Split out the handlers", prompt2="Also open a PR",
        edited="Edited 5 files  +84 −61",
        t_reply="All tests pass.", t_next="Creating a branch and opening the PR…",
        relay="Your relay", relay_sub="sees ciphertext only", lane="7f3a·c91e·04b2…",
        title="api · Claude Code", waiting="Needs approval", working="Running", idle="Idle",
        chat="Chat", term="Terminal", from_term="from the terminal",
        run="Run command", deny="Deny", allow="Allow", allowed="Allowed",
        message="Message",
    ),
    "zh": dict(
        alt=("动画概览：Claude Code 以原生形态跑在你电脑的 tmux 里，请求执行 go test；请求加密后经过你自己部署的中继，"
             "中继只看得到 ID、时间和大小；你在手机上允许；同一个会话接着运行；再从手机发出下一条消息。"),
        tmux="你的电脑 — tmux · ~/api",
        prompt1="把 handler 拆到单独的包里", prompt2="再开个 PR",
        edited="编辑 5 个文件  +84 −61",
        t_reply="测试全部通过。", t_next="正在新建分支并创建 PR…",
        relay="你的中继", relay_sub="只看得到密文", lane="7f3a·c91e·04b2…",
        title="api · Claude Code", waiting="等待审批", working="运行中", idle="空闲",
        chat="聊天", term="终端", from_term="来自终端",
        run="请求执行", deny="不允许", allow="允许", allowed="已允许",
        message="消息",
    ),
}


def width(text, size=13):
    """Rough rendered width: CJK glyphs are square, Latin about 0.55 em."""
    return sum(size if ord(ch) > 0x2e80 else size * 0.55 for ch in text)


def pct(t):
    return f"{max(0.0, min(100.0, t / T * 100)):.3f}%"


class Anim:
    """Collects one @keyframes rule per animated element."""

    def __init__(self):
        self.rules = []

    def _add(self, frames):
        name = f"k{len(self.rules) + 1}"
        self.rules.append((name, frames))
        return name

    def show(self, *spans):
        """Visible during the given (on, off) spans; on=0 or off=T join across the loop."""
        frames = [("0%", "opacity:1" if spans[0][0] <= 0 else "opacity:0")]
        for on, off in spans:
            if on > 0:
                frames += [(pct(on), "opacity:0"), (pct(on + FADE), "opacity:1")]
            if off < T:
                frames += [(pct(off), "opacity:1"), (pct(off + FADE), "opacity:0")]
        frames.append(("100%", "opacity:1" if spans[-1][1] >= T else "opacity:0"))
        return self._add(frames)

    def move(self, points):
        """points: (time, x) waypoints; visible only between the first and last."""
        (t0, x0), (t1, x1) = points[0], points[-1]
        frames = [("0%", f"opacity:0;transform:translateX({x0}px)"),
                  (pct(t0), f"opacity:0;transform:translateX({x0}px)")]
        frames += [(pct(t + (0.05 if i == 0 else 0)), f"opacity:1;transform:translateX({x}px)")
                   for i, (t, x) in enumerate(points)]
        frames += [(pct(t1 + 0.1), f"opacity:0;transform:translateX({x1}px)"),
                   ("100%", f"opacity:0;transform:translateX({x1}px)")]
        return self._add(frames)

    def pulse(self, t):
        """A tap: a circle that grows and fades."""
        return self._add([("0%", "opacity:0;transform:scale(.4)"), (pct(t), "opacity:0;transform:scale(.4)"),
                          (pct(t + 0.05), "opacity:.5;transform:scale(.5)"),
                          (pct(t + 0.5), "opacity:0;transform:scale(1.6)"), ("100%", "opacity:0;transform:scale(1.6)")])

    def css(self):
        return "\n".join(f"@keyframes {n}{{" + "".join(f"{k}{{{v}}}" for k, v in fr) + "}" for n, fr in self.rules)


def build(lang, theme, at=None, scale=1.0):
    c = THEMES[theme]
    s = TEXT[lang]
    a = Anim()
    e = escape
    parts = []
    add = parts.append

    def g(spans, body, post=True):
        """An element shown during spans. 'post' marks what the opening poster does not
        contain, hidden when motion is reduced."""
        cls = "a post" if post else "a"
        add(f'<g class="{cls}" style="animation-name:{a.show(*spans)}">{body}</g>')

    pending = [(0, 2.1), (RESET, T)]

    # ---------- computer: a tmux window running Claude Code ----------
    tx, ty, tw, th = 20, 16, 360, 316
    add(f'<rect x="{tx}" y="{ty}" width="{tw}" height="{th}" rx="10" fill="#0d1117" stroke="{c["edge"]}"/>')
    add(f'<path d="M{tx} {ty + 26}H{tx + tw}" stroke="#30363d"/>')
    for i, col in enumerate(["#ff5f57", "#febc2e", "#28c840"]):
        add(f'<circle cx="{tx + 16 + i * 15}" cy="{ty + 13}" r="4.5" fill="{col}"/>')
    add(f'<text x="{tx + tw / 2 + 20}" y="{ty + 17}" text-anchor="middle" fill="#8b949e" font-size="11.5">{e(s["tmux"])}</text>')

    def tline(y, text, color="#c9d1d9", spans=None):
        body = f'<text class="m" x="{tx + 12}" y="{y}" fill="{color}">{text}</text>'
        if spans:
            g(spans, body)
        else:
            add(body)

    tline(ty + 46, '<tspan fill="#7ee787">$</tspan> remote-agent claude')
    tline(ty + 68, f'<tspan fill="#8b949e">&gt;</tspan> {e(s["prompt1"])}', "#ffffff")
    tline(ty + 90, f'<tspan fill="#d2a8ff">●</tspan> {e(s["edited"])}')
    or_ = THEMES["dark"]["orange"]
    by = ty + 102
    box = (f'<rect x="{tx + 8}" y="{by}" width="{tw - 16}" height="80" rx="6" fill="none" stroke="{or_}"/>'
           f'<text class="m" x="{tx + 20}" y="{by + 20}" fill="{or_}" font-weight="600">Bash command</text>'
           f'<text class="m" x="{tx + 20}" y="{by + 41}" fill="#ffffff">go test ./... -count=1</text>'
           f'<text class="m" x="{tx + 20}" y="{by + 66}" fill="#c9d1d9">Do you want to proceed?  '
           f'<tspan fill="#7ee787">❯ 1. Yes</tspan>  2. No</text>')
    g([(0, 2.9), (RESET, T)], box, post=False)
    tline(by + 20, '<tspan fill="#7ee787">●</tspan> Bash(go test ./... -count=1)', spans=[(2.9, RESET)])
    tline(by + 42, '<tspan fill="#8b949e">  ⎿</tspan>  ok   api/internal/handler  0.42s', "#8b949e", [(3.3, RESET)])
    tline(by + 66, f'<tspan fill="#d2a8ff">●</tspan> {e(s["t_reply"])}', spans=[(3.7, RESET)])
    tline(by + 98, f'<tspan fill="#8b949e">&gt;</tspan> {e(s["prompt2"])}', "#ffffff", [(6.8, RESET)])
    tline(by + 120, f'<tspan fill="#d2a8ff">●</tspan> {e(s["t_next"])}', spans=[(7.3, RESET)])
    add(f'<rect class="blink post" x="{tx + 12}" y="{ty + th - 52}" width="8" height="14" fill="#c9d1d9"/>')
    # tmux status line: both CLIs side by side in one tmux
    sy = ty + th - 28
    add(f'<rect x="{tx + 1}" y="{sy}" width="{tw - 2}" height="20" fill="#2ea043"/>'
        f'<text class="m" x="{tx + 10}" y="{sy + 14}" fill="#0d1117">[api] 0:claude*  1:codex  2:zsh</text>')

    # ---------- relay ----------
    lane_y = 119
    rx0, ry0, rw, rh = 404, lane_y - 66, 172, 190
    add(f'<rect x="{rx0}" y="{ry0}" width="{rw}" height="{rh}" rx="12" fill="{c["relay"]}" stroke="{c["edge"]}" stroke-width="1.5"/>')
    add(f'<text class="h" x="{rx0 + rw / 2}" y="{ry0 + 26}" text-anchor="middle">{e(s["relay"])}</text>')
    add(f'<text class="s" x="{rx0 + rw / 2}" y="{ry0 + 44}" text-anchor="middle">{e(s["relay_sub"])}</text>')
    add(f'<rect x="{rx0 + 10}" y="{lane_y - 11}" width="{rw - 20}" height="22" rx="11" fill="{c["chip"]}"/>')
    add(f'<text class="x" x="{rx0 + rw / 2}" y="{lane_y + 4}" text-anchor="middle">{e(s["lane"])}</text>')
    add(f'<path d="M{tx + tw + 4} {lane_y}H{rx0 - 2}M{rx0 + rw + 2} {lane_y}H596" stroke="{c["muted"]}" '
        f'stroke-dasharray="3 5" stroke-width="1.5"/>')
    # what the relay stores: ciphertext it cannot read
    for i, blob in enumerate(["e15d 9a07 3fc8 b62e", "58d1 aa90 0c4f 71e3", "d208 6b1a 94ce 2f57", "3a6c f0b9 17e4 c85d"]):
        add(f'<text class="m" x="{rx0 + rw / 2}" y="{lane_y + 40 + i * 20}" text-anchor="middle" fill="{c["muted"]}" '
            f'opacity=".55">{blob}</text>')
    lock = ('<rect x="-20" y="-10" width="40" height="20" rx="10" fill="{fill}"/>'
            '<path d="M-4.5 -2.5v-2.5a4.5 4.5 0 0 1 9 0v2.5" fill="none" stroke="#fff" stroke-width="1.7"/>'
            '<rect x="-7" y="-2.5" width="14" height="9.5" rx="2" fill="#fff"/>')

    def packet(points, fill):
        body = f'<g transform="translate(0 {lane_y})">{lock.format(fill=fill)}</g>'
        add(f'<g class="a post" style="animation-name:{a.move(points)}">{body}</g>')

    mid = rx0 + rw / 2
    packet([(0.4, 384), (0.9, mid), (1.1, mid), (1.6, 596)], c["orange"])   # the request
    packet([(2.2, 596), (2.5, mid), (2.6, mid), (2.9, 384)], c["green"])    # the approval
    packet([(6.0, 596), (6.3, mid), (6.4, mid), (6.7, 384)], c["blue_bg"])  # the next message

    # ---------- phone ----------
    px, py, pw, ph = 600, 16, 260, 316
    cx = px + pw / 2
    add(f'<rect x="{px}" y="{py}" width="{pw}" height="{ph}" rx="28" fill="{c["card"]}" stroke="{c["frame"]}" stroke-width="5"/>')
    add(f'<text class="t" x="{cx}" y="{py + 32}" text-anchor="middle" font-weight="600">{e(s["title"])}</text>')

    def status(spans, color, word, post=True):
        x0 = cx - (width(word, 12) + 11) / 2
        g(spans, f'<circle cx="{x0 + 3.5}" cy="{py + 46}" r="3.5" fill="{color}"/>'
                 f'<text class="s" x="{x0 + 11}" y="{py + 50}">{e(word)}</text>', post)

    status(pending, c["orange"], s["waiting"], post=False)
    status([(2.1, 4.0)], c["blue"], s["working"])
    status([(4.0, 6.0)], c["green"], s["idle"])
    status([(6.0, RESET)], c["blue"], s["working"])
    # chat / terminal switch: the live terminal is one tap away
    seg_y = py + 60
    add(f'<rect x="{px + 16}" y="{seg_y}" width="{pw - 32}" height="24" rx="8" fill="{c["chip"]}"/>'
        f'<rect x="{px + 18}" y="{seg_y + 2}" width="{(pw - 36) / 2}" height="20" rx="6" fill="{c["card"]}"/>'
        f'<text class="x" x="{px + 18 + (pw - 36) / 4}" y="{seg_y + 16}" text-anchor="middle" font-weight="600" '
        f'style="fill:{c["fg"]}">{e(s["chat"])}</text>'
        f'<text class="x" x="{px + 18 + 3 * (pw - 36) / 4}" y="{seg_y + 16}" text-anchor="middle">{e(s["term"])}</text>')

    right, left = px + pw - 14, px + 14

    def bubble(y, text):
        w = 22 + width(text)
        return (f'<rect x="{right - w}" y="{y}" width="{w}" height="28" rx="14" fill="{c["blue_soft"]}"/>'
                f'<text class="b" x="{right - 11}" y="{y + 19}" text-anchor="end">{e(text)}</text>')

    add(bubble(py + 94, s["prompt1"]))
    add(f'<text class="x" x="{right - 4}" y="{py + 136}" text-anchor="end">{e(s["from_term"])}</text>')
    add(f'<text class="m" x="{left}" y="{py + 156}" fill="{c["fg2"]}"><tspan fill="{c["green"]}">✓</tspan> {e(s["edited"])}</text>')
    g([(2.1, RESET)], f'<text class="m" x="{left}" y="{py + 180}" fill="{c["fg2"]}"><tspan fill="{c["green"]}">✓</tspan> '
                      f'go test ./...  <tspan class="x">{e(s["allowed"])}</tspan></text>')
    g([(4.0, RESET)], f'<text class="b" x="{left}" y="{py + 204}">{e(s["t_reply"])}</text>')
    g([(6.0, RESET)], bubble(py + 216, s["prompt2"]))

    # the approval, docked above the composer
    dy = py + 168
    bw = (pw - 52) / 2
    dock = (f'<rect x="{px + 10}" y="{dy}" width="{pw - 20}" height="96" rx="14" fill="{c["soft"]}" stroke="{c["line"]}"/>'
            f'<circle cx="{px + 25}" cy="{dy + 17}" r="4" fill="{c["orange"]}"/>'
            f'<text class="b" x="{px + 35}" y="{dy + 22}" font-weight="600">{e(s["run"])}</text>'
            f'<rect x="{px + 20}" y="{dy + 30}" width="{pw - 40}" height="24" rx="6" fill="{c["card"]}"/>'
            f'<text class="m" x="{px + 28}" y="{dy + 46}" fill="{c["fg2"]}">go test ./... -count=1</text>'
            f'<rect x="{px + 20}" y="{dy + 60}" width="{bw}" height="28" rx="9" fill="{c["chip"]}"/>'
            f'<text class="b" x="{px + 20 + bw / 2}" y="{dy + 79}" text-anchor="middle" fill="{c["red"]}" font-weight="600">{e(s["deny"])}</text>'
            f'<rect x="{px + 32 + bw}" y="{dy + 60}" width="{bw}" height="28" rx="9" fill="{c["blue_bg"]}"/>'
            f'<text class="b" x="{px + 32 + 1.5 * bw}" y="{dy + 79}" text-anchor="middle" fill="#fff" font-weight="600">{e(s["allow"])}</text>')
    g(pending, dock, post=False)
    add(f'<g transform="translate({px + 32 + 1.5 * bw} {dy + 74})"><circle class="a post p" r="20" fill="{c["blue"]}" '
        f'style="animation-name:{a.pulse(1.8)}"/></g>')

    # composer
    cy0 = py + ph - 48
    add(f'<rect x="{px + 12}" y="{cy0}" width="{pw - 24}" height="34" rx="17" fill="{c["soft"]}" stroke="{c["line"]}"/>')
    g([(0, 5.0), (6.0, T)], f'<text class="b" x="{px + 28}" y="{cy0 + 22}" fill="{c["muted"]}">{e(s["message"])}</text>', post=False)
    g([(5.0, 6.0)], f'<text class="b" x="{px + 28}" y="{cy0 + 22}">{e(s["prompt2"])}</text>')
    sx, sy2 = px + pw - 30, cy0 + 17
    add(f'<circle cx="{sx}" cy="{sy2}" r="12" fill="{c["blue_bg"]}"/>'
        f'<path d="M{sx} {sy2 + 5}V{sy2 - 5}M{sx - 4.5} {sy2 - 0.5}L{sx} {sy2 - 5}L{sx + 4.5} {sy2 - 0.5}" stroke="#fff" '
        f'stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"/>')
    add(f'<g transform="translate({sx} {sy2})"><circle class="a post p" r="18" fill="{c["blue"]}" '
        f'style="animation-name:{a.pulse(5.8)}"/></g>')
    frozen = f".a,.blink{{animation-play-state:paused!important;animation-delay:-{at}s!important}}" if at is not None else ""
    fonts = '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", Helvetica, Arial, sans-serif'
    mono = 'ui-monospace, SFMono-Regular, Menlo, Consolas, "PingFang SC", monospace'
    style = f"""
svg{{fill:{c["fg"]}}}
text{{font-family:{fonts}}}
.h{{font-size:15px;font-weight:700}}
.t{{font-size:14px}}
.b{{font-size:13px}}
.s{{font-size:12px;fill:{c["muted"]}}}
.x{{font-size:11px;fill:{c["muted"]}}}
.m{{font-family:{mono};font-size:12px}}
.a{{animation-duration:{T}s;animation-iteration-count:infinite;animation-timing-function:linear}}
.p{{transform-box:fill-box;transform-origin:center}}
.blink{{animation:blink 1s steps(1) infinite}}
@keyframes blink{{50%{{opacity:0}}}}
{a.css()}
@media (prefers-reduced-motion:reduce){{.a,.blink{{animation:none!important}}.post{{display:none}}}}
{frozen}
"""
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W * scale:g}" height="{H * scale:g}" '
            f'role="img" aria-label="{e(s["alt"])}">\n<title>{e(s["alt"])}</title>\n<style>{style}</style>\n'
            f'<rect width="{W}" height="{H}" rx="16" fill="{c["page"]}"/>\n' + "\n".join(parts) + "\n</svg>\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--at", type=float, action="append", help="also write frames frozen at these seconds to /tmp")
    args = ap.parse_args()
    for lang in TEXT:
        for theme in THEMES:
            suffix = "" if lang == "en" else ".zh-CN"
            (HERE / f"demo-{theme}{suffix}.svg").write_text(build(lang, theme))
            for t in args.at or []:
                pathlib.Path(f"/tmp/ra-demo-{lang}-{theme}-{t}.svg").write_text(build(lang, theme, at=t, scale=0.8))


if __name__ == "__main__":
    main()
