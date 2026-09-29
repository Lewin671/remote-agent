import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";

// The built page only runs its own scripts and only talks to the server.
// It is added to builds only: the dev server injects inline scripts.
function csp(): Plugin {
  return {
    name: "csp",
    apply: "build",
    transformIndexHtml(html) {
      const policy = [
        "default-src 'self'",
        "script-src 'self'",
        "style-src 'self' 'unsafe-inline'",
        "img-src 'self' data:",
        "connect-src 'self' https: wss: http://localhost:* ws://localhost:* http://127.0.0.1:* ws://127.0.0.1:*",
        "object-src 'none'",
        "base-uri 'none'",
        "form-action 'none'",
      ].join("; ");
      return html.replace("<head>", `<head>\n    <meta http-equiv="Content-Security-Policy" content="${policy}">`);
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
    }
  : undefined;

export default defineConfig({
  plugins: [react(), csp()],
  server: { port: 5173, strictPort: true, proxy },
  preview: { port: 4173, strictPort: true, proxy },
});
