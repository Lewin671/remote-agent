// Minimal loader and wrapper for the Go WASM core (built by scripts/core/build.sh web).

export interface CoreWasmApi {
  readonly new: () => number;
  dispatch(handle: number, eventBytes?: Uint8Array): CoreWasmResult;
  view(handle: number, screenBytes?: Uint8Array): CoreWasmResult;
  close(handle: number): void;
  newProjection(agent: number): number;
  projectionReset(handle: number, agent: number): void;
  projectionAppend(handle: number, batchBytes?: Uint8Array): CoreWasmResult;
  projectionSnapshot(handle: number): CoreWasmResult;
  projectionCurrentActions(handle: number, liveBytes?: Uint8Array): CoreWasmResult;
  projectionFree(handle: number): void;
  reduceProjection(agent: number, batchBytes?: Uint8Array): CoreWasmResult;
  secCall(reqBytes?: Uint8Array): CoreWasmResult;
}

type CoreWasmResult = { data: Uint8Array } | { error: string };

interface GoRuntime {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): Promise<void>;
}

declare global {
  interface Window {
    Go?: new () => GoRuntime;
  }
  // Provided by wasm_exec.js in both browser and worker/node contexts.
  // eslint-disable-next-line no-var
  var Go: (new () => GoRuntime) | undefined;
  // eslint-disable-next-line no-var
  var __remoteAgentCore: CoreWasmApi | undefined;
  // eslint-disable-next-line no-var
  var __remoteAgentCoreReady: ((api: CoreWasmApi) => void) | undefined;
}

function unwrap(res: CoreWasmResult): Uint8Array {
  if ("error" in res) {
    throw new Error(res.error);
  }
  return res.data;
}

export class WasmProjection {
  private readonly api: CoreWasmApi;
  private readonly handle: number;
  private freed = false;

  constructor(api: CoreWasmApi, agent = 0) {
    this.api = api;
    this.handle = api.newProjection(agent);
  }

  reset(agent: number): void {
    this.assertAlive();
    this.api.projectionReset(this.handle, agent);
  }

  append(eventBatchBytes?: Uint8Array): Uint8Array {
    this.assertAlive();
    return unwrap(this.api.projectionAppend(this.handle, eventBatchBytes));
  }

  snapshot(): Uint8Array {
    this.assertAlive();
    return unwrap(this.api.projectionSnapshot(this.handle));
  }

  currentActions(liveSnapshotBytes?: Uint8Array): Uint8Array {
    this.assertAlive();
    return unwrap(this.api.projectionCurrentActions(this.handle, liveSnapshotBytes));
  }

  free(): void {
    if (!this.freed) {
      this.freed = true;
      this.api.projectionFree(this.handle);
    }
  }

  private assertAlive(): void {
    if (this.freed) {
      throw new Error("WasmProjection already freed");
    }
  }
}

export class WasmAppCore {
  private readonly api: CoreWasmApi;
  private readonly handle: number;
  private closed = false;

  constructor(api: CoreWasmApi) {
    this.api = api;
    this.handle = api.new();
  }

  dispatch(eventBytes?: Uint8Array): Uint8Array {
    this.assertAlive();
    return unwrap(this.api.dispatch(this.handle, eventBytes));
  }

  view(screenBytes?: Uint8Array): Uint8Array {
    this.assertAlive();
    return unwrap(this.api.view(this.handle, screenBytes));
  }

  close(): void {
    if (!this.closed) {
      this.closed = true;
      this.api.close(this.handle);
    }
  }

  private assertAlive(): void {
    if (this.closed) {
      throw new Error("WasmAppCore already closed");
    }
  }
}

export class RemoteAgentCore {
  private readonly api: CoreWasmApi;

  private constructor(api: CoreWasmApi) {
    this.api = api;
  }

  static fromGlobal(): RemoteAgentCore {
    if (!globalThis.__remoteAgentCore) {
      throw new Error("RemoteAgentCore is not loaded yet");
    }
    return new RemoteAgentCore(globalThis.__remoteAgentCore);
  }

  /**
   * Instantiates core.wasm after wasm_exec.js has registered globalThis.Go.
   * Pass either a fetch Response/Promise<Response> or raw WebAssembly bytes.
   */
  static async load(source: WebAssembly.WebAssemblyInstantiatedSource | BufferSource | Promise<Response>): Promise<RemoteAgentCore> {
    if (globalThis.__remoteAgentCore) {
      return new RemoteAgentCore(globalThis.__remoteAgentCore);
    }
    const GoCtor = globalThis.Go;
    if (!GoCtor) {
      throw new Error("wasm_exec.js must be loaded before RemoteAgentCore.load()");
    }
    const go = new GoCtor();
    let instantiated: WebAssembly.WebAssemblyInstantiatedSource;
    if ("instance" in source) {
      instantiated = source;
    } else if (source instanceof Promise) {
      const streaming = typeof WebAssembly.instantiateStreaming === "function";
      const response = await source;
      const isWasm = response.headers.get("content-type")?.includes("application/wasm");
      if (!response.ok) {
        throw new Error(`HTTP ${response.status}`);
      }
      if (streaming && isWasm) {
        instantiated = await WebAssembly.instantiateStreaming(response, go.importObject);
      } else {
        const bytes = await response.arrayBuffer();
        instantiated = await WebAssembly.instantiate(bytes, go.importObject);
      }
    } else {
      instantiated = await WebAssembly.instantiate(source, go.importObject);
    }

    const ready = new Promise<CoreWasmApi>((resolve) => {
      globalThis.__remoteAgentCoreReady = resolve;
    });
    void go.run(instantiated.instance);
    const api = globalThis.__remoteAgentCore ?? (await ready);
    return new RemoteAgentCore(api);
  }

  newAppCore(): WasmAppCore {
    return new WasmAppCore(this.api);
  }

  newProjection(agent = 0): WasmProjection {
    return new WasmProjection(this.api, agent);
  }

  reduce(agent: number, eventBatchBytes?: Uint8Array): Uint8Array {
    return unwrap(this.api.reduceProjection(agent, eventBatchBytes));
  }

  secCallBytes(reqBytes?: Uint8Array): Uint8Array {
    return unwrap(this.api.secCall(reqBytes));
  }
}
