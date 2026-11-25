// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App";
import {
  AppRootStage,
  CoreHost,
  ExitConfirmStep,
  ExitIntentKind,
  OnboardingMode,
} from "./core/host";
import * as coreRuntime from "./core/runtime";
import { Remote } from "./remote";
import { DemoRemote } from "./dev/demo";
import { storedSecurity } from "./test/stored-security-fixture";

vi.mock("./eventcache", () => ({
  clearEventCache: vi.fn(async () => undefined),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let container: HTMLDivElement | undefined;
let root: Root | undefined;

afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  container?.remove();
  container = undefined;
  localStorage.clear();
  location.hash = "";
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("creates exactly 1 WasmAppCore across startup -> ready (Workspace) -> exit -> onboarding and never calls location.reload", async () => {
  await coreRuntime.ensureCore();
  await storedSecurity();
  vi.stubGlobal("fetch", vi.fn(async () => {
    throw new Error("offline");
  }));
  const realCoreMod = coreRuntime.getCore();
  const newAppCoreSpy = vi.spyOn(realCoreMod, "newAppCore");
  const reloadSpy = vi.spyOn(location, "reload").mockImplementation(() => undefined);

  // Capture the singleton CoreHost instance created by App.
  let capturedHost: CoreHost | undefined;
  const origClientInit = CoreHost.prototype.clientInit;
  vi.spyOn(CoreHost.prototype, "clientInit").mockImplementation(function (this: CoreHost, params) {
    capturedHost = this;
    return origClientInit.call(this, params);
  });

  const demoRemote = DemoRemote.create(new URLSearchParams("role=member&empty=none"));
  vi.spyOn(demoRemote, "start").mockImplementation(() => undefined);
  vi.spyOn(demoRemote, "stop").mockImplementation(() => undefined);
  const loadSpy = vi.spyOn(Remote, "load").mockImplementation(async (_st, sharedHost) => {
    sharedHost.attachRemote(demoRemote);
    return demoRemote;
  });

  // Note: DemoRemote.create() does not construct a CoreHost until getCoreHost(demoRemote) is called,
  // and because sharedHost.attachRemote(demoRemote) registers demoRemote -> sharedHost in the WeakMap,
  // getCoreHost(demoRemote) returns the SAME singleton CoreHost!
  newAppCoreSpy.mockClear();

  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);

  await act(async () => {
    root!.render(<App />);
  });

  // Drive all the way into READY -> Remote.load -> attachRemote -> Workspace mounted on the singleton CoreHost.
  await vi.waitFor(() => {
    expect(capturedHost).toBeDefined();
    expect(capturedHost!.viewAppRoot().stage).toBe(AppRootStage.READY);
    expect(loadSpy).toHaveBeenCalledTimes(1);
    expect(container!.querySelector(".phone, .desk")).not.toBeNull();
  });
  expect(newAppCoreSpy).toHaveBeenCalledTimes(1);

  // Trigger an in-place exit (LEAVE_ACCOUNT) -> confirm -> reset -> back to ONBOARDING!
  await act(async () => {
    capturedHost!.exitRequest(ExitIntentKind.LEAVE_ACCOUNT, "https://ra.example.com");
  });
  await vi.waitFor(() => {
    expect(capturedHost!.viewExitConfirm().step).toBe(ExitConfirmStep.AWAITING_CONFIRM);
  });
  await act(async () => {
    capturedHost!.exitConfirm(true);
  });
  await vi.waitFor(() => {
    expect(capturedHost!.viewAppRoot().stage).toBe(AppRootStage.ONBOARDING);
    expect(capturedHost!.viewOnboarding().mode).toBe(OnboardingMode.ENTRY);
    expect(capturedHost!.viewOnboarding().create?.serverInput).toBe("https://ra.example.com");
    expect(container!.querySelector(".phone, .desk")).toBeNull();
  });

  // Core invariants: exactly 1 WasmAppCore instance and ZERO location.reload() calls!
  expect(newAppCoreSpy).toHaveBeenCalledTimes(1);
  expect(reloadSpy).not.toHaveBeenCalled();
});
