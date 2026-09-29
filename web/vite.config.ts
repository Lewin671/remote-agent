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
  "img-src 'self' data:",
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

export default defineConfig({
  plugins: [react(), csp()],
  server: { port: 5173, strictPort: true, proxy },
  preview: { port: 4173, strictPort: true, proxy },
});
