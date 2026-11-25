import { describe, expect, it, vi } from "vitest";
import { CoreHost } from "./host";
import * as coreRuntimeModule from "./runtime";
import { getCore } from "./runtime";

describe("core WASM app binding smoke", () => {
  it("closes WasmAppCore handle on CoreHost dispose", () => {
    const core = getCore();
    const appCore = core.newAppCore();
    const closeSpy = vi.spyOn(appCore, "close");
    const newAppSpy = vi.spyOn(core, "newAppCore").mockReturnValue(appCore);
    vi.spyOn(coreRuntimeModule, "getCore").mockReturnValue(core);
    const host = new CoreHost();
    try {
      expect(newAppSpy).toHaveBeenCalledTimes(1);
      host.dispose();
      expect(closeSpy).toHaveBeenCalledTimes(1);
    } finally {
      vi.restoreAllMocks();
    }
  });
});
