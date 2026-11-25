import { create } from "@bufbuild/protobuf";
import { vi } from "vitest";
import {
  AppRootStage,
  CoreHost,
  type AccountRuntimeChangedEffect,
} from "../core/host";
import {
  AccountRuntimeChangedEffectSchema,
  AppRootViewModelSchema,
} from "../gen/remoteagent/core/v1/security_view_pb";

/**
 * Mocks CoreHost startup in <App /> component tests so Core immediately reports
 * AppRootStage.READY and invokes onAccountRuntimeChanged(active=true) without
 * touching IndexedDB.
 */
export function mockAppReadyBootstrap(opts: {
  server?: string;
  deviceId?: string;
  accountId?: string;
} = {}) {
  const server = opts.server ?? "https://ra.example.com";
  const deviceId = opts.deviceId ?? "me";
  const accountId = opts.accountId ?? "acc";
  const readyVm = create(AppRootViewModelSchema, {
    stage: AppRootStage.READY,
    server,
    deviceId,
    accountId,
    isManager: true,
  });
  const runtimeEff: AccountRuntimeChangedEffect = create(AccountRuntimeChangedEffectSchema, {
    active: true,
    removed: false,
    server,
    deviceId,
    accountId,
  });

  vi.spyOn(CoreHost.prototype, "viewAppRoot").mockReturnValue(readyVm);
  vi.spyOn(CoreHost.prototype, "clientInit").mockImplementation(function (this: CoreHost) {
    const internal = this as unknown as {
      lastRuntimeEffect?: AccountRuntimeChangedEffect;
      onAccountRuntimeChanged?: (eff: AccountRuntimeChangedEffect) => void;
    };
    internal.lastRuntimeEffect = runtimeEff;
    internal.onAccountRuntimeChanged?.(runtimeEff);
  });
}
