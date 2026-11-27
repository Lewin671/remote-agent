import { create, fromBinary, toBinary, type MessageInitShape } from "@bufbuild/protobuf";
import { useCallback, useEffect, useMemo, useRef, useSyncExternalStore } from "react";
import { AttachmentExecutor, type ImagePrepareFn } from "../attachment-executor";
import { MemoryAttachmentStore, WebAttachmentStore, type AttachmentStore } from "../attachment-store";
import { newRequestID, UnsupportedHistoryError } from "../crypto";
import * as l10n from "../i18n";
import { PEER_PROTOCOL_VERSION } from "../protocol";
import { describeBrowser } from "../devices";
import { createStandalonePushUnsubscriber, pushPrefs, pushState, pushSupported, restorePushPrefs, setPushPrefs, unregisterPushOnExit, type PushPrefs } from "../push";
import { clearEventCache } from "../eventcache";
import { clearMarkdownCache } from "../markdown";
import { getTheme, setTheme, type Theme } from "../theme";
import { checkUpdate } from "../update";

export class SecurityError extends Error {
  constructor(
    readonly code:
      | "invalid"
      | "untrusted"
      | "stale"
      | "unknown"
      | "conflict"
      | "pending"
      | "capacity"
      | "storage"
      | "unavailable"
      | "lease"
      | "clock" = "invalid",
  ) {
    super(`security: ${code}`);
  }
}

function structuredToSecurityCode(code: SecurityErrorCode): SecurityError["code"] {
  switch (code) {
    case SecurityErrorCode.LEASE_EXPIRED:
      return "lease";
    case SecurityErrorCode.STALE:
      return "stale";
    case SecurityErrorCode.UNTRUSTED:
      return "untrusted";
    case SecurityErrorCode.CLOCK_SKEWED:
      return "clock";
    case SecurityErrorCode.CAPACITY:
      return "capacity";
    case SecurityErrorCode.STORAGE:
      return "storage";
    case SecurityErrorCode.CONFLICT:
      return "conflict";
    case SecurityErrorCode.RECOVERY_UNAVAILABLE:
    case SecurityErrorCode.UNAVAILABLE:
      return "unavailable";
    default:
      return "invalid";
  }
}

function makeSecErr(code: SecurityError["code"]): SecurityError {
  return new SecurityError(code);
}
import {
  CacheBootstrapLoadedEventSchema,
  CacheEventsLoadedEventSchema,
  CancelMachineCommandActionSchema,
  CancelPingMachineActionSchema,
  CommandOutcomeEventSchema,
  DirNavigateActionSchema,
  DirPickerCloseActionSchema,
  DirPickerOpenActionSchema,
  EffectBatchSchema,
  EntropyProvidedSchema,
  EventSchema,
  HomeLoadMoreEndedActionSchema,
  HomeRetryLoadActionSchema,
  HomeScreenRequestSchema,
  HomeSelectSessionActionSchema,
  HomeSetFilterActionSchema,
  HomeSetScopeActionSchema,
  LocalCalendarSchema,
  LocaleChangedEventSchema,
  MonthStartSchema,
  NavigateRoute,
  NetworkChangedEventSchema,
  NewSessionCloseActionSchema,
  NewSessionOpenActionSchema,
  NewSessionProjectActionSchema,
  NewSessionScreenRequestSchema,
  NewSessionSelectAgentActionSchema,
  NewSessionSelectMachineActionSchema,
  NewSessionSelectModelActionSchema,
  NewSessionSubmitActionSchema,
  OutcomeSource,
  PingMachineActionSchema,
  ProjectAddActionSchema,
  ProjectKeyActionSchema,
  ProjectScreenRequestSchema,
  ProjectSetSearchActionSchema,
  RunMachineCommandActionSchema,
  ScreenSchema,
  ScreenTarget,
  SessionWatchEventSchema,
  StreamClosedEventSchema,
  StreamReadyEventSchema,
  SyncLifecycleActionSchema,
  SyncLifecycleKind,
  TimerFiredEventSchema,
  TimerKind,
  ViewModelSchema,
  type CacheBootstrapLoadedEvent,
  type CacheDeleteEffect,
  type CacheEventsLoadedEvent,
  type CacheWriteEffect,
  type CommandOutcomeEvent,
  type DispatchCommandEffect,
  type Effect,
  type Event,
  type LocalCalendar,
  type MonthStart,
  type StreamClosedEvent,
  type ViewModel,
} from "../gen/remoteagent/core/v1/app_pb";
import { AttachmentCandidateInputSchema } from "../gen/remoteagent/core/v1/attachments_view_pb";
import {
  AttachmentConfigEventSchema,
  AttachmentDraftScreenRequestSchema,
  AttachmentPauseUploadActionSchema,
  AttachmentResumeUploadActionSchema,
  DraftAddItemsActionSchema,
  DraftClearAllActionSchema,
  DraftForgetSessionActionSchema,
  DraftImageViewerCloseActionSchema,
  DraftImageViewerNavigateActionSchema,
  DraftImageViewerOpenActionSchema,
  DraftOpenActionSchema,
  DraftPickedItemSchema,
  DraftPutBackInputActionSchema,
  DraftReleaseSlotsActionSchema,
  DraftRemoveItemActionSchema,
  DraftReserveSlotsActionSchema,
  DraftRetryItemActionSchema,
  DraftScopeKind,
  DraftScopeSchema,
  DraftSendSessionActionSchema,
  DraftSetOriginalActionSchema,
  DraftSetTextActionSchema,
  DraftStorageBucket,
  type AttachmentDraftViewModel,
  type DraftScope,
} from "../gen/remoteagent/core/v1/attachments_pb";
import {
  PushRpcActionSchema,
  RefreshDevicesActionSchema,
  SessionFetchImageActionSchema,
  StreamRawFrameEventSchema,
  TerminalCloseActionSchema,
  TerminalConfirmRebuildActionSchema,
  TerminalInboundBytesEventSchema,
  TerminalInputActionSchema,
  TerminalOpenActionSchema,
  TerminalSocketEventSchema,
  type PushRpcResultEffect,
  type RelayRpcCancelEffect,
  type RelayRpcEffect,
  type SessionImageLoadedEffect,
  type TerminalCloseSocketEffect,
  type TerminalDeliverOutputEffect,
  type TerminalOpenSocketEffect,
  type TerminalStateChangedEffect,
} from "../gen/remoteagent/core/v1/relay_pipe_pb";
import {
  SecurityConfigEventSchema,
  SecurityErrorCode,
  SecurityForegroundEventSchema,
  SecurityOpRequestEventSchema,
  type SecurityOpRequestEvent,
  type SecurityOpResultEffect,
  type SecurityStatusChangedEffect,
  type SecurityTokenUpdatedEffect,
} from "../gen/remoteagent/core/v1/security_pb";
import { buildEntropyProvided, requestAuthorityPersistence, SecurityClient } from "./security-client";
import { dispatchSecurityEffect, WebSecurityExecutor } from "./security-executor";
import {
  AccountCachesClearedEventSchema,
  AccountSecurityActionKind,
  AccountSecurityActionSchema,
  AccountSecurityCloseActionSchema,
  AccountSecurityOpenActionSchema,
  AccountSecurityScreenRequestSchema,
  AccountSecuritySubpage,
  AddDeviceActionKind,
  AddDeviceActionSchema,
  AddDeviceCloseActionSchema,
  AddDeviceOpenActionSchema,
  AddDeviceScreenRequestSchema,
  AddDeviceStep,
  AppRootDismissUpgradeNoticeActionSchema,
  AppRootRetryBootstrapActionSchema,
  AppRootScreenRequestSchema,
  AppRootStage,
  AppRootVerifyPendingOpActionSchema,
  ClientInitEventSchema,
  CreateAccountStep,
  ExitConfirmActionSchema,
  ExitConfirmScreenRequestSchema,
  ExitConfirmStep,
  ExitIntentKind,
  ExitRequestActionSchema,
  ExternalLogoutEventSchema,
  HandoffActionKind,
  HandoffActionSchema,
  HandoffRole,
  HandoffScreenRequestSchema,
  JoinPairingStep,
  OnboardingCreateEditActionSchema,
  OnboardingCreateSubmitActionSchema,
  OnboardingJoinBannerActionSchema,
  OnboardingJoinCancelActionSchema,
  OnboardingJoinConfirmSasActionSchema,
  OnboardingJoinEditActionSchema,
  OnboardingJoinSubmitActionSchema,
  OnboardingMode,
  OnboardingRecoveryActionKind,
  OnboardingRecoveryActionSchema,
  OnboardingRecoveryEditActionSchema,
  OnboardingScreenRequestSchema,
  OnboardingSetModeActionSchema,
  PendingSecurityOpKind,
  PushUnregisteredEventSchema,
  SecurityBannerAction,
  SecurityBannerTone,
  SecurityRecoveryPhase,
  StoragePersistResultEventSchema,
  type AccountRuntimeChangedEffect,
  type AccountSecurityViewModel,
  type AddDeviceViewModel,
  type AppRootViewModel,
  type ExitConfirmView,
  type HandoffViewModel,
  type OnboardingViewModel,
} from "../gen/remoteagent/core/v1/security_view_pb";
import { renderText } from "./text";
import {
  CommandRouteLeg,
  DirectPreferenceEntrySchema,
  DirectPreferencesLoadedEventSchema,
  DirectRefreshDiagnosticsActionSchema,
  DirectRetryNowActionSchema,
  DirectSignalDeliveryEventSchema,
  DirectSignalReceivedEventSchema,
  TerminalStreamEventKind,
  TerminalStreamEventSchema,
  TransportConfigEventSchema,
  type DirectDcReadStatsEffect,
  type DirectPcAddCandidatesEffect,
  type DirectPcCloseEffect,
  type DirectPcConfirmEffect,
  type DirectPcCreateEffect,
  type DirectPcDescribeEffect,
  type DirectPcSendProbeEffect,
  type DirectPcSetRemoteEffect,
  type DirectPreferenceEntry,
  type DirectSignalSendEffect,
  type TerminalDirectSendEffect,
} from "../gen/remoteagent/core/v1/transport_pb";
import { ApprovalMode, SendMode, type Agent } from "../gen/remoteagent/v1/event_pb";
import { PlanChoice, UsageRangePreset } from "../gen/remoteagent/v1/local_pb";
import {
  ConnState,
  HomeAbandonAttentionActionSchema,
  HomeAnswerQuestionActionSchema,
  HomeEditApprovalDenyActionSchema,
  HomeResolveApprovalActionSchema,
  HomeRetryAttentionActionSchema,
  type HomeViewModel,
} from "../gen/remoteagent/core/v1/home_view_pb";
import { CommandResultSchema, CommandSchema } from "../gen/remoteagent/v1/remote_pb";
import {
  MachineChartKind,
  MachinePollMode,
  MachineStatusCleanupEventSchema,
  MachineStatusPollTickEventSchema,
  MachineStatusRetryEventSchema,
  MachineStatusScreenRequestSchema,
  MachineStatusScrubEventSchema,
  MachineStatusTabEventSchema,
  MachineStatusWatchEventSchema,
  MachineUsageTab,
  QuotaScreenRequestSchema,
  type MachineStatusViewModel,
  type PrepareCleanupSessionEffect,
  type QuotaViewModel,
} from "../gen/remoteagent/core/v1/machine_view_pb";
import type { NewSessionViewModel } from "../gen/remoteagent/core/v1/new_session_view_pb";
import type { ProjectViewModel } from "../gen/remoteagent/core/v1/project_view_pb";
import {
  ScheduleBoolActionSchema,
  ScheduleCacheLoadedEventSchema,
  ScheduleCacheWrittenEventSchema,
  ScheduleDetailScreenRequestSchema,
  ScheduleFormBoolActionSchema,
  ScheduleFormOpenActionSchema,
  ScheduleFormScreenRequestSchema,
  ScheduleFormSelectAgentActionSchema,
  ScheduleFormSelectModelActionSchema,
  ScheduleFormSelectProjectActionSchema,
  ScheduleFormStringActionSchema,
  ScheduleFormUpdateTimingActionSchema,
  ScheduleLocalContextEventSchema,
  ScheduleMarkReadActionSchema,
  ScheduleMutationRefSchema,
  ScheduleNavigateRoute,
  ScheduleOperateActionSchema,
  ScheduleSeenLoadedEventSchema,
  ScheduleSessionStripScreenRequestSchema,
  SchedulesScreenRequestSchema,
  ScheduleStringActionSchema,
  ScheduleTaskBoolActionSchema,
  ScheduleTaskRefSchema,
  ScheduleTaskStringActionSchema,
  type ScheduleCacheLoadedEvent,
  type ScheduleDetailViewModel,
  type ScheduleFormMode,
  type ScheduleFormViewModel,
  type ScheduleNotificationRef,
  type ScheduleOperationKind,
  type ScheduleSeenLoadedEvent,
  type ScheduleSeenToken,
  type ScheduleSessionStripViewModel,
  type SchedulesViewModel,
} from "../gen/remoteagent/core/v1/schedules_view_pb";
import {
  SessionAnswerPlanActionSchema,
  SessionAnswerQuestionActionSchema,
  SessionEditApprovalDenyActionSchema,
  SessionImageViewerCloseActionSchema,
  SessionImageViewerNavigateActionSchema,
  SessionImageViewerOpenActionSchema,
  SessionOpenActionSchema,
  SessionRefreshHistoryActionSchema,
  SessionRenameActionSchema,
  SessionResolveApprovalActionSchema,
  SessionRetryCommandActionSchema,
  SessionRetryLoadActionSchema,
  SessionScreenRequestSchema,
  SessionSetApprovalModeActionSchema,
  SessionSetModelActionSchema,
  SessionSimpleCommandActionSchema,
  SessionSimpleCommandKind,
  SessionSlotActionSchema,
  SessionToggleQuestionOptionActionSchema,
  SessionTrayItemActionSchema,
  SessionTraySessionActionSchema,
  SessionWindowRequestSchema,
  SharedFileFailureKind,
  type SessionHeaderView,
  type SessionRestoreComposerEffect,
  type SessionViewModel,
} from "../gen/remoteagent/core/v1/session_view_pb";
import {
  SharedFileCancelDownloadActionSchema,
  SharedFileClearAllActionSchema,
  SharedFileConfigEventSchema,
  SharedFileDeleteLocalActionSchema,
  SharedFileDurationProbedEventSchema,
  SharedFileForgetSessionActionSchema,
  SharedFilePauseDownloadActionSchema,
  SharedFileSetVisibleSessionActionSchema,
  SharedFileStartDownloadActionSchema,
} from "../gen/remoteagent/core/v1/shared_files_pb";
import { SharedFileExecutor, WEB_LOCAL_CACHE_MAX_BYTES } from "../shared-files";
import { sharedFileStore, type SharedFileStore } from "../shared-file-store";
import { registerActiveAttachmentClearer, registerActiveSharedFileClearer, securityStoreV4, type SecurityStoreV4 } from "./security-store-v4";
import {
  CheckForUpdatesEventSchema,
  ClientDetailScreenRequestSchema,
  ClientUpdateStateEventSchema,
  ConnCheckStatusEventSchema,
  ConnProbeState,
  DirectDiagnosticsScreenRequestSchema,
  MachineSettingsOpenEventSchema,
  MachineSettingsScreenRequestSchema,
  MachineSettingsSelectAgentEventSchema,
  NotificationPermissionState,
  PushRegistrationState,
  SetAppearancePrefEventSchema,
  SetNotificationPrefEventSchema,
  ThemePreference,
  ToggleMachineAgentEventSchema,
  UpdateClientNameEventSchema,
  UpdateDirectPreferenceEventSchema,
  UpdateMachineAgentDefaultsEventSchema,
  UpdateMachineDaemonSettingsEventSchema,
  type ClientDetailViewModel,
  type ClientUpdateStateEvent,
  type DevicesViewModel,
  type DirectDiagnosticsViewModel,
  type MachineSettingsViewModel,
  type SetNotificationPrefEvent,
  type SettingsViewModel,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import {
  UsageCacheLoadedEventSchema,
  UsageCacheWrittenEventSchema,
  UsageScreenOpenEventSchema,
  UsageScreenRequestSchema,
  UsageScreenRetryEventSchema,
  UsageScreenScrubEventSchema,
  UsageScreenSelectSeriesEventSchema,
  UsageScreenSelectSortEventSchema,
  UsageSortColumn,
  type UsageViewModel,
} from "../gen/remoteagent/core/v1/usage_view_pb";
import {
  ConnBannerKind,
  ConnectionScreenRequestSchema,
  type ConnectionViewModel,
  type WatchedSessionRoute,
} from "../gen/remoteagent/core/v1/connection_view_pb";
import {
  TerminalHistoryEventKind,
  TerminalHistoryEventSchema,
  TerminalHistoryPhase,
  TerminalHistoryStatus,
  TerminalRunWidth,
  TerminalSgrColorKind,
  type TerminalHistoryParsedPage,
  type TerminalHistoryRun,
  type TerminalHistorySendRequestEffect,
  type TerminalHistoryUpdatedEffect,
} from "../gen/remoteagent/core/v1/terminal_history_pb";
import type { Remote } from "../remote";
import type { WasmAppCore } from "./projection";
import { getCore } from "./runtime";

const SCOPE_STORAGE_KEY = "ra-scope";
const CLOCK_TICK_MS = 60_000;
const CALENDAR_MONTH_HISTORY = 36;
const INITIAL_ENTROPY_COUNT = 32;

export type Go = (route: string, replace?: boolean) => void;

export interface DraftCandidateEntry {
  file?: Blob;
  name: string;
  mimeType: string;
  sizeBytes: number;
  isDirectory?: boolean;
}

export interface StreamLegCallbacks {
  onReady(legId: bigint, serverTimeMs: bigint): void;
  onRawFrame(legId: bigint, rawServerFrame: Uint8Array<ArrayBuffer>): void;
  onClosed(legId: bigint, info: Omit<StreamClosedEvent, "$typeName" | "legId">): void;
}

export interface HostBridge {
  start(): void;
  stop(): void;
  setForeground(active: boolean): void;
  setNetwork(available: boolean, signature?: string): void;
  reconnectNow(networkChanged?: boolean): void;
  retryLogin(): void;
  loadMoreSessions(): Promise<boolean>;
  retryList(): void;
  retryPrefs(): void;
  retrySession(sessionId: string): void;
  retryEvents(sessionId: string): void;
  refreshDevices(): Promise<void>;
  pushRpc(req: Omit<MessageInitShape<typeof PushRpcActionSchema>, "opId">): Promise<PushRpcResultEffect>;
  fetchSessionImage(sessionId: string, imageId: string, machineId?: string, cachedEnvelope?: Uint8Array<ArrayBuffer>): Promise<SessionImageLoadedEffect>;
  terminalOpen(opId: bigint, sessionId: string, machineId: string, terminalId: string): void;
  terminalClose(streamId: string, opId?: bigint): void;
  terminalSocket(ev: MessageInitShape<typeof TerminalSocketEventSchema>): void;
  terminalInboundBytes(streamId: string, rawEnvelope: Uint8Array<ArrayBuffer>, fromDirect: boolean, pcId: bigint): void;
  terminalInput(req: MessageInitShape<typeof TerminalInputActionSchema>): void;
  terminalConfirmRebuild(streamId: string): void;
  readonly securityClient: SecurityClient;
  securityOp(op: SecurityOpRequestEvent["op"]): Promise<SecurityOpResultEffect>;
  dispatchDirectSignal(raw: Uint8Array<ArrayBuffer>): void;
  dispatchDirectSignalDelivery(frameId: Uint8Array<ArrayBuffer>, status: number, retryAfterMs: number): void;
  pingMachine(machineId: string, signal?: AbortSignal): Promise<void>;
  runMachineCommand(
    machineId: string,
    sessionId: string,
    cmd: MessageInitShape<typeof CommandSchema>["kind"],
    signal?: AbortSignal,
    skipConnectivityPreflight?: boolean,
  ): Promise<void>;
  changeProjectPref(kind: "pin" | "unpin" | "hide" | "unhide", path: string): void;
  dispatchEvent(ev: Event, onProcessed?: () => void): void;
  subscribe(target: ScreenTarget, fn: () => void): () => void;
  viewConnection(): ConnectionViewModel;
  viewHome(endedLimit?: number): HomeViewModel;
  viewSession(sessionId: string): SessionViewModel;
}

export interface CoreExecutor {
  openStreamLeg(legId: bigint, token: string, callbacks: StreamLegCallbacks, helloFrame?: Uint8Array<ArrayBuffer>): void;
  closeStreamLeg(legId: bigint): void;
  securityConfig?(): { account: string; deviceId: string; server?: string } | undefined;
  readBootstrapCache(opId: bigint): Promise<CacheBootstrapLoadedEvent>;
  readEventsCache(
    opId: bigint,
    sessionId: string,
    newest: boolean,
    afterSeq: bigint,
    limit: number,
  ): Promise<CacheEventsLoadedEvent>;
  prepareCacheWrite?(eff: CacheWriteEffect): (() => Promise<void>) | undefined;
  releaseEventRefs?(ev: Event): void;
  writeCache(eff: CacheWriteEffect): Promise<void>;
  deleteCache(eff: CacheDeleteEffect): Promise<void>;
  relayRpc?(eff: RelayRpcEffect): void;
  relayRpcCancel?(eff: RelayRpcCancelEffect): void;
  executeCommandAttempt(eff: DispatchCommandEffect, signal?: AbortSignal): Promise<CommandOutcomeEvent>;
  readScheduleCache?(opId: bigint, machineId: string): Promise<ScheduleCacheLoadedEvent>;
  writeScheduleCache?(machineId: string, blob: Uint8Array): Promise<void>;
  deleteScheduleCache?(machineId: string): Promise<void>;
  loadScheduleSeen?(machineId: string): Promise<ScheduleSeenLoadedEvent>;
  saveScheduleSeen?(machineId: string, tokens: ScheduleSeenToken[]): Promise<void>;
  dismissScheduleNotifications?(runs: ScheduleNotificationRef[]): void;
  setWatchedSessions?(routes: WatchedSessionRoute[]): void;
  securityEffect?(eff: Effect["effect"]): void;
  securityTokenUpdated?(eff: SecurityTokenUpdatedEffect): void;
  securityStatusChanged?(eff: SecurityStatusChangedEffect): void;
  directSignalSend?(eff: DirectSignalSendEffect): void;
  directPcCreate?(eff: DirectPcCreateEffect): void;
  directPcDescribe?(eff: DirectPcDescribeEffect): void;
  directPcSetRemote?(eff: DirectPcSetRemoteEffect): void;
  directPcAddCandidates?(eff: DirectPcAddCandidatesEffect): void;
  directPcConfirm?(eff: DirectPcConfirmEffect): void;
  directPcClose?(eff: DirectPcCloseEffect): void;
  directPcSendProbe?(eff: DirectPcSendProbeEffect): void;
  directDcReadStats?(eff: DirectDcReadStatsEffect): void;
  terminalDirectSend?(eff: TerminalDirectSendEffect): void;
  terminalOpenSocket?(eff: TerminalOpenSocketEffect): void;
  terminalCloseSocket?(eff: TerminalCloseSocketEffect): void;
  terminalDeliverOutput?(eff: TerminalDeliverOutputEffect): void;
  terminalStateChanged?(eff: TerminalStateChangedEffect): void;
}

export interface SessionWindowOptions {
  limit?: number;
  beforeSeq?: bigint;
  aroundSeq?: bigint;
  revealSeq?: bigint;
  revealRunId?: string;
  expandedGroupIds?: string[];
}

interface CoreHostOptions {
  /** Overrides the executor used to execute Core Effects (defaults to Remote). */
  executor?: CoreExecutor;
  /** Optional observer hook called for every executed Effect (primarily for tests). */
  onEffect?: (effect: Effect) => void;
  go?: Go;
  /** Optional explicit HMAC key for scope tokens (tests without IDB). */
  scopeHmacKey?: Uint8Array;
  /** Optional shared-file store override (defaults to remote.sharedFileStore or OPFS singleton). */
  sharedFileStore?: SharedFileStore;
  /** Optional large-file (> 50 MiB) delivery hook (defaults to browser download save). */
  onDeliverLargeSharedFile?: (blob: Blob, displayName: string) => void;
  /** Optional attachment store override (defaults to remote.attachmentStore or persistent WebAttachmentStore). */
  attachmentStore?: AttachmentStore;
  /** Optional image preparation hook (defaults to browser compressImage/prepareOriginal). */
  prepareImage?: ImagePrepareFn;
  /** Optional video duration probe hook (defaults to browser <video> metadata probe). */
  probeVideoDurationMs?: (blob: Blob) => Promise<number | undefined>;
  /** Optional security store override for standalone CoreHost before Remote attaches. */
  securityStore?: SecurityStoreV4;
  /** Optional fetch override for standalone CoreHost security RPCs before Remote attaches. */
  securityFetch?: typeof fetch;
  /** Optional listener for Core's AccountRuntimeChangedEffect (active/removed transitions). */
  onAccountRuntimeChanged?: (eff: AccountRuntimeChangedEffect) => void;
}

/** Current browser IANA timezone ID (falls back to UTC when unavailable). */
function localTimeZoneId(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** Computes local timezone and tomorrow/day-after-tomorrow day boundaries for Core's ScheduleLocalContextEvent. */
function scheduleLocalContext(nowMs = Date.now()) {
  const d = new Date(nowMs);
  const tomorrow = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1).getTime();
  const dayAfter = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 2).getTime();
  return create(ScheduleLocalContextEventSchema, {
    localTimezone: localTimeZoneId(),
    tomorrowStartMs: BigInt(tomorrow),
    dayAfterTomorrowStartMs: BigInt(dayAfter),
  });
}

/** Computes local-timezone calendar boundaries + 36 month starts for Core's ClockUpdated event. */
function localCalendar(nowMs = Date.now()): LocalCalendar {
  const d = new Date(nowMs);
  const todayStart = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const yesterdayStart = new Date(d.getFullYear(), d.getMonth(), d.getDate() - 1).getTime();
  const weekStart = new Date(d.getFullYear(), d.getMonth(), d.getDate() - 6).getTime();
  const months: MonthStart[] = [];
  for (let i = CALENDAR_MONTH_HISTORY - 1; i >= 0; i--) {
    const md = new Date(d.getFullYear(), d.getMonth() - i, 1);
    months.push(
      create(MonthStartSchema, {
        year: md.getFullYear(),
        month: md.getMonth() + 1,
        startMs: BigInt(md.getTime()),
      }),
    );
  }
  return create(LocalCalendarSchema, {
    nowMs: BigInt(nowMs),
    todayStartMs: BigInt(todayStart),
    yesterdayStartMs: BigInt(yesterdayStart),
    weekStartMs: BigInt(weekStart),
    nowYear: d.getFullYear(),
    months,
  });
}

function readStoredScope(): string {
  try {
    const raw = localStorage.getItem(SCOPE_STORAGE_KEY) ?? "";
    return !raw || raw === "*" ? "all" : raw;
  } catch {
    return "all";
  }
}

function writeStoredScope(scopeToken: string) {
  try {
    localStorage.setItem(SCOPE_STORAGE_KEY, scopeToken || "*");
  } catch {
    /* Private storage may be unavailable. */
  }
}

function randomFloats(count: number): number[] {
  const arr = new Uint32Array(count);
  crypto.getRandomValues(arr);
  return Array.from(arr, (n) => n / 0x1_0000_0000);
}

function randomIds(count: number): string[] {
  return Array.from({ length: count }, () => newRequestID());
}

function defaultRaceTimeout<T>(work: Promise<T>, ms: number): Promise<T | undefined> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  return Promise.race([
    work.finally(() => clearTimeout(timer)),
    new Promise<undefined>((resolve) => {
      timer = setTimeout(() => resolve(undefined), ms);
    }),
  ]);
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function routeForSession(sessionId: string): string {
  return "s/" + encodeURIComponent(sessionId);
}

export function routeForProject(projectKey: string): string {
  return projectKey ? "p/" + encodeURIComponent(projectKey) : "projects";
}

export function routeForProjects(machineId = ""): string {
  return machineId ? "projects/" + encodeURIComponent(machineId) : "projects";
}

export function routeForMachineSettings(machineId: string): string {
  return "m/" + machineId;
}

export function routeForMachineStorage(machineId: string): string {
  return "m/" + encodeURIComponent(machineId) + "/storage";
}

export function routeForMachineCli(machineId: string, agent: Agent): string {
  return "m/" + encodeURIComponent(machineId) + "/cli/" + encodeURIComponent(String(agent));
}

export function routeForMachineStatus(machineId: string): string {
  return "machine-status/" + encodeURIComponent(machineId);
}

export function routeForTask(machineId: string, taskId: string): string {
  return "tasks/" + encodeURIComponent(machineId) + "/" + encodeURIComponent(taskId);
}

export function routeForUsage(machineId: string, focusAgent?: Agent, focusModel?: string): string {
  const base = "u/" + encodeURIComponent(machineId);
  if (focusAgent && focusModel) {
    return `${base}/${encodeURIComponent(String(focusAgent))}/${encodeURIComponent(focusModel)}`;
  }
  return base;
}

const USAGE_RETENTION_DAYS = 90;

export function localTzOffsetMinutes(nowMs = Date.now()): number {
  return -new Date(nowMs).getTimezoneOffset();
}

export function clampUsageCustomRange(
  customStartMs: bigint,
  customEndMs: bigint,
  nowMs = Date.now(),
): { startMs: bigint; endMs: bigint } {
  const now = new Date(nowMs);
  const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
  const minDay = new Date(now.getFullYear(), now.getMonth(), now.getDate() - (USAGE_RETENTION_DAYS - 1)).getTime();
  const sDate = customStartMs > 0n ? new Date(Number(customStartMs)) : new Date(now.getFullYear(), now.getMonth(), now.getDate() - 6);
  const eDate = customEndMs > 0n ? new Date(Number(customEndMs)) : new Date(todayStart);
  let sDay = new Date(sDate.getFullYear(), sDate.getMonth(), sDate.getDate()).getTime();
  let eDay = new Date(eDate.getFullYear(), eDate.getMonth(), eDate.getDate()).getTime();
  if (eDay < sDay) eDay = sDay;
  if (sDay < minDay) sDay = minDay;
  if (eDay > todayStart) eDay = todayStart;
  if (eDay < sDay) sDay = eDay;
  return { startMs: BigInt(sDay), endMs: BigInt(eDay) };
}

export function buildUsageOpenParams(
  preset: UsageRangePreset,
  customStartMs = 0n,
  customEndMs = 0n,
  nowMs = Date.now(),
): {
  preset: UsageRangePreset;
  customStartMs: bigint;
  customEndMs: bigint;
  timezone: string;
  tzOffsetMinutes: number;
  bucketBoundsMs: bigint[];
  prevStartMs: bigint;
} {
  const effectivePreset = preset === UsageRangePreset.UNSPECIFIED ? UsageRangePreset.SEVEN_DAYS : preset;
  const now = new Date(nowMs);
  const todayStartDate = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const tomorrowStartDate = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);

  let startDayDate: Date;
  let endExclDate: Date;
  let normCustomStart = 0n;
  let normCustomEnd = 0n;

  if (effectivePreset === UsageRangePreset.TODAY) {
    startDayDate = todayStartDate;
    endExclDate = tomorrowStartDate;
  } else if (effectivePreset === UsageRangePreset.THIRTY_DAYS) {
    startDayDate = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 29);
    endExclDate = tomorrowStartDate;
  } else if (effectivePreset === UsageRangePreset.CUSTOM) {
    const clamped = clampUsageCustomRange(customStartMs, customEndMs, nowMs);
    normCustomStart = clamped.startMs;
    normCustomEnd = clamped.endMs;
    startDayDate = new Date(Number(clamped.startMs));
    const endIncDate = new Date(Number(clamped.endMs));
    endExclDate = new Date(endIncDate.getFullYear(), endIncDate.getMonth(), endIncDate.getDate() + 1);
  } else {
    startDayDate = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 6);
    endExclDate = tomorrowStartDate;
  }

  let numDays = 0;
  for (let d = new Date(startDayDate.getFullYear(), startDayDate.getMonth(), startDayDate.getDate()); d.getTime() < endExclDate.getTime() && numDays <= USAGE_RETENTION_DAYS + 2; d = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1)) {
    numDays++;
  }

  const bounds: bigint[] = [];
  if (numDays <= 1) {
    const endMs = endExclDate.getTime();
    for (let cur = startDayDate.getTime(); cur < endMs; cur += 3600_000) {
      bounds.push(BigInt(cur));
    }
    bounds.push(BigInt(endMs));
  } else if (numDays <= 7) {
    let day = new Date(startDayDate.getFullYear(), startDayDate.getMonth(), startDayDate.getDate());
    while (day.getTime() < endExclDate.getTime()) {
      const nextDay = new Date(day.getFullYear(), day.getMonth(), day.getDate() + 1);
      const span = nextDay.getTime() - day.getTime();
      for (let q = 0; q < 4; q++) {
        bounds.push(BigInt(Math.round(day.getTime() + (span * q) / 4)));
      }
      day = nextDay;
    }
    bounds.push(BigInt(endExclDate.getTime()));
  } else {
    let cur = new Date(startDayDate.getFullYear(), startDayDate.getMonth(), startDayDate.getDate());
    while (cur.getTime() < endExclDate.getTime()) {
      bounds.push(BigInt(cur.getTime()));
      cur = new Date(cur.getFullYear(), cur.getMonth(), cur.getDate() + 1);
    }
    bounds.push(BigInt(endExclDate.getTime()));
  }

  const prevStartDate = new Date(startDayDate.getFullYear(), startDayDate.getMonth(), startDayDate.getDate() - numDays);
  return {
    preset: effectivePreset,
    customStartMs: normCustomStart,
    customEndMs: normCustomEnd,
    timezone: localTimeZoneId(),
    tzOffsetMinutes: localTzOffsetMinutes(nowMs),
    bucketBoundsMs: bounds,
    prevStartMs: BigInt(prevStartDate.getTime()),
  };
}

function encodeBytesBase64(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin);
}

function decodeBytesBase64(b64: string): Uint8Array {
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

const USAGE_CACHE_PRESETS: UsageRangePreset[] = [
  UsageRangePreset.TODAY,
  UsageRangePreset.SEVEN_DAYS,
  UsageRangePreset.THIRTY_DAYS,
];

const SETTINGS_TARGETS = [
  ScreenTarget.SETTINGS,
  ScreenTarget.MACHINE_STATUS,
  ScreenTarget.MACHINE_SETTINGS,
  ScreenTarget.QUOTA,
  ScreenTarget.DEVICES,
  ScreenTarget.CLIENT_DETAIL,
  ScreenTarget.USAGE,
  ScreenTarget.DIRECT_DIAGNOSTICS,
];

const SECURITY_TARGETS = [
  ScreenTarget.APP_ROOT,
  ScreenTarget.ONBOARDING,
  ScreenTarget.ADD_DEVICE,
  ScreenTarget.ACCOUNT_SECURITY,
  ScreenTarget.HANDOFF,
  ScreenTarget.EXIT_CONFIRM,
];

const ALL_TARGETS = [
  ScreenTarget.HOME,
  ScreenTarget.PROJECT,
  ScreenTarget.SESSION,
  ScreenTarget.NEW_SESSION,
  ScreenTarget.CONNECTION,
  ScreenTarget.SCHEDULES,
  ScreenTarget.SCHEDULE_DETAIL,
  ScreenTarget.SCHEDULE_FORM,
  ScreenTarget.SCHEDULE_SESSION_STRIP,
  ScreenTarget.ATTACHMENT_DRAFT,
  ...SETTINGS_TARGETS,
  ...SECURITY_TARGETS,
];

function draftScopeKey(scope: Pick<DraftScope, "kind" | "id">): string {
  return `${scope.kind}:${scope.id}`;
}

export function sessionDraftScope(sessionId: string): DraftScope {
  return create(DraftScopeSchema, { kind: DraftScopeKind.SESSION, id: sessionId });
}

export function newSessionDraftScope(projectKey: string): DraftScope {
  return create(DraftScopeSchema, { kind: DraftScopeKind.NEW_SESSION, id: projectKey });
}

function parseDraftStorageKeyToScope(key: string): DraftScope | undefined {
  if (key.startsWith("session:") && key.length > "session:".length) {
    return sessionDraftScope(key.slice("session:".length));
  }
  if (key.startsWith("new:") && key.length > "new:".length) {
    return newSessionDraftScope(key.slice("new:".length));
  }
  return undefined;
}

function toCoreTheme(t: Theme): ThemePreference {
  switch (t) {
    case "light": return ThemePreference.LIGHT;
    case "dark": return ThemePreference.DARK;
    default: return ThemePreference.AUTO;
  }
}

function fromCoreTheme(t: ThemePreference): Theme {
  switch (t) {
    case ThemePreference.LIGHT: return "light";
    case ThemePreference.DARK: return "dark";
    default: return "auto";
  }
}

function readNotificationState(): {
  supported: boolean;
  permission: NotificationPermissionState;
  pushState: PushRegistrationState;
  prefs: PushPrefs;
} {
  const prefs = pushPrefs();
  let state: ReturnType<typeof pushState> = "unsupported";
  let supported = false;
  try {
    supported = pushSupported();
    state = typeof matchMedia === "function" ? pushState() : (supported && Notification.permission === "granted" ? "ok" : "unsupported");
  } catch {
    state = "unsupported";
  }
  const permission = state === "ios-browser"
    ? NotificationPermissionState.NEEDS_HOME_SCREEN
    : state === "unsupported"
      ? NotificationPermissionState.UNSUPPORTED
      : state === "denied"
        ? NotificationPermissionState.DENIED
        : state === "ok"
          ? NotificationPermissionState.GRANTED
          : NotificationPermissionState.UNSPECIFIED;
  const anyEnabled = prefs.attention || prefs.turns || prefs.scheduled !== false;
  const regState = permission === NotificationPermissionState.GRANTED && anyEnabled
    ? PushRegistrationState.ACTIVE
    : PushRegistrationState.DISABLED;
  return { supported, permission, pushState: regState, prefs };
}

function writeStoredPushPrefs(prefs: PushPrefs) {
  try {
    localStorage.setItem("ra-push", JSON.stringify(prefs));
  } catch { /* ignore storage failure */ }
}

function timerKey(kind: TimerKind, key: string): string {
  return `${kind}:${key}`;
}

/**
 * Single-threaded main-thread Core shell wrapping Go's WasmAppCore.
 * Feeds browser time/entropy/lifecycle/network/UI events into Core,
 * executes all emitted Effects, and publishes per-screen Render notifications.
 */
export class CoreHost {
  private readonly core: WasmAppCore;
  private _remote?: Remote;
  private _executor?: CoreExecutor;
  private readonly explicitExecutor?: CoreExecutor;
  private readonly ownSecExec?: WebSecurityExecutor;
  private readonly onEffect?: (effect: Effect) => void;
  private onAccountRuntimeChanged?: (eff: AccountRuntimeChangedEffect) => void;
  private lastRuntimeEffect?: AccountRuntimeChangedEffect;
  private readonly explicitScopeHmacKey?: Uint8Array;
  private go?: Go;

  get remote(): Remote | undefined {
    return this._remote;
  }

  private get executor(): CoreExecutor | undefined {
    return this._executor;
  }

  private readonly screenVersions = new Map<ScreenTarget, number>(ALL_TARGETS.map((t) => [t, 0]));
  private readonly listeners = new Map<ScreenTarget, Set<() => void>>(ALL_TARGETS.map((t) => [t, new Set()]));
  private totalListeners = 0;

  private homeCache = new Map<number, { version: number; vm: HomeViewModel }>();
  private projectCache = new Map<string, { version: number; vm: ProjectViewModel }>();
  private sessionCache = new Map<string, { version: number; vm: SessionViewModel }>();
  private newSessionCache?: { version: number; key: string; vm: NewSessionViewModel };
  private connectionCache?: { version: number; vm: ConnectionViewModel };
  private schedulesCache?: { version: number; endedLimit: number; vm: SchedulesViewModel };
  private scheduleDetailCache?: { version: number; key: string; vm: ScheduleDetailViewModel };
  private scheduleFormCache = new Map<string, { version: number; vm: ScheduleFormViewModel }>();
  private scheduleStripCache = new Map<string, { version: number; vm: ScheduleSessionStripViewModel }>();
  private settingsCache?: { version: number; vm: SettingsViewModel };
  private machineStatusCache = new Map<string, { version: number; vm: MachineStatusViewModel }>();
  private machineSettingsCache = new Map<string, { version: number; vm: MachineSettingsViewModel }>();
  private quotaCache = new Map<string, { version: number; vm: QuotaViewModel }>();
  private devicesCache?: { version: number; vm: DevicesViewModel };
  private clientDetailCache = new Map<string, { version: number; vm: ClientDetailViewModel }>();
  private usageCache = new Map<string, { version: number; vm: UsageViewModel }>();
  private directDiagnosticsCache = new Map<string, { version: number; vm: DirectDiagnosticsViewModel }>();
  private attachmentDraftCache = new Map<string, { version: number; vm: AttachmentDraftViewModel }>();
  private appRootCache?: { version: number; vm: AppRootViewModel };
  private onboardingCache?: { version: number; vm: OnboardingViewModel };
  private addDeviceCache?: { version: number; vm: AddDeviceViewModel };
  private accountSecurityCache?: { version: number; vm: AccountSecurityViewModel };
  private handoffCache?: { version: number; vm: HandoffViewModel };
  private exitConfirmCache?: { version: number; vm: ExitConfirmView };
  private knownDraftScopes = new Map<string, DraftScope>();
  private usageHydratedMachines = new Set<string>();
  private nextMachineCmdOpId = 0n;
  private machineCmdWaiters = new Map<bigint, { resolve: () => void; reject: (err: Error) => void }>();
  private nextPingOpId = 0n;
  private pingWaiters = new Map<bigint, { resolve: () => void; reject: (err: Error) => void }>();
  private inFlightDispatches = new Map<string, Map<string, AbortController>>();
  private retryNowWaiters = new Map<string, Array<(started: boolean) => void>>();
  private terminalReopenListeners = new Map<string, () => void>();
  private terminalHistoryRequestListeners = new Map<string, (req: TerminalHistorySendRequestEffect) => void>();
  private terminalHistoryUpdatedListeners = new Map<string, (upd: TerminalHistoryUpdatedEffect) => void>();

  private started = false;
  private startGen = 0;
  private closed = false;
  private watchCounts = new Map<string, number>();
  private restoreComposerListeners = new Map<string, Set<(eff: SessionRestoreComposerEffect) => void>>();
  private scheduleFormCloseListeners = new Map<string, Set<() => void>>();
  private scheduleFormTimingListeners = new Map<string, Set<(formKey: string) => void>>();
  private scheduleFocus = new Map<string, { runId: string; seq: bigint }>();
  private openFormKeys = new Set<string>();
  private timers = new Map<string, { id: bigint; handle: ReturnType<typeof setTimeout> }>();
  private machinePollTimers = new Map<string, { generation: bigint; handle: ReturnType<typeof setTimeout> }>();
  private machineWatchCounts = new Map<string, { live: number; overview: number }>();
  private machineWatchActive = new Map<string, { visible: boolean; mode: MachinePollMode }>();
  private revertingDirect = new Set<string>();
  private syncingPrefs = false;
  private revertingPushPrefs = false;
  private lastPushPrefSig?: string;
  private lastPersistedPrefs?: PushPrefs;
  private directToggleErrors = new Map<string, string>();
  private clientRenameStates = new Map<string, { saving: boolean; error: string }>();
  private cleanupPickerListeners = new Set<(eff: PrepareCleanupSessionEffect) => void>();
  private extraListeners = new Set<() => void>();
  private extraVersion = 0;
  private clockTimer?: ReturnType<typeof setInterval>;
  private unsubscribeLocale?: () => void;
  private unregisterSharedFileClearer?: () => void;
  private unregisterAttachmentClearer?: () => void;
  readonly sharedFiles: SharedFileExecutor;
  readonly attachments: AttachmentExecutor;
  private attachmentConfigured = false;
  private cacheWriteQueue: Promise<void> = Promise.resolve();

  private pendingEvents: Array<{ ev: Event; onProcessed?: () => void }> = [];
  private draining = false;
  private dirtyTargets = new Set<ScreenTarget>();

  private pendingStart = false;
  private lastProjectPrefEvent?: Event;
  private bootstrapped = false;
  private preloadedBootstrap?: CacheBootstrapLoadedEvent;
  private securityConfigured = false;
  private securityBootstrapped = false;
  private securityBootstrapPromise?: Promise<void>;
  readonly securityClient: SecurityClient;
  private nextSecurityOpId = 1n;
  private securityOpWaiters = new Map<bigint, { resolve: (eff: SecurityOpResultEffect) => void; reject: (err: Error) => void }>();
  private nextPushRpcOpId = 1n;
  private pushRpcWaiters = new Map<bigint, { resolve: (eff: PushRpcResultEffect) => void; reject: (err: Error) => void }>();
  private nextImageFetchOpId = 1n;
  private imageFetchWaiters = new Map<bigint, { resolve: (eff: SessionImageLoadedEffect) => void; reject: (err: Error) => void }>();
  private readonly scheduleCacheReads = new Map<bigint, Promise<void>>();

  constructor(remote?: Remote, opts: CoreHostOptions = {}) {
    this.core = getCore().newAppCore();
    this._remote = remote;
    this.explicitExecutor = opts.executor;
    this._executor = opts.executor ?? remote;
    this.onEffect = opts.onEffect;
    this.onAccountRuntimeChanged = opts.onAccountRuntimeChanged;
    this.go = opts.go;
    this.explicitScopeHmacKey = opts.scopeHmacKey;
    if (!opts.executor) {
      this.ownSecExec = new WebSecurityExecutor({
        store: opts.securityStore ?? securityStoreV4,
        fetch: opts.securityFetch,
        emit: (ev) => this.dispatch(ev),
      });
    }
    this.securityClient = new SecurityClient({
      dispatchSecurityEvent: (ev) => this.dispatch(ev),
      setForeground: (active) => this.setForeground(active),
      isClosed: () => this.closed,
    });
    this.sharedFiles = new SharedFileExecutor({
      store: opts.sharedFileStore ?? remote?.sharedFileStore ?? sharedFileStore,
      onEvent: (event) => {
        if (!this.closed) this.emit(event);
      },
      onDeliverOnly: opts.onDeliverLargeSharedFile,
    });
    const attStore =
      opts.attachmentStore ??
      remote?.attachmentStore ??
      (remote ? new WebAttachmentStore(remote.account) : new MemoryAttachmentStore());
    attStore.onOpfsDegraded = () => this.onAttachmentStorageDegraded();
    this.attachments = new AttachmentExecutor({
      store: attStore,
      onEvent: (event) => {
        if (this.closed) return;
        if (event.case === "draftStorageListed") {
          for (const entry of event.value.entries) {
            if (entry.bucket === DraftStorageBucket.DRAFT && !entry.corrupt) {
              const scope = parseDraftStorageKeyToScope(entry.key);
              if (scope) this.knownDraftScopes.set(draftScopeKey(scope), scope);
            }
          }
        }
        this.emit(event);
      },
      ensureStorageReady: () => this.ensureSecurityBootstrapped(),
      prepareImage: opts.prepareImage ?? ((blob, pOpts) => {
        if (remote?.prepareImageOverride) return remote.prepareImageOverride(blob, pOpts);
        return import("./attachment-prepare-default").then((m) => m.prepareImageDefault(blob, pOpts));
      }),
      probeVideoDurationMs: opts.probeVideoDurationMs,
    });
    this.unregisterSharedFileClearer = registerActiveSharedFileClearer(() => this.sharedFileClearAllAndWait());
    this.unregisterAttachmentClearer = registerActiveAttachmentClearer(() => this.draftClearAllAndWait());

    if (this._remote) {
      this.bindRemoteBridge(this._remote);
    }

    this.dispatch(
      create(EventSchema, {
        event: { case: "clockUpdated", value: localCalendar() },
        randomIds: randomIds(INITIAL_ENTROPY_COUNT),
        unitFloats: randomFloats(INITIAL_ENTROPY_COUNT),
      }),
    );
    this.emit({
      case: "entropyProvided",
      value: create(EntropyProvidedSchema, {
        randomBytes: crypto.getRandomValues(new Uint8Array(4096)),
      }),
    });
    this.emit({ case: "scheduleLocalContext", value: scheduleLocalContext() });
    this.emit({
      case: "localeChanged",
      value: create(LocaleChangedEventSchema, { uiLocale: l10n.localeStore.snapshot() }),
    });
    this.ensureSecurityConfigured();
    this.unsubscribeLocale = l10n.localeStore.subscribe(() => {
      if (!this.closed) {
        this.emit({
          case: "localeChanged",
          value: create(LocaleChangedEventSchema, { uiLocale: l10n.localeStore.snapshot() }),
        });
      }
    });
  }

  private emit(event: Event["event"], onProcessed?: () => void) {
    this.dispatch(create(EventSchema, { event }), onProcessed);
  }

  private emitClock() {
    const now = Date.now();
    this.emit({ case: "clockUpdated", value: localCalendar(now) });
    this.emit({ case: "scheduleLocalContext", value: scheduleLocalContext(now) });
  }

  setNavigator(go?: Go) {
    this.go = go;
  }

  private ensureAttachmentConfigured() {
    if (this.closed || this.attachmentConfigured) return;
    this.attachmentConfigured = true;
    const owned = this.attachments.store.storageOwned;
    this.emit({
      case: "attachmentConfig",
      value: create(AttachmentConfigEventSchema, {
        persistDrafts: owned,
        persistOutbox: owned,
        scanStorage: owned,
        maxChunkBytes: 0,
        hasPersistedPreparedImages: owned && this.attachments.store.hasPersistedPreparedImages,
        storageLockedByOther: !owned,
      }),
    });
  }

  private onAttachmentStorageDegraded() {
    if (this.closed || !this.attachmentConfigured) return;
    const owned = this.attachments.store.storageOwned;
    this.emit({
      case: "attachmentConfig",
      value: create(AttachmentConfigEventSchema, {
        persistDrafts: owned,
        persistOutbox: owned,
        scanStorage: false,
        maxChunkBytes: 0,
        hasPersistedPreparedImages: false,
        storageLockedByOther: !owned,
      }),
    });
  }

  private bindRemoteBridge(remote: Remote) {
    remote.attachHost({
      start: () => this.start(),
      stop: () => this.stop(),
      setForeground: (active) => this.setForeground(active),
      setNetwork: (available, signature) => this.setNetwork(available, signature),
      reconnectNow: () => this.reconnectNow(),
      retryLogin: () => this.retryLogin(),
      loadMoreSessions: () => this.loadMoreSessions(),
      retryList: () => this.retryList(),
      retryPrefs: () => this.retryPrefs(),
      retrySession: (sid) => this.retrySession(sid),
      retryEvents: (sid) => this.retryEvents(sid),
      refreshDevices: () => this.refreshDevices(),
      pushRpc: (req) => this.pushRpc(req),
      fetchSessionImage: (sid, imageId, machineId, cachedEnvelope) =>
        this.fetchSessionImage(sid, imageId, machineId, cachedEnvelope),
      terminalOpen: (opId, sessionId, machineId, terminalId) =>
        this.emit({ case: "terminalOpen", value: create(TerminalOpenActionSchema, { opId, sessionId, machineId, terminalId }) }),
      terminalClose: (streamId, opId = 0n) =>
        this.emit({ case: "terminalClose", value: create(TerminalCloseActionSchema, { streamId, opId }) }),
      terminalSocket: (ev) =>
        this.emit({ case: "terminalSocket", value: create(TerminalSocketEventSchema, ev) }),
      terminalInboundBytes: (streamId, rawEnvelope, fromDirect, pcId) =>
        this.emit({ case: "terminalInboundBytes", value: create(TerminalInboundBytesEventSchema, { streamId, rawEnvelope, fromDirect, pcId }) }),
      terminalInput: (req) =>
        this.emit({ case: "terminalInput", value: create(TerminalInputActionSchema, req) }),
      terminalConfirmRebuild: (streamId) =>
        this.emit({ case: "terminalConfirmRebuild", value: create(TerminalConfirmRebuildActionSchema, { streamId }) }),
      securityClient: this.securityClient,
      securityOp: (op) => this.securityOp(op),
      dispatchDirectSignal: (raw) => this.dispatchDirectSignal(raw),
      dispatchDirectSignalDelivery: (frameId, status, retryAfterMs) =>
        this.dispatchDirectSignalDelivery(frameId, status, retryAfterMs),
      pingMachine: (machineId, signal) => this.pingMachine(machineId, signal),
      runMachineCommand: (machineId, sessionId, cmd, signal, skipConnectivityPreflight) =>
        this.runMachineCommand(machineId, sessionId, cmd, signal, skipConnectivityPreflight),
      changeProjectPref: (kind, path) =>
        this.emit({ case: kind === "pin" || kind === "unpin" ? "projectTogglePin" : "projectToggleHide", value: create(ProjectKeyActionSchema, { projectKey: path }) }),
      dispatchEvent: (ev, onProcessed) => this.dispatch(ev, onProcessed),
      subscribe: (target, fn) => this.subscribe(target, fn),
      viewConnection: () => this.viewConnection(),
      viewHome: (endedLimit) => this.viewHome(endedLimit),
      viewSession: (sid) => this.viewSession(sid),
    });
  }

  setOnAccountRuntimeChanged(fn?: (eff: AccountRuntimeChangedEffect) => void) {
    this.onAccountRuntimeChanged = fn;
    if (fn && this.lastRuntimeEffect) {
      fn(this.lastRuntimeEffect);
    }
  }

  /** Attaches a Remote instance to this singleton CoreHost without constructing a second WasmAppCore. */
  attachRemote(remote: Remote) {
    if (this._remote === remote) return;
    if (this._remote) {
      hosts.delete(this._remote);
      this._remote.dispose(false);
    }
    this._remote = remote;
    if (!this.explicitExecutor) {
      this._executor = remote;
    }
    hosts.set(remote, this);
    this.bindRemoteBridge(remote);
    if (this.viewAppRoot().stage === AppRootStage.READY || this.viewAppRoot().stage === AppRootStage.REMOVED) {
      this.securityConfigured = true;
      this.securityBootstrapped = true;
    }
  }

  /** Detaches the active Remote on exit/logout while keeping this CoreHost and its WasmAppCore alive. */
  detachRemote() {
    const prev = this._remote;
    this._remote = undefined;
    this._executor = this.explicitExecutor;
    if (prev) {
      // Keep prev -> this in the WeakMap until GC so any React teardown frame still holding prev
      // resolves to the same singleton CoreHost instead of spawning a second WasmAppCore.
      prev.dispose(false);
    }
    this.started = false;
    this.securityConfigured = false;
    this.securityBootstrapped = false;
    this.securityBootstrapPromise = undefined;
    this.bootstrapped = false;
    this.preloadedBootstrap = undefined;
    for (const w of this.imageFetchWaiters.values()) {
      w.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    }
    this.imageFetchWaiters.clear();
    for (const w of this.pushRpcWaiters.values()) {
      w.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    }
    this.pushRpcWaiters.clear();
    this.attachmentConfigured = false;
    this.usageHydratedMachines.clear();
    this.knownDraftScopes.clear();
    this.watchCounts.clear();
    this.openFormKeys.clear();
    this.directToggleErrors.clear();
    this.clientRenameStates.clear();
    this.clearTimers();
    for (const map of this.inFlightDispatches.values()) {
      for (const ac of map.values()) ac.abort();
    }
    this.inFlightDispatches.clear();
    for (const target of ALL_TARGETS) {
      this.invalidateTarget(target);
    }
  }

  // --- Security View Models & Semantic Actions (P2-6) ---

  viewAppRoot(): AppRootViewModel {
    const ver = this.version(ScreenTarget.APP_ROOT);
    if (this.appRootCache?.version === ver) return this.appRootCache.vm;
    const vm = this.queryView<AppRootViewModel>({ case: "appRoot", value: create(AppRootScreenRequestSchema, {}) }, "appRoot");
    this.appRootCache = { version: ver, vm };
    return vm;
  }

  viewOnboarding(): OnboardingViewModel {
    const ver = this.version(ScreenTarget.ONBOARDING);
    if (this.onboardingCache?.version === ver) return this.onboardingCache.vm;
    const vm = this.queryView<OnboardingViewModel>({ case: "onboarding", value: create(OnboardingScreenRequestSchema, {}) }, "onboarding");
    this.onboardingCache = { version: ver, vm };
    return vm;
  }

  viewAddDevice(): AddDeviceViewModel {
    const ver = this.version(ScreenTarget.ADD_DEVICE);
    if (this.addDeviceCache?.version === ver) return this.addDeviceCache.vm;
    const vm = this.queryView<AddDeviceViewModel>({ case: "addDevice", value: create(AddDeviceScreenRequestSchema, {}) }, "addDevice");
    this.addDeviceCache = { version: ver, vm };
    return vm;
  }

  viewAccountSecurity(): AccountSecurityViewModel {
    const ver = this.version(ScreenTarget.ACCOUNT_SECURITY);
    if (this.accountSecurityCache?.version === ver) return this.accountSecurityCache.vm;
    const vm = this.queryView<AccountSecurityViewModel>({ case: "accountSecurity", value: create(AccountSecurityScreenRequestSchema, {}) }, "accountSecurity");
    this.accountSecurityCache = { version: ver, vm };
    return vm;
  }

  viewHandoff(): HandoffViewModel {
    const ver = this.version(ScreenTarget.HANDOFF);
    if (this.handoffCache?.version === ver) return this.handoffCache.vm;
    const vm = this.queryView<HandoffViewModel>({ case: "handoff", value: create(HandoffScreenRequestSchema, {}) }, "handoff");
    this.handoffCache = { version: ver, vm };
    return vm;
  }

  viewExitConfirm(): ExitConfirmView {
    const ver = this.version(ScreenTarget.EXIT_CONFIRM);
    if (this.exitConfirmCache?.version === ver) return this.exitConfirmCache.vm;
    const vm = this.queryView<ExitConfirmView>({ case: "exitConfirm", value: create(ExitConfirmScreenRequestSchema, {}) }, "exitConfirm");
    this.exitConfirmCache = { version: ver, vm };
    return vm;
  }

  clientInit(params: {
    uiLocale?: string;
    platform?: string;
    deviceDescription?: string;
    inlineCrypto?: boolean;
    foreground?: boolean;
    networkAvailable?: boolean;
    networkSignature?: string;
    initialUrl?: string;
    defaultServer?: string;
    requestStoragePersist?: boolean;
  }) {
    this.emit({
      case: "clientInit",
      value: create(ClientInitEventSchema, {
        uiLocale: params.uiLocale ?? l10n.localeStore.snapshot(),
        platform: params.platform ?? "web",
        deviceDescription: params.deviceDescription ?? (typeof navigator === "undefined" ? "" : describeBrowser(navigator.userAgent)),
        inlineCrypto: params.inlineCrypto ?? false,
        foreground: params.foreground ?? (typeof document === "undefined" ? true : !document.hidden),
        networkAvailable: params.networkAvailable ?? (typeof navigator === "undefined" ? true : navigator.onLine),
        networkSignature: params.networkSignature ?? "",
        initialUrl: params.initialUrl ?? "",
        defaultServer: params.defaultServer ?? "",
        requestStoragePersist: params.requestStoragePersist ?? true,
      }),
    });
  }

  appRootRetryBootstrap() {
    this.emit({ case: "appRootRetryBootstrap", value: create(AppRootRetryBootstrapActionSchema, {}) });
  }

  appRootVerifyPendingOp() {
    this.emit({ case: "appRootVerifyPendingOp", value: create(AppRootVerifyPendingOpActionSchema, {}) });
  }

  appRootDismissUpgradeNotice() {
    this.emit({ case: "appRootDismissUpgradeNotice", value: create(AppRootDismissUpgradeNoticeActionSchema, {}) });
  }

  onboardingSetMode(mode: OnboardingMode, opts?: { initialUrl?: string; existingRepair?: boolean; existingServer?: string }) {
    this.emit({
      case: "onboardingSetMode",
      value: create(OnboardingSetModeActionSchema, {
        mode,
        initialUrl: opts?.initialUrl ?? "",
        existingRepair: !!opts?.existingRepair,
        existingServer: opts?.existingServer ?? "",
      }),
    });
  }

  onboardingCreateEdit(patch: { serverInput?: string; codeSavedChecked?: boolean }) {
    this.emit({
      case: "onboardingCreateEdit",
      value: create(OnboardingCreateEditActionSchema, {
        hasServerInput: patch.serverInput !== undefined,
        serverInput: patch.serverInput ?? "",
        hasCodeSavedChecked: patch.codeSavedChecked !== undefined,
        codeSavedChecked: !!patch.codeSavedChecked,
      }),
    });
  }

  onboardingCreateSubmit(params: { serverInput?: string; setupToken?: string; confirmCodeSaved?: boolean } = {}) {
    this.emit({
      case: "onboardingCreateSubmit",
      value: create(OnboardingCreateSubmitActionSchema, {
        serverInput: params.serverInput ?? "",
        setupToken: params.setupToken ?? "",
        confirmCodeSaved: !!params.confirmCodeSaved,
      }),
    });
  }

  onboardingJoinEdit(patch: { codeInput?: string; serverInput?: string }) {
    this.emit({
      case: "onboardingJoinEdit",
      value: create(OnboardingJoinEditActionSchema, {
        hasCodeInput: patch.codeInput !== undefined,
        codeInput: patch.codeInput ?? "",
        hasServerInput: patch.serverInput !== undefined,
        serverInput: patch.serverInput ?? "",
      }),
    });
  }

  onboardingJoinSubmit(params: { rawLink?: string; serverInput?: string; codeInput?: string } = {}) {
    this.emit({
      case: "onboardingJoinSubmit",
      value: create(OnboardingJoinSubmitActionSchema, {
        rawLink: params.rawLink ?? "",
        serverInput: params.serverInput ?? "",
        codeInput: params.codeInput ?? "",
      }),
    });
  }

  onboardingJoinConfirmSas() {
    this.emit({ case: "onboardingJoinConfirmSas", value: create(OnboardingJoinConfirmSasActionSchema, {}) });
  }

  onboardingJoinCancel() {
    this.emit({ case: "onboardingJoinCancel", value: create(OnboardingJoinCancelActionSchema, {}) });
  }

  onboardingJoinBannerAction() {
    this.emit({ case: "onboardingJoinBannerAction", value: create(OnboardingJoinBannerActionSchema, {}) });
  }

  onboardingRecoveryEdit(patch: { serverInput?: string; codeSavedChecked?: boolean }) {
    this.emit({
      case: "onboardingRecoveryEdit",
      value: create(OnboardingRecoveryEditActionSchema, {
        hasServerInput: patch.serverInput !== undefined,
        serverInput: patch.serverInput ?? "",
        hasCodeSavedChecked: patch.codeSavedChecked !== undefined,
        codeSavedChecked: !!patch.codeSavedChecked,
      }),
    });
  }

  onboardingRecoveryAction(kind: OnboardingRecoveryActionKind, params?: { serverInput?: string; oldCode?: string }) {
    this.emit({
      case: "onboardingRecoveryAction",
      value: create(OnboardingRecoveryActionSchema, {
        kind,
        serverInput: params?.serverInput ?? "",
        oldCode: params?.oldCode ?? "",
      }),
    });
  }

  addDeviceOpen(params: { replacesDeviceId?: string; webOrigin?: string; autoStart?: boolean } = {}) {
    this.emit({
      case: "addDeviceOpen",
      value: create(AddDeviceOpenActionSchema, {
        replacesDeviceId: params.replacesDeviceId ?? "",
        webOrigin: params.webOrigin ?? "",
        autoStart: !!params.autoStart,
      }),
    });
  }

  addDeviceClose(replacesDeviceId = "") {
    this.emit({
      case: "addDeviceClose",
      value: create(AddDeviceCloseActionSchema, { replacesDeviceId }),
    });
  }

  addDeviceAction(kind: AddDeviceActionKind, params?: { flag?: boolean; deviceId?: string }) {
    this.emit({
      case: "addDeviceAction",
      value: create(AddDeviceActionSchema, {
        kind,
        flag: !!params?.flag,
        deviceId: params?.deviceId ?? "",
      }),
    });
  }

  accountSecurityOpen(subpage = AccountSecuritySubpage.OVERVIEW) {
    this.emit({
      case: "accountSecurityOpen",
      value: create(AccountSecurityOpenActionSchema, { subpage }),
    });
  }

  accountSecurityClose() {
    this.emit({
      case: "accountSecurityClose",
      value: create(AccountSecurityCloseActionSchema, {}),
    });
  }

  externalLogout() {
    this.emit({
      case: "externalLogout",
      value: create(ExternalLogoutEventSchema, {}),
    });
  }

  accountSecurityAction(kind: AccountSecurityActionKind, params?: { subpage?: AccountSecuritySubpage; flag?: boolean }) {
    this.emit({
      case: "accountSecurityAction",
      value: create(AccountSecurityActionSchema, {
        kind,
        subpage: params?.subpage ?? AccountSecuritySubpage.UNSPECIFIED,
        flag: !!params?.flag,
      }),
    });
  }

  handoffAction(kind: HandoffActionKind, params?: { targetDeviceId?: string; flag?: boolean; recoveryCode?: string }) {
    this.emit({
      case: "handoffAction",
      value: create(HandoffActionSchema, {
        kind,
        targetDeviceId: params?.targetDeviceId ?? "",
        flag: !!params?.flag,
        recoveryCode: params?.recoveryCode ?? "",
      }),
    });
  }

  exitRequest(intent: ExitIntentKind, server = "") {
    this.emit({
      case: "exitRequest",
      value: create(ExitRequestActionSchema, { intent, server }),
    });
  }

  exitConfirm(confirm: boolean) {
    this.emit({
      case: "exitConfirm",
      value: create(ExitConfirmActionSchema, { confirm }),
    });
  }

  // --- Lifecycle & Sync triggers ---

  /** Bound startup cache reads so slow/blocked IndexedDB never stalls initial mount. */
  static readonly WARM_BOOTSTRAP_CACHE_TIMEOUT_MS = 150;
  /** Bound initial schedule cache hydration when opening on #/tasks. */
  static readonly WARM_SCHEDULE_CACHE_TIMEOUT_MS = 120;

  private ensureSecurityConfigured() {
    if (this.closed || this.securityConfigured) return;
    const cfg = this.executor?.securityConfig?.();
    if (!cfg) return;
    this.securityConfigured = true;
    this.emit({
      case: "securityConfig",
      value: create(SecurityConfigEventSchema, {
        account: cfg.account,
        deviceId: cfg.deviceId,
        server: cfg.server ?? "",
        inlineCrypto: false,
        foreground: typeof document === "undefined" ? true : !document.hidden,
        platform: "web",
        deviceDescription: typeof navigator === "undefined" ? "" : describeBrowser(navigator.userAgent),
      }),
    });
  }

  private async ensureSecurityBootstrapped(): Promise<void> {
    if (this.closed) return;
    if (!this.executor?.securityEffect) {
      return;
    }
    if (this.securityBootstrapped || !this.executor?.securityConfig?.()) return;
    this.ensureSecurityConfigured();
    if (!this.securityBootstrapPromise) {
      this.securityBootstrapPromise = this.securityClient
        .bootstrap()
        .then(() => {
          this.securityBootstrapped = true;
        })
        .catch(() => {
          // Live workflow error surfaced via securityStatusChanged / sync loop.
        })
        .finally(() => {
          this.securityBootstrapPromise = undefined;
        });
    }
    await this.securityBootstrapPromise;
  }

  /** Reads IndexedDB bootstrap (and schedule cache on task routes) before first render
   * so sub-route refreshes never flash empty or missing-device states. */
  async warmLocalCache(
    route: string,
    raceTimeout: <T>(work: Promise<T>, ms: number) => Promise<T | undefined> = defaultRaceTimeout,
  ): Promise<void> {
    if (this.closed || !this.executor) return;
    this.ensureSecurityConfigured();
    await this.ensureSecurityBootstrapped();
    if (this.closed) return;
    const owned = await (this.attachments.store.acquireStorageLock?.() ?? Promise.resolve(true)).catch(() => true);
    if (this.closed) return;
    if (owned) {
      await this.attachments.store.probe().catch(() => false);
      if (this.closed) return;
    }
    this.ensureAttachmentConfigured();
    if (!this.bootstrapped) {
      try {
        const boot = await raceTimeout(
          this.executor.readBootstrapCache(0n),
          CoreHost.WARM_BOOTSTRAP_CACHE_TIMEOUT_MS,
        );
        if (this.closed) return;
        if (boot && !this.bootstrapped) {
          this.preloadedBootstrap = boot;
          this.emit({ case: "cacheBootstrapLoaded", value: boot });
        }
      } catch {
        // Live sync will hydrate when connectivity starts.
      }
    }
    const [kind, ...rest] = route.split("/");
    if (kind === "tasks") {
      this.scheduleListOpen(true);
      const [mid, tid] = rest.map((part) => {
        try {
          return decodeURIComponent(part);
        } catch {
          return part;
        }
      });
      if (mid && tid && mid !== "new") {
        this.scheduleDetailOpen(mid, tid);
      }
      if (this.scheduleCacheReads.size > 0) {
        await raceTimeout(
          Promise.all([...this.scheduleCacheReads.values()]),
          CoreHost.WARM_SCHEDULE_CACHE_TIMEOUT_MS,
        );
      }
    }
  }

  start() {
    if (this.closed || this.started) return;
    this.started = true;
    const gen = ++this.startGen;
    clearInterval(this.clockTimer);
    this.clockTimer = setInterval(() => {
      if (!this.closed && this.started) this.emitClock();
    }, CLOCK_TICK_MS);
    this.emitClock();
    this.ensureSecurityConfigured();
    const emitLifecycleStart = () => {
      if (this.closed || !this.started || gen !== this.startGen) return;
      const available = this.remote ? this.remote.network : typeof navigator === "undefined" || navigator.onLine !== false;
      const storedPrefs = this.readStoredDirectPreferences();
      if (storedPrefs.length > 0) {
        this.emit({
          case: "directPreferencesLoaded",
          value: create(DirectPreferencesLoadedEventSchema, { entries: storedPrefs }),
        });
      }
      const serverHost = this.remote ? new URL(this.remote.account.server).hostname : "localhost";
      const configuredStun = import.meta.env.VITE_DIRECT_STUN_URL ?? "";
      this.emit({
        case: "transportConfig",
        value: create(TransportConfigEventSchema, {
          supported: typeof RTCPeerConnection !== "undefined",
          account: "",
          protocolVersion: PEER_PROTOCOL_VERSION,
          serverHost,
          configuredStun,
          platformAgent: typeof navigator === "undefined" ? "" : navigator.userAgent,
        }),
      });
      this.ensureAttachmentConfigured();
      this.emit({
        case: "sharedFileConfig",
        value: create(SharedFileConfigEventSchema, {
          localCacheMaxBytes: WEB_LOCAL_CACHE_MAX_BYTES,
          storageFullKind: SharedFileFailureKind.BROWSER_STORAGE_FULL,
          scanLocal: true,
        }),
      });
      this.emit({
        case: "syncLifecycle",
        value: create(SyncLifecycleActionSchema, {
          kind: SyncLifecycleKind.START,
          homeScopeToken: readStoredScope(),
          scopeHmacKey: this.explicitScopeHmacKey ?? new Uint8Array(),
        }),
      });
      this.emit({
        case: "networkChanged",
        value: create(NetworkChangedEventSchema, { available, signature: "" }),
      });
      try {
        if (localStorage.getItem("schedule-ended-open") === "true") {
          this.scheduleToggleEnded(true);
        }
      } catch {}
    };
    if (this.executor?.securityEffect && this.executor?.securityConfig?.() && !this.securityBootstrapped) {
      void this.ensureSecurityBootstrapped().then(emitLifecycleStart);
    } else {
      void this.ensureSecurityBootstrapped();
      emitLifecycleStart();
    }
  }

  stop() {
    if (!this.started && this.timers.size === 0 && this.clockTimer === undefined) return;
    this.started = false;
    this.startGen++;
    this.clearTimers();
    clearInterval(this.clockTimer);
    this.clockTimer = undefined;
    this.rejectSecurityOps();
    this.securityClient.rejectAll();
    if (!this.closed) this.emit({ case: "syncLifecycle", value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.STOP }) });
    for (const w of this.imageFetchWaiters.values()) {
      w.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    }
    this.imageFetchWaiters.clear();
    for (const w of this.pushRpcWaiters.values()) {
      w.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    }
    this.pushRpcWaiters.clear();
  }

  private rejectSecurityOps() {
    for (const w of this.securityOpWaiters.values()) {
      w.reject(makeSecErr("unavailable"));
    }
    this.securityOpWaiters.clear();
  }

  setForeground(active: boolean) {
    if (this.closed) return;
    if (active) this.emitClock();
    this.emit({
      case: "securityForeground",
      value: create(SecurityForegroundEventSchema, { foreground: active }),
    });
    this.emit({
      case: "syncLifecycle",
      value: create(SyncLifecycleActionSchema, { kind: active ? SyncLifecycleKind.FOREGROUND : SyncLifecycleKind.BACKGROUND }),
    });
  }

  setNetwork(available: boolean, signature?: string) {
    if (this.closed) return;
    this.emit({ case: "networkChanged", value: create(NetworkChangedEventSchema, { available, signature: signature ?? "" }) });
  }

  reconnectNow() {
    if (!this.closed) this.emit({ case: "syncLifecycle", value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.RECONNECT_NOW }) });
  }

  retryLogin() {
    if (!this.closed) this.emit({ case: "syncLifecycle", value: create(SyncLifecycleActionSchema, { kind: SyncLifecycleKind.RETRY_LOGIN }) });
  }

  loadMoreSessions(): Promise<boolean> {
    this.emit({ case: "homeLoadMoreEnded", value: create(HomeLoadMoreEndedActionSchema, { step: 50 }) });
    return Promise.resolve(true);
  }

  retryList() {
    this.emit({ case: "homeRetryLoad", value: create(HomeRetryLoadActionSchema) });
  }

  retryPrefs() {
    if (this.lastProjectPrefEvent && this.viewProject("", true).manage?.saveError) {
      this.dispatch(this.lastProjectPrefEvent);
      return;
    }
    this.emit({ case: "homeRetryLoad", value: create(HomeRetryLoadActionSchema) });
  }

  retrySession(sessionId: string) {
    if (sessionId) this.emit({ case: "sessionRetryLoad", value: create(SessionRetryLoadActionSchema, { sessionId }) });
  }

  retryEvents(sessionId: string) {
    this.retrySession(sessionId);
  }

  refreshDevices(): Promise<void> {
    if (this.closed) return Promise.resolve();
    this.ensureSecurityConfigured();
    return new Promise<void>((resolve) => {
      let done = false;
      const finish = () => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        unsub();
        resolve();
      };
      const unsub = this.subscribe(ScreenTarget.DEVICES, finish);
      const timer = setTimeout(finish, 15_000);
      this.emit({ case: "refreshDevices", value: create(RefreshDevicesActionSchema) });
    });
  }

  pushRpc(req: Omit<MessageInitShape<typeof PushRpcActionSchema>, "opId">): Promise<PushRpcResultEffect> {
    if (this.closed) return Promise.reject(new Error("closed"));
    const opId = this.nextPushRpcOpId++;
    return new Promise<PushRpcResultEffect>((resolve, reject) => {
      this.pushRpcWaiters.set(opId, { resolve, reject });
      this.emit({
        case: "pushRpc",
        value: create(PushRpcActionSchema, { ...req, opId } as MessageInitShape<typeof PushRpcActionSchema>),
      });
    });
  }

  fetchSessionImage(
    sessionId: string,
    imageId: string,
    machineId = "",
    cachedEnvelope?: Uint8Array<ArrayBuffer>,
  ): Promise<SessionImageLoadedEffect> {
    const opId = this.nextImageFetchOpId++;
    const ev = {
      case: "sessionFetchImage" as const,
      value: create(SessionFetchImageActionSchema, {
        opId,
        sessionId,
        imageId,
        machineId,
        ...(cachedEnvelope ? { cachedEnvelope } : {}),
      }),
    };
    if (this.closed) return Promise.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    return new Promise<SessionImageLoadedEffect>((resolve, reject) => {
      this.imageFetchWaiters.set(opId, { resolve, reject });
      this.emit(ev);
    });
  }

  sharedFileStartDownload(sessionId: string, fileId: string, autoOpenIfStaying: boolean) {
    if (!this.closed && sessionId && fileId) this.emit({ case: "sharedFileStartDownload", value: create(SharedFileStartDownloadActionSchema, { sessionId, fileId, autoOpenIfStaying }) });
  }
  sharedFilePauseDownload(sessionId: string, fileId: string) {
    if (!this.closed && sessionId && fileId) this.emit({ case: "sharedFilePauseDownload", value: create(SharedFilePauseDownloadActionSchema, { sessionId, fileId }) });
  }
  sharedFileCancelDownload(sessionId: string, fileId: string) {
    if (!this.closed && sessionId && fileId) this.emit({ case: "sharedFileCancelDownload", value: create(SharedFileCancelDownloadActionSchema, { sessionId, fileId }) });
  }
  sharedFileDeleteLocal(sessionId: string, fileId: string) {
    if (!this.closed && sessionId && fileId) this.emit({ case: "sharedFileDeleteLocal", value: create(SharedFileDeleteLocalActionSchema, { sessionId, fileId }) });
  }
  sharedFileSetVisibleSession(sessionId: string) {
    if (!this.closed) this.emit({ case: "sharedFileSetVisibleSession", value: create(SharedFileSetVisibleSessionActionSchema, { sessionId }) });
  }
  sharedFileForgetSession(sessionId: string) {
    if (!this.closed && sessionId) this.emit({ case: "sharedFileForgetSession", value: create(SharedFileForgetSessionActionSchema, { sessionId }) });
  }
  sharedFileClearAll() {
    if (!this.closed) this.emit({ case: "sharedFileClearAll", value: create(SharedFileClearAllActionSchema) });
  }

  async sharedFileClearAllAndWait(): Promise<void> {
    if (!this.closed) this.sharedFileClearAll();
    await this.sharedFiles.whenIdle();
  }
  sharedFileDurationProbed(sessionId: string, fileId: string, durationMs: number) {
    if (!this.closed && sessionId && fileId && Number.isFinite(durationMs) && durationMs > 0) {
      this.emit({ case: "sharedFileDurationProbed", value: create(SharedFileDurationProbedEventSchema, { sessionId, fileId, durationMs: BigInt(Math.round(durationMs)) }) });
    }
  }

  sessionImageViewerOpen = (
    sessionId: string,
    opts: { seq?: bigint; imageIndex?: number; viewerIndex?: number },
  ) => {
    if (!sessionId) return;
    this.emit({
      case: "sessionImageViewerOpen",
      value: create(SessionImageViewerOpenActionSchema, {
        sessionId,
        seq: opts.seq ?? 0n,
        imageIndex: opts.imageIndex ?? 0,
        viewerIndex: opts.viewerIndex ?? 0,
      }),
    });
  };

  sessionImageViewerNavigate = (
    sessionId: string,
    opts: { delta?: number; setIndex?: boolean; index?: number },
  ) => {
    if (!sessionId) return;
    this.emit({
      case: "sessionImageViewerNavigate",
      value: create(SessionImageViewerNavigateActionSchema, {
        sessionId,
        delta: opts.delta ?? 0,
        setIndex: !!opts.setIndex,
        index: opts.index ?? 0,
      }),
    });
  };

  sessionImageViewerClose = (sessionId: string) => {
    if (!sessionId) return;
    this.emit({
      case: "sessionImageViewerClose",
      value: create(SessionImageViewerCloseActionSchema, { sessionId }),
    });
  };

  // --- Screen Intents ---

  homeSetScope = (scopeKey: string) => this.emit({ case: "homeSetScope", value: create(HomeSetScopeActionSchema, { scopeKey }) });
  homeSetFilter = (query: string) => this.emit({ case: "homeSetFilter", value: create(HomeSetFilterActionSchema, { query }) });
  homeLoadMoreEnded = (step = 30) => this.emit({ case: "homeLoadMoreEnded", value: create(HomeLoadMoreEndedActionSchema, { step }) });
  homeRetryLoad = () => this.emit({ case: "homeRetryLoad", value: create(HomeRetryLoadActionSchema) });
  homeSelectSession = (sessionId: string) => this.emit({ case: "homeSelectSession", value: create(HomeSelectSessionActionSchema, { sessionId }) });
  homeResolveApproval = (sessionId: string, approvalId: string, allow: boolean, always: boolean, denyReason = "") =>
    this.emit({
      case: "homeResolveApproval",
      value: create(HomeResolveApprovalActionSchema, { sessionId, approvalId, allow, always, denyReason }),
    });
  homeEditApprovalDeny = (sessionId: string, approvalId: string, denyingOpen: boolean) =>
    this.emit({
      case: "homeEditApprovalDeny",
      value: create(HomeEditApprovalDenyActionSchema, { sessionId, approvalId, denyingOpen }),
    });
  homeAnswerQuestion = (sessionId: string, questionId: string, optionNum: number) =>
    this.emit({
      case: "homeAnswerQuestion",
      value: create(HomeAnswerQuestionActionSchema, { sessionId, questionId, optionNum }),
    });
  homeRetryAttention = (sessionId: string, itemId: string) =>
    this.emit({
      case: "homeRetryAttention",
      value: create(HomeRetryAttentionActionSchema, { sessionId, itemId }),
    });
  homeAbandonAttention = (sessionId: string, itemId: string) =>
    this.emit({
      case: "homeAbandonAttention",
      value: create(HomeAbandonAttentionActionSchema, { sessionId, itemId }),
    });

  projectSetSearch = (query: string) => this.emit({ case: "projectSetSearch", value: create(ProjectSetSearchActionSchema, { query }) });
  projectTogglePin = (projectKey: string) => this.emit({ case: "projectTogglePin", value: create(ProjectKeyActionSchema, { projectKey }) });
  projectToggleHide = (projectKey: string) => this.emit({ case: "projectToggleHide", value: create(ProjectKeyActionSchema, { projectKey }) });
  projectOpenDirPicker = (forNewTask: boolean, machineId: string, path: string, pickerKey: string) =>
    this.emit({ case: "projectOpenDirPicker", value: create(DirPickerOpenActionSchema, { forNewTask, machineId, path, pickerKey }) });
  projectDirNavigate = (machineId: string, path: string, pickerKey: string) =>
    this.emit({ case: "projectDirNavigate", value: create(DirNavigateActionSchema, { machineId, path, pickerKey }) });
  projectCloseDirPicker = (pickerKey: string) =>
    this.emit({ case: "projectCloseDirPicker", value: create(DirPickerCloseActionSchema, { pickerKey }) });
  projectAdd = (path: string, pickerKey = "") =>
    this.emit({ case: "projectAdd", value: create(ProjectAddActionSchema, { path, pickerKey }) });

  newSessionOpen = (projectKey: string) => this.emit({ case: "newSessionOpen", value: create(NewSessionOpenActionSchema, { projectKey }) });
  newSessionClose = (projectKey: string) => this.emit({ case: "newSessionClose", value: create(NewSessionCloseActionSchema, { projectKey }) });
  newSessionSelectMachine = (machineId: string) => this.emit({ case: "newSessionSelectMachine", value: create(NewSessionSelectMachineActionSchema, { machineId }) });
  newSessionSelectAgent = (agent: Agent) => this.emit({ case: "newSessionSelectAgent", value: create(NewSessionSelectAgentActionSchema, { agent }) });
  newSessionSelectModel = (model: string, effort: string, standardContext?: boolean) =>
    this.emit({ case: "newSessionSelectModel", value: create(NewSessionSelectModelActionSchema, { model, effort, ...(standardContext !== undefined ? { standardContext } : {}) }) });
  newSessionRetryModels = (projectKey: string) => this.emit({ case: "newSessionRetryModels", value: create(NewSessionProjectActionSchema, { projectKey }) });
  newSessionRetrySaveConfig = (projectKey: string) => this.emit({ case: "newSessionRetrySaveConfig", value: create(NewSessionProjectActionSchema, { projectKey }) });
  newSessionSubmit = (projectKey: string) => {
    this.ensureAttachmentConfigured();
    this.emit({ case: "newSessionSubmit", value: create(NewSessionSubmitActionSchema, { projectKey }) });
  };
  newSessionAbandonStart = (projectKey: string) =>
    this.emit({ case: "newSessionAbandonStart", value: create(NewSessionProjectActionSchema, { projectKey }) });

  // --- Attachment & Draft Intents (Contract §1.3) ---

  private trackDraftScope(scope: DraftScope) {
    if (scope.id) this.knownDraftScopes.set(draftScopeKey(scope), scope);
  }

  draftOpen = (scope: DraftScope, machineId = "") => {
    if (!scope.id) return;
    this.ensureAttachmentConfigured();
    this.trackDraftScope(scope);
    this.emit({ case: "draftOpen", value: create(DraftOpenActionSchema, { scope, machineId }) });
  };

  draftSetText = (scope: DraftScope, text: string) => {
    if (!scope.id) return;
    this.ensureAttachmentConfigured();
    this.trackDraftScope(scope);
    this.emit({ case: "draftSetText", value: create(DraftSetTextActionSchema, { scope, text }) });
  };

  draftReserveSlots = (scope: DraftScope, reservationId: string, imageSlots: number, fileSlots: number) => {
    if (!scope.id || !reservationId) return;
    this.ensureAttachmentConfigured();
    this.trackDraftScope(scope);
    this.emit({
      case: "draftReserveSlots",
      value: create(DraftReserveSlotsActionSchema, { scope, reservationId, imageSlots, fileSlots }),
    });
  };

  draftReleaseSlots = (scope: DraftScope, reservationId: string) => {
    if (!scope.id || !reservationId) return;
    this.ensureAttachmentConfigured();
    this.emit({
      case: "draftReleaseSlots",
      value: create(DraftReleaseSlotsActionSchema, { scope, reservationId }),
    });
  };

  stageAndAddDraftItems = (
    scope: DraftScope,
    entries: readonly DraftCandidateEntry[],
    forceFile: boolean,
  ): void => {
    if (!scope.id || !entries.length) return;
    this.ensureAttachmentConfigured();
    this.trackDraftScope(scope);
    const reservationId = newRequestID();
    let imageSlots = 0;
    let fileSlots = 0;
    for (const entry of entries) {
      if (entry.isDirectory) continue;
      if (!forceFile && entry.mimeType.toLowerCase().startsWith("image/")) imageSlots++;
      else fileSlots++;
    }
    if (imageSlots > 0 || fileSlots > 0) {
      this.draftReserveSlots(scope, reservationId, imageSlots, fileSlots);
    }
    const stagedRefs: string[] = [];
    try {
      const items = entries.map((entry) => {
        const sourceRef = !entry.isDirectory && entry.file ? this.attachments.stagePickedBlob(entry.file) : "";
        if (sourceRef) stagedRefs.push(sourceRef);
        return create(DraftPickedItemSchema, {
          candidate: create(AttachmentCandidateInputSchema, {
            name: entry.name,
            mimeType: entry.mimeType,
            sizeBytes: BigInt(Math.max(0, Math.floor(entry.sizeBytes))),
            sizeUnknown: false,
            isDirectory: !!entry.isDirectory,
            forceFile,
          }),
          sourceRef,
          thumbRef: "",
          hasOriginalSource: !!sourceRef,
          hasDuration: false,
          durationMs: 0n,
        });
      });
      this.emit({
        case: "draftAddItems",
        value: create(DraftAddItemsActionSchema, {
          scope,
          reservationId: imageSlots > 0 || fileSlots > 0 ? reservationId : "",
          items,
        }),
      });
    } catch (err) {
      if (imageSlots > 0 || fileSlots > 0) this.draftReleaseSlots(scope, reservationId);
      if (stagedRefs.length > 0) void this.attachments.store.deleteLocalRefs(stagedRefs).catch(() => undefined);
      throw err;
    }
  };

  draftRemoveItem = (scope: DraftScope, itemId: string) => {
    if (!scope.id || !itemId) return;
    this.ensureAttachmentConfigured();
    this.emit({ case: "draftRemoveItem", value: create(DraftRemoveItemActionSchema, { scope, itemId }) });
  };

  draftRetryItem = (scope: DraftScope, itemId: string) => {
    if (!scope.id || !itemId) return;
    this.ensureAttachmentConfigured();
    this.emit({ case: "draftRetryItem", value: create(DraftRetryItemActionSchema, { scope, itemId }) });
  };

  draftSetOriginal = (scope: DraftScope, original: boolean) => {
    if (!scope.id) return;
    this.ensureAttachmentConfigured();
    this.emit({ case: "draftSetOriginal", value: create(DraftSetOriginalActionSchema, { scope, original }) });
  };

  draftForgetSession = (sessionId: string) => {
    if (!sessionId) return;
    this.ensureAttachmentConfigured();
    this.knownDraftScopes.delete(draftScopeKey(sessionDraftScope(sessionId)));
    this.emit({ case: "draftForgetSession", value: create(DraftForgetSessionActionSchema, { sessionId }) });
  };

  draftClearAll = () => {
    this.ensureAttachmentConfigured();
    this.knownDraftScopes.clear();
    this.emit({ case: "draftClearAll", value: create(DraftClearAllActionSchema, {}) });
  };

  async draftClearAllAndWait(): Promise<void> {
    if (!this.closed) this.draftClearAll();
    else this.attachments.handleDraftStorageClearAll();
    await this.attachments.whenIdle();
  }

  attachmentPauseUpload = (scope: DraftScope, itemId: string) => {
    if (!scope.id || !itemId) return;
    this.ensureAttachmentConfigured();
    this.emit({ case: "attachmentPauseUpload", value: create(AttachmentPauseUploadActionSchema, { scope, itemId }) });
  };

  attachmentResumeUpload = (scope: DraftScope, itemId: string) => {
    if (!scope.id || !itemId) return;
    this.ensureAttachmentConfigured();
    this.emit({ case: "attachmentResumeUpload", value: create(AttachmentResumeUploadActionSchema, { scope, itemId }) });
  };

  draftSendSession = (sessionId: string, mode: SendMode) => {
    if (!sessionId) return;
    this.ensureAttachmentConfigured();
    this.emit({ case: "draftSendSession", value: create(DraftSendSessionActionSchema, { sessionId, mode }) });
  };

  draftPutBackInput = (sessionId: string, requestId: string) => {
    if (!sessionId || !requestId) return;
    this.ensureAttachmentConfigured();
    this.emit({ case: "draftPutBackInput", value: create(DraftPutBackInputActionSchema, { sessionId, requestId }) });
  };

  draftImageViewerOpen = (scope: DraftScope, index: number) => {
    if (!scope.id) return;
    this.ensureAttachmentConfigured();
    this.emit({ case: "draftImageViewerOpen", value: create(DraftImageViewerOpenActionSchema, { scope, index }) });
  };

  draftImageViewerNavigate = (scope: DraftScope, opts: { delta?: number; setIndex?: boolean; index?: number }) => {
    if (!scope.id) return;
    this.ensureAttachmentConfigured();
    this.emit({
      case: "draftImageViewerNavigate",
      value: create(DraftImageViewerNavigateActionSchema, {
        scope,
        delta: opts.delta ?? 0,
        setIndex: !!opts.setIndex,
        index: opts.index ?? 0,
      }),
    });
  };

  draftImageViewerClose = (scope: DraftScope) => {
    if (!scope.id) return;
    this.ensureAttachmentConfigured();
    this.emit({ case: "draftImageViewerClose", value: create(DraftImageViewerCloseActionSchema, { scope }) });
  };

  appendNewSessionPrompt = (projectKey: string, prompt: string) => {
    if (!projectKey || !prompt) return;
    const scope = newSessionDraftScope(projectKey);
    const existing = this.viewAttachmentDraft(scope).draft?.text ?? "";
    const next = !existing ? prompt : existing.includes(prompt) ? existing : `${existing}\n\n${prompt}`;
    this.draftSetText(scope, next);
  };

  hasUnsavedDrafts(): boolean {
    for (const scope of this.knownDraftScopes.values()) {
      if (this.viewAttachmentDraft(scope).draft?.hasUnsavedContent) return true;
    }
    return false;
  }

  private clearSessionCacheFor(sessionId: string) {
    const prefix = `${sessionId}\0`;
    for (const key of [...this.sessionCache.keys()]) {
      if (key.startsWith(prefix)) this.sessionCache.delete(key);
    }
  }

  watchSession(sessionId: string) {
    if (!sessionId) return;
    const prev = this.watchCounts.get(sessionId) ?? 0;
    this.watchCounts.set(sessionId, prev + 1);
    if (prev === 0) {
      this.clearSessionCacheFor(sessionId);
      this.emit({ case: "sessionWatch", value: create(SessionWatchEventSchema, { sessionId, watched: true }) });
    }
  }

  unwatchSession(sessionId: string) {
    if (!sessionId) return;
    const prev = this.watchCounts.get(sessionId) ?? 0;
    if (prev <= 1) {
      this.watchCounts.delete(sessionId);
      this.clearSessionCacheFor(sessionId);
      if (prev === 1) {
        this.emit({ case: "sessionWatch", value: create(SessionWatchEventSchema, { sessionId, watched: false }) });
      }
      return;
    }
    this.watchCounts.set(sessionId, prev - 1);
  }

  sessionOpen(sessionId: string, refreshModels = false, refreshSettings = false) {
    if (!sessionId) return;
    this.emit({
      case: "sessionOpen",
      value: create(SessionOpenActionSchema, { sessionId, refreshModels, refreshSettings }),
    });
  }

  sessionEditApprovalDeny(sessionId: string, approvalId: string, denyingOpen: boolean) {
    this.emit({
      case: "sessionEditApprovalDeny",
      value: create(SessionEditApprovalDenyActionSchema, { sessionId, approvalId, denyingOpen }),
    });
  }

  sessionResolveApproval(sessionId: string, approvalId: string, allow: boolean, always: boolean, denyReason = "") {
    this.emit({
      case: "sessionResolveApproval",
      value: create(SessionResolveApprovalActionSchema, { sessionId, approvalId, allow, always, denyReason }),
    });
  }

  sessionToggleQuestionOption(sessionId: string, questionId: string, optionNum: number) {
    this.emit({
      case: "sessionToggleQuestionOption",
      value: create(SessionToggleQuestionOptionActionSchema, { sessionId, questionId, optionNum }),
    });
  }

  sessionAnswerQuestion(sessionId: string, questionId: string, optionNum = 0, optionNums: number[] = []) {
    this.emit({
      case: "sessionAnswerQuestion",
      value: create(SessionAnswerQuestionActionSchema, { sessionId, questionId, optionNum, optionNums }),
    });
  }

  sessionAnswerPlan(sessionId: string, choice: PlanChoice) {
    this.emit({
      case: "sessionAnswerPlan",
      value: create(SessionAnswerPlanActionSchema, { sessionId, choice }),
    });
  }

  sessionSimpleCommand(sessionId: string, kind: SessionSimpleCommandKind) {
    this.emit({
      case: "sessionSimpleCommand",
      value: create(SessionSimpleCommandActionSchema, { sessionId, kind }),
    });
  }

  sessionRename(sessionId: string, title: string) {
    this.emit({
      case: "sessionRename",
      value: create(SessionRenameActionSchema, { sessionId, title }),
    });
  }

  sessionSetApprovalMode(sessionId: string, mode: ApprovalMode) {
    this.emit({
      case: "sessionSetApprovalMode",
      value: create(SessionSetApprovalModeActionSchema, { sessionId, mode }),
    });
  }

  sessionSetModel(sessionId: string, model: string, effort = "") {
    this.emit({
      case: "sessionSetModel",
      value: create(SessionSetModelActionSchema, { sessionId, model, effort }),
    });
  }

  sessionRetryCommand(sessionId: string, slotKey: string) {
    this.emit({
      case: "sessionRetryCommand",
      value: create(SessionRetryCommandActionSchema, { sessionId, slotKey }),
    });
  }

  sessionAbandonCommand(slotKey: string, sessionId: string) {
    this.emit({
      case: "sessionAbandonCommand",
      value: create(SessionSlotActionSchema, { slotKey, sessionId }),
    });
  }

  sessionCancelTrayItem(sessionId: string, requestId: string) {
    this.emit({
      case: "sessionCancelTrayItem",
      value: create(SessionTrayItemActionSchema, { sessionId, requestId }),
    });
  }

  sessionIgnoreTrayItem(sessionId: string, requestId: string) {
    this.emit({
      case: "sessionIgnoreTrayItem",
      value: create(SessionTrayItemActionSchema, { sessionId, requestId }),
    });
  }

  sessionPromoteTrayItem(sessionId: string, requestId: string) {
    this.emit({
      case: "sessionPromoteTrayItem",
      value: create(SessionTrayItemActionSchema, { sessionId, requestId }),
    });
  }

  sessionRestoreTrayItem(sessionId: string, requestId: string) {
    this.emit({
      case: "sessionRestoreTrayItem",
      value: create(SessionTrayItemActionSchema, { sessionId, requestId }),
    });
  }

  sessionRetryTrayItem(sessionId: string, requestId: string) {
    this.emit({
      case: "sessionRetryTrayItem",
      value: create(SessionTrayItemActionSchema, { sessionId, requestId }),
    });
  }

  sessionResumeTrayItem(sessionId: string, requestId: string) {
    this.emit({
      case: "sessionResumeTrayItem",
      value: create(SessionTrayItemActionSchema, { sessionId, requestId }),
    });
  }

  sessionCancelAllPaused(sessionId: string) {
    this.emit({
      case: "sessionCancelAllPaused",
      value: create(SessionTraySessionActionSchema, { sessionId }),
    });
  }

  sessionUndoCancel(sessionId: string) {
    this.emit({
      case: "sessionUndoCancel",
      value: create(SessionTraySessionActionSchema, { sessionId }),
    });
  }

  sessionLoadEarlier(sessionId: string) {
    if (!sessionId) return;
    this.emit({
      case: "sessionLoadEarlier",
      value: create(SessionRefreshHistoryActionSchema, { sessionId }),
    });
  }

  onRestoreComposer(sessionId: string, fn: (eff: SessionRestoreComposerEffect) => void): () => void {
    let set = this.restoreComposerListeners.get(sessionId);
    if (!set) {
      set = new Set();
      this.restoreComposerListeners.set(sessionId, set);
    }
    set.add(fn);
    return () => {
      set?.delete(fn);
      if (set?.size === 0) this.restoreComposerListeners.delete(sessionId);
    };
  }

  // --- Schedule Intents & Listeners ---

  scheduleRefresh = (machineId = "") =>
    this.emit({ case: "scheduleRefresh", value: create(ScheduleStringActionSchema, { value: machineId }) });
  scheduleSetVisibleSession = (sessionId: string) =>
    this.emit({ case: "scheduleSetVisibleSession", value: create(ScheduleStringActionSchema, { value: sessionId }) });
  scheduleListOpen = (open: boolean) =>
    this.emit({ case: "scheduleListOpen", value: create(ScheduleBoolActionSchema, { value: open }) });
  scheduleSelectTask = (machineId: string, taskId: string) =>
    this.emit({ case: "scheduleSelectTask", value: create(ScheduleTaskRefSchema, { machineId, taskId }) });
  scheduleToggleEnded = (open?: boolean) => {
    this.emit({ case: "scheduleToggleEnded", value: create(ScheduleBoolActionSchema, open === undefined ? {} : { value: open }) });
    try {
      localStorage.setItem("schedule-ended-open", String(this.viewSchedules().endedExpanded));
    } catch {}
  };
  scheduleMarkRead = (tasks: Array<{ machineId: string; taskId: string }>, allFresh = false) =>
    this.emit({
      case: "scheduleMarkRead",
      value: create(ScheduleMarkReadActionSchema, {
        tasks: tasks.map((t) => create(ScheduleTaskRefSchema, t)),
        allFresh,
      }),
    });
  scheduleMarkUnread = (machineId: string, taskId: string, unread = true) =>
    this.emit({ case: "scheduleMarkUnread", value: create(ScheduleTaskBoolActionSchema, { machineId, taskId, value: unread }) });
  scheduleDetailOpen = (machineId: string, taskId: string) =>
    this.emit({ case: "scheduleDetailOpen", value: create(ScheduleTaskRefSchema, { machineId, taskId }) });
  scheduleDetailClose = (machineId: string, taskId: string) =>
    this.emit({ case: "scheduleDetailClose", value: create(ScheduleTaskRefSchema, { machineId, taskId }) });
  scheduleDetailShowLatestResult = (machineId: string, taskId: string) =>
    this.emit({ case: "scheduleDetailShowLatestResult", value: create(ScheduleTaskRefSchema, { machineId, taskId }) });
  scheduleDetailToggleQuietGroup = (machineId: string, taskId: string, groupKey: string) =>
    this.emit({ case: "scheduleDetailToggleQuietGroup", value: create(ScheduleTaskStringActionSchema, { machineId, taskId, value: groupKey }) });
  scheduleDetailShowAllRuns = (machineId: string, taskId: string, open: boolean) =>
    this.emit({ case: "scheduleDetailShowAllRuns", value: create(ScheduleTaskBoolActionSchema, { machineId, taskId, value: open }) });
  scheduleDetailOpenRun = (machineId: string, taskId: string, runId: string) =>
    this.emit({ case: "scheduleDetailOpenRun", value: create(ScheduleTaskStringActionSchema, { machineId, taskId, value: runId }) });
  scheduleOperate = (machineId: string, taskId: string, operation: ScheduleOperationKind) =>
    this.emit({ case: "scheduleOperate", value: create(ScheduleOperateActionSchema, { machineId, taskId, operation }) });
  scheduleSetNotify = (machineId: string, taskId: string, notify: boolean) =>
    this.emit({ case: "scheduleSetNotify", value: create(ScheduleTaskBoolActionSchema, { machineId, taskId, value: notify }) });
  scheduleAbandonMutation = (machineId = "", taskId = "", formKey = "") =>
    this.emit({ case: "scheduleAbandonMutation", value: create(ScheduleMutationRefSchema, { formKey, machineId, taskId }) });
  scheduleRetryMutation = (machineId = "", taskId = "", formKey = "") =>
    this.emit({ case: "scheduleRetryMutation", value: create(ScheduleMutationRefSchema, { formKey, machineId, taskId }) });

  scheduleFormOpen(
    mode: ScheduleFormMode,
    opts: { machineId?: string; taskId?: string; sessionId?: string; projectKey?: string } = {},
  ): string {
    this.emit({
      case: "scheduleFormOpen",
      value: create(ScheduleFormOpenActionSchema, {
        mode,
        machineId: opts.machineId ?? "",
        taskId: opts.taskId ?? "",
        sessionId: opts.sessionId ?? "",
        projectKey: opts.projectKey ?? "",
        localTimezone: localTimeZoneId(),
      }),
    });
    const vm = this.viewScheduleForm("");
    if (vm.open && vm.formKey) this.openFormKeys.add(vm.formKey);
    return vm.formKey;
  }

  scheduleFormClose = (formKey: string, discard = false) => {
    if (discard) this.openFormKeys.delete(formKey);
    this.emit({ case: "scheduleFormClose", value: create(ScheduleFormBoolActionSchema, { formKey, value: discard }) });
  };

  scheduleFormSetNotify = (formKey: string, value: boolean) =>
    this.emit({ case: "scheduleFormSetNotify", value: create(ScheduleFormBoolActionSchema, { formKey, value }) });
  scheduleFormSubmit = (formKey: string, allowDuplicate = false) =>
    this.emit({ case: "scheduleFormSubmit", value: create(ScheduleFormBoolActionSchema, { formKey, value: allowDuplicate }) });
  scheduleFormSetMessage = (formKey: string, value: string) =>
    this.emit({ case: "scheduleFormSetMessage", value: create(ScheduleFormStringActionSchema, { formKey, value }) });
  scheduleFormSetTitle = (formKey: string, value: string) =>
    this.emit({ case: "scheduleFormSetTitle", value: create(ScheduleFormStringActionSchema, { formKey, value }) });
  scheduleFormSelectMachine = (formKey: string, machineId: string) =>
    this.emit({ case: "scheduleFormSelectMachine", value: create(ScheduleFormStringActionSchema, { formKey, value: machineId }) });
  scheduleFormRetryModels = (formKey: string) =>
    this.emit({ case: "scheduleFormRetryModels", value: create(ScheduleFormStringActionSchema, { formKey, value: "" }) });
  scheduleFormSelectProject = (formKey: string, projectKey: string, machineId = "") =>
    this.emit({ case: "scheduleFormSelectProject", value: create(ScheduleFormSelectProjectActionSchema, { formKey, projectKey, machineId }) });
  scheduleFormSelectAgent = (formKey: string, agent: Agent) =>
    this.emit({ case: "scheduleFormSelectAgent", value: create(ScheduleFormSelectAgentActionSchema, { formKey, agent }) });
  scheduleFormSelectModel = (formKey: string, model: string, effort: string, mode?: ApprovalMode) =>
    this.emit({
      case: "scheduleFormSelectModel",
      value: create(ScheduleFormSelectModelActionSchema, { formKey, model, effort, ...(mode !== undefined ? { mode } : {}) }),
    });
  scheduleFormUpdateTiming = (opts: Omit<Parameters<typeof create<typeof ScheduleFormUpdateTimingActionSchema>>[1], "$typeName">) =>
    this.emit({ case: "scheduleFormUpdateTiming", value: create(ScheduleFormUpdateTimingActionSchema, opts) });

  private addFormListener<T extends unknown[]>(map: Map<string, Set<(...args: T) => void>>, formKey: string, fn: (...args: T) => void): () => void {
    let set = map.get(formKey);
    if (!set) {
      set = new Set();
      map.set(formKey, set);
    }
    set.add(fn);
    return () => {
      set?.delete(fn);
      if (set?.size === 0) map.delete(formKey);
    };
  }

  onScheduleFormClose(formKey: string, fn: () => void): () => void {
    return this.addFormListener(this.scheduleFormCloseListeners, formKey, fn);
  }

  onScheduleFormOpenTiming(formKey: string, fn: (formKey: string) => void): () => void {
    return this.addFormListener(this.scheduleFormTimingListeners, formKey, fn);
  }

  getScheduleFocus(sessionId: string): { runId: string; seq: bigint } | undefined {
    return this.scheduleFocus.get(sessionId);
  }

  clearScheduleFocus(sessionId: string) {
    this.scheduleFocus.delete(sessionId);
  }

  hasUnsavedScheduleDrafts(): boolean {
    if (this.viewScheduleForm("").hasUnsavedChanges) return true;
    for (const key of this.openFormKeys) {
      const vm = this.viewScheduleForm(key);
      if (!vm.open) this.openFormKeys.delete(key);
      else if (vm.hasUnsavedChanges) return true;
    }
    return false;
  }

  // --- Settings, Machine & Devices Intents ---

  subscribeExtra(fn: () => void): () => void {
    this.extraListeners.add(fn);
    return () => { this.extraListeners.delete(fn); };
  }

  getExtraVersion(): number {
    return this.extraVersion;
  }

  private bumpExtra() {
    this.extraVersion++;
    for (const fn of [...this.extraListeners]) fn();
  }

  directError(machineId: string): string {
    return this.directToggleErrors.get(machineId) ?? "";
  }

  private setDirectError(machineId: string, error: string) {
    if ((this.directToggleErrors.get(machineId) ?? "") === error) return;
    if (error) this.directToggleErrors.set(machineId, error);
    else this.directToggleErrors.delete(machineId);
    this.bumpExtra();
  }

  clientRenameState(deviceId: string): { saving: boolean; error: string } {
    return this.clientRenameStates.get(deviceId) ?? { saving: false, error: "" };
  }

  private setClientRenameState(deviceId: string, next: { saving: boolean; error: string }) {
    const prev = this.clientRenameState(deviceId);
    if (prev.saving === next.saving && prev.error === next.error) return;
    if (!next.saving && !next.error) this.clientRenameStates.delete(deviceId);
    else this.clientRenameStates.set(deviceId, next);
    this.bumpExtra();
  }

  onCleanupPicker(fn: (eff: PrepareCleanupSessionEffect) => void): () => void {
    this.cleanupPickerListeners.add(fn);
    return () => { this.cleanupPickerListeners.delete(fn); };
  }

  private emitNotificationSync(patch: Omit<Partial<SetNotificationPrefEvent>, "$typeName">, revert = false) {
    if (revert) this.revertingPushPrefs = true;
    else this.syncingPrefs = true;
    try {
      this.emit({ case: "setNotificationPref", value: create(SetNotificationPrefEventSchema, patch) });
    } finally {
      if (revert) this.revertingPushPrefs = false;
      else this.syncingPrefs = false;
    }
  }

  syncBrowserPrefs() {
    if (this.closed) return;
    const ns = readNotificationState();
    this.syncingPrefs = true;
    try {
      this.lastPersistedPrefs = ns.prefs;
      this.lastPushPrefSig = `${ns.prefs.attention}:${ns.prefs.turns}:${ns.prefs.scheduled !== false}`;
      this.emit({ case: "setAppearancePref", value: create(SetAppearancePrefEventSchema, { theme: toCoreTheme(getTheme()) }) });
      this.emitNotificationSync({
        supported: ns.supported,
        permission: ns.permission,
        pushState: ns.pushState,
        notifyAttention: ns.prefs.attention,
        notifyTurnEnded: ns.prefs.turns,
        notifyScheduled: ns.prefs.scheduled !== false,
        errorDetail: "",
      });
    } finally {
      this.syncingPrefs = false;
    }
  }

  private syncMachineWatch(machineId: string) {
    if (!machineId || this.closed) return;
    const c = this.machineWatchCounts.get(machineId);
    const mode = c && c.live > 0 ? MachinePollMode.LIVE : c && c.overview > 0 ? MachinePollMode.OVERVIEW : MachinePollMode.OFF;
    const visible = mode !== MachinePollMode.OFF;
    const prev = this.machineWatchActive.get(machineId);
    if ((prev && prev.visible === visible && prev.mode === mode) || (!visible && !prev)) return;
    if (visible) this.machineWatchActive.set(machineId, { visible, mode });
    else this.machineWatchActive.delete(machineId);
    this.emit({ case: "machineStatusWatch", value: create(MachineStatusWatchEventSchema, { machineId, visible, mode }) });
  }

  watchMachineStatus(machineId: string, mode: MachinePollMode): () => void {
    if (!machineId || mode === MachinePollMode.OFF) return () => {};
    let entry = this.machineWatchCounts.get(machineId);
    if (!entry) {
      entry = { live: 0, overview: 0 };
      this.machineWatchCounts.set(machineId, entry);
    }
    if (mode === MachinePollMode.LIVE) entry.live++;
    else entry.overview++;
    this.syncMachineWatch(machineId);
    let released = false;
    return () => {
      if (released) return;
      released = true;
      const cur = this.machineWatchCounts.get(machineId);
      if (!cur) return;
      if (mode === MachinePollMode.LIVE) cur.live = Math.max(0, cur.live - 1);
      else cur.overview = Math.max(0, cur.overview - 1);
      if (cur.live === 0 && cur.overview === 0) this.machineWatchCounts.delete(machineId);
      this.syncMachineWatch(machineId);
    };
  }

  retryMachineStatus(machineId: string) {
    if (machineId && !this.closed) this.emit({ case: "machineStatusRetry", value: create(MachineStatusRetryEventSchema, { machineId }) });
  }

  selectMachineStatusTab(machineId: string, tab: MachineUsageTab) {
    if (machineId && !this.closed) this.emit({ case: "machineStatusTab", value: create(MachineStatusTabEventSchema, { machineId, tab }) });
  }

  scrubMachineStatus(machineId: string, kind: MachineChartKind, pointIndex: number, clear = false) {
    if (machineId && !this.closed) this.emit({ case: "machineStatusScrub", value: create(MachineStatusScrubEventSchema, { machineId, kind, pointIndex, clear }) });
  }

  cleanupMachineStatus(machineId: string) {
    if (machineId && !this.closed) this.emit({ case: "machineStatusCleanup", value: create(MachineStatusCleanupEventSchema, { machineId }) });
  }

  machineSettingsOpen(machineId: string) {
    if (machineId && !this.closed) this.emit({ case: "machineSettingsOpen", value: create(MachineSettingsOpenEventSchema, { machineId }) });
  }

  machineSettingsSelectAgent(machineId: string, agent: Agent) {
    if (machineId && !this.closed) this.emit({ case: "machineSettingsSelectAgent", value: create(MachineSettingsSelectAgentEventSchema, { machineId, agent }) });
  }

  toggleMachineAgent(machineId: string, agent: Agent, enabled: boolean) {
    if (machineId && !this.closed) {
      this.emit({ case: "toggleMachineAgent", value: create(ToggleMachineAgentEventSchema, { machineId, agent, enabled }) });
    }
  }

  private usageCacheStorageKey(machineId: string, preset: UsageRangePreset): string {
    const scope = this.remote?.account ? `${this.remote.account.server}|${this.remote.account.deviceId}` : "local";
    return `ra-usage-cache:${scope}:${machineId}:${preset}`;
  }

  private hydrateUsageCache(machineId: string) {
    if (!machineId || this.closed || this.usageHydratedMachines.has(machineId)) return;
    this.usageHydratedMachines.add(machineId);
    if (typeof localStorage === "undefined") return;
    for (const preset of USAGE_CACHE_PRESETS) {
      try {
        const raw = localStorage.getItem(this.usageCacheStorageKey(machineId, preset));
        if (!raw) continue;
        const rawSnapshot = decodeBytesBase64(raw);
        if (rawSnapshot.byteLength === 0) continue;
        this.emit({
          case: "usageCacheLoaded",
          value: create(UsageCacheLoadedEventSchema, { machineId, preset, rawSnapshot }),
        });
      } catch {
        // Ignore corrupt or inaccessible local cache entries.
      }
    }
  }

  private deleteUsageCache(machineId: string) {
    if (!machineId) return;
    this.usageHydratedMachines.delete(machineId);
    if (typeof localStorage === "undefined") return;
    for (const preset of USAGE_CACHE_PRESETS) {
      try {
        localStorage.removeItem(this.usageCacheStorageKey(machineId, preset));
      } catch {
        // Ignore storage failure.
      }
    }
  }

  private persistUsageCache(machineId: string, preset: UsageRangePreset, rawSnapshot: Uint8Array) {
    if (!machineId || rawSnapshot.byteLength === 0 || preset === UsageRangePreset.CUSTOM || preset === UsageRangePreset.UNSPECIFIED) return;
    if (typeof localStorage === "undefined") return;
    try {
      const encoded = encodeBytesBase64(rawSnapshot);
      localStorage.setItem(this.usageCacheStorageKey(machineId, preset), encoded);
    } catch {
      // Storage may be full or restricted.
    }
  }

  openUsageScreen(
    machineId: string,
    preset: UsageRangePreset,
    customStartMs = 0n,
    customEndMs = 0n,
    opts?: { applyFocus?: boolean; focusAgent?: Agent; focusModel?: string },
  ) {
    if (!machineId || this.closed) return;
    const params = buildUsageOpenParams(preset, customStartMs, customEndMs);
    this.emit({
      case: "usageScreenOpen",
      value: create(UsageScreenOpenEventSchema, {
        machineId,
        preset: params.preset,
        customStartMs: params.customStartMs,
        customEndMs: params.customEndMs,
        timezone: params.timezone,
        tzOffsetMinutes: params.tzOffsetMinutes,
        bucketBoundsMs: params.bucketBoundsMs,
        prevStartMs: params.prevStartMs,
        applyFocus: opts?.applyFocus ?? false,
        focusAgent: opts?.focusAgent ?? (0 as Agent),
        focusModel: opts?.focusModel ?? "",
      }),
    });
  }

  retryUsageScreen(machineId: string) {
    if (machineId && !this.closed) {
      this.emit({ case: "usageScreenRetry", value: create(UsageScreenRetryEventSchema, { machineId }) });
    }
  }

  selectUsageSort(machineId: string, sortBy: UsageSortColumn) {
    if (machineId && !this.closed) {
      this.emit({
        case: "usageScreenSelectSort",
        value: create(UsageScreenSelectSortEventSchema, { machineId, sortBy }),
      });
    }
  }

  selectUsageSeries(machineId: string, seriesKey: string, toggle = true) {
    if (machineId && !this.closed) {
      this.emit({
        case: "usageScreenSelectSeries",
        value: create(UsageScreenSelectSeriesEventSchema, { machineId, seriesKey, toggle }),
      });
    }
  }

  scrubUsage(machineId: string, bucketIndex: number, clear = false) {
    if (machineId && !this.closed) {
      this.emit({
        case: "usageScreenScrub",
        value: create(UsageScreenScrubEventSchema, { machineId, bucketIndex, clear }),
      });
    }
  }

  updateMachineAgentDefaults(machineId: string, agent: Agent, patch: { model?: string; effort?: string; mode?: ApprovalMode }) {
    if (machineId && !this.closed) this.emit({ case: "updateMachineAgentDefaults", value: create(UpdateMachineAgentDefaultsEventSchema, { machineId, agent, ...patch }) });
  }

  updateMachineDaemonSettings(
    machineId: string,
    patch: { approvalTimeoutSeconds?: number; idleCloseMinutes?: number; imageUploadQuotaBytes?: bigint; cleanImages?: boolean },
  ) {
    if (machineId && !this.closed) this.emit({ case: "updateMachineDaemonSettings", value: create(UpdateMachineDaemonSettingsEventSchema, { machineId, ...patch, cleanImages: !!patch.cleanImages }) });
  }

  private directPrefPrefix(): string {
    return `remote-agent-direct:${this.remote?.account.server ?? "local"}:${this.remote?.account.deviceId ?? "local"}:`;
  }

  private directPrefKey(machineId: string): string {
    return `${this.directPrefPrefix()}${machineId}`;
  }

  private readStoredDirectPreferences(): DirectPreferenceEntry[] {
    if (typeof localStorage === "undefined") return [];
    const prefix = this.directPrefPrefix();
    const entries: DirectPreferenceEntry[] = [];
    try {
      for (let i = 0; i < localStorage.length; i++) {
        const k = localStorage.key(i);
        if (!k || !k.startsWith(prefix)) continue;
        const machineId = k.slice(prefix.length);
        if (!machineId) continue;
        entries.push(
          create(DirectPreferenceEntrySchema, {
            machineId,
            enabled: localStorage.getItem(k) !== "off",
          }),
        );
      }
    } catch { /* Storage unavailable. */ }
    return entries;
  }

  updateDirectPreference(machineId: string, enabled: boolean) {
    if (!machineId || this.closed) return;
    this.setDirectError(machineId, "");
    this.emit({ case: "updateDirectPreference", value: create(UpdateDirectPreferenceEventSchema, { machineId, enabled }) });
  }

  retryDirectNow(machineId: string): Promise<boolean> {
    if (!machineId || this.closed || !this.started) return Promise.resolve(false);
    return new Promise<boolean>((resolve) => {
      let list = this.retryNowWaiters.get(machineId);
      if (!list) {
        list = [];
        this.retryNowWaiters.set(machineId, list);
      }
      list.push(resolve);
      this.emit({ case: "directRetryNow", value: create(DirectRetryNowActionSchema, { machineId }) });
    });
  }

  refreshDirectDiagnostics(machineId: string) {
    if (machineId && !this.closed && this.started) {
      this.emit({ case: "directRefreshDiagnostics", value: create(DirectRefreshDiagnosticsActionSchema, { machineId }) });
    }
  }

  sendTerminalStream(terminalId: string, kind: TerminalStreamEventKind, opened: boolean) {
    if (terminalId && !this.closed) {
      this.emit({ case: "terminalStream", value: create(TerminalStreamEventSchema, { terminalId, kind, opened }) });
    }
  }

  onTerminalReopen(terminalId: string, fn: () => void): () => void {
    this.terminalReopenListeners.set(terminalId, fn);
    return () => {
      if (this.terminalReopenListeners.get(terminalId) === fn) this.terminalReopenListeners.delete(terminalId);
    };
  }

  sendTerminalHistory(terminalId: string, kind: TerminalHistoryEventKind, rawPage?: Uint8Array<ArrayBuffer>) {
    if (terminalId && !this.closed) {
      this.emit({
        case: "terminalHistory",
        value: create(TerminalHistoryEventSchema, { terminalId, kind, rawPage }),
      });
    }
  }

  onTerminalHistorySendRequest(terminalId: string, fn: (req: TerminalHistorySendRequestEffect) => void): () => void {
    this.terminalHistoryRequestListeners.set(terminalId, fn);
    return () => {
      if (this.terminalHistoryRequestListeners.get(terminalId) === fn) {
        this.terminalHistoryRequestListeners.delete(terminalId);
      }
    };
  }

  onTerminalHistoryUpdated(terminalId: string, fn: (upd: TerminalHistoryUpdatedEffect) => void): () => void {
    this.terminalHistoryUpdatedListeners.set(terminalId, fn);
    return () => {
      if (this.terminalHistoryUpdatedListeners.get(terminalId) === fn) {
        this.terminalHistoryUpdatedListeners.delete(terminalId);
      }
    };
  }

  securityOp(op: SecurityOpRequestEvent["op"]): Promise<SecurityOpResultEffect> {
    if (this.closed) return Promise.reject(makeSecErr("unavailable"));
    this.ensureSecurityConfigured();
    const opId = this.nextSecurityOpId++;
    return new Promise<SecurityOpResultEffect>((resolve, reject) => {
      this.securityOpWaiters.set(opId, { resolve, reject });
      this.emit({
        case: "securityOpRequest",
        value: create(SecurityOpRequestEventSchema, { opId, op }),
      });
    });
  }

  dispatchDirectSignal(raw: Uint8Array<ArrayBuffer>) {
    if (!this.closed && raw.byteLength > 0) {
      this.emit({ case: "directSignalReceived", value: create(DirectSignalReceivedEventSchema, { raw }) });
    }
  }

  dispatchDirectSignalDelivery(frameId: Uint8Array<ArrayBuffer>, status: number, retryAfterMs: number) {
    if (!this.closed && frameId.byteLength > 0) {
      this.emit({ case: "directSignalDelivery", value: create(DirectSignalDeliveryEventSchema, { frameId, status, retryAfterMs: BigInt(Math.max(0, Math.floor(retryAfterMs))) }) });
    }
  }

  pingMachine(machineId: string, signal?: AbortSignal): Promise<void>;
  pingMachine(machineId: string, signal?: AbortSignal): Promise<void> {
    if (this.closed) return Promise.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    if (signal?.aborted) return Promise.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    const opId = ++this.nextPingOpId;
    return new Promise<void>((resolve, reject) => {
      const onAbort = () => {
        if (!this.pingWaiters.delete(opId)) return;
        if (!this.closed) {
          this.emit({ case: "cancelPingMachine", value: create(CancelPingMachineActionSchema, { opId }) });
        }
        reject(new Error(l10n.t("errors.terminal_connection_canceled")));
      };
      if (signal) signal.addEventListener("abort", onAbort, { once: true });
      this.pingWaiters.set(opId, {
        resolve: () => {
          signal?.removeEventListener("abort", onAbort);
          resolve();
        },
        reject: (err) => {
          signal?.removeEventListener("abort", onAbort);
          reject(err);
        },
      });
      this.emit({
        case: "pingMachine",
        value: create(PingMachineActionSchema, { opId, machineId }),
      });
    });
  }

  runMachineCommand(
    machineId: string,
    sessionId: string,
    cmd: MessageInitShape<typeof CommandSchema>["kind"],
    signal?: AbortSignal,
    skipConnectivityPreflight = false,
  ): Promise<void> {
    if (this.closed) return Promise.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    if (signal?.aborted) return Promise.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    const opId = ++this.nextMachineCmdOpId;
    return new Promise<void>((resolve, reject) => {
      const onAbort = () => {
        if (!this.machineCmdWaiters.delete(opId)) return;
        if (!this.closed) {
          this.emit({ case: "cancelMachineCommand", value: create(CancelMachineCommandActionSchema, { opId }) });
        }
        reject(new Error(l10n.t("errors.terminal_connection_canceled")));
      };
      if (signal) signal.addEventListener("abort", onAbort, { once: true });
      this.machineCmdWaiters.set(opId, {
        resolve: () => {
          signal?.removeEventListener("abort", onAbort);
          resolve();
        },
        reject: (err) => {
          signal?.removeEventListener("abort", onAbort);
          reject(err);
        },
      });
      this.emit({
        case: "runMachineCommand",
        value: create(RunMachineCommandActionSchema, {
          opId,
          machineId,
          sessionId,
          command: create(CommandSchema, { kind: cmd }),
          skipConnectivityPreflight,
        }),
      });
    });
  }

  updateClientName(deviceId: string, name: string, restoreDefault = false) {
    if (!deviceId || this.closed) return;
    this.setClientRenameState(deviceId, { saving: false, error: "" });
    this.emit({ case: "updateClientName", value: create(UpdateClientNameEventSchema, { deviceId, name, restoreDefault }) });
  }

  setNotificationPref(patch: Omit<Partial<SetNotificationPrefEvent>, "$typeName">) {
    if (!this.closed) this.emit({ case: "setNotificationPref", value: create(SetNotificationPrefEventSchema, patch) });
  }

  enablePushNotifications() {
    if (this.closed || !this.remote) return;
    const remote = this.remote;
    const prefs = this.lastPersistedPrefs ?? pushPrefs();
    this.setNotificationPref({ pushState: PushRegistrationState.REGISTERING, errorDetail: "" });
    void setPushPrefs(remote, prefs).then(
      () => {
        if (this.closed) return;
        const ns = readNotificationState();
        this.emitNotificationSync({ supported: ns.supported, permission: ns.permission, pushState: ns.pushState, errorDetail: "" });
      },
      (err) => {
        if (this.closed) return;
        const ns = readNotificationState();
        this.emitNotificationSync({ supported: ns.supported, permission: ns.permission, pushState: PushRegistrationState.FAILED, errorDetail: errorMessage(err) });
      },
    );
  }

  setAppearancePref(theme: ThemePreference) {
    if (!this.closed) this.emit({ case: "setAppearancePref", value: create(SetAppearancePrefEventSchema, { theme }) });
  }

  tickClock() {
    if (!this.closed) this.emit({ case: "clockUpdated", value: localCalendar() });
  }

  reportConnCheckStatus(serverState: ConnProbeState, latencyMs = 0, lastFailure = "") {
    if (!this.closed) this.emit({ case: "connCheckStatus", value: create(ConnCheckStatusEventSchema, { serverState, latencyMs, lastFailure }) });
  }

  reportClientUpdateState(ev: Omit<Partial<ClientUpdateStateEvent>, "$typeName">) {
    if (!this.closed) this.emit({ case: "clientUpdateState", value: create(ClientUpdateStateEventSchema, ev) });
  }

  checkForUpdates() {
    if (!this.closed) this.emit({ case: "checkForUpdates", value: create(CheckForUpdatesEventSchema) });
  }

  // --- Subscriptions & ViewModel lookups ---

  subscribe(target: ScreenTarget, fn: () => void): () => void {
    const set = this.listeners.get(target);
    if (!set) return () => {};
    set.add(fn);
    this.totalListeners++;
    return () => {
      if (set.delete(fn)) this.totalListeners--;
    };
  }

  version(target: ScreenTarget): number {
    return this.screenVersions.get(target) ?? 0;
  }

  viewHome(endedLimit = 30): HomeViewModel {
    const ver = this.version(ScreenTarget.HOME);
    const cached = this.homeCache.get(endedLimit);
    if (cached && cached.version === ver) {
      return cached.vm;
    }
    const value = this.queryView<HomeViewModel>({ case: "home", value: create(HomeScreenRequestSchema, { endedLimit }) }, "home");
    this.homeCache.set(endedLimit, { version: ver, vm: value });
    return value;
  }

  viewProject(projectKey: string, manage: boolean, endedLimit = 30, machineId = "", pickerKey = ""): ProjectViewModel {
    const ver = this.version(ScreenTarget.PROJECT);
    const cacheKey = `${projectKey}\0${manage ? 1 : 0}\0${endedLimit}\0${machineId}\0${pickerKey}`;
    const cached = this.projectCache.get(cacheKey);
    if (cached && cached.version === ver) {
      return cached.vm;
    }
    const value = this.queryView<ProjectViewModel>(
      { case: "project", value: create(ProjectScreenRequestSchema, { projectKey, manage, endedLimit, machineId, pickerKey }) },
      "project",
    );
    this.projectCache.set(cacheKey, { version: ver, vm: value });
    return value;
  }

  viewNewSession(projectKey = ""): NewSessionViewModel {
    const ver = this.version(ScreenTarget.NEW_SESSION);
    if (this.newSessionCache && this.newSessionCache.version === ver && this.newSessionCache.key === projectKey) {
      return this.newSessionCache.vm;
    }
    const value = this.queryView<NewSessionViewModel>({ case: "newSession", value: create(NewSessionScreenRequestSchema, { projectKey }) }, "newSession");
    this.newSessionCache = { version: ver, key: projectKey, vm: value };
    return value;
  }

  viewAttachmentDraft(scope: DraftScope): AttachmentDraftViewModel {
    const ver = this.version(ScreenTarget.ATTACHMENT_DRAFT);
    const key = draftScopeKey(scope);
    const cached = this.attachmentDraftCache.get(key);
    if (cached && cached.version === ver) return cached.vm;
    const value = this.queryView<AttachmentDraftViewModel>(
      { case: "attachmentDraft", value: create(AttachmentDraftScreenRequestSchema, { scope }) },
      "attachmentDraft",
    );
    this.attachmentDraftCache.set(key, { version: ver, vm: value });
    return value;
  }

  viewConnection(): ConnectionViewModel {
    const ver = this.version(ScreenTarget.CONNECTION);
    if (this.connectionCache && this.connectionCache.version === ver) {
      return this.connectionCache.vm;
    }
    const value = this.queryView<ConnectionViewModel>(
      { case: "connection", value: create(ConnectionScreenRequestSchema, {}) },
      "connection",
    );
    this.connectionCache = { version: ver, vm: value };
    return value;
  }

  viewSession(sessionId: string, window?: SessionWindowOptions): SessionViewModel {
    const ver = this.version(ScreenTarget.SESSION);
    const expandedGroupIds = window?.expandedGroupIds ?? [];
    const windowKey = window
      ? `${window.limit ?? 0}:${window.beforeSeq ?? 0n}:${window.aroundSeq ?? 0n}:${window.revealSeq ?? 0n}:${window.revealRunId ?? ""}:${expandedGroupIds.join(",")}`
      : "";
    const cacheKey = `${sessionId}\0${windowKey}`;
    const cached = this.sessionCache.get(cacheKey);
    if (cached && cached.version === ver) {
      return cached.vm;
    }
    const value = this.queryView<SessionViewModel>(
      {
        case: "session",
        value: create(SessionScreenRequestSchema, {
          sessionId,
          window: window
            ? create(SessionWindowRequestSchema, {
                limit: window.limit ?? 0,
                beforeSeq: window.beforeSeq ?? 0n,
                aroundSeq: window.aroundSeq ?? 0n,
                revealSeq: window.revealSeq ?? 0n,
                revealRunId: window.revealRunId ?? "",
                expandedGroupIds,
              })
            : undefined,
        }),
      },
      "session",
    );
    this.sessionCache.set(cacheKey, { version: ver, vm: value });
    return value;
  }

  viewSessionHeader(sessionId: string): SessionHeaderView | undefined {
    const ver = this.version(ScreenTarget.SESSION);
    const prefix = `${sessionId}\0`;
    for (const [key, entry] of this.sessionCache) {
      if (key.startsWith(prefix) && entry.version === ver) {
        return entry.vm.header;
      }
    }
    return this.viewSession(sessionId).header;
  }

  private queryView<T>(
    screen: NonNullable<Parameters<typeof create<typeof ScreenSchema>>[1]>["screen"],
    expectedCase: NonNullable<ViewModel["view"]["case"]>,
  ): T {
    const req = create(ScreenSchema, { screen } as Parameters<typeof create<typeof ScreenSchema>>[1]);
    const vm = fromBinary(ViewModelSchema, this.core.view(toBinary(ScreenSchema, req)));
    if (vm.view.case !== expectedCase) throw new Error("unexpected view model case: " + vm.view.case);
    return vm.view.value as T;
  }

  viewSchedules(endedLimit = 30): SchedulesViewModel {
    const ver = this.version(ScreenTarget.SCHEDULES);
    if (this.schedulesCache && this.schedulesCache.version === ver && this.schedulesCache.endedLimit === endedLimit) {
      return this.schedulesCache.vm;
    }
    const value = this.queryView<SchedulesViewModel>({ case: "schedules", value: create(SchedulesScreenRequestSchema, { endedLimit }) }, "schedules");
    this.schedulesCache = { version: ver, endedLimit, vm: value };
    return value;
  }

  viewScheduleDetail(machineId: string, taskId: string, earlierRunsLimit = 10): ScheduleDetailViewModel {
    const ver = this.version(ScreenTarget.SCHEDULE_DETAIL);
    const key = `${machineId}\0${taskId}\0${earlierRunsLimit}`;
    if (this.scheduleDetailCache && this.scheduleDetailCache.version === ver && this.scheduleDetailCache.key === key) {
      return this.scheduleDetailCache.vm;
    }
    const value = this.queryView<ScheduleDetailViewModel>(
      { case: "scheduleDetail", value: create(ScheduleDetailScreenRequestSchema, { machineId, taskId, earlierRunsLimit }) },
      "scheduleDetail",
    );
    this.scheduleDetailCache = { version: ver, key, vm: value };
    return value;
  }

  viewScheduleForm(formKey = ""): ScheduleFormViewModel {
    const ver = this.version(ScreenTarget.SCHEDULE_FORM);
    const cached = this.scheduleFormCache.get(formKey);
    if (cached && cached.version === ver) return cached.vm;
    const value = this.queryView<ScheduleFormViewModel>(
      { case: "scheduleForm", value: create(ScheduleFormScreenRequestSchema, { formKey }) },
      "scheduleForm",
    );
    if (value.open && value.formKey) this.openFormKeys.add(value.formKey);
    this.scheduleFormCache.set(formKey, { version: ver, vm: value });
    return value;
  }

  viewScheduleSessionStrip(sessionId: string): ScheduleSessionStripViewModel {
    const ver = this.version(ScreenTarget.SCHEDULE_SESSION_STRIP);
    const cached = this.scheduleStripCache.get(sessionId);
    if (cached && cached.version === ver) return cached.vm;
    const value = this.queryView<ScheduleSessionStripViewModel>(
      { case: "scheduleSessionStrip", value: create(ScheduleSessionStripScreenRequestSchema, { sessionId }) },
      "scheduleSessionStrip",
    );
    this.scheduleStripCache.set(sessionId, { version: ver, vm: value });
    return value;
  }

  viewSettings(): SettingsViewModel {
    const ver = this.version(ScreenTarget.SETTINGS);
    if (this.settingsCache?.version === ver) return this.settingsCache.vm;
    const vm = this.queryView<SettingsViewModel>({ case: "settings", value: {} }, "settings");
    this.settingsCache = { version: ver, vm };
    return vm;
  }

  viewMachineStatus(machineId: string): MachineStatusViewModel {
    const ver = this.version(ScreenTarget.MACHINE_STATUS);
    const cached = this.machineStatusCache.get(machineId);
    if (cached?.version === ver) return cached.vm;
    const vm = this.queryView<MachineStatusViewModel>({ case: "machineStatus", value: create(MachineStatusScreenRequestSchema, { machineId }) }, "machineStatus");
    this.machineStatusCache.set(machineId, { version: ver, vm });
    return vm;
  }

  viewMachineSettings(machineId: string): MachineSettingsViewModel {
    const ver = this.version(ScreenTarget.MACHINE_SETTINGS);
    const cached = this.machineSettingsCache.get(machineId);
    if (cached?.version === ver) return cached.vm;
    const vm = this.queryView<MachineSettingsViewModel>({ case: "machineSettings", value: create(MachineSettingsScreenRequestSchema, { machineId }) }, "machineSettings");
    this.machineSettingsCache.set(machineId, { version: ver, vm });
    return vm;
  }

  viewQuota(machineId: string): QuotaViewModel {
    const ver = this.version(ScreenTarget.QUOTA);
    const cached = this.quotaCache.get(machineId);
    if (cached?.version === ver) return cached.vm;
    const vm = this.queryView<QuotaViewModel>({ case: "quota", value: create(QuotaScreenRequestSchema, { machineId }) }, "quota");
    this.quotaCache.set(machineId, { version: ver, vm });
    return vm;
  }

  viewDevices(): DevicesViewModel {
    const ver = this.version(ScreenTarget.DEVICES);
    if (this.devicesCache?.version === ver) return this.devicesCache.vm;
    const vm = this.queryView<DevicesViewModel>({ case: "devices", value: {} }, "devices");
    this.devicesCache = { version: ver, vm };
    return vm;
  }

  viewClientDetail(deviceId: string): ClientDetailViewModel {
    const ver = this.version(ScreenTarget.CLIENT_DETAIL);
    const cached = this.clientDetailCache.get(deviceId);
    if (cached?.version === ver) return cached.vm;
    const vm = this.queryView<ClientDetailViewModel>({ case: "clientDetail", value: create(ClientDetailScreenRequestSchema, { deviceId }) }, "clientDetail");
    this.clientDetailCache.set(deviceId, { version: ver, vm });
    return vm;
  }

  viewUsage(req: {
    machineId: string;
    focusAgent?: Agent;
    focusModel?: string;
    preset?: UsageRangePreset;
    customStartMs?: bigint;
    customEndMs?: bigint;
  }): UsageViewModel {
    const ver = this.version(ScreenTarget.USAGE);
    const preset = !req.preset ? UsageRangePreset.SEVEN_DAYS : req.preset;
    const clamped = preset === UsageRangePreset.CUSTOM ? clampUsageCustomRange(req.customStartMs ?? 0n, req.customEndMs ?? 0n) : { startMs: 0n, endMs: 0n };
    const focusAgent = req.focusAgent ?? (0 as Agent);
    const focusModel = req.focusModel ?? "";
    const cacheKey = `${req.machineId}:${focusAgent}:${focusModel}:${preset}:${clamped.startMs}:${clamped.endMs}`;
    const cached = this.usageCache.get(cacheKey);
    if (cached?.version === ver) return cached.vm;
    const vm = this.queryView<UsageViewModel>(
      {
        case: "usage",
        value: create(UsageScreenRequestSchema, {
          machineId: req.machineId,
          focusAgent,
          focusModel,
          preset,
          customStartMs: clamped.startMs,
          customEndMs: clamped.endMs,
        }),
      },
      "usage",
    );
    this.usageCache.set(cacheKey, { version: ver, vm });
    return vm;
  }

  viewDirectDiagnostics(machineId: string): DirectDiagnosticsViewModel {
    const ver = this.version(ScreenTarget.DIRECT_DIAGNOSTICS);
    const cached = this.directDiagnosticsCache.get(machineId);
    if (cached?.version === ver) return cached.vm;
    const vm = this.queryView<DirectDiagnosticsViewModel>(
      {
        case: "directDiagnostics",
        value: create(DirectDiagnosticsScreenRequestSchema, { machineId }),
      },
      "directDiagnostics",
    );
    this.directDiagnosticsCache.set(machineId, { version: ver, vm });
    return vm;
  }

  // --- Dispatch & Effect Execution ---

  dispatch(ev: Event, onProcessed?: () => void) {
    if (this.closed) {
      onProcessed?.();
      this.executor?.releaseEventRefs?.(ev);
      return;
    }
    if (ev.nowMs === 0n) ev.nowMs = BigInt(Date.now());
    if (ev.monoMs === 0n) ev.monoMs = BigInt(Math.max(0, Math.floor(performance.now())));

    if (ev.event.case === "cacheBootstrapLoaded") {
      this.bootstrapped = true;
    }
    if (ev.event.case === "newSessionSubmit") {
      this.pendingStart = true;
    }
    if (
      ev.event.case === "projectTogglePin" ||
      ev.event.case === "projectToggleHide" ||
      ev.event.case === "projectAdd"
    ) {
      this.lastProjectPrefEvent = ev;
    }

    this.pendingEvents.push({ ev, onProcessed });
    if (this.draining) return;

    this.draining = true;
    try {
      while ((this.pendingEvents.length > 0 || this.dirtyTargets.size > 0) && !this.closed) {
        while (this.pendingEvents.length > 0 && !this.closed) {
          const next = this.pendingEvents.shift()!;
          try {
            const batchBytes = this.core.dispatch(toBinary(EventSchema, next.ev));
            const batch = fromBinary(EffectBatchSchema, batchBytes);
            for (const eff of batch.effects) {
              if (this.closed) break;
              this.handleEffect(eff);
            }
          } finally {
            next.onProcessed?.();
            this.executor?.releaseEventRefs?.(next.ev);
          }
        }
        this.flushRenders();
      }
    } finally {
      this.draining = false;
    }
  }

  private handleEffect(eff: Effect) {
    const val = eff.effect;
    switch (val.case) {
      case "render":
        for (const target of val.value.screens) {
          if (target !== ScreenTarget.UNSPECIFIED) {
            this.invalidateTarget(target);
            this.dirtyTargets.add(target);
          }
        }
        break;

      case "scheduleTimer": {
        const { timerId, kind, key, delayMs } = val.value;
        const tk = timerKey(kind, key);
        const prev = this.timers.get(tk);
        if (prev) clearTimeout(prev.handle);
        const handle = setTimeout(() => {
          const cur = this.timers.get(tk);
          if (!cur || cur.id !== timerId) return;
          this.timers.delete(tk);
          if (!this.closed) this.emit({ case: "timerFired", value: create(TimerFiredEventSchema, { timerId, kind, key }) });
        }, Math.max(0, Number(delayMs)));
        this.timers.set(tk, { id: timerId, handle });
        break;
      }

      case "cancelTimer": {
        const { timerId, kind, key } = val.value;
        const tk = timerKey(kind, key);
        const cur = this.timers.get(tk);
        if (cur && (timerId === 0n || cur.id === timerId)) {
          clearTimeout(cur.handle);
          this.timers.delete(tk);
        }
        break;
      }

      case "openStream":
        this.executor?.openStreamLeg(
          val.value.legId,
          val.value.token,
          {
            onReady: (id, serverTimeMs) => !this.closed && this.emit({ case: "streamReady", value: create(StreamReadyEventSchema, { legId: id, serverTimeMs }) }),
            onRawFrame: (id, rawServerFrame) =>
              !this.closed && this.emit({ case: "streamRawFrame", value: create(StreamRawFrameEventSchema, { legId: id, rawServerFrame }) }),
            onClosed: (id, info) => !this.closed && this.emit({ case: "streamClosed", value: create(StreamClosedEventSchema, { legId: id, ...info }) }),
          },
          val.value.helloFrame.byteLength > 0 ? new Uint8Array(val.value.helloFrame) : undefined,
        );
        break;

      case "closeStream":
        this.executor?.closeStreamLeg(val.value.legId);
        break;

      case "cacheRead": {
        const kind = val.value.kind;
        if (kind.case === "bootstrap") {
          const opId = kind.value.opId;
          const boot = this.preloadedBootstrap;
          this.preloadedBootstrap = undefined;
          const read = boot
            ? Promise.resolve(create(CacheBootstrapLoadedEventSchema, { ...boot, opId }))
            : this.cacheWriteQueue.then(async () => {
                await this.ensureSecurityBootstrapped();
                return this.executor?.readBootstrapCache(opId);
              });
          void read.then(
            (value) => !this.closed && value && this.emit({ case: "cacheBootstrapLoaded", value }),
            (err) => !this.closed && this.emit({ case: "cacheBootstrapLoaded", value: create(CacheBootstrapLoadedEventSchema, { opId, error: errorMessage(err) }) }),
          );
        } else if (kind.case === "events") {
          const { opId, sessionId, newest, afterSeq, limit } = kind.value;
          void this.cacheWriteQueue
            .then(() => this.executor?.readEventsCache(opId, sessionId, newest, afterSeq, limit))
            .then(
              (value) => !this.closed && value && this.emit({ case: "cacheEventsLoaded", value }),
              (err) => !this.closed && this.emit({ case: "cacheEventsLoaded", value: create(CacheEventsLoadedEventSchema, { opId, sessionId, error: errorMessage(err) }) }),
            );
        }
        break;
      }

      case "cacheWrite": {
        const writeEff = val.value;
        const prepared = this.executor?.prepareCacheWrite
          ? this.executor.prepareCacheWrite(writeEff)
          : () => this.executor?.writeCache(writeEff);
        if (prepared) this.cacheWriteQueue = this.cacheWriteQueue.then(() => prepared()).catch(() => undefined);
        break;
      }

      case "cacheDelete": {
        const delEff = val.value;
        this.cacheWriteQueue = this.cacheWriteQueue.then(() => this.executor?.deleteCache(delEff)).catch(() => undefined);
        if (delEff.kind.case === "removeSessionId" && delEff.kind.value) {
          this.sharedFileForgetSession(delEff.kind.value);
          this.draftForgetSession(delEff.kind.value);
        }
        break;
      }

      case "relayRpc":
        if (this.executor?.relayRpc) this.executor.relayRpc(val.value);
        else if (this.executor?.securityEffect) this.executor.securityEffect(val);
        else if (this.ownSecExec) void this.ownSecExec.relayRpc(val.value);
        break;

      case "relayRpcCancel":
        if (this.executor?.relayRpcCancel) this.executor.relayRpcCancel(val.value);
        else if (this.executor?.securityEffect) this.executor.securityEffect(val);
        else if (this.ownSecExec) this.ownSecExec.relayRpcCancel(val.value);
        break;

      case "sessionImageLoaded": {
        const res = val.value;
        const w = this.imageFetchWaiters.get(res.opId);
        if (w) {
          this.imageFetchWaiters.delete(res.opId);
          w.resolve(res);
        }
        break;
      }

      case "pushRpcResult": {
        const res = val.value;
        const w = this.pushRpcWaiters.get(res.opId);
        if (w) {
          this.pushRpcWaiters.delete(res.opId);
          if (res.ok) w.resolve(res);
          else w.reject(new Error(res.error || "push rpc failed"));
        }
        break;
      }

      case "dispatchCommand": {
        if (!this.executor) break;
        const cmdEff = val.value;
        const ac = new AbortController();
        let byAttempt = this.inFlightDispatches.get(cmdEff.requestId);
        if (!byAttempt) {
          byAttempt = new Map();
          this.inFlightDispatches.set(cmdEff.requestId, byAttempt);
        }
        byAttempt.set(cmdEff.attemptId, ac);
        const cleanup = () => {
          const map = this.inFlightDispatches.get(cmdEff.requestId);
          if (map) {
            map.delete(cmdEff.attemptId);
            if (map.size === 0) this.inFlightDispatches.delete(cmdEff.requestId);
          }
        };
        void this.executor.executeCommandAttempt(cmdEff, ac.signal).then(
          (value) => {
            cleanup();
            if (!this.closed && !ac.signal.aborted) this.emit({ case: "commandOutcome", value });
          },
          (err) => {
            cleanup();
            if (this.closed || ac.signal.aborted) return;
            const c = err as { code?: string; fromDaemon?: boolean; outcome?: string; message?: string };
            const value = c?.fromDaemon
              ? create(CommandOutcomeEventSchema, {
                  requestId: cmdEff.requestId,
                  attemptId: cmdEff.attemptId,
                  source: OutcomeSource.DAEMON,
                  signed: true,
                  hashesMatch: true,
                  attemptBound: true,
                  outerMatchesInner: true,
                  outerOutcome: c.outcome || "executed",
                  outerReason: c.code || "",
                  rawResult: toBinary(CommandResultSchema, create(CommandResultSchema, { outcome: c.outcome || "executed", errorCode: c.code || "", errorMessage: c.message || "" })),
                })
              : create(CommandOutcomeEventSchema, {
                  requestId: cmdEff.requestId,
                  attemptId: cmdEff.attemptId,
                  source: OutcomeSource.LOCAL,
                  notForwarded: c?.outcome !== "unknown",
                  transportCode: c?.code || "failed_precondition",
                  transportDetail: errorMessage(err),
                });
            this.emit({ case: "commandOutcome", value });
          },
        );
        break;
      }

      case "cancelDispatch": {
        const map = this.inFlightDispatches.get(val.value.requestId);
        if (map) {
          this.inFlightDispatches.delete(val.value.requestId);
          for (const ac of map.values()) ac.abort();
        }
        break;
      }

      case "saveHomeScope":
        writeStoredScope(val.value.scopeToken);
        break;

      case "navigate": {
        const nav = val.value;
        const replaceSession = nav.route === NavigateRoute.SESSION && this.pendingStart;
        if (nav.route === NavigateRoute.SESSION && nav.sessionId) {
          this.pendingStart = false;
        }
        if (nav.route === NavigateRoute.SESSION && !replaceSession && this.watchCounts.size === 0) {
          break;
        }
        this.navigateTo(nav.route, nav.projectKey, nav.sessionId, nav.machineId, replaceSession);
        break;
      }

      case "requestEntropy": {
        this.pendingEvents.push({
          ev: create(EventSchema, {
            nowMs: BigInt(Date.now()),
            monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
            event: { case: "entropyProvided", value: buildEntropyProvided(val.value.minIds, val.value.minFloats, val.value.minRandomBytes) },
          }),
        });
        break;
      }

      case "pingMachineResult": {
        const res = val.value;
        const w = this.pingWaiters.get(res.opId);
        if (w) {
          this.pingWaiters.delete(res.opId);
          if (res.ok) {
            w.resolve();
          } else {
            const msg = renderText(res.errorText) || res.errorCode || l10n.t("errors.connection_timed_out");
            w.reject(new Error(msg));
          }
        }
        break;
      }

      case "runMachineCommandResult": {
        const res = val.value;
        const w = this.machineCmdWaiters.get(res.opId);
        if (w) {
          this.machineCmdWaiters.delete(res.opId);
          if (res.ok) {
            w.resolve();
          } else {
            const msg = renderText(res.errorText) || res.errorCode || l10n.t("errors.connection_timed_out");
            w.reject(new Error(msg));
          }
        }
        break;
      }

      case "sessionRestoreComposer": {
        const eff = val.value;
        const set = this.restoreComposerListeners.get(eff.sessionId);
        if (set) for (const fn of [...set]) fn(eff);
        break;
      }

      case "scheduleReadCache": {
        const { opId, machineId } = val.value;
        if (this.executor?.readScheduleCache) {
          const p = this.executor
            .readScheduleCache(opId, machineId)
            .then(
              (value) => {
                if (!this.closed) this.emit({ case: "scheduleCacheLoaded", value });
              },
              (err) => {
                if (!this.closed) {
                  this.emit({
                    case: "scheduleCacheLoaded",
                    value: create(ScheduleCacheLoadedEventSchema, { opId, machineId, error: errorMessage(err) }),
                  });
                }
              },
            )
            .finally(() => {
              this.scheduleCacheReads.delete(opId);
            });
          this.scheduleCacheReads.set(opId, p);
        } else {
          this.pendingEvents.push({
            ev: create(EventSchema, {
              nowMs: BigInt(Date.now()),
              monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
              event: {
                case: "scheduleCacheLoaded",
                value: create(ScheduleCacheLoadedEventSchema, { opId, machineId }),
              },
            }),
          });
        }
        break;
      }

      case "scheduleWriteCache": {
        const { machineId, blob } = val.value;
        const done = () => {
          if (!this.closed) {
            this.emit({
              case: "scheduleCacheWritten",
              value: create(ScheduleCacheWrittenEventSchema, { machineId }),
            });
          }
        };
        if (this.executor?.writeScheduleCache) {
          void this.executor.writeScheduleCache(machineId, blob).then(done, done);
        } else {
          done();
        }
        break;
      }

      case "scheduleDeleteCache":
        void this.executor?.deleteScheduleCache?.(val.value.machineId)?.catch(() => undefined);
        break;

      case "scheduleLoadSeen": {
        const { machineId } = val.value;
        if (this.executor?.loadScheduleSeen) {
          void this.executor.loadScheduleSeen(machineId).then(
            (value) => !this.closed && this.emit({ case: "scheduleSeenLoaded", value }),
            (err) =>
              !this.closed &&
              this.emit({
                case: "scheduleSeenLoaded",
                value: create(ScheduleSeenLoadedEventSchema, { machineId, error: errorMessage(err) }),
              }),
          );
        } else {
          this.pendingEvents.push({
            ev: create(EventSchema, {
              nowMs: BigInt(Date.now()),
              monoMs: BigInt(Math.max(0, Math.floor(performance.now()))),
              event: {
                case: "scheduleSeenLoaded",
                value: create(ScheduleSeenLoadedEventSchema, { machineId, found: false }),
              },
            }),
          });
        }
        break;
      }

      case "scheduleSaveSeen":
        void this.executor?.saveScheduleSeen?.(val.value.machineId, val.value.tokens)?.catch(() => undefined);
        break;

      case "scheduleDismissNotifications":
        this.executor?.dismissScheduleNotifications?.(val.value.runs);
        break;

      case "scheduleNavigate": {
        const nav = val.value;
        switch (nav.route) {
          case ScheduleNavigateRoute.LIST:
            this.go?.("tasks");
            break;
          case ScheduleNavigateRoute.DETAIL:
            if (nav.machineId && nav.taskId) this.go?.(routeForTask(nav.machineId, nav.taskId));
            break;
          case ScheduleNavigateRoute.SESSION:
            if (nav.sessionId && (nav.runId || nav.firstSeq > 0n)) {
              this.scheduleFocus.set(nav.sessionId, { runId: nav.runId, seq: nav.firstSeq });
            }
            if (nav.sessionId) this.go?.(routeForSession(nav.sessionId));
            break;
          case ScheduleNavigateRoute.FORM_TIMING:
            for (const key of nav.formKey ? [nav.formKey, ""] : [""]) {
              const set = this.scheduleFormTimingListeners.get(key);
              if (set) for (const fn of [...set]) fn(nav.formKey);
            }
            break;
          case ScheduleNavigateRoute.CLOSE_FORM:
            if (nav.formKey) this.openFormKeys.delete(nav.formKey);
            for (const key of nav.formKey ? [nav.formKey, ""] : [""]) {
              const set = this.scheduleFormCloseListeners.get(key);
              if (set) for (const fn of [...set]) fn();
            }
            break;
          case ScheduleNavigateRoute.UNSPECIFIED:
            break;
        }
        break;
      }

      case "scheduleMachinePoll": {
        const { machineId, delayMs, generation, cancel } = val.value;
        const prev = this.machinePollTimers.get(machineId);
        if (prev) {
          clearTimeout(prev.handle);
          this.machinePollTimers.delete(machineId);
        }
        if (!cancel && !this.closed) {
          const handle = setTimeout(() => {
            const cur = this.machinePollTimers.get(machineId);
            if (!cur || cur.generation !== generation) return;
            this.machinePollTimers.delete(machineId);
            if (!this.closed) {
              this.emit({
                case: "machineStatusPollTick",
                value: create(MachineStatusPollTickEventSchema, { machineId, generation }),
              });
            }
          }, Math.max(0, Number(delayMs)));
          this.machinePollTimers.set(machineId, { generation, handle });
        }
        break;
      }

      case "directPreference": {
        const { machineId, enabled } = val.value;
        if (this.revertingDirect.delete(machineId)) break;
        const onFail = (err: unknown) => {
          if (this.closed) return;
          this.setDirectError(machineId, errorMessage(err));
          this.revertingDirect.add(machineId);
          this.emit({
            case: "updateDirectPreference",
            value: create(UpdateDirectPreferenceEventSchema, { machineId, enabled: !enabled }),
          });
        };
        try {
          if (typeof localStorage !== "undefined") {
            localStorage.setItem(this.directPrefKey(machineId), enabled ? "auto" : "off");
          }
        } catch (err) {
          onFail(err);
        }
        break;
      }

      case "renameClient": {
        const { deviceId, name } = val.value;
        if (!this.remote) break;
        const remote = this.remote;
        this.setClientRenameState(deviceId, { saving: true, error: "" });
        void Promise.resolve()
          .then(() => remote.renameDevice(deviceId, name))
          .then(() => {
            if (!this.closed) this.setClientRenameState(deviceId, { saving: false, error: "" });
          })
          .catch((err) => {
            if (!this.closed) this.setClientRenameState(deviceId, { saving: false, error: errorMessage(err) });
          });
        break;
      }

      case "checkUpdates":
        void checkUpdate(true);
        break;

      case "persistClientPrefs": {
        const eff = val.value;
        if (eff.theme !== ThemePreference.UNSPECIFIED) {
          const nextTheme = fromCoreTheme(eff.theme);
          if (getTheme() !== nextTheme) setTheme(nextTheme);
        }
        const sig = `${eff.notifyAttention}:${eff.notifyTurnEnded}:${eff.notifyScheduled}`;
        if (!this.syncingPrefs && !this.revertingPushPrefs && this.lastPushPrefSig !== sig) {
          const prevPrefs = this.lastPersistedPrefs ?? pushPrefs();
          const nextPrefs: PushPrefs = {
            attention: eff.notifyAttention,
            turns: eff.notifyTurnEnded,
            scheduled: eff.notifyScheduled,
          };
          this.lastPushPrefSig = sig;
          this.lastPersistedPrefs = nextPrefs;
          writeStoredPushPrefs(nextPrefs);
          if (this.remote) {
            const remote = this.remote;
            void setPushPrefs(remote, nextPrefs)
              .then(() => {
                if (this.closed) return;
                const ns = readNotificationState();
                this.emitNotificationSync({ supported: ns.supported, permission: ns.permission, pushState: ns.pushState, errorDetail: "" });
              })
              .catch((err) => {
                if (this.closed) return;
                const ns = readNotificationState();
                this.lastPersistedPrefs = prevPrefs;
                this.lastPushPrefSig = `${prevPrefs.attention}:${prevPrefs.turns}:${prevPrefs.scheduled !== false}`;
                writeStoredPushPrefs(prevPrefs);
                this.emitNotificationSync({
                  supported: ns.supported,
                  permission: ns.permission,
                  pushState: PushRegistrationState.FAILED,
                  notifyAttention: prevPrefs.attention,
                  notifyTurnEnded: prevPrefs.turns,
                  notifyScheduled: prevPrefs.scheduled !== false,
                  errorDetail: errorMessage(err),
                }, true);
              });
          }
        }
        break;
      }

      case "prepareCleanupSession": {
        const eff = val.value;
        if (eff.needsDirectoryPicker) {
          for (const fn of [...this.cleanupPickerListeners]) fn(eff);
          break;
        }
        if (eff.projectKey && this.remote) {
          const prompt = l10n.t("machine_status.cleanup_prompt");
          this.appendNewSessionPrompt(eff.projectKey, prompt);
          if (eff.machineId) this.remote.draftMachines.set(eff.projectKey, eff.machineId);
          this.remote.focusProject = eff.projectKey;
          this.remote.touch();
          this.go?.(routeForProject(eff.projectKey));
        }
        break;
      }

      case "usageCacheWrite":
        this.persistUsageCache(val.value.machineId, val.value.preset, val.value.rawSnapshot);
        if (!this.closed) {
          this.emit({
            case: "usageCacheWritten",
            value: create(UsageCacheWrittenEventSchema, { machineId: val.value.machineId }),
          });
        }
        break;

      case "watchedSessions":
        this.executor?.setWatchedSessions?.(val.value.sessions);
        break;

      case "usageCacheRequest":
        if (val.value.deleteCache) {
          this.deleteUsageCache(val.value.machineId);
        } else {
          this.hydrateUsageCache(val.value.machineId);
        }
        break;

      case "syncPushSubscription":
        if (this.remote && !this.closed) {
          void restorePushPrefs(this.remote);
        }
        break;

      case "directSignalSend":
        this.executor?.directSignalSend?.(val.value);
        break;

      case "directPcCreate":
        this.executor?.directPcCreate?.(val.value);
        break;

      case "directPcDescribe":
        this.executor?.directPcDescribe?.(val.value);
        break;

      case "directPcSetRemote":
        this.executor?.directPcSetRemote?.(val.value);
        break;

      case "directPcAddCandidates":
        this.executor?.directPcAddCandidates?.(val.value);
        break;

      case "directPcConfirm":
        this.executor?.directPcConfirm?.(val.value);
        break;

      case "directPcClose":
        this.executor?.directPcClose?.(val.value);
        break;

      case "directPcSendProbe":
        this.executor?.directPcSendProbe?.(val.value);
        break;

      case "directDcReadStats":
        this.executor?.directDcReadStats?.(val.value);
        break;

      case "terminalReopen": {
        this.terminalReopenListeners.get(val.value.terminalId)?.();
        break;
      }

      case "terminalHistorySendRequest": {
        this.terminalHistoryRequestListeners.get(val.value.terminalId)?.(val.value);
        break;
      }

      case "terminalHistoryUpdated": {
        this.terminalHistoryUpdatedListeners.get(val.value.terminalId)?.(val.value);
        break;
      }

      case "terminalDirectSend":
        this.executor?.terminalDirectSend?.(val.value);
        break;

      case "terminalOpenSocket":
        this.executor?.terminalOpenSocket?.(val.value);
        break;

      case "terminalCloseSocket":
        this.executor?.terminalCloseSocket?.(val.value);
        break;

      case "terminalDeliverOutput":
        this.executor?.terminalDeliverOutput?.(val.value);
        break;

      case "terminalStateChanged":
        this.executor?.terminalStateChanged?.(val.value);
        break;

      case "directRetryNowResult": {
        const list = this.retryNowWaiters.get(val.value.machineId);
        if (list && list.length > 0) {
          const fn = list.shift()!;
          if (list.length === 0) this.retryNowWaiters.delete(val.value.machineId);
          fn(val.value.started);
        }
        break;
      }

      case "securityTokenUpdated":
        this.executor?.securityTokenUpdated?.(val.value);
        break;

      case "securityWorkflowResult":
        this.securityClient.handleWorkflowResult(val.value);
        break;

      case "securityStatusChanged":
        if (val.value.status) {
          this.securityClient.handleStatusChanged(val.value.status);
        }
        this.executor?.securityStatusChanged?.(val.value);
        break;

      case "securityNotifyOpened":
        break;

      case "securityOpResult": {
        const res = val.value;
        const w = this.securityOpWaiters.get(res.opId);
        if (w) {
          this.securityOpWaiters.delete(res.opId);
          if (res.error) {
            if (res.structuredErrorCode === SecurityErrorCode.UNSUPPORTED_HISTORY) {
              w.reject(new UnsupportedHistoryError());
            } else {
              w.reject(makeSecErr(structuredToSecurityCode(res.structuredErrorCode)));
            }
          } else {
            w.resolve(res);
          }
        }
        break;
      }

      case "sharedFileScanLocal":
        this.sharedFiles.handleScanLocal(val.value);
        break;

      case "sharedFileInspectDisk":
        this.sharedFiles.handleInspectDisk(val.value);
        break;

      case "sharedFileWriteChunk":
        this.sharedFiles.handleWriteChunk(val.value);
        break;

      case "sharedFileVerifyAndCommit":
        this.sharedFiles.handleVerifyAndCommit(val.value);
        break;

      case "sharedFileDeletePartial":
        this.sharedFiles.handleDeletePartial(val.value);
        break;

      case "sharedFileDeleteLocal":
        this.sharedFiles.handleDeleteLocal(val.value);
        break;

      case "sharedFileDeleteSession":
        this.sharedFiles.handleDeleteSession(val.value);
        break;

      case "sharedFileClearAll":
        this.sharedFiles.handleClearAll();
        break;

      case "sharedFileCompleted":
        this.sharedFiles.handleCompleted(val.value);
        break;

      case "attachmentPrepareImage":
        this.attachments.handlePrepareImage(val.value);
        break;

      case "attachmentHashFile":
        this.attachments.handleHashFile(val.value);
        break;

      case "attachmentReadChunk":
        this.attachments.handleReadChunk(val.value);
        break;

      case "attachmentReleaseLocal":
        this.attachments.handleReleaseLocal(val.value);
        break;

      case "draftStorageList":
        this.attachments.handleDraftStorageList(val.value);
        break;

      case "draftStorageRead":
        this.attachments.handleDraftStorageRead(val.value);
        break;

      case "draftStorageWrite":
        this.attachments.handleDraftStorageWrite(val.value);
        break;

      case "draftStorageDelete":
        this.attachments.handleDraftStorageDelete(val.value);
        break;

      case "draftStorageClearAll":
        this.attachments.handleDraftStorageClearAll();
        break;

      case "requestStoragePersist": {
        const opId = val.value.opId;
        void requestAuthorityPersistence()
          .catch(() => false)
          .then((persisted) => {
            if (!this.closed) {
              this.emit({
                case: "storagePersistResult",
                value: create(StoragePersistResultEventSchema, { opId, persisted }),
              });
            }
          });
        break;
      }

      case "unregisterPushOnExit": {
        const { opId, server, authToken } = val.value;
        const pushTarget =
          this._remote ??
          (server
            ? createStandalonePushUnsubscriber({
                server,
                getToken: () => authToken,
                ensureToken: async () => authToken,
              })
            : undefined);
        void unregisterPushOnExit(pushTarget)
          .catch(() => undefined)
          .finally(() => {
            if (!this.closed) {
              this.emit({
                case: "pushUnregistered",
                value: create(PushUnregisteredEventSchema, { opId }),
              });
            }
          });
        break;
      }

      case "clearAccountCaches": {
        const { opId, preserveServer } = val.value;
        void (async () => {
          let errMsg = "";
          try {
            this.detachRemote();
            clearMarkdownCache();
            await clearEventCache().catch(() => undefined);
            try {
              localStorage.removeItem(SCOPE_STORAGE_KEY);
              if (preserveServer) {
                localStorage.setItem("ra-server", preserveServer);
              }
            } catch {
              /* ignore restricted storage */
            }
          } catch (e) {
            errMsg = errorMessage(e);
          }
          if (!this.closed) {
            this.emit({
              case: "accountCachesCleared",
              value: create(AccountCachesClearedEventSchema, { opId, error: errMsg }),
            });
          }
        })();
        break;
      }

      case "accountRuntimeChanged":
        this.lastRuntimeEffect = val.value;
        this.onAccountRuntimeChanged?.(val.value);
        break;

      case "dismissUpgradeNotice":
        securityStoreV4.dismissUpgradeNotice();
        break;

      case "persistLastServer":
        if (val.value.server) {
          try {
            localStorage.setItem("ra-server", val.value.server);
          } catch {
            /* ignore restricted storage */
          }
        }
        break;

      case undefined:
        break;

      default:
        if (this.executor?.securityEffect) {
          this.executor.securityEffect(val);
        } else if (this.ownSecExec) {
          dispatchSecurityEffect(this.ownSecExec, val);
        }
        break;
    }
    this.onEffect?.(eff);
  }

  private navigateTo(route: NavigateRoute, projectKey: string, sessionId: string, machineId: string, replace = false) {
    if (!this.go) return;
    switch (route) {
      case NavigateRoute.SESSION:
        if (sessionId) this.go(routeForSession(sessionId), replace || undefined);
        break;
      case NavigateRoute.PROJECT:
        this.go(routeForProject(projectKey));
        break;
      case NavigateRoute.MACHINE_SETTINGS:
        if (machineId) this.go(routeForMachineSettings(machineId));
        break;
      case NavigateRoute.MACHINE_STATUS:
        if (machineId) this.go(routeForMachineStatus(machineId));
        break;
      case NavigateRoute.UNSPECIFIED:
        break;
    }
  }

  private invalidateTarget(target: ScreenTarget) {
    this.screenVersions.set(target, (this.screenVersions.get(target) ?? 0) + 1);
    if (target === ScreenTarget.HOME) this.homeCache.clear();
    else if (target === ScreenTarget.PROJECT) this.projectCache.clear();
    else if (target === ScreenTarget.SESSION) this.sessionCache.clear();
    else if (target === ScreenTarget.NEW_SESSION) this.newSessionCache = undefined;
    else if (target === ScreenTarget.CONNECTION) this.connectionCache = undefined;
    else if (target === ScreenTarget.SCHEDULES) this.schedulesCache = undefined;
    else if (target === ScreenTarget.SCHEDULE_DETAIL) this.scheduleDetailCache = undefined;
    else if (target === ScreenTarget.SCHEDULE_FORM) this.scheduleFormCache.clear();
    else if (target === ScreenTarget.SCHEDULE_SESSION_STRIP) this.scheduleStripCache.clear();
    else if (target === ScreenTarget.SETTINGS) this.settingsCache = undefined;
    else if (target === ScreenTarget.MACHINE_STATUS) this.machineStatusCache.clear();
    else if (target === ScreenTarget.MACHINE_SETTINGS) this.machineSettingsCache.clear();
    else if (target === ScreenTarget.QUOTA) this.quotaCache.clear();
    else if (target === ScreenTarget.DEVICES) this.devicesCache = undefined;
    else if (target === ScreenTarget.CLIENT_DETAIL) this.clientDetailCache.clear();
    else if (target === ScreenTarget.USAGE) this.usageCache.clear();
    else if (target === ScreenTarget.DIRECT_DIAGNOSTICS) this.directDiagnosticsCache.clear();
    else if (target === ScreenTarget.ATTACHMENT_DRAFT) this.attachmentDraftCache.clear();
    else if (target === ScreenTarget.APP_ROOT) this.appRootCache = undefined;
    else if (target === ScreenTarget.ONBOARDING) this.onboardingCache = undefined;
    else if (target === ScreenTarget.ADD_DEVICE) this.addDeviceCache = undefined;
    else if (target === ScreenTarget.ACCOUNT_SECURITY) this.accountSecurityCache = undefined;
    else if (target === ScreenTarget.HANDOFF) this.handoffCache = undefined;
    else if (target === ScreenTarget.EXIT_CONFIRM) this.exitConfirmCache = undefined;
  }

  private flushRenders() {
    if (this.dirtyTargets.size === 0) return;
    const targets = [...this.dirtyTargets];
    this.dirtyTargets.clear();

    if (targets.includes(ScreenTarget.CONNECTION) || targets.includes(ScreenTarget.HOME) || targets.includes(ScreenTarget.PROJECT)) {
      for (const st of SETTINGS_TARGETS) {
        if (!targets.includes(st)) {
          this.invalidateTarget(st);
          if ((this.listeners.get(st)?.size ?? 0) > 0) targets.push(st);
        }
      }
    }
    for (const target of targets) {
      const listeners = this.listeners.get(target);
      if (listeners) for (const fn of [...listeners]) fn();
    }
  }

  fireActiveTimer(kind: TimerKind, key = ""): boolean {
    const tk = timerKey(kind, key);
    const cur = this.timers.get(tk);
    if (!cur) return false;
    clearTimeout(cur.handle);
    this.timers.delete(tk);
    if (!this.closed) {
      this.dispatch(
        create(EventSchema, {
          event: {
            case: "timerFired",
            value: create(TimerFiredEventSchema, { timerId: cur.id, kind, key }),
          },
        }),
      );
    }
    return true;
  }

  private clearTimers() {
    for (const entry of this.timers.values()) clearTimeout(entry.handle);
    this.timers.clear();
    for (const entry of this.machinePollTimers.values()) clearTimeout(entry.handle);
    this.machinePollTimers.clear();
  }

  dispose() {
    if (this.closed) return;
    this.unsubscribeLocale?.();
    this.unsubscribeLocale = undefined;
    this.unregisterSharedFileClearer?.();
    this.unregisterSharedFileClearer = undefined;
    this.unregisterAttachmentClearer?.();
    this.unregisterAttachmentClearer = undefined;
    this.sharedFiles.setAutoOpenHandler(undefined);
    this.stop();
    this.closed = true;
    this.pendingEvents = [];
    this.dirtyTargets.clear();
    for (const set of this.listeners.values()) set.clear();
    this.totalListeners = 0;
    this.knownDraftScopes.clear();
    this.attachmentDraftCache.clear();
    this.scheduleFormCloseListeners.clear();
    this.scheduleFormTimingListeners.clear();
    this.scheduleFocus.clear();
    this.openFormKeys.clear();
    this.cleanupPickerListeners.clear();
    this.extraListeners.clear();
    this.machineWatchCounts.clear();
    this.machineWatchActive.clear();
    this.terminalReopenListeners.clear();
    this.terminalHistoryRequestListeners.clear();
    this.terminalHistoryUpdatedListeners.clear();
    for (const map of this.inFlightDispatches.values()) {
      for (const ac of map.values()) ac.abort();
    }
    this.inFlightDispatches.clear();
    for (const w of this.pingWaiters.values()) {
      w.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    }
    this.pingWaiters.clear();
    for (const w of this.machineCmdWaiters.values()) {
      w.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    }
    this.machineCmdWaiters.clear();
    for (const w of this.pushRpcWaiters.values()) {
      w.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    }
    this.pushRpcWaiters.clear();
    for (const w of this.imageFetchWaiters.values()) {
      w.reject(new Error(l10n.t("errors.terminal_connection_canceled")));
    }
    this.imageFetchWaiters.clear();
    for (const list of this.retryNowWaiters.values()) {
      for (const fn of list) fn(false);
    }
    this.retryNowWaiters.clear();
    this.rejectSecurityOps();
    this.securityClient.rejectAll();
    this.ownSecExec?.close();
    this.attachments.store.releaseStorageLock?.();
    this.core.close();
  }
}

// --- React integration & singleton lookup ---

const hosts = new WeakMap<Remote, CoreHost>();

export function getCoreHost(remote: Remote): CoreHost {
  let host = hosts.get(remote);
  if (!host) {
    host = new CoreHost(remote);
    hosts.set(remote, host);
  }
  return host;
}

export function disposeCoreHost(remote: Remote): void {
  const host = hosts.get(remote);
  if (!host) return;
  hosts.delete(remote);
  host.dispose();
}

export function useCoreHost(remote: Remote, go?: Go): CoreHost {
  const host = getCoreHost(remote);
  useEffect(() => {
    if (go) host.setNavigator(go);
  }, [host, go]);
  return host;
}

function useScreenVersion(host: CoreHost, target: ScreenTarget): number {
  const subscribe = useCallback((fn: () => void) => host.subscribe(target, fn), [host, target]);
  const getSnapshot = useCallback(() => host.version(target), [host, target]);
  return useSyncExternalStore(subscribe, getSnapshot);
}

function useExtraVersion(host: CoreHost): number {
  const subscribe = useCallback((fn: () => void) => host.subscribeExtra(fn), [host]);
  const getSnapshot = useCallback(() => host.getExtraVersion(), [host]);
  return useSyncExternalStore(subscribe, getSnapshot);
}

export function useHomeView(host: CoreHost, endedLimit = 30): HomeViewModel {
  const version = useScreenVersion(host, ScreenTarget.HOME);
  return useMemo(() => host.viewHome(endedLimit), [host, version, endedLimit]);
}

export function useProjectView(host: CoreHost, projectKey: string, manage: boolean, endedLimit = 30, machineId = "", pickerKey = ""): ProjectViewModel {
  const version = useScreenVersion(host, ScreenTarget.PROJECT);
  return useMemo(() => host.viewProject(projectKey, manage, endedLimit, machineId, pickerKey), [host, version, projectKey, manage, endedLimit, machineId, pickerKey]);
}

export function useNewSessionView(host: CoreHost, projectKey = ""): NewSessionViewModel {
  const version = useScreenVersion(host, ScreenTarget.NEW_SESSION);
  return useMemo(() => host.viewNewSession(projectKey), [host, version, projectKey]);
}

export function useConnectionView(host: CoreHost): ConnectionViewModel {
  const version = useScreenVersion(host, ScreenTarget.CONNECTION);
  return useMemo(() => host.viewConnection(), [host, version]);
}

export function useSessionHeader(host: CoreHost, sessionId: string): SessionHeaderView | undefined {
  const subscribe = useCallback(
    (cb: () => void) => (sessionId ? host.subscribe(ScreenTarget.SESSION, cb) : () => {}),
    [host, sessionId],
  );
  const version = useSyncExternalStore(
    subscribe,
    () => (sessionId ? host.version(ScreenTarget.SESSION) : 0),
  );
  return useMemo(
    () => (sessionId ? host.viewSessionHeader(sessionId) : undefined),
    [host, version, sessionId],
  );
}

export function useSessionView(
  host: CoreHost,
  sessionId: string,
  window?: SessionWindowOptions,
  openOpts?: { refreshModels?: boolean; refreshSettings?: boolean },
): SessionViewModel {
  const refreshModels = !!openOpts?.refreshModels;
  const refreshSettings = !!openOpts?.refreshSettings;
  useEffect(() => {
    if (!sessionId) return;
    host.watchSession(sessionId);
    host.sessionOpen(sessionId, refreshModels, refreshSettings);
    return () => host.unwatchSession(sessionId);
  }, [host, sessionId, refreshModels, refreshSettings]);
  const version = useScreenVersion(host, ScreenTarget.SESSION);
  const limit = window?.limit;
  const beforeSeq = window?.beforeSeq;
  const aroundSeq = window?.aroundSeq;
  const revealSeq = window?.revealSeq;
  const revealRunId = window?.revealRunId;
  const expandedKey = (window?.expandedGroupIds ?? []).join(",");
  return useMemo(
    () =>
      host.viewSession(sessionId, {
        limit,
        beforeSeq,
        aroundSeq,
        revealSeq,
        revealRunId,
        expandedGroupIds: expandedKey ? expandedKey.split(",") : [],
      }),
    [host, version, sessionId, limit, beforeSeq, aroundSeq, revealSeq, revealRunId, expandedKey],
  );
}

export function useSchedulesView(host: CoreHost, endedLimit = 30): SchedulesViewModel {
  const version = useScreenVersion(host, ScreenTarget.SCHEDULES);
  return useMemo(() => host.viewSchedules(endedLimit), [host, version, endedLimit]);
}

export function useScheduleDetailView(host: CoreHost, machineId: string, taskId: string, earlierRunsLimit = 10): ScheduleDetailViewModel {
  const version = useScreenVersion(host, ScreenTarget.SCHEDULE_DETAIL);
  return useMemo(() => host.viewScheduleDetail(machineId, taskId, earlierRunsLimit), [host, version, machineId, taskId, earlierRunsLimit]);
}

export function useScheduleFormView(host: CoreHost, formKey = ""): ScheduleFormViewModel {
  const version = useScreenVersion(host, ScreenTarget.SCHEDULE_FORM);
  return useMemo(() => host.viewScheduleForm(formKey), [host, version, formKey]);
}

export function useScheduleSessionStripView(host: CoreHost, sessionId: string): ScheduleSessionStripViewModel {
  const version = useScreenVersion(host, ScreenTarget.SCHEDULE_SESSION_STRIP);
  return useMemo(() => host.viewScheduleSessionStrip(sessionId), [host, version, sessionId]);
}

export function useSettingsView(host: CoreHost): SettingsViewModel {
  const version = useScreenVersion(host, ScreenTarget.SETTINGS);
  return useMemo(() => host.viewSettings(), [host, version]);
}

export function useMachineStatusView(
  host: CoreHost,
  machineId: string,
  mode: MachinePollMode = MachinePollMode.LIVE,
): MachineStatusViewModel {
  useEffect(() => host.watchMachineStatus(machineId, mode), [host, machineId, mode]);
  const version = useScreenVersion(host, ScreenTarget.MACHINE_STATUS);
  return useMemo(() => host.viewMachineStatus(machineId), [host, version, machineId]);
}

export function useMachineSettingsView(host: CoreHost, machineId: string): MachineSettingsViewModel {
  useEffect(() => {
    if (machineId) host.machineSettingsOpen(machineId);
  }, [host, machineId]);
  const version = useScreenVersion(host, ScreenTarget.MACHINE_SETTINGS);
  return useMemo(() => host.viewMachineSettings(machineId), [host, version, machineId]);
}

export function useQuotaView(host: CoreHost, machineId: string): QuotaViewModel {
  const version = useScreenVersion(host, ScreenTarget.QUOTA);
  return useMemo(() => host.viewQuota(machineId), [host, version, machineId]);
}

export function useDevicesView(host: CoreHost): DevicesViewModel {
  const version = useScreenVersion(host, ScreenTarget.DEVICES);
  return useMemo(() => host.viewDevices(), [host, version]);
}

export function useClientDetailView(host: CoreHost, deviceId: string): ClientDetailViewModel {
  const version = useScreenVersion(host, ScreenTarget.CLIENT_DETAIL);
  return useMemo(() => host.viewClientDetail(deviceId), [host, version, deviceId]);
}

export function useUsageView(
  host: CoreHost,
  req: {
    machineId: string;
    focusAgent?: Agent;
    focusModel?: string;
    preset?: UsageRangePreset;
    customStartMs?: bigint;
    customEndMs?: bigint;
  },
): UsageViewModel {
  const preset = !req.preset ? UsageRangePreset.SEVEN_DAYS : req.preset;
  const customStartMs = req.customStartMs ?? 0n;
  const customEndMs = req.customEndMs ?? 0n;
  const appliedFocusRef = useRef("");
  const focusSig = `${req.machineId}:${req.focusAgent ?? 0}:${req.focusModel ?? ""}`;
  useEffect(() => {
    if (!req.machineId) return;
    const applyFocus = appliedFocusRef.current !== focusSig;
    appliedFocusRef.current = focusSig;
    host.openUsageScreen(req.machineId, preset, customStartMs, customEndMs, {
      applyFocus,
      focusAgent: req.focusAgent,
      focusModel: req.focusModel,
    });
  }, [host, focusSig, req.machineId, req.focusAgent, req.focusModel, preset, customStartMs, customEndMs]);
  const version = useScreenVersion(host, ScreenTarget.USAGE);
  return useMemo(
    () =>
      host.viewUsage({
        machineId: req.machineId,
        focusAgent: req.focusAgent,
        focusModel: req.focusModel,
        preset,
        customStartMs,
        customEndMs,
      }),
    [host, version, req.machineId, req.focusAgent, req.focusModel, preset, customStartMs, customEndMs],
  );
}

export function useDirectError(host: CoreHost, machineId: string): string {
  useExtraVersion(host);
  return host.directError(machineId);
}

export function useClientRenameState(host: CoreHost, deviceId: string): { saving: boolean; error: string } {
  const ver = useExtraVersion(host);
  return useMemo(() => host.clientRenameState(deviceId), [host, ver, deviceId]);
}

export function useDirectDiagnosticsView(host: CoreHost, machineId: string): DirectDiagnosticsViewModel {
  const version = useScreenVersion(host, ScreenTarget.DIRECT_DIAGNOSTICS);
  return useMemo(() => host.viewDirectDiagnostics(machineId), [host, version, machineId]);
}

export function useAttachmentDraftView(host: CoreHost, scope: DraftScope): AttachmentDraftViewModel {
  const version = useScreenVersion(host, ScreenTarget.ATTACHMENT_DRAFT);
  const kind = scope.kind;
  const id = scope.id;
  return useMemo(
    () => host.viewAttachmentDraft(create(DraftScopeSchema, { kind, id })),
    [host, version, kind, id],
  );
}

export function useAppRootView(host: CoreHost): AppRootViewModel {
  const version = useScreenVersion(host, ScreenTarget.APP_ROOT);
  return useMemo(() => host.viewAppRoot(), [host, version]);
}

export function useOnboardingView(host: CoreHost): OnboardingViewModel {
  const version = useScreenVersion(host, ScreenTarget.ONBOARDING);
  return useMemo(() => host.viewOnboarding(), [host, version]);
}

export function useAddDeviceView(host: CoreHost): AddDeviceViewModel {
  const version = useScreenVersion(host, ScreenTarget.ADD_DEVICE);
  return useMemo(() => host.viewAddDevice(), [host, version]);
}

export function useAccountSecurityView(host: CoreHost): AccountSecurityViewModel {
  const version = useScreenVersion(host, ScreenTarget.ACCOUNT_SECURITY);
  return useMemo(() => host.viewAccountSecurity(), [host, version]);
}

export function useHandoffView(host: CoreHost): HandoffViewModel {
  const version = useScreenVersion(host, ScreenTarget.HANDOFF);
  return useMemo(() => host.viewHandoff(), [host, version]);
}

export function useExitConfirmView(host: CoreHost): ExitConfirmView {
  const version = useScreenVersion(host, ScreenTarget.EXIT_CONFIRM);
  return useMemo(() => host.viewExitConfirm(), [host, version]);
}

export {
  AccountSecurityActionKind,
  AccountSecuritySubpage,
  AddDeviceActionKind,
  AddDeviceStep,
  AppRootStage,
  CommandRouteLeg,
  ConnBannerKind,
  ConnProbeState,
  CreateAccountStep,
  ExitConfirmStep,
  ExitIntentKind,
  HandoffActionKind,
  HandoffRole,
  JoinPairingStep,
  OnboardingMode,
  OnboardingRecoveryActionKind,
  PendingSecurityOpKind,
  SecurityBannerAction,
  SecurityBannerTone,
  SecurityRecoveryPhase,
  TerminalHistoryEventKind,
  TerminalHistoryPhase,
  TerminalHistoryStatus,
  TerminalRunWidth,
  TerminalSgrColorKind,
  TerminalStreamEventKind,
  ConnState,
  MachinePollMode,
  NotificationPermissionState,
  PushRegistrationState,
  ThemePreference,
  UsageRangePreset,
  UsageSortColumn,
};
export type {
  AccountRuntimeChangedEffect,
  AccountSecurityViewModel,
  AddDeviceViewModel,
  AppRootViewModel,
  ExitConfirmView,
  HandoffViewModel,
  OnboardingViewModel,
  TerminalHistoryParsedPage,
  TerminalHistoryRun,
  TerminalHistorySendRequestEffect,
  TerminalHistoryUpdatedEffect,
};
