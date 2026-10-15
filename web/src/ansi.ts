/** The colours the terminal reader draws terminal history with. The live
 * terminal is an xterm.js instance whose theme sets only background,
 * foreground and cursor, so its 16 ANSI colours are xterm.js's defaults;
 * the reader uses the same ones, so switching between the two looks like
 * one picture moving. Indices 16-255 follow the standard cube and grey
 * ramp. */
const PALETTE = [
  "#2e3436", "#cc0000", "#4e9a06", "#c4a000", "#3465a4", "#75507b", "#06989a", "#d3d7cf",
  "#555753", "#ef2929", "#8ae234", "#fce94f", "#729fcf", "#ad7fa8", "#34e2e2", "#eeeeec",
];

export const DEFAULT_FG = "#c9d1d9";
export const DEFAULT_BG = "#0d1117";

function rgb(r: number, g: number, b: number): string {
  return "#" + ((r << 16) | (g << 8) | b).toString(16).padStart(6, "0");
}

/** A palette index as a CSS colour. A bold run brightens the eight base
 * colours, as xterm.js does by default. */
export function ansiColor(index: number, bold = false): string {
  const i = bold && index >= 0 && index < 8 ? index + 8 : index;
  if (i >= 0 && i < 16) return PALETTE[i];
  if (i >= 16 && i < 232) {
    const ramp = [0x00, 0x5f, 0x87, 0xaf, 0xd7, 0xff];
    const n = i - 16;
    return rgb(ramp[Math.floor(n / 36)], ramp[Math.floor(n / 6) % 6], ramp[n % 6]);
  }
  if (i >= 232 && i <= 255) {
    const c = 8 + (i - 232) * 10;
    return rgb(c, c, c);
  }
  return DEFAULT_FG;
}
