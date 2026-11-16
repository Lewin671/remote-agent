import "../../../build/core/web/wasm_exec.js";
import coreWasmUrl from "../../../build/core/web/core.wasm?url";
import { RemoteAgentCore } from "./projection";

let readyCore: RemoteAgentCore | undefined;
let loading: Promise<RemoteAgentCore> | undefined;

/** Loads the Go WASM core once in the browser; in Node tests, uses the preloaded global. */
export async function ensureCore(): Promise<RemoteAgentCore> {
  if (readyCore) return readyCore;
  if (globalThis.__remoteAgentCore) {
    readyCore = RemoteAgentCore.fromGlobal();
    return readyCore;
  }
  if (!loading) {
    loading = (async () => {
      const core = await RemoteAgentCore.load(fetch(coreWasmUrl));
      readyCore = core;
      return core;
    })().catch((error) => {
      loading = undefined;
      throw error;
    });
  }
  return loading;
}

/** Returns the initialized WASM core synchronously; throws if ensureCore() has not completed. */
export function getCore(): RemoteAgentCore {
  if (readyCore) return readyCore;
  if (globalThis.__remoteAgentCore) {
    return RemoteAgentCore.fromGlobal();
  }
  throw new Error("RemoteAgentCore is not loaded yet");
}
