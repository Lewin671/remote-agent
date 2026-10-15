/** One styled stretch of a terminal history line, as `capture-pane -e`
 * writes it. A colour is absent for the terminal default, a number for a
 * 256-colour palette index, a string for #rrggbb. */
export interface Run {
  text: string;
  fg?: number | string;
  bg?: number | string;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
  inverse?: boolean;
}

interface Style {
  fg: number | string | null;
  bg: number | string | null;
  bold: boolean;
  dim: boolean;
  italic: boolean;
  underline: boolean;
  inverse: boolean;
}

const plain: Style = { fg: null, bg: null, bold: false, dim: false, italic: false, underline: false, inverse: false };

function same(a: Style, b: Style): boolean {
  return a.fg === b.fg && a.bg === b.bg && a.bold === b.bold && a.dim === b.dim && a.italic === b.italic && a.underline === b.underline && a.inverse === b.inverse;
}

function hex(r: number, g: number, b: number): string {
  return "#" + ((r << 16) | (g << 8) | b).toString(16).padStart(6, "0");
}

/** Applies the parameters of one SGR sequence (the text between ESC [ and
 * m) to a style, ignoring the codes it does not know. */
function sgr(style: Style, params: string): Style {
  const s: Style = { ...style };
  const codes = params.split(";").map((p) => (p === "" ? 0 : Number(p)));
  for (let i = 0; i < codes.length; i++) {
    const n = codes[i];
    switch (n) {
      case 0: Object.assign(s, plain); break;
      case 1: s.bold = true; break;
      case 2: s.dim = true; break;
      case 3: s.italic = true; break;
      case 4: s.underline = true; break;
      case 7: s.inverse = true; break;
      case 22: s.bold = false; s.dim = false; break;
      case 23: s.italic = false; break;
      case 24: s.underline = false; break;
      case 27: s.inverse = false; break;
      case 39: s.fg = null; break;
      case 49: s.bg = null; break;
      case 38:
      case 48: {
        const key = n === 38 ? "fg" : "bg";
        if (codes[i + 1] === 5 && i + 2 < codes.length) { s[key] = codes[i + 2]; i += 2; }
        else if (codes[i + 1] === 2 && i + 4 < codes.length) { s[key] = hex(codes[i + 2], codes[i + 3], codes[i + 4]); i += 4; }
        break;
      }
      default:
        if (n >= 30 && n <= 37) s.fg = n - 30;
        else if (n >= 90 && n <= 97) s.fg = n - 90 + 8;
        else if (n >= 40 && n <= 47) s.bg = n - 40;
        else if (n >= 100 && n <= 107) s.bg = n - 100 + 8;
    }
  }
  return s;
}

/** Parses one line of `capture-pane -e` history into styled runs, starting
 * from the default style. Only SGR changes style; every other escape or
 * control sequence is dropped without printing anything. Adjacent runs of
 * the same style merge and empty runs are left out, as
 * testdata/terminal/history-sgr.json spells out. */
export function parseSgrLine(line: string): Run[] {
  const out: { text: string; style: Style }[] = [];
  let style = plain;
  let text = "";
  const flush = () => {
    if (!text) return;
    const last = out[out.length - 1];
    if (last && same(last.style, style)) last.text += text;
    else out.push({ text, style });
    text = "";
  };
  for (let i = 0; i < line.length; ) {
    const c = line.charCodeAt(i);
    if (c === 0x1b) {
      const kind = line[i + 1];
      if (kind === "[") {
        let end = i + 2;
        while (end < line.length && (line.charCodeAt(end) < 0x40 || line.charCodeAt(end) > 0x7e)) end++;
        if (end >= line.length) break; // truncated: nothing more is printed
        if (line[end] === "m") {
          flush();
          style = sgr(style, line.slice(i + 2, end));
        }
        i = end + 1;
      } else if (kind === "]") {
        // An OSC string, ended by BEL or ST, is dropped whole.
        let end = i + 2;
        while (end < line.length && line.charCodeAt(end) !== 0x07 && !(line.charCodeAt(end) === 0x1b && line[end + 1] === "\\")) end++;
        i = end >= line.length ? line.length : end + (line[end] === "\u0007" ? 1 : 2);
      } else {
        i += 2; // a two-character escape, dropped
      }
    } else if (c === 0x09) {
      text += line[i++]; // a tab is kept; other controls are dropped
    } else if (c < 0x20 || c === 0x7f) {
      i++;
    } else {
      text += line[i++];
    }
  }
  flush();
  return out.map(({ text, style }) => {
    const run: Run = { text };
    if (style.fg !== null) run.fg = style.fg;
    if (style.bg !== null) run.bg = style.bg;
    if (style.bold) run.bold = true;
    if (style.dim) run.dim = true;
    if (style.italic) run.italic = true;
    if (style.underline) run.underline = true;
    if (style.inverse) run.inverse = true;
    return run;
  });
}

/** East Asian wide and fullwidth code points, which take two cells. */
export function isWide(code: number): boolean {
  return (
    (code >= 0x1100 && code <= 0x115f) ||
    (code >= 0x2e80 && code <= 0x303e) ||
    (code >= 0x3041 && code <= 0x33ff) ||
    (code >= 0x3400 && code <= 0x4dbf) ||
    (code >= 0x4e00 && code <= 0x9fff) ||
    (code >= 0xa000 && code <= 0xa4cf) ||
    (code >= 0xac00 && code <= 0xd7a3) ||
    (code >= 0xf900 && code <= 0xfaff) ||
    (code >= 0xfe10 && code <= 0xfe19) ||
    (code >= 0xfe30 && code <= 0xfe6f) ||
    (code >= 0xff00 && code <= 0xff60) ||
    (code >= 0xffe0 && code <= 0xffe6) ||
    (code >= 0x1f300 && code <= 0x1faff) ||
    (code >= 0x20000 && code <= 0x3fffd)
  );
}

/** Splits a run's text into stretches of narrow (one cell) and wide (two
 * cells) characters, so a wide stretch can be given two `ch` per character
 * and line up with the live terminal's columns. */
export function widthSegments(text: string): { text: string; wide: boolean }[] {
  const out: { text: string; wide: boolean }[] = [];
  let wide: boolean | undefined;
  let from = 0;
  let at = 0;
  for (const ch of text) {
    const w = isWide(ch.codePointAt(0)!);
    if (w !== wide) {
      if (at > from) out.push({ text: text.slice(from, at), wide: wide! });
      wide = w;
      from = at;
    }
    at += ch.length;
  }
  if (at > from) out.push({ text: text.slice(from, at), wide: wide! });
  return out;
}
