import { create } from "@bufbuild/protobuf";
import * as l10n from "../i18n";
import {
  EntropyProvidedSchema,
  EventSchema,
  type Event,
} from "../gen/remoteagent/core/v1/app_pb";
import {
  SecurityActionEmptySchema,
  SecurityBoolActionSchema,
  SecurityErrorCode,
  SecurityRemoveDeviceActionSchema,
  SecurityStringActionSchema,
  SecuritySyncNowActionSchema,
  SecurityUpdateDeviceMetaActionSchema,
  SecurityWorkflowActionEventSchema,
  type SecurityWorkflowActionEvent,
  type SecurityWorkflowResultEffect,
  type SecurityWorkflowStatus,
} from "../gen/remoteagent/core/v1/security_pb";
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

export function translateSecurityErrorMessage(
  res: Pick<SecurityWorkflowResultEffect, "errorText" | "structuredErrorCode">,
): string {
  if (res.errorText) {
    const rendered = renderText(res.errorText);
    if (rendered) return rendered;
  }
  if (res.structuredErrorCode) {
    return SecurityErrorCode[res.structuredErrorCode] ?? "";
  }
  return "";
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
  async syncNow(touch = false, authToken = ""): Promise<SecurityWorkflowStatus> {
    return (await this.run({ case: "syncNow", value: create(SecuritySyncNowActionSchema, { touch, authToken }) })).status!;
  }
  renewLease = async (force = true): Promise<SecurityWorkflowStatus> => (await this.bool("renewLease", force)).status!;
  async removeDevice(deviceId: string): Promise<void> {
    await this.run({ case: "removeDevice", value: create(SecurityRemoveDeviceActionSchema, { deviceId }) });
  }
  async deleteMachineRecords(machineId: string): Promise<void> { await this.str("deleteMachineRecords", machineId); }
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
  setForeground(active: boolean): void { this.host.setForeground(active); }
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
