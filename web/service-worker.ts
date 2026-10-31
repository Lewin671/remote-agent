import { fileURLToPath } from "node:url";
import { build, type Plugin, type Rollup } from "vite";

// A self-contained classic worker works in Safari and requires no runtime
// imports. Use the same bundler/dependencies as the page, in dev and releases.
async function bundleWorker(): Promise<string> {
  const result = await build({
    configFile: false,
    logLevel: "error",
    build: {
      write: false,
      copyPublicDir: false,
      lib: { entry: fileURLToPath(new URL("./src/sw.js", import.meta.url)), formats: ["iife"], name: "RemoteAgentWorker", fileName: () => "sw.js" },
    },
  });
  const output = (Array.isArray(result) ? result[0] : result) as Rollup.RollupOutput;
  const script = output.output.find((item) => item.type === "chunk" && item.fileName === "sw.js");
  if (!script || script.type !== "chunk" || output.output.length !== 1) throw new Error("Expected one self-contained service worker");
  return script.code;
}

export function serviceWorker(): Plugin {
  return {
    name: "service-worker",
    async generateBundle() {
      this.emitFile({ type: "asset", fileName: "sw.js", source: await bundleWorker() });
    },
    configureServer(server) {
      server.middlewares.use("/sw.js", async (_req, res, next) => {
        try {
          const code = await bundleWorker();
          res.setHeader("Content-Type", "text/javascript");
          res.setHeader("Cache-Control", "no-cache");
          res.end(code);
        } catch (e) { next(e); }
      });
    },
  };
}
