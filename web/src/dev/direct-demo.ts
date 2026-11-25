// Showcase presets for the direct-connection views in demo mode (?demo&direct=...).
// Only activates when ?direct, ?diag, or ?speeds is specified, or when installDemoDirect is invoked in tests.

import { create } from "@bufbuild/protobuf";
import { getCoreHost, type CoreHost } from "../core/host";
import { EventSchema as CoreEventSchema } from "../gen/remoteagent/core/v1/app_pb";
import { MachineStatusDot } from "../gen/remoteagent/core/v1/machine_view_pb";
import { DemoDirectReportSchema, SeedDemoWorldEventSchema } from "../gen/remoteagent/core/v1/security_pb";
import { SessionDirectChipViewSchema } from "../gen/remoteagent/core/v1/session_view_pb";
import {
  ConnectionGroupStage,
  ConnectionGroupTone,
  ConnectionGroupViewSchema,
  DiagCheckMark,
  DirectDiagnosticsViewModelSchema,
  DirectPathChoice,
  DirectRouteReason,
  DirectRouteReportSchema,
  DirectRouteState,
  PlainDiagCheckViewSchema,
  type ConnectionGroupView,
  type DirectDiagnosticsViewModel,
} from "../gen/remoteagent/core/v1/settings_view_pb";
import { TextArgSchema, TextSchema, type Text } from "../gen/remoteagent/core/v1/text_pb";
import type { Remote } from "../remote";

export type DemoPreset =
  | "direct"
  | "file"
  | "read"
  | "file-fallback"
  | "measuring"
  | "relay"
  | "relay-faster"
  | "trying"
  | "none"
  | "peer_unsupported"
  | "no_response"
  | "no_traversal"
  | "failed"
  | "off"
  | "unsupported";

export interface DemoDirectSpec {
  preset: DemoPreset;
  rttMs?: number;
  speeds?: string;
  diag?: string;
  offline?: boolean;
}

const DIAG_VERDICT_KEYS: Record<string, string> = {
  connected: "transport.diag_verdict_connected",
  lost: "transport.diag_verdict_lost",
  cancelled: "transport.diag_verdict_cancelled",
  no_reply: "transport.diag_verdict_no_reply",
  peer_refused: "transport.diag_verdict_peer_refused",
  signal_limited: "transport.diag_verdict_signal_limited",
  signal_offline: "transport.diag_verdict_signal_offline",
  no_local: "transport.diag_verdict_no_local",
  no_stun: "transport.diag_verdict_no_stun",
  no_remote: "transport.diag_verdict_no_remote",
  no_sdp: "transport.diag_verdict_no_sdp",
  ipv6_silent: "transport.diag_verdict_ipv6_silent",
  all_failed: "transport.diag_verdict_all_failed",
  unselected: "transport.diag_verdict_unselected",
  dtls: "transport.diag_verdict_dtls",
  channel: "transport.diag_verdict_channel",
  confirm: "transport.diag_verdict_confirm",
  budget: "transport.diag_verdict_budget",
  create_failed: "transport.diag_verdict_create_failed",
  unknown: "transport.diag_verdict_unknown",
};

function tk(key: string, params?: Record<string, string | number | Text>): Text {
  const args = params
    ? Object.entries(params).map(([name, val]) =>
        create(TextArgSchema, {
          name,
          value: typeof val === "object"
            ? { case: "textValue", value: val }
            : typeof val === "number"
              ? { case: "intValue", value: BigInt(val) }
              : { case: "strValue", value: val },
        }),
      )
    : [];
  return create(TextSchema, { key, args });
}

function buildSpeedMetrics(preset: DemoPreset, speeds: string, rttMs?: number): { file?: Text; read?: Text; metric?: Text } {
  const metric = rttMs !== undefined ? tk("transport.rtt_ms", { ms: rttMs }) : undefined;
  if (speeds === "none") return { metric };
  if (preset === "direct") {
    return {
      metric,
      file: speeds === "relay" ? tk("transport.file_speed_relay", { relay: "9.8" }) : speeds === "direct" ? tk("transport.file_speed_direct", { direct: "24" }) : tk("transport.file_speed_both", { direct: "24", relay: "9.8" }),
      read: speeds === "relay" ? tk("transport.read_latency_relay", { relay: rttMs ?? 185 }) : speeds === "direct" ? tk("transport.read_latency_direct", { direct: rttMs ?? 12 }) : tk("transport.read_latency_both", { direct: String(rttMs ?? 12), relay: "185" }),
    };
  }
  if (preset === "relay" || preset === "relay-faster") {
    return {
      metric,
      file: tk("transport.file_speed_both", { direct: "1.2", relay: "8.4" }),
      read: tk("transport.read_latency_both", { direct: "180", relay: String(rttMs ?? 42) }),
    };
  }
  if (preset === "file" || preset === "read" || preset === "file-fallback") {
    return {
      metric,
      file: tk("transport.file_speed_both", { direct: preset === "file" ? "21" : "1.8", relay: "8.4" }),
      read: tk("transport.read_latency_both", { direct: preset === "file" ? "180" : String(rttMs ?? 14), relay: "42" }),
    };
  }
  return { metric };
}

function buildConnectionGroup(spec: DemoDirectSpec): ConnectionGroupView {
  const { preset, speeds = "", rttMs, offline } = spec;
  const m = buildSpeedMetrics(preset, speeds, rttMs);
  const footer = tk("transport.footer");
  if (offline) {
    return create(ConnectionGroupViewSchema, {
      toggleVisible: true,
      toggleEnabled: true,
      toggleChecked: preset !== "off",
      stage: ConnectionGroupStage.OFFLINE,
      tone: ConnectionGroupTone.MUTED,
      rowDot: MachineStatusDot.GRAY_STATIC,
      rowValueText: tk("common.ios_offline"),
      footerText: footer,
    });
  }
  switch (preset) {
    case "unsupported":
      return create(ConnectionGroupViewSchema, {
        toggleVisible: true,
        toggleEnabled: false,
        toggleChecked: false,
        stage: ConnectionGroupStage.UNSUPPORTED,
        tone: ConnectionGroupTone.MUTED,
        statusText: tk("transport.status_unsupported"),
        detailText: tk("transport.detail_relay_only"),
        footerText: tk("transport.conn_footer_unsupported_browser"),
        rowDot: MachineStatusDot.ORANGE_STATIC,
        rowValueText: tk("transport.row_unavailable"),
        rowSubtitleText: tk("transport.status_unsupported"),
      });
    case "off":
      return create(ConnectionGroupViewSchema, {
        toggleVisible: true,
        toggleEnabled: true,
        toggleChecked: false,
        stage: ConnectionGroupStage.DISABLED,
        tone: ConnectionGroupTone.MUTED,
        statusText: tk("transport.summary_off"),
        footerText: footer,
        rowDot: MachineStatusDot.GRAY_STATIC,
        rowValueText: tk("transport.row_off"),
      });
    case "trying":
      return create(ConnectionGroupViewSchema, {
        toggleVisible: true,
        toggleEnabled: true,
        toggleChecked: true,
        stage: ConnectionGroupStage.CONNECTING,
        tone: ConnectionGroupTone.LIVE,
        statusText: tk("transport.status_connecting"),
        footerText: footer,
        rowDot: MachineStatusDot.ORANGE_STATIC,
        rowSpinning: true,
        rowValueText: tk("transport.status_connecting"),
      });
    case "none":
      return create(ConnectionGroupViewSchema, {
        toggleVisible: true,
        toggleEnabled: true,
        toggleChecked: true,
        stage: ConnectionGroupStage.RELAY,
        tone: ConnectionGroupTone.MUTED,
        statusText: tk("transport.status_relay"),
        detailText: tk("transport.reason_starting"),
        footerText: footer,
        rowDot: MachineStatusDot.ORANGE_STATIC,
        rowValueText: tk("transport.row_relay"),
        rowSubtitleText: tk("transport.reason_starting"),
      });
    case "direct":
      return create(ConnectionGroupViewSchema, {
        toggleVisible: true,
        toggleEnabled: true,
        toggleChecked: true,
        stage: ConnectionGroupStage.DIRECT,
        tone: ConnectionGroupTone.LIVE,
        statusText: tk("transport.status_direct"),
        metricText: m.metric,
        fileSpeedText: m.file,
        readLatencyText: m.read,
        footerText: footer,
        rowDot: MachineStatusDot.GREEN_LIVE,
        rowValueText: rttMs !== undefined ? tk("transport.row_using_direct_ms", { ms: rttMs }) : tk("transport.row_using_direct"),
      });
    case "file":
      return create(ConnectionGroupViewSchema, {
        toggleVisible: true,
        toggleEnabled: true,
        toggleChecked: true,
        stage: ConnectionGroupStage.DIRECT,
        tone: ConnectionGroupTone.LIVE,
        statusText: tk("transport.status_direct"),
        detailText: tk("transport.use_file"),
        metricText: m.metric,
        fileSpeedText: m.file,
        readLatencyText: m.read,
        footerText: footer,
        rowDot: MachineStatusDot.GREEN_LIVE,
        rowValueText: rttMs !== undefined ? tk("transport.row_using_direct_ms", { ms: rttMs }) : tk("transport.row_using_direct"),
        rowSubtitleText: tk("transport.use_file"),
      });
    case "read":
    case "file-fallback":
      return create(ConnectionGroupViewSchema, {
        toggleVisible: true,
        toggleEnabled: true,
        toggleChecked: true,
        stage: ConnectionGroupStage.DIRECT,
        tone: ConnectionGroupTone.LIVE,
        statusText: tk("transport.status_direct"),
        detailText: tk("transport.use_read"),
        metricText: m.metric,
        fileSpeedText: m.file,
        readLatencyText: m.read,
        footerText: footer,
        rowDot: MachineStatusDot.GREEN_LIVE,
        rowValueText: rttMs !== undefined ? tk("transport.row_using_direct_ms", { ms: rttMs }) : tk("transport.row_using_direct"),
        rowSubtitleText: tk("transport.use_read"),
      });
    case "measuring":
      return create(ConnectionGroupViewSchema, {
        toggleVisible: true,
        toggleEnabled: true,
        toggleChecked: true,
        stage: ConnectionGroupStage.MEASURING,
        tone: ConnectionGroupTone.LIVE,
        statusText: tk("transport.status_connecting"),
        footerText: footer,
        rowDot: MachineStatusDot.ORANGE_STATIC,
        rowSpinning: true,
        rowValueText: tk("transport.status_connecting"),
      });
    case "relay":
    case "relay-faster":
      return create(ConnectionGroupViewSchema, {
        toggleVisible: true,
        toggleEnabled: true,
        toggleChecked: true,
        stage: ConnectionGroupStage.RELAY,
        tone: ConnectionGroupTone.WARN,
        statusText: tk("transport.status_relay"),
        detailText: tk("transport.direct_slow"),
        metricText: m.metric,
        fileSpeedText: m.file,
        readLatencyText: m.read,
        footerText: footer,
        rowDot: MachineStatusDot.GRAY_STATIC,
        rowValueText: rttMs !== undefined ? tk("transport.row_relay_ms", { ms: rttMs }) : tk("transport.row_relay"),
        rowSubtitleText: tk("transport.direct_slow"),
      });
    default: {
      const reasonKey =
        preset === "peer_unsupported"
          ? "transport.reason_peer_unsupported"
          : preset === "no_response"
            ? "transport.reason_no_response"
            : preset === "no_traversal"
              ? "transport.reason_no_traversal"
              : "transport.reason_failed";
      return create(ConnectionGroupViewSchema, {
        toggleVisible: true,
        toggleEnabled: true,
        toggleChecked: true,
        stage: ConnectionGroupStage.RELAY,
        tone: ConnectionGroupTone.FAIL,
        statusText: tk("transport.status_relay"),
        detailText: tk(reasonKey),
        showDiagnosticsEntry: true,
        footerText: footer,
        rowDot: MachineStatusDot.ORANGE_STATIC,
        rowValueText: rttMs !== undefined ? tk("transport.row_relay_ms", { ms: rttMs }) : tk("transport.row_relay"),
        rowSubtitleText: tk(reasonKey),
      });
    }
  }
}

function buildDemoDirectReport(machineId: string, spec: DemoDirectSpec) {
  const { preset, rttMs } = spec;
  const rtt = rttMs ?? 0;
  let enabled = true;
  let capable = true;
  let state = DirectRouteState.NONE;
  let reason = DirectRouteReason.UNSPECIFIED;
  let readRoute = DirectPathChoice.RELAY;
  let fileRoute = DirectPathChoice.RELAY;
  let readSlow = false;
  switch (preset) {
    case "unsupported":
      enabled = false;
      capable = false;
      state = DirectRouteState.UNSUPPORTED;
      break;
    case "off":
      enabled = false;
      state = DirectRouteState.NONE;
      reason = DirectRouteReason.DISABLED;
      break;
    case "trying":
    case "measuring":
      state = DirectRouteState.CONNECTING;
      break;
    case "none":
      state = DirectRouteState.NONE;
      reason = DirectRouteReason.STARTING;
      break;
    case "direct":
      state = DirectRouteState.CONNECTED;
      readRoute = DirectPathChoice.DIRECT;
      fileRoute = DirectPathChoice.DIRECT;
      break;
    case "file":
      state = DirectRouteState.CONNECTED;
      readRoute = DirectPathChoice.RELAY;
      fileRoute = DirectPathChoice.DIRECT;
      break;
    case "read":
    case "file-fallback":
      state = DirectRouteState.CONNECTED;
      readRoute = DirectPathChoice.DIRECT;
      fileRoute = DirectPathChoice.RELAY;
      break;
    case "relay":
    case "relay-faster":
      state = DirectRouteState.CONNECTED;
      readRoute = DirectPathChoice.RELAY;
      fileRoute = DirectPathChoice.RELAY;
      readSlow = true;
      break;
    case "peer_unsupported":
      reason = DirectRouteReason.PEER_UNSUPPORTED;
      break;
    case "no_response":
      reason = DirectRouteReason.NO_RESPONSE;
      break;
    case "no_traversal":
      reason = DirectRouteReason.NO_TRAVERSAL;
      break;
    default:
      reason = DirectRouteReason.FAILED;
      break;
  }
  return create(DemoDirectReportSchema, {
    machineId,
    enabled,
    report: create(DirectRouteReportSchema, {
      enabled,
      capable,
      state,
      reason,
      readRoute,
      fileRoute,
      readSlow,
      hasActiveRttMs: rtt > 0,
      activeRttMs: rtt,
    }),
  });
}

function buildDiagnostics(machineId: string, spec: DemoDirectSpec): DirectDiagnosticsViewModel {
  const { preset, diag: diagParam = "" } = spec;
  const verdict = DIAG_VERDICT_KEYS[diagParam] ? diagParam : preset === "direct" ? "connected" : preset === "no_response" ? "no_reply" : preset === "peer_unsupported" ? "peer_refused" : "all_failed";
  const supported = preset !== "unsupported";
  const enabled = preset !== "off" && supported;
  const connected = preset === "direct" || preset === "file" || preset === "read" || preset === "file-fallback";
  const trying = preset === "trying" || preset === "none";
  const peerOutdated = verdict === "peer_refused";
  const blocked = verdict === "no_stun" || verdict === "all_failed" || verdict === "no_traversal";

  const titleText = connected
    ? tk("transport.diag_connected_title")
    : peerOutdated
      ? tk("transport.diag_status_peer_old")
      : blocked
        ? tk("transport.diag_title_probe_blocked")
        : verdict === "no_reply"
          ? tk("transport.diag_title_no_reply")
          : tk("transport.diag_title_generic");

  const checks = [
    create(PlainDiagCheckViewSchema, {
      mark: peerOutdated || verdict === "no_reply" ? DiagCheckMark.OFF : DiagCheckMark.ONLINE,
      labelText: tk("transport.diag_check_peer"),
      detailText: peerOutdated ? tk("transport.diag_peer_old_desc") : verdict === "no_reply" ? tk("transport.diag_peer_no_reply_desc") : undefined,
      statusText: peerOutdated ? tk("transport.diag_status_peer_old") : verdict === "no_reply" ? tk("transport.diag_status_no_reply") : tk("transport.diag_status_ok"),
    }),
    create(PlainDiagCheckViewSchema, {
      mark: connected ? DiagCheckMark.ONLINE : trying ? DiagCheckMark.RUN : DiagCheckMark.OFF,
      labelText: tk("transport.diag_check_path"),
      detailText: blocked ? tk("transport.diag_path_udp_blocked") : !connected ? tk("transport.diag_path_failed") : undefined,
      statusText: connected ? tk("transport.diag_status_connected") : trying ? tk("transport.diag_status_checking") : tk("transport.diag_status_blocked"),
    }),
    create(PlainDiagCheckViewSchema, {
      mark: DiagCheckMark.ONLINE,
      labelText: tk("transport.diag_check_retry"),
      detailText: peerOutdated ? tk("transport.diag_retry_wait_desc") : tk("transport.diag_retry_auto_desc"),
      statusText: peerOutdated ? tk("transport.diag_status_wait_update") : tk("transport.diag_status_enabled"),
    }),
  ];

  const vKey = DIAG_VERDICT_KEYS[verdict] ?? "transport.diag_verdict_unknown";
  return create(DirectDiagnosticsViewModelSchema, {
    machineId,
    supported,
    enabled,
    connected,
    peerOutdated,
    trying,
    canRetry: !connected && !trying && !peerOutdated && supported && enabled,
    titleText,
    verdictText: tk(vKey),
    checks,
    reportLines: [
      tk("transport.diag_report_title"),
      tk("transport.diag_report_verdict", { verdict: tk(vKey) }),
      tk("transport.diag_report_round", { n: 1, age: tk("machine_status.seconds_ago", { count: 4 }) }),
    ],
  });
}

function buildChip(spec: DemoDirectSpec, machineName: string) {
  const { preset, rttMs } = spec;
  if (preset === "off" || preset === "unsupported" || preset === "none") return undefined;
  const direct = preset === "direct" || preset === "file" || preset === "read" || preset === "file-fallback";
  const header = direct
    ? (rttMs !== undefined ? tk("transport.popover_direct_title_rtt", { name: machineName, ms: rttMs }) : tk("transport.popover_direct_title", { name: machineName }))
    : tk("transport.popover_relay_title", { name: machineName });
  const body =
    preset === "direct"
      ? tk("transport.popover_stable")
      : preset === "file"
        ? tk("transport.popover_file_only")
        : preset === "read" || preset === "file-fallback"
          ? tk("transport.popover_read_only")
          : tk("transport.popover_switched_relay");
  return create(SessionDirectChipViewSchema, {
    visible: direct || preset === "relay" || preset === "relay-faster" || preset === "failed" || preset === "no_traversal" || preset === "no_response" || preset === "peer_unsupported",
    active: direct,
    readDirect: preset === "direct" || preset === "read" || preset === "file-fallback",
    fileDirect: preset === "direct" || preset === "file",
    labelText: tk("transport.direct"),
    popoverTitle: header,
    popoverBody: body,
    popoverFoot: tk("transport.popover_settings"),
  });
}

function resolveMachinePreset(base: DemoPreset, raw: string, machineId: string, diag: string): DemoPreset {
  if (diag) {
    if (machineId !== "mac") return "direct";
    if (diag === "connected") return "direct";
    if (diag === "no_reply") return "no_response";
    if (diag === "peer_refused") return "peer_unsupported";
    return "no_traversal";
  }
  if (raw === "mix") {
    return machineId === "mini" ? "no_traversal" : "direct";
  }
  return base;
}

interface DemoDirectState {
  map: Map<string, DemoDirectSpec>;
  sync: () => void;
}

const demoOverrides = new WeakMap<CoreHost, DemoDirectState>();

export function setDemoDirect(remote: Remote, machineId: string, spec: DemoPreset | DemoDirectSpec) {
  const host = getCoreHost(remote);
  if (!demoOverrides.has(host)) {
    installDemoDirectHost(remote, host, "");
  }
  const st = demoOverrides.get(host)!;
  st.map.set(machineId, typeof spec === "string" ? { preset: spec } : spec);
  st.sync();
}

export function clearDemoDirect(remote: Remote, machineId?: string) {
  const host = getCoreHost(remote);
  const st = demoOverrides.get(host);
  if (!st) return;
  if (machineId) st.map.delete(machineId);
  else st.map.clear();
  st.sync();
}

function installDemoDirectHost(
  _remote: Remote,
  host: CoreHost,
  speedsParam: string,
  fallback?: (machineId: string) => DemoDirectSpec | undefined,
  initialMachineIds: readonly string[] = [],
) {
  const map = new Map<string, DemoDirectSpec>();
  const specFor = (id: string) => map.get(id) ?? fallback?.(id);
  const sync = () => {
    const ids = new Set<string>([...initialMachineIds, ...host.viewSettings().machines.map((m) => m.machineId), ...map.keys()]);
    const directReports = [];
    for (const id of ids) {
      const s = specFor(id);
      if (s) directReports.push(buildDemoDirectReport(id, s));
    }
    host.dispatch(
      create(CoreEventSchema, {
        event: {
          case: "seedDemoWorld",
          value: create(SeedDemoWorldEventSchema, {
            clearDirectReports: true,
            directReports,
          }),
        },
      }),
    );
  };
  demoOverrides.set(host, { map, sync });

  const origViewMachineSettings = host.viewMachineSettings.bind(host);
  host.viewMachineSettings = (machineId: string) => {
    const vm = origViewMachineSettings(machineId);
    const s = specFor(machineId);
    if (s) vm.connection = buildConnectionGroup({ speeds: speedsParam, offline: !vm.online, ...s });
    return vm;
  };

  const origViewDirectDiagnostics = host.viewDirectDiagnostics.bind(host);
  host.viewDirectDiagnostics = (machineId: string) => {
    const vm = origViewDirectDiagnostics(machineId);
    const s = specFor(machineId);
    if (!s) return vm;
    return buildDiagnostics(machineId, s);
  };

  const origViewSession = host.viewSession.bind(host);
  host.viewSession = (sessionId, windowOpts) => {
    const vm = origViewSession(sessionId, windowOpts);
    const mid = vm.header?.machineId || "mac";
    const s = specFor(mid);
    if (s && vm.header) {
      const mName = host.viewSettings().machines.find((m) => m.machineId === mid)?.name || mid;
      vm.header.directChip = buildChip(s, mName);
    }
    return vm;
  };

  const origUpdatePref = host.updateDirectPreference.bind(host);
  host.updateDirectPreference = (machineId: string, enabled: boolean) => {
    const s = specFor(machineId);
    if (s && s.preset !== "unsupported") {
      if (!enabled) {
        map.set(machineId, { ...s, preset: "off" });
      } else {
        map.delete(machineId);
      }
      sync();
    }
    origUpdatePref(machineId, enabled);
  };

  sync();
}

export function seedDirect(remote: Remote, opts: URLSearchParams, initialMachineIds: readonly string[] = []) {
  const directParam = opts.get("direct") ?? "";
  const diagParam = opts.get("diag") ?? "";
  const speedsParam = opts.get("speeds") ?? "";
  if (!directParam && !diagParam && !speedsParam) return;

  const host = getCoreHost(remote);
  const basePreset = ((directParam === "mix" ? "direct" : directParam) || "failed") as DemoPreset;
  const fallback = (id: string): DemoDirectSpec => ({
    preset: resolveMachinePreset(basePreset, directParam, id, diagParam),
    speeds: speedsParam,
    diag: id === "mac" ? diagParam : "",
    rttMs: directParam === "direct" ? 4 : undefined,
  });
  installDemoDirectHost(remote, host, speedsParam, fallback, initialMachineIds);
}
