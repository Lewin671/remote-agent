// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { t as message } from "./i18n";
import { App } from "./App";
import { AppRootStage, CoreHost } from "./core/host";
import {
  AppRootViewModelSchema,
  BootstrapErrorViewSchema,
} from "./gen/remoteagent/core/v1/security_view_pb";
import { TextSchema } from "./gen/remoteagent/core/v1/text_pb";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(() => vi.restoreAllMocks());

it.each([false, true])(
  "renders BOOTSTRAP_ERROR with retry and respects canReset=%s",
  async (canReset) => {
    const bootVm = create(AppRootViewModelSchema, {
      stage: AppRootStage.BOOTSTRAP_ERROR,
      bootstrapError: create(BootstrapErrorViewSchema, {
        message: create(TextSchema, { literal: "bootstrap failed" }),
        canRetry: true,
        canReset,
      }),
    });
    vi.spyOn(CoreHost.prototype, "clientInit").mockImplementation(() => undefined);
    vi.spyOn(CoreHost.prototype, "viewAppRoot").mockReturnValue(bootVm);
    const retrySpy = vi.spyOn(CoreHost.prototype, "appRootRetryBootstrap").mockImplementation(() => undefined);

    const container = document.createElement("div");
    document.body.append(container);
    const root = createRoot(container);
    try {
      await act(async () => root.render(<App />));
      await vi.waitFor(() => expect(container.textContent).toContain("bootstrap failed"));
      const button = (label: string) =>
        [...container.querySelectorAll("button")].find((x) => x.textContent === label);
      expect(button(message("common.retry"))).toBeTruthy();
      expect(!!button(message("security.exit_and_start_over"))).toBe(canReset);
      await act(async () => button(message("common.retry"))!.click());
      expect(retrySpy).toHaveBeenCalledOnce();
    } finally {
      await act(async () => root.unmount());
      container.remove();
    }
  },
);
