/** East Asian wide and fullwidth code points, which take two cells in CSS. */
function isWide(code: number): boolean {
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
