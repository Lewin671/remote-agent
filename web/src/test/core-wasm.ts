import { existsSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import "../../../build/core/web/wasm_exec.js";
import { RemoteAgentCore } from "../core/projection";

function wasmPath(): string {
  const fromWeb = resolve(process.cwd(), "../build/core/web/core.wasm");
  return existsSync(fromWeb) ? fromWeb : resolve(process.cwd(), "build/core/web/core.wasm");
}

if (!globalThis.__remoteAgentCore) {
  await RemoteAgentCore.load(new Uint8Array(readFileSync(wasmPath())));
}
