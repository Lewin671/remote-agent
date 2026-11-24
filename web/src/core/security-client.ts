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
  SecurityErrorCode,
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
import { dispatchSecurityEffect, WebSecurityExecutor } from "./security-executor";
import { SecurityStoreV4, securityStoreV4 } from "./security-store-v4";
import { renderText } from "./text";

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
    readonly code: SecurityErrorCode,
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

export function translateSecurityErrorMessage(
  res: Pick<SecurityWorkflowResultEffect, "error" | "errorAction" | "errorText">,
): string {
  if (res.errorText) {
    const rendered = renderText(res.errorText);
    if (rendered) return rendered;
  }
  return translateSecurityKey(res.error || "security error");
}

export function renderExitAdviceMessage(
  a: Pick<SecurityExitAdvice, "message" | "messageText">,
): string {
  if (a.messageText) {
    const rendered = renderText(a.messageText);
    if (rendered) return rendered;
  }
  return translateSecurityKey(a.message);
}

export interface SecurityWorkflowDispatcher {
  dispatchSecurityEvent(ev: Event): void;
  setForeground(active: boolean): void;
  isClosed(): boolean;
}

type Action = SecurityWorkflowActionEvent["action"];
type EmptyCase = Extract<Action, { value: { $typeName: "remoteagent.core.v1.SecurityActionEmpty" } }>["case"];
type BoolCase = Extract<Action, { value: { $typeName: "remoteagent.core.v1.SecurityBoolAction" } }>["case"];
type StringCase = Extract<Action, { value: { $typeName: "remoteagent.core.v1.SecurityStringAction" } }>["case"];

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
    return () => this.statusListeners.delete(fn);
  }

  /** Called by CoreHost / StandaloneSecurityHost when Go emits SecurityWorkflowResultEffect. */
  handleWorkflowResult(res: SecurityWorkflowResultEffect): void {
    if (res.status) this.handleStatusChanged(res.status);
    const w = this.waiters.get(res.actionId);
    if (!w) return;
    this.waiters.delete(res.actionId);
    if (!res.ok) {
      w.reject(
        new SecurityWorkflowError(
          translateSecurityErrorMessage(res),
          res.structuredErrorCode,
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

  private run(action: Action): Promise<SecurityWorkflowResultEffect> {
    if (this.host.isClosed()) return Promise.reject(new Error("security client closed"));
    const actionId = this.nextActionId++;
    return new Promise<SecurityWorkflowResultEffect>((resolve, reject) => {
      this.waiters.set(actionId, { resolve, reject });
      try {
        this.host.dispatchSecurityEvent(
          create(EventSchema, {
            nowMs: BigInt(Date.now()),
            monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
            event: { case: "securityAction", value: create(SecurityWorkflowActionEventSchema, { actionId, action }) },
          }),
        );
      } catch (e) {
        this.waiters.delete(actionId);
        reject(e);
      }
    });
  }

  private empty(c: EmptyCase) {
    return this.run({ case: c, value: create(SecurityActionEmptySchema, {}) } as Action);
  }
  private bool(c: BoolCase, flag: boolean) {
    return this.run({ case: c, value: create(SecurityBoolActionSchema, { flag }) } as Action);
  }
  private str(c: StringCase, value: string) {
    return this.run({ case: c, value: create(SecurityStringActionSchema, { value }) } as Action);
  }

  bootstrap = async (): Promise<SecurityWorkflowStatus> => (await this.empty("bootstrap")).status!;
  getStatus = async (): Promise<SecurityWorkflowStatus> => (await this.empty("getStatus")).status!;
  async dismissUpgradeNotice(): Promise<SecurityWorkflowStatus> {
    const r = await this.empty("dismissUpgradeNotice");
    securityStoreV4.dismissUpgradeNotice();
    return r.status!;
  }
  async syncNow(touch = false, authToken = ""): Promise<SecurityWorkflowStatus> {
    return (await this.run({ case: "syncNow", value: create(SecuritySyncNowActionSchema, { touch, authToken }) })).status!;
  }
  renewLease = async (force = true): Promise<SecurityWorkflowStatus> => (await this.bool("renewLease", force)).status!;
  resumePendingOp = (): Promise<SecurityWorkflowResultEffect> => this.empty("resumePendingOp");
  async discardPendingOp(abandonHandoff = false): Promise<void> {
    await this.bool("discardPendingOp", abandonHandoff);
  }
  async prepareCreation(server: string, storagePersisted: boolean): Promise<{ recoveryCode: string; resumedDraft: boolean; codeSaved: boolean }> {
    const r = await this.run({ case: "prepareCreation", value: create(SecurityPrepareCreationActionSchema, { server, storagePersisted }) });
    return { recoveryCode: r.recoveryCode, resumedDraft: r.resumedDraft, codeSaved: r.codeSaved };
  }
  async confirmCreationCodeSaved(): Promise<void> { await this.empty("confirmCreationCodeSaved"); }
  continueCreation = (setupToken = ""): Promise<SecurityWorkflowResultEffect> => this.str("continueCreation", setupToken);
  adoptCreationWithoutCode = (): Promise<SecurityWorkflowResultEffect> => this.empty("adoptCreationWithoutCode");
  async removeDevice(deviceId: string, abandonPairDelivery = false): Promise<void> {
    await this.run({ case: "removeDevice", value: create(SecurityRemoveDeviceActionSchema, { deviceId, abandonPairDelivery }) });
  }
  async deleteMachineRecords(machineId: string): Promise<void> { await this.str("deleteMachineRecords", machineId); }
  async deleteAccount(): Promise<void> { await this.empty("deleteAccount"); }
  prepareRecoveryRotation = async (): Promise<{ recoveryCode: string }> => ({ recoveryCode: (await this.empty("prepareRecoveryRotation")).recoveryCode });
  async confirmRecoveryRotation(): Promise<void> { await this.empty("confirmRecoveryRotation"); }
  prepareHandoffCode = async (storagePersisted: boolean): Promise<{ recoveryCode: string }> => ({ recoveryCode: (await this.bool("prepareHandoffCode", storagePersisted)).recoveryCode });
  async publishHandoffOffer(): Promise<void> { await this.empty("publishHandoffOffer"); }
  listHandoffCandidates = async (): Promise<SecurityHandoffCandidate[]> => (await this.empty("listHandoffCandidates")).handoffCandidates;
  async handoffAuthority(targetDeviceId: string): Promise<void> { await this.str("handoffAuthority", targetDeviceId); }
  async completeAccountPublicProfile(server: string, code: string): Promise<void> {
    await this.run({ case: "completeAccountPublicProfile", value: create(SecurityServerCodeActionSchema, { server, code }) });
  }
  async startPairing(webOrigin: string, replacesDeviceId = ""): Promise<SecurityPairingInfo> {
    return (await this.run({ case: "startPairing", value: create(SecurityStartPairingActionSchema, { webOrigin, replacesDeviceId }) })).pairing!;
  }
  pollPairing = async (pairingId: string): Promise<SecurityPairingInfo> => (await this.str("pollPairing", pairingId)).pairing!;
  async approvePairing(pairingId: string, sasConfirmed: boolean): Promise<SecurityPairingInfo> {
    return (await this.run({ case: "approvePairing", value: create(SecurityApprovePairingActionSchema, { pairingId, sasConfirmed }) })).pairing!;
  }
  finishPairingDelivery = async (pairingId: string): Promise<SecurityPairingInfo> => (await this.str("finishPairingDelivery", pairingId)).pairing!;
  async cancelPairing(pairingId: string): Promise<void> { await this.str("cancelPairing", pairingId); }
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
  confirmJoinSas = (pairingId: string): Promise<SecurityWorkflowResultEffect> => this.str("confirmJoinSas", pairingId);
  async cancelJoinPairing(pairingId: string): Promise<void> { await this.str("cancelJoinPairing", pairingId); }
  async prepareEmergencyRecovery(server: string, code: string): Promise<{ recoveryCode: string; recovery?: SecurityRecoveryInfo }> {
    const r = await this.run({ case: "prepareEmergencyRecovery", value: create(SecurityServerCodeActionSchema, { server, code }) });
    return { recoveryCode: r.recoveryCode, recovery: r.recovery };
  }
  async confirmRecoveryCodeSaved(): Promise<void> { await this.empty("confirmRecoveryCodeSaved"); }
  continueRecoverySigning = async (oldCode: string): Promise<SecurityRecoveryInfo | undefined> => (await this.str("continueRecoverySigning", oldCode)).recovery;
  resumeRecovery = async (): Promise<SecurityRecoveryInfo | undefined> => (await this.empty("resumeRecovery")).recovery;
  completeRecovery = (): Promise<SecurityWorkflowResultEffect> => this.empty("completeRecovery");
  async discardUnsignedRecovery(): Promise<void> { await this.empty("discardUnsignedRecovery"); }
  async discardRejectedRecovery(): Promise<void> { await this.empty("discardRejectedRecovery"); }
  checkExitAdvice = async (): Promise<SecurityExitAdvice> => (await this.empty("checkExitAdvice")).exitAdvice!;
  async clearSecurity(): Promise<void> { await this.empty("clearSecurity"); }
  checkRetainedDenied = async (): Promise<boolean> => (await this.empty("checkRetainedDenied")).flag;
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
  async republishRecoveryObjects(flag = false): Promise<void> { await this.bool("republishRecoveryObjects", flag); }
  setForeground(active: boolean): void { this.host.setForeground(active); }
}

export interface StandaloneSecurityHandle {
  client: SecurityClient;
  dispose: () => void;
}

export function buildEntropyProvided(minIds: number, minFloats: number, minRandomBytes: number) {
  const nIds = Math.max(minIds, 16);
  const randomIds: string[] = [];
  for (let i = 0; i < nIds; i++) randomIds.push(crypto.randomUUID());
  const raw = new Uint32Array(Math.max(minFloats, 16));
  crypto.getRandomValues(raw);
  const unitFloats = Array.from(raw, (x) => x / 0x100000000);
  const randomBytes = minRandomBytes > 0
    ? crypto.getRandomValues(new Uint8Array(Math.max(minRandomBytes, 4096)))
    : new Uint8Array();
  return create(EntropyProvidedSchema, { randomIds, unitFloats, randomBytes });
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

  const exec = new WebSecurityExecutor({ store: opts.store ?? securityStoreV4, fetch: opts.fetch, emit: (ev) => dispatch(ev) });

  const dispatch = (ev: Event) => {
    if (closed) return;
    if (ev.nowMs === 0n) ev.nowMs = BigInt(Date.now());
    if (ev.monoMs === 0n) ev.monoMs = BigInt(Math.max(0, Math.floor(performance.now())));
    pending.push(ev);
    if (draining) return;
    draining = true;
    try {
      while (pending.length > 0 && !closed) {
        const batch = fromBinary(EffectBatchSchema, core.dispatch(toBinary(EventSchema, pending.shift()!)));
        for (const eff of batch.effects) {
          if (closed) break;
          handleEffect(eff);
        }
      }
    } finally {
      draining = false;
    }
  };

  const client = new SecurityClient({
    dispatchSecurityEvent: dispatch,
    setForeground: (foreground) =>
      !closed && dispatch(create(EventSchema, { event: { case: "securityForeground", value: create(SecurityForegroundEventSchema, { foreground }) } })),
    isClosed: () => closed,
  });

  const handleEffect = (eff: Effect) => {
    const val = eff.effect;
    if (dispatchSecurityEffect(exec, val)) return;
    switch (val.case) {
      case "requestEntropy":
        pending.push(
          create(EventSchema, {
            nowMs: BigInt(Date.now()),
            monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
            event: { case: "entropyProvided", value: buildEntropyProvided(val.value.minIds, val.value.minFloats, val.value.minRandomBytes) },
          }),
        );
        break;
      case "scheduleTimer": {
        const { timerId, kind, key, delayMs } = val.value;
        const tk = `${kind}:${key}`;
        const prev = timers.get(tk);
        if (prev) clearTimeout(prev.handle);
        const handle = setTimeout(() => {
          const cur = timers.get(tk);
          if (!cur || cur.id !== timerId) return;
          timers.delete(tk);
          if (!closed) dispatch(create(EventSchema, { event: { case: "timerFired", value: create(TimerFiredEventSchema, { timerId, kind, key }) } }));
        }, Math.max(0, Number(delayMs)));
        timers.set(tk, { id: timerId, handle });
        break;
      }
      case "cancelTimer": {
        const tk = `${val.value.kind}:${val.value.key}`;
        const cur = timers.get(tk);
        if (cur && (val.value.timerId === 0n || cur.id === val.value.timerId)) {
          clearTimeout(cur.handle);
          timers.delete(tk);
        }
        break;
      }
      case "securityTokenUpdated": opts.onToken?.(val.value); break;
      case "securityWorkflowResult": client.handleWorkflowResult(val.value); break;
      case "securityStatusChanged": client.handleStatusChanged(val.value.status!); break;
    }
  };

  const visibleNow = typeof document === "undefined" || document.visibilityState === "visible";
  dispatch(create(EventSchema, { event: { case: "securityConfig", value: create(SecurityConfigEventSchema, { inlineCrypto: false, foreground: visibleNow }) } }));

  const onVisibility = () => client.setForeground(typeof document === "undefined" || !document.hidden);
  const onHide = () => client.setForeground(false);
  globalThis.addEventListener?.("visibilitychange", onVisibility);
  globalThis.addEventListener?.("pagehide", onHide);
  globalThis.addEventListener?.("pageshow", onVisibility);

  return {
    client,
    dispose: () => {
      if (closed) return;
      closed = true;
      globalThis.removeEventListener?.("visibilitychange", onVisibility);
      globalThis.removeEventListener?.("pagehide", onHide);
      globalThis.removeEventListener?.("pageshow", onVisibility);
      for (const t of timers.values()) clearTimeout(t.handle);
      timers.clear();
      exec.close();
      client.rejectAll();
      core.close();
    },
  };
}
