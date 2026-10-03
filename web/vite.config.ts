import { readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

// The built page only runs its own scripts and only talks to the server.
// Builds carry the policy twice: in a meta tag, and in a _headers file for
// hosts like Cloudflare Pages, since frame-ancestors only works as a real
// header. It is left out of the dev server, which injects inline scripts.
const policy = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self' 'unsafe-inline'",
  // Session images are shown from decrypted bytes as blob: URLs.
  "img-src 'self' blob: data:",
  "connect-src 'self' https: wss: http://localhost:* ws://localhost:* http://127.0.0.1:* ws://127.0.0.1:*",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
];

function csp(): Plugin {
  return {
    name: "csp",
    apply: "build",
    transformIndexHtml(html) {
      return html.replace("<head>", `<head>\n    <meta http-equiv="Content-Security-Policy" content="${policy.join("; ")}">`);
    },
    generateBundle() {
      const headers = [
        "/*",
        `  Content-Security-Policy: ${[...policy, "frame-ancestors 'none'"].join("; ")}`,
        "  X-Content-Type-Options: nosniff",
        "  Referrer-Policy: no-referrer",
        "  Permissions-Policy: camera=(), microphone=(), geolocation=()",
        "  Strict-Transport-Security: max-age=31536000",
        // Built assets carry a content hash in their name: never asked for
        // again. Fonts keep their names, so they are only kept for a while.
        "/assets/*",
        "  Cache-Control: public, max-age=31536000, immutable",
        "/fonts/*",
        "  Cache-Control: public, max-age=2592000",
        "",
      ].join("\n");
      this.emitFile({ type: "asset", fileName: "_headers", source: headers });
    },
  };
}

// RA_PROXY=https://server makes the dev and preview servers forward the
// API and the stream to that server, for browsers whose content blockers
// stop cross-site WebSockets from localhost. Build with
// VITE_SAME_ORIGIN_API=1 so the page calls its own origin.
const target = process.env.RA_PROXY;
const proxy = target
  ? {
      "/remoteagent.v1.": { target, changeOrigin: true },
      "/v1/stream": { target, changeOrigin: true, ws: true },
      "/v1/term": { target, changeOrigin: true, ws: true },
    }
  : undefined;

const productVersion = readFileSync(new URL("../version.properties", import.meta.url), "utf8").match(/^version=(.+)$/m)![1];
const commit = execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim();
const version = process.env.RA_OFFICIAL_RELEASE === "true" ? productVersion : `${productVersion}-dev.${commit.slice(0, 7)}`;

export default defineConfig({
  define: {
    "import.meta.env.VITE_PRODUCT_VERSION": JSON.stringify(version),
    "import.meta.env.VITE_BUILD_COMMIT": JSON.stringify(commit),
  },
  plugins: [react(), csp()],
  server: { port: 5173, strictPort: true, proxy },
  preview: { port: 4173, strictPort: true, proxy },
});
