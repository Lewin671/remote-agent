import { readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import react from "@vitejs/plugin-react";
import { serviceWorker } from "./service-worker.ts";
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
    generateBundle(_, bundle) {
      // Only the files this build made are kept for good. A name it did
      // not make must not be: a host may answer it with something else
      // (Cloudflare Pages answers a missing path with the page itself while
      // a release is spreading), and a browser holding that for a year
      // never loads the page again.
      const hashed = Object.keys(bundle)
        .filter((name) => name.startsWith("assets/"))
        .sort()
        .flatMap((name) => [`/${name}`, "  Cache-Control: public, max-age=31536000, immutable"]);
      // Cloudflare Pages reads at most 100 rules and drops the rest.
      if (hashed.length / 2 > 90) this.error(`${hashed.length / 2} built assets: too many header rules for Cloudflare Pages`);
      const headers = [
        "/*",
        `  Content-Security-Policy: ${[...policy, "frame-ancestors 'none'"].join("; ")}`,
        "  X-Content-Type-Options: nosniff",
        "  Referrer-Policy: no-referrer",
        "  Permissions-Policy: camera=(), microphone=(), geolocation=()",
        "  Strict-Transport-Security: max-age=31536000",
        // Built assets carry a content hash in their name: never asked for
        // again. Fonts keep their names, so they are only kept for a while.
        ...hashed,
        "/fonts/*",
        "  Cache-Control: public, max-age=2592000",
        "",
      ].join("\n");
      this.emitFile({ type: "asset", fileName: "_headers", source: headers });
      // Every route is in the fragment of "/", so any other path is simply
      // not there. With a 404.html, Cloudflare Pages says so instead of
      // answering every missing path with the page.
      this.emitFile({
        type: "asset",
        fileName: "404.html",
        source: '<!doctype html>\n<meta charset="utf-8">\n<title>Not found</title>\n<p>Not found. <a href="/">Open remote-agent</a></p>\n',
      });
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
  plugins: [react(), csp(), serviceWorker()],
  build: {
    rollupOptions: {
      output: {
        // Libraries change far less often than the app: in a chunk of their
        // own they keep their name, and browsers their copy, across releases.
        manualChunks: (id) => (/node_modules\/(react|react-dom|scheduler|react-virtuoso|@bufbuild|@connectrpc)\//.test(id) ? "vendor" : undefined),
      },
    },
  },
  server: { port: 5173, strictPort: true, proxy },
  preview: { port: 4173, strictPort: true, proxy },
});
