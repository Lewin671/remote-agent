#!/usr/bin/env python3
"""Generate the animated README demo (docs/assets/demo-*.svg).

A terminal window running Claude Code in tmux, the relay, and an iPhone drawn
to its real proportions with the client's conversation screen
(docs/design/client-v53.html). The loop opens on a pending permission request,
so the first frame is a complete picture. Motion only changes state: the
request crosses the relay as ciphertext, it is approved on the phone, the
tests run on the computer and their results reach the phone, the phone opens
the live terminal of the same session, and the next message is sent from the
phone. Plain CSS animation, so GitHub plays it inside <img>; with reduced
motion the opening frame is shown.

    python3 docs/assets/make-demo.py              # write all four variants
    python3 docs/assets/make-demo.py --at 1 6     # also write frozen frames to /tmp for review
"""

import argparse
import pathlib
from xml.sax.saxutils import escape as e

T = 15.0      # loop length, seconds
RESET = 14.2  # what the loop added fades out and the pending request returns
FADE = 0.2
W, H = 1000, 600

HERE = pathlib.Path(__file__).resolve().parent

# Timeline
ALLOW = 2.0       # tap Allow on the phone
APPROVED = 2.4    # the request leaves the phone's dock
RAN = 2.8         # the terminal starts the command
DONE = 4.4        # the agent's reply
TERM_ON, TERM_OFF = 5.6, 8.4   # the phone shows the live terminal
TYPED = 9.0       # the next message is typed on the phone
SENT = 10.0
ECHO = 10.7       # it arrives in the terminal

# Phone colors follow docs/design/client-v53.html (:root and [data-theme="dark"]).
THEMES = {
    "light": dict(page="#ffffff", fg="#16181c", fg2="#3a3f46", muted="#626970", line="#e6e8eb",
                  card="#ffffff", soft="#f5f5f7", chip="#eff0f2", frame="#1b1d21", edge="#d0d7de",
                  blue="#2563d9", blue_bg="#2f6feb", orange="#e07a12", orange_ink="#8a4a06",
                  green="#22a05a", red="#c62f2f", relay="#f6f8fa", shadow=".14"),
    "dark": dict(page="#0d1117", fg="#e8eaed", fg2="#c9ced4", muted="#a0a7af", line="#2a2d32",
                 card="#17181b", soft="#1f2125", chip="#272a2f", frame="#3a3d42", edge="#30363d",
                 blue="#7aa7ff", blue_bg="#2f6feb", orange="#f0a04b", orange_ink="#f3bd82",
                 green="#3fbf73", red="#ff7b7b", relay="#161b22", shadow=".5"),
}

TEXT = {
    "en": dict(
        alt=("Animated overview: Claude Code runs natively in tmux on your computer and asks to run go test; "
             "the request is encrypted and crosses a relay you host, which sees only ciphertext; you approve it "
             "on your phone; the tests run on the computer and the results reach the phone; the phone opens the "
             "live terminal of the same session; the next message is sent from the phone and appears in the terminal."),
        win="~/api — remote-agent claude — tmux",
        t_prompt="Split the handlers into their own package",
        t_plan="I'll move the handlers into internal/handler and fix the imports.",
        t_split="Split done. Running the tests to check nothing broke.",
        t_pass="All tests pass. The handlers now live in internal/handler.",
        t_next="Also open a PR",
        relay="Your relay", relay_sub="ciphertext only",
        title="Refactor the API layer", waiting="Needs approval", working="Running", idle="Idle",
        chat="Chat", term="Terminal", source="MacBook terminal",
        me1=["Split the handlers into", "their own package"],
        edited="Edited 5 files  +84 −61",
        allowed="Allowed",
        ai1=["Split done. Running the tests to", "check nothing broke."],
        ai2=["All tests pass. The handlers now", "live in internal/handler."],
        me2=["Also open a PR"],
        run="Run command", left="8 min left", collapse="Collapse",
        desc="Run all tests after the split",
        deny="Deny", allow="Allow", message="Message",
        fitted="Fitted to this screen · 38×24", restore="Restore", keyboard="Keyboard",
        p_pass=["All tests pass. The handlers", "now live in internal/handler."],
    ),
    "zh": dict(
        alt=("动画概览：Claude Code 以原生形态跑在你电脑的 tmux 里，请求执行 go test；请求加密后经过你自己部署的中继，"
             "中继只看得到密文；你在手机上允许；测试在电脑上运行，结果回到手机；手机打开同一个会话的实时终端；"
             "再从手机发出下一条消息，它出现在电脑的终端里。"),
        win="~/api — remote-agent claude — tmux",
        t_prompt="把 handler 拆到单独的包里",
        t_plan="我把 handler 挪到 internal/handler，并修正 import。",
        t_split="拆分完成，跑一下测试确认没有破坏现有行为。",
        t_pass="测试全部通过，handler 现在在 internal/handler 里。",
        t_next="再开个 PR",
        relay="你的中继", relay_sub="只看得到密文",
        title="重构 API 层", waiting="等待审批", working="运行中", idle="空闲",
        chat="聊天", term="终端", source="MacBook 终端",
        me1=["把 handler 拆到单独的包里"],
        edited="编辑 5 个文件  +84 −61",
        allowed="已允许",
        ai1=["拆分完成，跑一下测试确认没有破坏", "现有行为。"],
        ai2=["测试全部通过，handler 现在在", "internal/handler 里。"],
        me2=["再开个 PR"],
        run="请求执行", left="剩 8 分钟", collapse="收起",
        desc="跑一遍全部测试，确认拆包没有破坏行为",
        deny="拒绝", allow="允许", message="消息",
        fitted="已适配这块屏幕 · 38×24", restore="还原", keyboard="键盘",
        p_pass=["测试全部通过，handler 现在在", "internal/handler 里。"],
    ),
}

ICONS = {  # lucide, 24×24
    "chevron-left": "m15 18-6-6 6-6", "chevron-up": "m18 15-6-6-6 6", "check": "M20 6 9 17l-5-5",
    "arrow-up": "m5 12 7-7 7 7M12 19V5", "arrow-down": "M12 5v14m7-7-7 7-7-7",
    "arrow-left": "m12 19-7-7 7-7M19 12H5", "arrow-right": "M5 12h14m-7-7 7 7-7 7",
    "plus": "M5 12h14M12 5v14", "corner-down-left": "M20 4v7a4 4 0 0 1-4 4H4m5-5-5 5 5 5",
    "keyboard": "M10 8h.01M12 12h.01M14 8h.01M16 12h.01M18 8h.01M6 8h.01M7 16h10M8 12h.01M4 4h16a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z",
}


def icon(name, x, y, size, color, sw=2):
    s = size / 24
    return (f'<path transform="translate({x - size / 2:g} {y - size / 2:g}) scale({s:g})" d="{ICONS[name]}" fill="none" '
            f'stroke="{color}" stroke-width="{sw}" stroke-linecap="round" stroke-linejoin="round"/>')


def width(text, size):
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
                          (pct(t + 0.05), "opacity:.45;transform:scale(.5)"),
                          (pct(t + 0.5), "opacity:0;transform:scale(1.6)"), ("100%", "opacity:0;transform:scale(1.6)")])

    def css(self):
        return "\n".join(f"@keyframes {n}{{" + "".join(f"{k}{{{v}}}" for k, v in fr) + "}" for n, fr in self.rules)


def build(lang, theme, at=None, scale=1.0):
    c = THEMES[theme]
    s = TEXT[lang]
    a = Anim()
    parts = []
    add = parts.append

    def g(spans, body, post=True):
        """An element shown during spans. 'post' marks what the opening frame does not
        contain, hidden when motion is reduced."""
        cls = "a post" if post else "a"
        add(f'<g class="{cls}" style="animation-name:{a.show(*spans)}">{body}</g>')

    def tap(x, y, t, color):
        add(f'<g transform="translate({x:g} {y:g})"><circle class="a post p" r="20" fill="{color}" '
            f'style="animation-name:{a.pulse(t)}"/></g>')

    pending = [(0, APPROVED), (RESET, T)]
    after = [(APPROVED, RESET)]

    # ================= computer: a terminal window running Claude Code in tmux =================
    tx, ty, tw, th = 16, 40, 550, 520
    add(f'<rect x="{tx}" y="{ty + 6}" width="{tw}" height="{th}" rx="12" fill="#000" opacity="{c["shadow"]}" filter="url(#blur)"/>')
    add(f'<rect x="{tx}" y="{ty}" width="{tw}" height="{th}" rx="11" fill="#0d1117" stroke="{c["edge"]}"/>')
    add(f'<path d="M{tx + 11} {ty + 0.5}H{tx + tw - 11}a10.5 10.5 0 0 1 10.5 10.5V{ty + 28}H{tx + 0.5}V{ty + 11}'
        f'a10.5 10.5 0 0 1 10.5 -10.5z" fill="#1c2128"/>')
    add(f'<path d="M{tx} {ty + 28.5}H{tx + tw}" stroke="#30363d"/>')
    for i, col in enumerate(["#ff5f57", "#febc2e", "#28c840"]):
        add(f'<circle cx="{tx + 18 + i * 20}" cy="{ty + 14}" r="6" fill="{col}"/>')
    add(f'<text x="{tx + tw / 2}" y="{ty + 18.5}" text-anchor="middle" fill="#9198a1" font-size="12.5" '
        f'font-weight="500">{e(s["win"])}</text>')

    lx = tx + 14
    row0 = ty + 52

    def row(k):
        return row0 + 17 * k

    def tline(k, body, spans=None, post=True, color="#c9d1d9"):
        el = f'<text class="m" x="{lx}" y="{row(k)}" fill="{color}" xml:space="preserve">{body}</text>'
        if spans:
            g(spans, el, post)
        else:
            add(el)

    def user_row(k, text, spans=None):
        band = f'<rect x="{tx + 6}" y="{row(k) - 12.5}" width="{tw - 12}" height="17" rx="2" fill="#262c36"/>'
        el = band + (f'<text class="m" x="{lx}" y="{row(k)}" fill="#e6edf3" xml:space="preserve">'
                     f'<tspan fill="#8b949e">&gt;</tspan> {e(text)}</text>')
        if spans:
            g(spans, el)
        else:
            add(el)

    dot = '<tspan fill="#e6edf3">⏺</tspan>'
    gdot = '<tspan fill="#3fb950">⏺</tspan>'
    elbow = '<tspan fill="#8b949e">  ⎿  </tspan>'
    user_row(0, s["t_prompt"])
    tline(2, f'{dot} {e(s["t_plan"])}')
    tline(4, f'{gdot} <tspan font-weight="700">Update</tspan>(internal/handler/user.go)')
    tline(5, f'{elbow}<tspan fill="#8b949e">Updated internal/handler/user.go with </tspan>'
             f'<tspan fill="#3fb950">31</tspan><tspan fill="#8b949e"> additions and </tspan>'
             f'<tspan fill="#f85149">22</tspan><tspan fill="#8b949e"> removals</tspan>')
    tline(7, f'{gdot} <tspan font-weight="700">Bash</tspan>(mkdir -p internal/handler)')
    tline(8, f'{elbow}<tspan fill="#8b949e">(No content)</tspan>')
    tline(10, f'{dot} {e(s["t_split"])}')

    # Claude Code's own permission prompt, replaced by the run once approved
    oc = "#e3a35b"
    prompt = (f'<path d="M{tx + 8} {row(12) - 5}H{tx + tw - 8}" stroke="{oc}" opacity=".7"/>'
              f'<text class="m" x="{lx}" y="{row(13)}" fill="{oc}" font-weight="700">Bash command</text>'
              f'<text class="m" x="{lx}" y="{row(15)}" fill="#e6edf3" xml:space="preserve">  go test ./... -count=1</text>'
              f'<text class="m" x="{lx}" y="{row(16)}" fill="#8b949e" xml:space="preserve">  Run all tests after the split</text>'
              f'<text class="m" x="{lx}" y="{row(18)}" fill="#e6edf3">Do you want to proceed?</text>'
              f'<text class="m" x="{lx}" y="{row(19)}" fill="#79c0ff" xml:space="preserve">❯ 1. Yes</text>'
              f'<text class="m" x="{lx}" y="{row(20)}" fill="#c9d1d9" xml:space="preserve">  2. Yes, and don\'t ask again for go test commands</text>'
              f'<text class="m" x="{lx}" y="{row(21)}" fill="#c9d1d9" xml:space="preserve">  3. No, and tell Claude what to do differently</text>')
    g([(0, RAN), (RESET, T)], prompt, post=False)
    tline(12, f'{gdot} <tspan font-weight="700">Bash</tspan>(go test ./... -count=1)', [(RAN, RESET)])
    for i, (pkg, secs) in enumerate([("internal/handler", "0.42s"), ("internal/store", "0.18s"), ("cmd/api", "0.09s")]):
        lead = elbow if i == 0 else '<tspan fill="#8b949e">     </tspan>'
        tline(13 + i, f'{lead}<tspan fill="#8b949e">ok    example.com/api/{pkg:<18}{secs}</tspan>',
              [(RAN + 0.4 + 0.3 * i, RESET)])
    tline(17, f'{dot} {e(s["t_pass"])}', [(DONE, RESET)])
    user_row(19, s["t_next"], [(ECHO, RESET)])
    tline(21, f'{gdot} <tspan font-weight="700">Bash</tspan>(git switch -c split-handlers)', [(ECHO + 0.9, RESET)])
    tline(23, '<tspan fill="#d77757">✶</tspan> <tspan fill="#d77757">Working…</tspan> '
              '<tspan fill="#8b949e">(esc to interrupt)</tspan>', [(ECHO + 0.5, RESET)])

    # the input box, hidden while the permission prompt is up
    iy0 = ty + th - 64
    box = (f'<path d="M{tx + 8} {iy0}H{tx + tw - 8}M{tx + 8} {iy0 + 26}H{tx + tw - 8}" stroke="#3d444d"/>'
           f'<text class="m" x="{lx}" y="{iy0 + 17}" fill="#8b949e">&gt;</text>'
           f'<rect class="blink" x="{lx + 15}" y="{iy0 + 6}" width="7.5" height="14" fill="#c9d1d9"/>')
    g([(RAN, RESET)], box)
    # tmux status line
    sy = ty + th - 30
    add(f'<rect x="{tx + 1}" y="{sy}" width="{tw - 2}" height="19" fill="#2ea043"/>'
        f'<text class="m" x="{lx}" y="{sy + 13.5}" fill="#0d1117" xml:space="preserve">[api] 0:claude*  1:codex  2:zsh</text>'
        f'<text class="m" x="{tx + tw - 14}" y="{sy + 13.5}" fill="#0d1117" text-anchor="end">"MacBook" 09:41</text>')
    add(f'<path d="M{tx + 0.5} {sy + 19}V{ty + th - 11}a10.5 10.5 0 0 0 10.5 10.5H{tx + tw - 11}'
        f'a10.5 10.5 0 0 0 10.5 -10.5V{sy + 19}z" fill="#0d1117"/>')

    # ================= relay =================
    lane_y = 300
    px0 = 716
    rx0, rw = 588, 106
    ry0, rh = lane_y - 66, 200
    add(f'<rect x="{rx0}" y="{ry0}" width="{rw}" height="{rh}" rx="12" fill="{c["relay"]}" stroke="{c["edge"]}" stroke-width="1.5"/>')
    add(f'<text class="h" x="{rx0 + rw / 2}" y="{ry0 + 26}" text-anchor="middle">{e(s["relay"])}</text>')
    add(f'<text class="x" x="{rx0 + rw / 2}" y="{ry0 + 44}" text-anchor="middle">{e(s["relay_sub"])}</text>')
    add(f'<path d="M{tx + tw + 4} {lane_y}H{rx0 - 2}M{rx0 + rw + 2} {lane_y}H{px0 - 2}" stroke="{c["muted"]}" '
        f'stroke-dasharray="3 5" stroke-width="1.5"/>')
    add(f'<path d="M{rx0 + 10} {lane_y}H{rx0 + rw - 10}" stroke="{c["edge"]}" stroke-width="1.5"/>')
    for i, blob in enumerate(["e15d 9a07", "3fc8 b62e", "58d1 aa90", "0c4f 71e3", "d208 6b1a"]):
        add(f'<text class="m" x="{rx0 + rw / 2}" y="{lane_y + 34 + i * 19}" text-anchor="middle" fill="{c["muted"]}" '
            f'opacity=".6">{blob}</text>')
    lock = ('<rect x="-19" y="-10" width="38" height="20" rx="10" fill="{fill}"/>'
            '<path d="M-4.5 -2.5v-2.5a4.5 4.5 0 0 1 9 0v2.5" fill="none" stroke="#fff" stroke-width="1.7"/>'
            '<rect x="-7" y="-2.5" width="14" height="9.5" rx="2" fill="#fff"/>')

    def packet(points, fill):
        body = f'<g transform="translate(0 {lane_y})">{lock.format(fill=fill)}</g>'
        add(f'<g class="a post" style="animation-name:{a.move(points)}">{body}</g>')

    mid = rx0 + rw / 2
    left_x, right_x = tx + tw + 20, px0 - 18
    packet([(0.3, left_x), (0.8, mid), (1.0, mid), (1.5, right_x)], c["orange"])               # the request
    packet([(ALLOW + 0.15, right_x), (ALLOW + 0.4, mid), (ALLOW + 0.5, mid), (RAN, left_x)], c["green"])  # the approval
    packet([(RAN + 0.9, left_x), (RAN + 1.2, mid), (RAN + 1.3, mid), (DONE - 0.4, right_x)], c["muted"])  # results
    packet([(SENT + 0.1, right_x), (SENT + 0.35, mid), (SENT + 0.45, mid), (ECHO, left_x)], c["blue_bg"])  # message

    # ================= phone: iPhone proportions, the conversation screen =================
    ps = 0.84
    add(f'<rect x="{px0 + 6}" y="{23 + 10}" width="{320 * ps - 12}" height="{660 * ps - 8}" rx="34" fill="#000" '
        f'opacity="{c["shadow"]}" filter="url(#blur)"/>')
    add(f'<g transform="translate({px0} 23) scale({ps})">')
    # body and screen, in the design draft's 320×660 coordinates
    add(f'<rect width="320" height="660" rx="48" fill="{c["frame"]}"/>')
    add(f'<rect x="8" y="8" width="304" height="644" rx="40" fill="{c["card"]}"/>')
    ix, iy, iw = 8, 8, 304
    L, R = ix + 14, ix + iw - 14

    def status_bar(color):
        out = f'<text x="{ix + 30}" y="{iy + 26}" font-size="14" font-weight="600" fill="{color}">9:41</text>'
        bx = ix + iw - 30
        for i in range(4):
            h = 4 + i * 2
            out += f'<rect x="{bx - 46 + i * 4.5}" y="{iy + 26 - h}" width="3" height="{h}" rx="1" fill="{color}"/>'
        out += (f'<rect x="{bx - 22}" y="{iy + 15}" width="22" height="11" rx="3.5" fill="none" stroke="{color}" opacity=".45"/>'
                f'<rect x="{bx - 20}" y="{iy + 17}" width="15" height="7" rx="2" fill="{color}"/>'
                f'<path d="M{bx + 1.5} {iy + 18.5}v4" stroke="{color}" stroke-width="1.5" stroke-linecap="round" opacity=".45"/>')
        return out

    def navbar(fg, sub, accent):
        return (icon("chevron-left", ix + 24, iy + 61, 24, accent) +
                f'<text x="{ix + iw / 2}" y="{iy + 58}" text-anchor="middle" font-size="15" font-weight="600" fill="{fg}">'
                f'{e(s["title"])}</text>' +
                ''.join(f'<circle cx="{ix + iw - 32 + i * 7}" cy="{iy + 61}" r="1.7" fill="{accent}"/>' for i in range(3)))

    def nav_status(spans, color, word, sub_color, post=True):
        w = width(word, 12) + 11
        x0 = ix + iw / 2 - w / 2
        g(spans, f'<circle cx="{x0 + 3.5}" cy="{iy + 72}" r="3.5" fill="{color}"/>'
                 f'<text x="{x0 + 11}" y="{iy + 76}" font-size="12" fill="{sub_color}">{e(word)}</text>', post)

    def seg(active, fg, muted, line, under):
        y0, y1 = iy + 88, iy + 130
        mid_x = ix + iw / 2
        cols = [(s["chat"], (L + mid_x) / 2), (s["term"], (mid_x + R) / 2)]
        out = f'<path d="M{L} {y1 + 0.5}H{R}" stroke="{line}"/>'
        for i, (word, cx) in enumerate(cols):
            on = i == active
            out += (f'<text x="{cx}" y="{(y0 + y1) / 2 + 4.5}" text-anchor="middle" font-size="13" '
                    f'font-weight="{600 if on else 400}" fill="{fg if on else muted}">{e(word)}</text>')
        x_on = (L, mid_x) if active == 0 else (mid_x, R)
        out += f'<rect x="{x_on[0]}" y="{y1 - 1.5}" width="{x_on[1] - x_on[0]}" height="2" fill="{under}"/>'
        return out

    def island():
        return f'<rect x="{ix + iw / 2 - 46}" y="{iy + 10}" width="92" height="26" rx="13" fill="#000"/>'

    # --- chat view ---
    add(status_bar(c["fg"]))
    add(navbar(c["fg"], c["muted"], c["blue"]))
    nav_status(pending, c["orange"], s["waiting"], c["muted"], post=False)
    nav_status([(APPROVED, DONE)], c["blue"], s["working"], c["muted"])
    nav_status([(DONE, SENT)], c["green"], s["idle"], c["muted"])
    nav_status([(SENT, RESET)], c["blue"], s["working"], c["muted"])
    add(seg(0, c["fg"], c["muted"], c["line"], c["fg2"]))

    def bubble(y, lines, label=None):
        lh = 21
        w = max(width(t, 15) for t in lines) + 24
        h = 16 + lh * len(lines)
        x1 = R
        x0 = x1 - w
        out = (f'<path d="M{x0 + 18} {y}H{x1 - 18}a18 18 0 0 1 18 18V{y + h - 5}a5 5 0 0 1 -5 5H{x0 + 18}'
               f'a18 18 0 0 1 -18 -18V{y + 18}a18 18 0 0 1 18 -18z" fill="{c["soft"]}"/>')
        for i, t in enumerate(lines):
            out += f'<text x="{x0 + 12}" y="{y + 23 + i * lh}" font-size="15" fill="{c["fg"]}">{e(t)}</text>'
        if label:
            out += f'<text x="{x1}" y="{y + h + 15}" text-anchor="end" font-size="12" fill="{c["muted"]}">{e(label)}</text>'
        return out

    def tool_row(y, text, right=None, spin=False):
        out = (f'<g class="spin" style="transform-origin:{L + 7}px {y}px"><circle cx="{L + 7}" cy="{y}" r="4.5" fill="none" '
               f'stroke="{c["blue"]}" stroke-width="1.5" stroke-dasharray="21 7"/></g>' if spin
               else icon("check", L + 7, y, 14, c["muted"]))
        out += f'<text class="mc" x="{L + 22}" y="{y + 4}" fill="{c["fg2"]}" xml:space="preserve">{e(text)}</text>'
        if right:
            out += f'<text x="{R}" y="{y + 4.5}" text-anchor="end" font-size="13" fill="{c["muted"]}">{e(right)}</text>'
        return out

    def ai(y, lines):
        return "".join(f'<text x="{L}" y="{y + i * 24}" font-size="15" fill="{c["fg"]}">{e(t)}</text>'
                       for i, t in enumerate(lines))

    y = iy + 146
    add(bubble(y, s["me1"], s["source"]))
    y += 16 + 21 * len(s["me1"]) + 32
    add(tool_row(y, s["edited"]))
    y += 24
    add(tool_row(y, "mkdir -p internal/handler", s["allowed"]))
    y += 34
    add(ai(y, s["ai1"]))
    y += 24 * (len(s["ai1"]) - 1) + 30
    g(after, tool_row(y, "go test ./... -count=1", s["allowed"]))
    y += 34
    g([(DONE, RESET)], ai(y, s["ai2"]))
    y += 24 * (len(s["ai2"]) - 1) + 22
    g([(SENT, RESET)], bubble(y, s["me2"]))
    y += 16 + 21 * len(s["me2"]) + 26
    g([(ECHO + 1.1, RESET)], tool_row(y, "git switch -c split-handlers", spin=True))

    # composer and home indicator
    cy0 = iy + 574
    add(f'<rect x="{ix + 10}" y="{cy0}" width="{iw - 20}" height="50" rx="25" fill="{c["card"]}" stroke="{c["line"]}"/>')
    add(icon("plus", ix + 10 + 25, cy0 + 25, 20, c["fg2"]))
    g([(0, TYPED), (SENT, T)], f'<text x="{ix + 56}" y="{cy0 + 30}" font-size="15" fill="{c["muted"]}">{e(s["message"])}</text>',
      post=False)
    g([(TYPED, SENT)], f'<text x="{ix + 56}" y="{cy0 + 30}" font-size="15" fill="{c["fg"]}">{e(s["me2"][0])}</text>')
    sx, sy2 = ix + iw - 10 - 25, cy0 + 25
    send = f'<circle cx="{sx}" cy="{sy2}" r="16" fill="{c["blue_bg"]}"/>' + icon("arrow-up", sx, sy2, 16, "#fff")
    g([(0, TYPED), (SENT, T)], f'<g opacity=".4">{send}</g>', post=False)
    g([(TYPED, SENT)], send)
    tap(sx, sy2, SENT - 0.2, c["fg"])
    add(f'<rect x="{ix + iw / 2 - 60}" y="{iy + 634}" width="120" height="4.5" rx="2.25" fill="{c["fg"]}" opacity=".85"/>')

    # the request, docked above the composer
    dy = cy0 - 4 - 172
    bw = (iw - 28 - 8) / 2
    dock = (f'<rect x="{ix}" y="{dy}" width="{iw}" height="172" fill="{c["card"]}"/>'
            f'<path d="M{L} {dy + 0.5}H{R}" stroke="{c["line"]}"/>'
            f'<circle cx="{L + 3.5}" cy="{dy + 22}" r="3.5" fill="{c["orange_ink"]}"/>'
            f'<text x="{L + 13}" y="{dy + 26.5}" font-size="13" fill="{c["fg2"]}">{e(s["run"])}</text>'
            f'<text x="{L + 19 + width(s["run"], 13)}" y="{dy + 26.5}" font-size="13" fill="{c["muted"]}">{e(s["left"])}</text>'
            f'<text x="{R - 16}" y="{dy + 26.5}" text-anchor="end" font-size="13" fill="{c["blue"]}">{e(s["collapse"])}</text>'
            + icon("chevron-up", R - 6, dy + 22, 14, c["blue"]) +
            f'<rect x="{L}" y="{dy + 46}" width="{R - L}" height="34" rx="6" fill="{c["soft"]}"/>'
            f'<text class="mc" x="{L + 10}" y="{dy + 67}" fill="{c["fg2"]}">go test ./... -count=1</text>'
            f'<text x="{L}" y="{dy + 102}" font-size="13" fill="{c["fg2"]}">{e(s["desc"])}</text>'
            f'<rect x="{L}" y="{dy + 118}" width="{bw}" height="44" rx="10" fill="{c["chip"]}"/>'
            f'<text x="{L + bw / 2}" y="{dy + 145}" text-anchor="middle" font-size="15" font-weight="600" fill="{c["red"]}">{e(s["deny"])}</text>'
            f'<rect x="{L + bw + 8}" y="{dy + 118}" width="{bw}" height="44" rx="10" fill="{c["blue_bg"]}"/>'
            f'<text x="{L + 1.5 * bw + 8}" y="{dy + 145}" text-anchor="middle" font-size="15" font-weight="600" fill="#fff">{e(s["allow"])}</text>')
    g(pending, dock, post=False)
    tap(L + 1.5 * bw + 8, dy + 140, ALLOW, c["fg"])
    tap((ix + iw / 2 + R) / 2, iy + 109, TERM_ON - 0.2, c["fg"])

    # --- terminal view of the same session, fitted to the phone ---
    tb = "#0d1117"
    term = [f'<rect x="{ix}" y="{iy}" width="{iw}" height="644" rx="40" fill="{tb}"/>',
            status_bar("#e6edf3"), navbar("#e6edf3", "#9da7b3", "#79c0ff"),
            f'<circle cx="{ix + iw / 2 - (width(s["idle"], 12) + 11) / 2 + 3.5}" cy="{iy + 72}" r="3.5" fill="#3fbf73"/>'
            f'<text x="{ix + iw / 2 - (width(s["idle"], 12) + 11) / 2 + 11}" y="{iy + 76}" font-size="12" fill="#9da7b3">'
            f'{e(s["idle"])}</text>',
            seg(1, "#e6edf3", "#9da7b3", "#30363d", "#e6edf3"),
            f'<rect x="{ix}" y="{iy + 134}" width="{iw}" height="40" fill="#161b22"/>'
            f'<text x="{L}" y="{iy + 158}" font-size="12" fill="#9da7b3">{e(s["fitted"])}</text>'
            f'<text x="{R}" y="{iy + 158}" text-anchor="end" font-size="13" font-weight="600" fill="#79c0ff">{e(s["restore"])}</text>']
    ty0 = iy + 196

    def prow(k, body, color="#c9d1d9"):
        return (f'<text class="mc" x="{ix + 10}" y="{ty0 + k * 18}" fill="{color}" xml:space="preserve">{body}</text>')

    term += [prow(0, f'{gdot} <tspan font-weight="700">Bash</tspan>(go test ./... -count=1)'),
             prow(1, f'{elbow}<tspan fill="#8b949e">ok  …/internal/handler 0.42s</tspan>'),
             prow(2, '<tspan fill="#8b949e">     ok  …/internal/store   0.18s</tspan>'),
             prow(3, '<tspan fill="#8b949e">     ok  …/cmd/api          0.09s</tspan>'),
             prow(5, f'{dot} {e(s["p_pass"][0])}'),
             prow(6, f'  {e(s["p_pass"][1])}'),
             f'<path d="M{ix + 8} {ty0 + 8 * 18 - 6}H{ix + iw - 8}M{ix + 8} {ty0 + 9 * 18 + 6}H{ix + iw - 8}" stroke="#3d444d"/>',
             prow(9, '<tspan fill="#8b949e">&gt;</tspan>'),
             f'<rect x="{ix + 26}" y="{ty0 + 9 * 18 - 11}" width="7" height="14" fill="#c9d1d9"/>']
    ky = iy + 644 - 128
    term += [f'<rect x="{ix}" y="{ky - 22}" width="{iw}" height="19" fill="#2ea043"/>'
             f'<text class="mc" x="{ix + 10}" y="{ky - 8.5}" fill="#0d1117" xml:space="preserve">[api] 0:claude*  1:codex</text>',
             f'<path d="M{ix} {ky}H{ix + iw}V{iy + 604}a40 40 0 0 1 -40 40H{ix + 40}a40 40 0 0 1 -40 -40z" fill="#161b22"/>']
    kx0, kgap = ix + 8, 5
    kw6 = (iw - 16 - 5 * kgap) / 6
    for i, lab in enumerate(["Esc", "arrow-left", "arrow-up", "arrow-down", "arrow-right", "corner-down-left"]):
        x = kx0 + i * (kw6 + kgap)
        term.append(f'<rect x="{x}" y="{ky + 8}" width="{kw6}" height="42" rx="7" fill="#2d333b"/>')
        term.append(f'<text x="{x + kw6 / 2}" y="{ky + 33}" text-anchor="middle" font-size="13" fill="#e6edf3">{lab}</text>'
                    if lab == "Esc" else icon(lab, x + kw6 / 2, ky + 29, 15, "#e6edf3"))
    kw3 = (iw - 16 - 2 * kgap) / 3
    for i, lab in enumerate(["Tab", "Ctrl+C", s["keyboard"]]):
        x = kx0 + i * (kw3 + kgap)
        term.append(f'<rect x="{x}" y="{ky + 55}" width="{kw3}" height="42" rx="7" fill="#2d333b"/>')
        if i == 2:
            tw2 = width(lab, 13)
            term.append(icon("keyboard", x + kw3 / 2 - tw2 / 2 - 4, ky + 76, 14, "#e6edf3", 1.8))
            term.append(f'<text x="{x + kw3 / 2 + 8}" y="{ky + 80.5}" text-anchor="middle" font-size="13" fill="#e6edf3">{e(lab)}</text>')
        else:
            term.append(f'<text x="{x + kw3 / 2}" y="{ky + 80.5}" text-anchor="middle" font-size="13" fill="#e6edf3">{lab}</text>')
    term.append(f'<rect x="{ix + iw / 2 - 60}" y="{iy + 634}" width="120" height="4.5" rx="2.25" fill="#e6edf3" opacity=".85"/>')
    g([(TERM_ON, TERM_OFF)], "".join(term))
    tap((L + ix + iw / 2) / 2, iy + 109, TERM_OFF - 0.2, "#e6edf3")

    add(island())
    add('</g>')

    frozen = f".a,.blink,.spin{{animation-play-state:paused!important;animation-delay:-{at}s!important}}" if at is not None else ""
    fonts = '-apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", Helvetica, Arial, sans-serif'
    mono = 'ui-monospace, SFMono-Regular, Menlo, Consolas, "PingFang SC", monospace'
    style = f"""
svg{{fill:{c["fg"]}}}
text{{font-family:{fonts}}}
.h{{font-size:15px;font-weight:600}}
.x{{font-size:12px;fill:{c["muted"]}}}
.m{{font-family:{mono};font-size:12px}}
.mc{{font-family:{mono};font-size:12.5px}}
.a{{animation-duration:{T}s;animation-iteration-count:infinite;animation-timing-function:linear}}
.p{{transform-box:fill-box;transform-origin:center}}
.blink{{animation:blink 1s steps(1) infinite}}
.spin{{animation:spin .9s linear infinite}}
@keyframes blink{{50%{{opacity:0}}}}
@keyframes spin{{to{{transform:rotate(360deg)}}}}
{a.css()}
@media (prefers-reduced-motion:reduce){{.a,.blink,.spin{{animation:none!important}}.post{{display:none}}}}
{frozen}
"""
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W * scale:g}" height="{H * scale:g}" '
            f'role="img" aria-label="{e(s["alt"])}">\n<title>{e(s["alt"])}</title>\n<style>{style}</style>\n'
            f'<defs><filter id="blur" x="-20%" y="-20%" width="140%" height="140%"><feGaussianBlur stdDeviation="10"/></filter></defs>\n'
            f'<rect width="{W}" height="{H}" fill="{c["page"]}"/>\n' + "\n".join(parts) + "\n</svg>\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--at", type=float, nargs="*", help="also write frames frozen at these seconds to /tmp")
    args = ap.parse_args()
    for lang in TEXT:
        for theme in THEMES:
            suffix = "" if lang == "en" else ".zh-CN"
            (HERE / f"demo-{theme}{suffix}.svg").write_text(build(lang, theme))
            for t in args.at or []:
                pathlib.Path(f"/tmp/ra-demo-{lang}-{theme}-{t:g}.svg").write_text(build(lang, theme, at=t))


if __name__ == "__main__":
    main()
