import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { brotliCompressSync, constants as zlibConstants } from "node:zlib";

// 2026-11-17 Web first-paint baseline: wasm (~1,256 KiB) + JS (~595 KiB) = 1,851 KiB (1,895,424 B).
// Budget: +15% => 2,179,738 B (docs/architecture.md 体积与性能预算).
const BASELINE_BYTES = 1_895_424;
const BUDGET_BYTES = Math.round(BASELINE_BYTES * 1.15);

const distDir = resolve(import.meta.dirname, "../dist");
const indexHtml = readFileSync(resolve(distDir, "index.html"), "utf8");

const urls = new Set();
const appFilesMeta = indexHtml.match(/<meta\s+name="app-files"\s+content="([^"]+)"/i);
if (appFilesMeta) {
  for (const token of appFilesMeta[1].split(/\s+/)) {
    if (token.startsWith("/")) urls.add(token);
  }
}
for (const m of indexHtml.matchAll(/<(?:script|link)\b[^>]*\b(?:src|href)="(\/assets\/[^"]+)"/gi)) {
  urls.add(m[1]);
}

function brotliSize(filePath) {
  const raw = readFileSync(filePath);
  return brotliCompressSync(raw, {
    params: {
      [zlibConstants.BROTLI_PARAM_QUALITY]: 11,
      [zlibConstants.BROTLI_PARAM_LGWIN]: 24,
      [zlibConstants.BROTLI_PARAM_SIZE_HINT]: raw.length,
    },
  }).length;
}

let fpJs = 0;
let fpCss = 0;
let fpWasm = 0;

for (const u of [...urls].sort()) {
  const rel = u.replace(/^\/+/, "");
  const full = resolve(distDir, rel);
  const sz = brotliSize(full);
  if (u.endsWith(".js") || u.endsWith(".mjs")) {
    fpJs += sz;
  } else if (u.endsWith(".css")) {
    fpCss += sz;
  } else if (u.endsWith(".wasm")) {
    fpWasm += sz;
  }
}

const fpTotal = fpJs + fpCss + fpWasm;
console.log(
  `web first-paint brotli: fpJs=${fpJs} B, fpCss=${fpCss} B, fpWasm=${fpWasm} B, fpTotal=${fpTotal} B, budget=${BUDGET_BYTES} B (baseline=${BASELINE_BYTES} B +15%)`,
);

if (fpWasm === 0 || fpJs === 0) {
  console.error("error: first-paint asset list in dist/index.html is incomplete");
  process.exit(1);
}
if (fpTotal > BUDGET_BYTES) {
  console.error(`error: first-paint brotli total ${fpTotal} B exceeds budget ${BUDGET_BYTES} B`);
  process.exit(1);
}
