// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "../App";
import { t } from "../i18n";
import * as coreRuntime from "./runtime";
import * as remoteModule from "../remote";
import { DemoRemote } from "../dev/demo";
import { mockAppReadyBootstrap } from "../test/security-host-fixture";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | undefined;
let host: HTMLDivElement | undefined;

afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  host?.remove();
  vi.restoreAllMocks();
});

it("blocks workspace rendering until core is ready and shows a localized error with retry on failure", async () => {
  mockAppReadyBootstrap();
  const remote = DemoRemote.create(new URLSearchParams("role=member&empty=none"));
  vi.spyOn(remoteModule.Remote, "load").mockResolvedValue(remote);
  vi.spyOn(remote, "start").mockImplementation(() => {});
  vi.spyOn(remote, "stop").mockImplementation(() => {});

  const realEnsure = coreRuntime.ensureCore;
  const ensureSpy = vi
    .spyOn(coreRuntime, "ensureCore")
    .mockRejectedValueOnce(new Error("boom"))
    .mockImplementation(() => realEnsure());

  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<App />);
  });

  expect(ensureSpy).toHaveBeenCalledTimes(1);
  expect(host.textContent).toContain(t("errors.core_load_failed", { reason: "boom" }));
  expect(host.textContent).not.toContain(t("security.exit_and_start_over"));

  const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === t("common.retry"))!;
  expect(retry).toBeTruthy();
  await act(async () => {
    retry.click();
    await import("../Workspace");
  });

  expect(ensureSpy).toHaveBeenCalledTimes(2);
  expect(host.textContent).not.toContain("boom");
});
