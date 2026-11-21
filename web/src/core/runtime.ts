import "../../../build/core/web/wasm_exec.js";
import coreWasmUrl from "../../../build/core/web/core.wasm?url";
import { RemoteAgentCore } from "./projection";
import { setActiveCore } from "./sec-call";

let readyCore: RemoteAgentCore | undefined;
let loading: Promise<RemoteAgentCore> | undefined;

/** Loads the Go WASM core once in the browser; in Node tests, uses the preloaded global. */
export async function ensureCore(): Promise<RemoteAgentCore> {
  if (readyCore) return readyCore;
  if (globalThis.__remoteAgentCore) {
    readyCore = RemoteAgentCore.fromGlobal();
    setActiveCore(readyCore);
    return readyCore;
  }
  if (!loading) {
    loading = (async () => {
      const core = await RemoteAgentCore.load(fetch(coreWasmUrl));
      readyCore = core;
      setActiveCore(core);
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
    const core = RemoteAgentCore.fromGlobal();
    setActiveCore(core);
    return core;
  }
  throw new Error("RemoteAgentCore is not loaded yet");
}
