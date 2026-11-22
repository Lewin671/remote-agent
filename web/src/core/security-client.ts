import { create, fromBinary, toBinary } from "@bufbuild/protobuf";
import { checkServer } from "../crypto";
import * as l10n from "../i18n";
import {
  EffectBatchSchema,
  EntropyProvidedSchema,
  EventSchema,
  TimerFiredEventSchema,
  type Effect,
  type Event,
} from "../gen/remoteagent/core/v1/app_pb";
import {
  SecurityActionEmptySchema,
  SecurityApprovePairingActionSchema,
  SecurityBoolActionSchema,
  SecurityConfigEventSchema,
  SecurityForegroundEventSchema,
  SecurityPrepareCreationActionSchema,
  SecurityRemoveDeviceActionSchema,
  SecurityServerCodeActionSchema,
  SecurityStartJoinPairingActionSchema,
  SecurityStartPairingActionSchema,
  SecurityStringActionSchema,
  SecuritySyncNowActionSchema,
  SecurityUpdateDeviceMetaActionSchema,
  SecurityWorkflowActionEventSchema,
  type SecurityExitAdvice,
  type SecurityHandoffCandidate,
  type SecurityPairingInfo,
  type SecurityRecoveryInfo,
  type SecurityTokenUpdatedEffect,
  type SecurityWorkflowActionEvent,
  type SecurityWorkflowResultEffect,
  type SecurityWorkflowStatus,
} from "../gen/remoteagent/core/v1/security_pb";
import type { WasmAppCore } from "./projection";
import { getCore } from "./runtime";
import { WebSecurityExecutor } from "./security-executor";
import { SecurityStoreV4, securityStoreV4 } from "./security-store-v4";

export const BROWSER_AUTHORITY_NOTICE = () =>
  l10n.t("security.this_browser_will_be_the_only_managing_client_clearing");

export async function requestAuthorityPersistence(): Promise<boolean> {
  try {
    return (await navigator.storage?.persist?.()) ?? false;
  } catch {
    return false;
  }
}

export interface PairLink {
  code: string;
  commit?: Uint8Array;
  server: string;
}

const fromB64url = (s: string) =>
  Uint8Array.from(
    atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4)),
    (c) => c.charCodeAt(0),
  );

/** Light query parser for `#/pair?...` deep-link prefill only; verification runs in Go clientcore. */
export function parsePairLinkQuery(query: string): PairLink | undefined {
  const q = new URLSearchParams(query);
  const code = q.get("code");
  const server = q.get("s");
  if (!code || !server) return undefined;
  const c = q.get("c");
  try {
    const commit = c ? fromB64url(c) : undefined;
    if (!/^\d{8}$/.test(code) || (commit && commit.length !== 32)) return undefined;
    return { code, server: checkServer(server), commit };
  } catch {
    return undefined;
  }
}

export class SecurityWorkflowError extends Error {
  constructor(
    message: string,
    readonly code: string,
    readonly retryable: boolean,
    readonly uncertain: boolean,
    readonly errorAction: string,
  ) {
    super(message);
  }
}

export function translateSecurityKey(raw: string): string {
  if (raw.includes(".") && !raw.includes(" ")) {
    const translated = (l10n.t as (key: string) => string)(raw);
    if (translated && translated !== raw) return translated;
  }
  return raw;
}

export function translateSecurityErrorMessage(res: Pick<SecurityWorkflowResultEffect, "error" | "errorCode" | "errorAction">): string {
  return translateSecurityKey(res.error || res.errorCode || "security error");
}

export interface SecurityWorkflowDispatcher {
  dispatchSecurityEvent(ev: Event): void;
  setForeground(active: boolean): void;
  isClosed(): boolean;
}

export class SecurityClient {
  private nextActionId = 1n;
  private readonly waiters = new Map<
    bigint,
    { resolve: (r: SecurityWorkflowResultEffect) => void; reject: (e: unknown) => void }
  >();
  private readonly statusListeners = new Set<(status: SecurityWorkflowStatus) => void>();
  private _status?: SecurityWorkflowStatus;

  constructor(private readonly host: SecurityWorkflowDispatcher) {}

  get status(): SecurityWorkflowStatus | undefined {
    return this._status;
  }

  subscribeStatus(fn: (status: SecurityWorkflowStatus) => void): () => void {
    this.statusListeners.add(fn);
    return () => {
      this.statusListeners.delete(fn);
    };
  }

  /** Called by CoreHost / StandaloneSecurityHost when Go emits SecurityWorkflowResultEffect. */
  handleWorkflowResult(res: SecurityWorkflowResultEffect): void {
    if (res.status) {
      this._status = res.status;
      for (const fn of [...this.statusListeners]) fn(res.status);
    }
    const w = this.waiters.get(res.actionId);
    if (!w) return;
    this.waiters.delete(res.actionId);
    if (!res.ok) {
      w.reject(
        new SecurityWorkflowError(
          translateSecurityErrorMessage(res),
          res.errorCode,
          res.retryable,
          res.uncertain,
          res.errorAction,
        ),
      );
      return;
    }
    w.resolve(res);
  }

  /** Called by CoreHost / StandaloneSecurityHost when Go emits SecurityStatusChangedEffect. */
  handleStatusChanged(status: SecurityWorkflowStatus): void {
    this._status = status;
    for (const fn of [...this.statusListeners]) fn(status);
  }

  rejectAll(reason = "security client closed"): void {
    for (const [, w] of this.waiters) w.reject(new Error(reason));
    this.waiters.clear();
  }

  private run(action: SecurityWorkflowActionEvent["action"]): Promise<SecurityWorkflowResultEffect> {
    if (this.host.isClosed()) {
      return Promise.reject(new Error("security client closed"));
    }
    const actionId = this.nextActionId++;
    return new Promise<SecurityWorkflowResultEffect>((resolve, reject) => {
      this.waiters.set(actionId, { resolve, reject });
      try {
        this.host.dispatchSecurityEvent(
          create(EventSchema, {
            nowMs: BigInt(Date.now()),
            monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
            event: {
              case: "securityAction",
              value: create(SecurityWorkflowActionEventSchema, { actionId, action }),
            },
          }),
        );
      } catch (e) {
        this.waiters.delete(actionId);
        reject(e);
      }
    });
  }

  async bootstrap(): Promise<SecurityWorkflowStatus> {
    const r = await this.run({ case: "bootstrap", value: create(SecurityActionEmptySchema, {}) });
    return r.status!;
  }

  async getStatus(): Promise<SecurityWorkflowStatus> {
    const r = await this.run({ case: "getStatus", value: create(SecurityActionEmptySchema, {}) });
    return r.status!;
  }

  async dismissUpgradeNotice(): Promise<SecurityWorkflowStatus> {
    const r = await this.run({ case: "dismissUpgradeNotice", value: create(SecurityActionEmptySchema, {}) });
    securityStoreV4.dismissUpgradeNotice();
    return r.status!;
  }

  async syncNow(touch = false, authToken = ""): Promise<SecurityWorkflowStatus> {
    const r = await this.run({ case: "syncNow", value: create(SecuritySyncNowActionSchema, { touch, authToken }) });
    return r.status!;
  }

  async renewLease(force = true): Promise<SecurityWorkflowStatus> {
    const r = await this.run({ case: "renewLease", value: create(SecurityBoolActionSchema, { flag: force }) });
    return r.status!;
  }

  async resumePendingOp(): Promise<SecurityWorkflowResultEffect> {
    return this.run({ case: "resumePendingOp", value: create(SecurityActionEmptySchema, {}) });
  }

  async discardPendingOp(abandonHandoff = false): Promise<void> {
    await this.run({ case: "discardPendingOp", value: create(SecurityBoolActionSchema, { flag: abandonHandoff }) });
  }

  async prepareCreation(server: string, storagePersisted: boolean): Promise<{ recoveryCode: string; resumedDraft: boolean; codeSaved: boolean }> {
    const r = await this.run({
      case: "prepareCreation",
      value: create(SecurityPrepareCreationActionSchema, { server, storagePersisted }),
    });
    return { recoveryCode: r.recoveryCode, resumedDraft: r.resumedDraft, codeSaved: r.codeSaved };
  }

  async confirmCreationCodeSaved(): Promise<void> {
    await this.run({ case: "confirmCreationCodeSaved", value: create(SecurityActionEmptySchema, {}) });
  }

  async continueCreation(setupToken = ""): Promise<SecurityWorkflowResultEffect> {
    return this.run({ case: "continueCreation", value: create(SecurityStringActionSchema, { value: setupToken }) });
  }

  async adoptCreationWithoutCode(): Promise<SecurityWorkflowResultEffect> {
    return this.run({ case: "adoptCreationWithoutCode", value: create(SecurityActionEmptySchema, {}) });
  }

  async removeDevice(deviceId: string, abandonPairDelivery = false): Promise<void> {
    await this.run({
      case: "removeDevice",
      value: create(SecurityRemoveDeviceActionSchema, { deviceId, abandonPairDelivery }),
    });
  }

  async deleteMachineRecords(machineId: string): Promise<void> {
    await this.run({ case: "deleteMachineRecords", value: create(SecurityStringActionSchema, { value: machineId }) });
  }

  async deleteAccount(): Promise<void> {
    await this.run({ case: "deleteAccount", value: create(SecurityActionEmptySchema, {}) });
  }

  async prepareRecoveryRotation(): Promise<{ recoveryCode: string }> {
    const r = await this.run({ case: "prepareRecoveryRotation", value: create(SecurityActionEmptySchema, {}) });
    return { recoveryCode: r.recoveryCode };
  }

  async confirmRecoveryRotation(): Promise<void> {
    await this.run({ case: "confirmRecoveryRotation", value: create(SecurityActionEmptySchema, {}) });
  }

  async prepareHandoffCode(storagePersisted: boolean): Promise<{ recoveryCode: string }> {
    const r = await this.run({
      case: "prepareHandoffCode",
      value: create(SecurityBoolActionSchema, { flag: storagePersisted }),
    });
    return { recoveryCode: r.recoveryCode };
  }

  async publishHandoffOffer(): Promise<void> {
    await this.run({ case: "publishHandoffOffer", value: create(SecurityActionEmptySchema, {}) });
  }

  async listHandoffCandidates(): Promise<SecurityHandoffCandidate[]> {
    const r = await this.run({ case: "listHandoffCandidates", value: create(SecurityActionEmptySchema, {}) });
    return r.handoffCandidates;
  }

  async handoffAuthority(targetDeviceId: string): Promise<void> {
    await this.run({ case: "handoffAuthority", value: create(SecurityStringActionSchema, { value: targetDeviceId }) });
  }

  async completeAccountPublicProfile(server: string, code: string): Promise<void> {
    await this.run({
      case: "completeAccountPublicProfile",
      value: create(SecurityServerCodeActionSchema, { server, code }),
    });
  }

  async startPairing(webOrigin: string, replacesDeviceId = ""): Promise<SecurityPairingInfo> {
    const r = await this.run({
      case: "startPairing",
      value: create(SecurityStartPairingActionSchema, { webOrigin, replacesDeviceId }),
    });
    return r.pairing!;
  }

  async pollPairing(pairingId: string): Promise<SecurityPairingInfo> {
    const r = await this.run({ case: "pollPairing", value: create(SecurityStringActionSchema, { value: pairingId }) });
    return r.pairing!;
  }

  async approvePairing(pairingId: string, sasConfirmed: boolean): Promise<SecurityPairingInfo> {
    const r = await this.run({
      case: "approvePairing",
      value: create(SecurityApprovePairingActionSchema, { pairingId, sasConfirmed }),
    });
    return r.pairing!;
  }

  async finishPairingDelivery(pairingId: string): Promise<SecurityPairingInfo> {
    const r = await this.run({
      case: "finishPairingDelivery",
      value: create(SecurityStringActionSchema, { value: pairingId }),
    });
    return r.pairing!;
  }

  async cancelPairing(pairingId: string): Promise<void> {
    await this.run({ case: "cancelPairing", value: create(SecurityStringActionSchema, { value: pairingId }) });
  }

  async startJoinPairing(params: {
    server?: string;
    code?: string;
    commitHint?: Uint8Array;
    existingRepair?: boolean;
    existingServer?: string;
    rawLink?: string;
  }): Promise<SecurityPairingInfo> {
    const r = await this.run({
      case: "startJoinPairing",
      value: create(SecurityStartJoinPairingActionSchema, {
        server: params.server ?? "",
        code: params.code ?? "",
        commitHint: params.commitHint ? new Uint8Array(params.commitHint) : new Uint8Array(),
        existingRepair: !!params.existingRepair,
        existingServer: params.existingServer ?? "",
        rawLink: params.rawLink ?? "",
      }),
    });
    return r.pairing!;
  }

  async confirmJoinSas(pairingId: string): Promise<SecurityWorkflowResultEffect> {
    return this.run({ case: "confirmJoinSas", value: create(SecurityStringActionSchema, { value: pairingId }) });
  }

  async cancelJoinPairing(pairingId: string): Promise<void> {
    await this.run({ case: "cancelJoinPairing", value: create(SecurityStringActionSchema, { value: pairingId }) });
  }

  async prepareEmergencyRecovery(server: string, code: string): Promise<{ recoveryCode: string; recovery?: SecurityRecoveryInfo }> {
    const r = await this.run({
      case: "prepareEmergencyRecovery",
      value: create(SecurityServerCodeActionSchema, { server, code }),
    });
    return { recoveryCode: r.recoveryCode, recovery: r.recovery };
  }

  async confirmRecoveryCodeSaved(): Promise<void> {
    await this.run({ case: "confirmRecoveryCodeSaved", value: create(SecurityActionEmptySchema, {}) });
  }

  async continueRecoverySigning(oldCode: string): Promise<SecurityRecoveryInfo | undefined> {
    const r = await this.run({ case: "continueRecoverySigning", value: create(SecurityStringActionSchema, { value: oldCode }) });
    return r.recovery;
  }

  async resumeRecovery(): Promise<SecurityRecoveryInfo | undefined> {
    const r = await this.run({ case: "resumeRecovery", value: create(SecurityActionEmptySchema, {}) });
    return r.recovery;
  }

  async completeRecovery(): Promise<SecurityWorkflowResultEffect> {
    return this.run({ case: "completeRecovery", value: create(SecurityActionEmptySchema, {}) });
  }

  async discardUnsignedRecovery(): Promise<void> {
    await this.run({ case: "discardUnsignedRecovery", value: create(SecurityActionEmptySchema, {}) });
  }

  async discardRejectedRecovery(): Promise<void> {
    await this.run({ case: "discardRejectedRecovery", value: create(SecurityActionEmptySchema, {}) });
  }

  async checkExitAdvice(): Promise<SecurityExitAdvice> {
    const r = await this.run({ case: "checkExitAdvice", value: create(SecurityActionEmptySchema, {}) });
    return r.exitAdvice!;
  }

  async clearSecurity(): Promise<void> {
    await this.run({ case: "clearSecurity", value: create(SecurityActionEmptySchema, {}) });
  }

  async checkRetainedDenied(): Promise<boolean> {
    const r = await this.run({ case: "checkRetainedDenied", value: create(SecurityActionEmptySchema, {}) });
    return r.flag;
  }

  async updateDeviceMeta(params: {
    deviceId?: string;
    name?: string;
    platform?: string;
    description?: string;
    describeSelfOnly?: boolean;
  }): Promise<void> {
    await this.run({
      case: "updateDeviceMeta",
      value: create(SecurityUpdateDeviceMetaActionSchema, {
        deviceId: params.deviceId ?? "",
        name: params.name ?? "",
        platform: params.platform ?? "",
        description: params.description ?? "",
        describeSelfOnly: !!params.describeSelfOnly,
      }),
    });
  }

  async republishRecoveryObjects(flag = false): Promise<void> {
    await this.run({ case: "republishRecoveryObjects", value: create(SecurityBoolActionSchema, { flag }) });
  }

  setForeground(active: boolean): void {
    this.host.setForeground(active);
  }
}

export interface StandaloneSecurityHandle {
  client: SecurityClient;
  dispose: () => void;
}

/**
 * Hosts a standalone WasmAppCore + WebSecurityExecutor for unauthenticated entry screens
 * (bootstrap, account creation, pairing join, emergency recovery, setup reset, rejoin).
 */
export function createStandaloneSecurityClient(opts: {
  store?: SecurityStoreV4;
  fetch?: typeof fetch;
  onToken?: (eff: SecurityTokenUpdatedEffect) => void;
} = {}): StandaloneSecurityHandle {
  const core: WasmAppCore = getCore().newAppCore();
  let closed = false;
  const timers = new Map<string, { id: bigint; handle: ReturnType<typeof setTimeout> }>();
  const pending: Event[] = [];
  let draining = false;

  const exec = new WebSecurityExecutor({
    store: opts.store ?? securityStoreV4,
    fetch: opts.fetch,
    emit: (ev) => dispatch(ev),
  });

  const dispatch = (ev: Event) => {
    if (closed) return;
    if (ev.nowMs === 0n) ev.nowMs = BigInt(Date.now());
    if (ev.monoMs === 0n) ev.monoMs = BigInt(Math.max(0, Math.floor(performance.now())));
    pending.push(ev);
    if (draining) return;
    draining = true;
    try {
      while (pending.length > 0 && !closed) {
        const next = pending.shift()!;
        const batch = fromBinary(EffectBatchSchema, core.dispatch(toBinary(EventSchema, next)));
        for (const eff of batch.effects) {
          if (closed) break;
          handleEffect(eff);
        }
      }
    } finally {
      draining = false;
    }
  };

  const client: SecurityClient = new SecurityClient({
    dispatchSecurityEvent: dispatch,
    setForeground: (active) => {
      if (closed) return;
      dispatch(
        create(EventSchema, {
          event: {
            case: "securityForeground",
            value: create(SecurityForegroundEventSchema, { foreground: active }),
          },
        }),
      );
    },
    isClosed: () => closed,
  });

  const handleEffect = (eff: Effect) => {
    const val = eff.effect;
    switch (val.case) {
      case "requestEntropy": {
        const randomIds: string[] = [];
        for (let i = 0; i < Math.max(val.value.minIds, 16); i++) {
          randomIds.push(crypto.randomUUID());
        }
        const unitFloats: number[] = [];
        const raw = new Uint32Array(Math.max(val.value.minFloats, 16));
        crypto.getRandomValues(raw);
        for (let i = 0; i < raw.length; i++) unitFloats.push(raw[i] / 0x100000000);
        const randomBytes = val.value.minRandomBytes > 0
          ? crypto.getRandomValues(new Uint8Array(Math.max(val.value.minRandomBytes, 4096)))
          : new Uint8Array();
        pending.push(
          create(EventSchema, {
            nowMs: BigInt(Date.now()),
            monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
            event: {
              case: "entropyProvided",
              value: create(EntropyProvidedSchema, { randomIds, unitFloats, randomBytes }),
            },
          }),
        );
        break;
      }
      case "scheduleTimer": {
        const { timerId, kind, key, delayMs } = val.value;
        const tk = `${kind}:${key}`;
        const prev = timers.get(tk);
        if (prev) clearTimeout(prev.handle);
        const handle = setTimeout(() => {
          const cur = timers.get(tk);
          if (!cur || cur.id !== timerId) return;
          timers.delete(tk);
          if (!closed) {
            dispatch(
              create(EventSchema, {
                event: { case: "timerFired", value: create(TimerFiredEventSchema, { timerId, kind, key }) },
              }),
            );
          }
        }, Math.max(0, Number(delayMs)));
        timers.set(tk, { id: timerId, handle });
        break;
      }
      case "cancelTimer": {
        const { timerId, kind, key } = val.value;
        const tk = `${kind}:${key}`;
        const cur = timers.get(tk);
        if (cur && (timerId === 0n || cur.id === timerId)) {
          clearTimeout(cur.handle);
          timers.delete(tk);
        }
        break;
      }
      case "securityKeyGenerate":
        void exec.keyGenerate(val.value);
        break;
      case "securityKeySign":
        void exec.keySign(val.value);
        break;
      case "securityKeyAgree":
        void exec.keyAgree(val.value);
        break;
      case "securityKeyEncapsulate":
        void exec.keyEncapsulate(val.value);
        break;
      case "securityKeyDiscard":
        void exec.keyDiscard(val.value);
        break;
      case "securityStorage":
        void exec.storage(val.value);
        break;
      case "securityRpc":
        void exec.rpc(val.value);
        break;
      case "securityCryptoBatch":
        void exec.cryptoBatch(val.value);
        break;
      case "securityTokenUpdated":
        opts.onToken?.(val.value);
        break;
      case "securityWorkflowResult":
        client.handleWorkflowResult(val.value);
        break;
      case "securityStatusChanged":
        client.handleStatusChanged(val.value.status!);
        break;
      default:
        break;
    }
  };

  // Seed initial config & foreground visibility.
  const visibleNow = typeof document === "undefined" || document.visibilityState === "visible";
  dispatch(
    create(EventSchema, {
      event: {
        case: "securityConfig",
        value: create(SecurityConfigEventSchema, {
          inlineCrypto: false,
          foreground: visibleNow,
        }),
      },
    }),
  );

  const onVisibility = () => {
    const visible = typeof document === "undefined" || !document.hidden;
    client.setForeground(visible);
  };
  const onHide = () => client.setForeground(false);
  const onShow = () => client.setForeground(typeof document === "undefined" || !document.hidden);
  globalThis.addEventListener?.("visibilitychange", onVisibility);
  globalThis.addEventListener?.("pagehide", onHide);
  globalThis.addEventListener?.("pageshow", onShow);

  return {
    client,
    dispose: () => {
      if (closed) return;
      closed = true;
      globalThis.removeEventListener?.("visibilitychange", onVisibility);
      globalThis.removeEventListener?.("pagehide", onHide);
      globalThis.removeEventListener?.("pageshow", onShow);
      for (const t of timers.values()) clearTimeout(t.handle);
      timers.clear();
      exec.close();
      client.rejectAll();
      core.close();
    },
  };
}
