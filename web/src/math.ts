// Conservative LaTeX fallback, mirrored by RAMarkdown/Math.swift and ui/Math.kt.
// Unsupported syntax returns the entire source, never a partially simplified formula.
const symbols: Record<string, string> = {
  "times": "×",
  "cdot": "·",
  "div": "÷",
  "pm": "±",
  "le": "≤",
  "leq": "≤",
  "ge": "≥",
  "geq": "≥",
  "ne": "≠",
  "neq": "≠",
  "approx": "≈",
  "to": "→",
  "rightarrow": "→",
  "Rightarrow": "⇒",
  "infty": "∞",
  "sum": "∑",
  "prod": "∏",
  "alpha": "α",
  "beta": "β",
  "gamma": "γ",
  "delta": "δ",
  "epsilon": "ε",
  "zeta": "ζ",
  "eta": "η",
  "theta": "θ",
  "iota": "ι",
  "kappa": "κ",
  "lambda": "λ",
  "mu": "μ",
  "nu": "ν",
  "xi": "ξ",
  "omicron": "ο",
  "pi": "π",
  "rho": "ρ",
  "sigma": "σ",
  "tau": "τ",
  "upsilon": "υ",
  "phi": "φ",
  "chi": "χ",
  "psi": "ψ",
  "omega": "ω",
  "Gamma": "Γ",
  "Delta": "Δ",
  "Theta": "Θ",
  "Lambda": "Λ",
  "Xi": "Ξ",
  "Pi": "Π",
  "Sigma": "Σ",
  "Upsilon": "Υ",
  "Phi": "Φ",
  "Psi": "Ψ",
  "Omega": "Ω",
  "varepsilon": "ϵ",
  "vartheta": "ϑ",
  "varpi": "ϖ",
  "varrho": "ϱ",
  "varsigma": "ς",
  "varphi": "ϕ",
  "%": "%",
  "{": "{",
  "}": "}",
  "_": "_",
  "$": "$",
  "!": "",
  ",": " ",
  ";": " ",
  ":": " ",
  " ": " ",
  "quad": " ",
  "qquad": " ",
  "enspace": " ",
  "thinspace": " ",
  "left": "",
  "right": "",
};
const formats = new Set(["text", "textrm", "textnormal", "textbf", "textit", "mathrm", "mathbf", "mathit", "mathsf", "mathtt", "mathnormal", "operatorname"]);
const superscript: Record<string, string> = {"0": "⁰", "1": "¹", "2": "²", "3": "³", "4": "⁴", "5": "⁵", "6": "⁶", "7": "⁷", "8": "⁸", "9": "⁹", "+": "⁺", "-": "⁻", "=": "⁼", "(": "⁽", ")": "⁾", "i": "ⁱ", "n": "ⁿ", "a": "ᵃ", "b": "ᵇ", "c": "ᶜ", "d": "ᵈ", "e": "ᵉ", "f": "ᶠ", "g": "ᵍ", "h": "ʰ", "j": "ʲ", "k": "ᵏ", "l": "ˡ", "m": "ᵐ", "o": "ᵒ", "p": "ᵖ", "r": "ʳ", "s": "ˢ", "t": "ᵗ", "u": "ᵘ", "v": "ᵛ", "w": "ʷ", "x": "ˣ", "y": "ʸ", "z": "ᶻ", "A": "ᴬ", "B": "ᴮ", "D": "ᴰ", "E": "ᴱ", "G": "ᴳ", "H": "ᴴ", "I": "ᴵ", "J": "ᴶ", "K": "ᴷ", "L": "ᴸ", "M": "ᴹ", "N": "ᴺ", "O": "ᴼ", "P": "ᴾ", "R": "ᴿ", "T": "ᵀ", "U": "ᵁ", "V": "ⱽ", "W": "ᵂ", "−": "⁻"};
const subscript: Record<string, string> = {"0": "₀", "1": "₁", "2": "₂", "3": "₃", "4": "₄", "5": "₅", "6": "₆", "7": "₇", "8": "₈", "9": "₉", "+": "₊", "-": "₋", "=": "₌", "(": "₍", ")": "₎", "a": "ₐ", "e": "ₑ", "h": "ₕ", "i": "ᵢ", "j": "ⱼ", "k": "ₖ", "l": "ₗ", "m": "ₘ", "n": "ₙ", "o": "ₒ", "p": "ₚ", "r": "ᵣ", "s": "ₛ", "t": "ₜ", "u": "ᵤ", "v": "ᵥ", "x": "ₓ", "−": "₋"};
const grouped = (v: string) => /[+\-*/=<>×·÷±≤≥≠≈→⇒^_\s]/u.test(v) ? `(${v})` : v;

export function mathText(source: string): string {
  let i = 0, valid = true;
  const skip = () => { while (i < source.length && /\s/u.test(source[i])) i++; };
  const argument = (depth: number, required = true): string => {
    skip();
    if (source[i] === "{") { i++; return sequence(depth + 1, true); }
    if (required || i >= source.length || "}^_".includes(source[i])) { valid = false; return ""; }
    return atom(depth + 1);
  };
  const atom = (depth: number): string => {
    if (depth > 32) { valid = false; return ""; }
    const c = source[i++];
    if (c === "{") return sequence(depth + 1, true);
    if (c === "\\") {
      const start = i;
      while (i < source.length && /[A-Za-z]/.test(source[i])) i++;
      if (i === start && i < source.length) i++;
      const command = source.slice(start, i);
      if (formats.has(command)) return argument(depth);
      if (["frac", "dfrac", "tfrac"].includes(command)) {
        const a = argument(depth), b = argument(depth);
        return `${grouped(a)}/${grouped(b)}`;
      }
      if (command === "sqrt") return "√" + grouped(argument(depth));
      if (Object.hasOwn(symbols, command)) return symbols[command];
      valid = false;
      return "";
    }
    if ("}&#$".includes(c)) valid = false;
    return c;
  };
  const sequence = (depth: number, group = false): string => {
    const out: string[] = [];
    while (valid && i < source.length && source[i] !== "}") {
      if (source[i] === "^" || source[i] === "_") {
        const mark = source[i++], value = argument(depth, false);
        const map = mark === "^" ? superscript : subscript;
        const chars = [...value];
        out.push(chars.length && chars.every(c => Object.hasOwn(map, c)) ? chars.map(c => map[c]).join("") : mark + grouped(value));
      } else out.push(atom(depth));
    }
    if (group) { if (source[i] === "}") i++; else valid = false; }
    return out.join("");
  };
  const result = sequence(0);
  return valid && i === source.length ? result.replace(/\s+/gu, " ").trim() : source;
}

export interface MathRange { start: number; end: number; source: string }

/** Index closing delimiters and matching code runs once, including failed openers. */
export function mathRanges(text: string, singleLineCode = false): MathRange[] {
  const tokens: { at: number; value: string; escaped: boolean; line: number }[] = [];
  let slashes = 0, line = 0;
  for (let i = 0; i < text.length; i++) {
    const pair = text.slice(i, i + 2);
    if (text[i] === "`") {
      let end = i + 1;
      while (text[end] === "`") end++;
      tokens.push({ at: i, value: text.slice(i, end), escaped: slashes % 2 === 1, line });
      i = end - 1;
    } else if (pair === "$$" || pair === "\\[" || pair === "\\]" || pair === "\\(" || pair === "\\)") {
      tokens.push({ at: i, value: pair, escaped: slashes % 2 === 1, line });
      i++;
    }
    if (text[i] === "\n") line++;
    slashes = text[i] === "\\" ? slashes + 1 : 0;
  }
  const next = new Map<string, number>(), close = new Map<number, number>();
  for (const t of [...tokens].reverse()) {
    const key = t.value === "\\[" ? "\\]" : t.value === "\\(" ? "\\)" : t.value;
    const code = singleLineCode && t.value[0] === "`";
    const end = next.get(code ? `${t.line}:${key}` : key);
    if (end !== undefined) close.set(t.at, end);
    // Escaped ticks may close code; escaped math delimiters cannot close math.
    if (!t.escaped || t.value[0] === "`") next.set(code ? `${t.line}:${t.value}` : t.value, t.at);
  }
  const out: MathRange[] = [];
  let until = 0;
  for (const t of tokens) {
    if (t.at < until || t.escaped) continue;
    const end = close.get(t.at);
    if (end === undefined) continue;
    if (t.value[0] === "`") until = end + t.value.length;
    else if (t.value === "$$" || t.value === "\\[" || t.value === "\\(") {
      out.push({ start: t.at, end: end + 2, source: text.slice(t.at + 2, end) });
      until = end + 2;
    }
  }
  return out;
}

export function maskMath(text: string, ranges: MathRange[]): string {
  if (!ranges.length) return text;
  const chars = text.split("");
  for (const r of ranges) for (let i = r.start + 2; i < r.end; i++) if (chars[i] !== "\n") chars[i] = "a";
  return chars.join("");
}

/** Standalone display formulas remain paragraphs, with their body opaque to block Markdown. */
export function mathParagraphs(lines: string[]): Map<number, number> {
  const starts: number[] = [], ends: number[] = [], numbers: number[] = [];
  let offset = 0;
  for (let i = 0; i < lines.length; i++) {
    const end = offset + lines[i].length;
    const first = offset + lines[i].length - lines[i].trimStart().length;
    const last = offset + lines[i].trimEnd().length;
    for (let j = offset; j <= end; j++) { starts[j] = first; ends[j] = last; numbers[j] = i; }
    offset = end + 1;
  }
  const out = new Map<number, number>();
  const text = lines.join("\n");
  let fence: { mark: string; count: number } | undefined;
  const outsideCode = lines.map(line => {
    const trimmed = line.trim();
    if (fence) {
      const { mark, count } = fence;
      if (trimmed.length >= count && [...trimmed].every(c => c === mark)) fence = undefined;
      return " ".repeat(line.length);
    }
    const opener = /^\s*(`{3,}|~{3,})/.exec(line);
    if (!opener) return line;
    fence = { mark: opener[1][0], count: opener[1].length };
    return " ".repeat(line.length);
  }).join("\n");
  for (const r of mathRanges(outsideCode, true)) {
    if (text.startsWith("\\(", r.start) || r.start !== starts[r.start] || r.end !== ends[r.end - 1]) continue;
    out.set(numbers[r.start], numbers[r.end - 1]);
  }
  return out;
}

/** Preserve paragraph line breaks without splitting a formula before inline parsing. */
export function mathLines(text: string): string[] {
  const ranges = new Map(mathRanges(text, true).map(r => [r.start, r]));
  const lines: string[] = [];
  let start = 0;
  for (let i = 0; i < text.length; i++) {
    const formula = ranges.get(i);
    if (formula) i = formula.end - 1;
    else if (text[i] === "\n") { lines.push(text.slice(start, i)); start = i + 1; }
  }
  lines.push(text.slice(start));
  return lines;
}
