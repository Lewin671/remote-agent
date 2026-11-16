import * as l10n from "../i18n";
import {
  bestAttempt, leadPc, parseKey, stageOf, tally, verdictOf,
  type AttemptData, type CandKind, type CandType, type Family, type PcData, type RoundData, type Stage, type StateKind, type Tally, type Verdict,
} from "../transport/direct-diag";

// The diagnostics sheet and the text it copies, from the same rows (design-system
// §6 「连接」组 · 诊断信息). Nothing here sees an address: the collector kept only
// type, family and range.

export interface DiagRow { label: string; value: string; sub?: string }
export interface DiagGroup { title: string; rows: DiagRow[] }
export interface DiagView {
  verdict: Verdict;
  sentence: string;
  /** The round the sentence and the three groups describe. */
  round?: RoundData;
  groups: DiagGroup[];
  /** 结果: where it stopped, and the last rounds. */
  result: DiagGroup;
  /** The same data, all rounds and both paths, as plain text. */
  report: string;
}
export interface DiagInput { rounds: RoundData[]; blockedAt?: number; now: number; browser?: string }

const SEP = " · ";
const seconds = (ms: number) => (ms / 1000).toFixed(1);
const TYPE_ORDER: CandType[] = ["host", "srflx", "prflx", "relay", "other"];

const verdictText = (v: Verdict): string => {
  switch (v) {
    case "trying": return l10n.t("transport.status_connecting");
    case "none": return l10n.t("transport.diag_verdict_none");
    case "connected": return l10n.t("transport.diag_verdict_connected");
    case "lost": return l10n.t("transport.diag_verdict_lost");
    case "cancelled": return l10n.t("transport.diag_verdict_cancelled");
    case "no_reply": return l10n.t("transport.diag_verdict_no_reply");
    case "peer_refused": return l10n.t("transport.diag_verdict_peer_refused");
    case "signal_limited": return l10n.t("transport.diag_verdict_signal_limited");
    case "signal_offline": return l10n.t("transport.diag_verdict_signal_offline");
    case "no_local": return l10n.t("transport.diag_verdict_no_local");
    case "no_stun": return l10n.t("transport.diag_verdict_no_stun");
    case "no_remote": return l10n.t("transport.diag_verdict_no_remote");
    case "no_sdp": return l10n.t("transport.diag_verdict_no_sdp");
    case "ipv6_silent": return l10n.t("transport.diag_verdict_ipv6_silent");
    case "all_failed": return l10n.t("transport.diag_verdict_all_failed");
    case "dtls": return l10n.t("transport.diag_verdict_dtls");
    case "channel": return l10n.t("transport.diag_verdict_channel");
    case "confirm": return l10n.t("transport.diag_verdict_confirm");
    case "budget": return l10n.t("transport.diag_verdict_budget");
    case "create_failed": return l10n.t("transport.diag_verdict_create_failed");
    case "unknown": return l10n.t("transport.diag_verdict_unknown");
  }
};

function typeWord(t: CandType): string {
  switch (t) {
    case "host": return l10n.t("transport.diag_type_host");
    case "srflx": return l10n.t("transport.diag_type_srflx");
    case "prflx": return l10n.t("transport.diag_type_prflx");
    case "relay": return l10n.t("transport.diag_type_relay");
    case "other": return l10n.t("transport.diag_type_other");
  }
}
function rangeWord(c: CandKind["cls"]): string {
  switch (c) {
    case "private": return l10n.t("transport.diag_range_private");
    case "public": return l10n.t("transport.diag_range_public");
    case "cgnat": return l10n.t("transport.diag_range_cgnat");
    case "link_local": return l10n.t("transport.diag_range_link_local");
    case "loopback": return l10n.t("transport.diag_range_loopback");
    case "global": return l10n.t("transport.diag_range_global");
    case "ula": return l10n.t("transport.diag_range_ula");
    case "mdns": case "other": return l10n.t("transport.diag_range_other");
  }
}
const familyWord = (f: Family) => (f === "4" ? l10n.t("transport.diag_ipv4") : f === "6" ? l10n.t("transport.diag_ipv6") : l10n.t("transport.diag_family_unknown"));

/** `主机 ×2（私网）· 公网映射 ×1（公网）` for one family, or 没有. */
function family(t: Tally, f: Family): string {
  const rows = Object.entries(t).map(([key, n]) => ({ k: parseKey(key), n })).filter(({ k }) => k.family === f && k.cls !== "mdns")
    .sort((a, b) => TYPE_ORDER.indexOf(a.k.type) - TYPE_ORDER.indexOf(b.k.type) || a.k.cls.localeCompare(b.k.cls));
  if (!rows.length) return l10n.t("transport.diag_none");
  return rows.map(({ k, n }) => l10n.t("transport.diag_cand", { kind: typeWord(k.type), count: n, scope: rangeWord(k.cls) })).join(SEP);
}

type Word = "none" | "doing" | "ok" | "failed" | "dropped" | "closed";
const WORDS: Record<string, Word> = { new: "none", checking: "doing", connecting: "doing", gathering: "doing", closing: "doing", connected: "ok", completed: "ok", complete: "ok", open: "ok", failed: "failed", disconnected: "dropped", closed: "closed" };
function stateWord(v: string): string {
  switch (WORDS[v] ?? "none") {
    case "none": return l10n.t("transport.diag_state_none");
    case "doing": return l10n.t("transport.diag_state_doing");
    case "ok": return l10n.t("transport.diag_state_ok");
    case "failed": return l10n.t("transport.diag_state_failed");
    case "dropped": return l10n.t("transport.diag_state_dropped");
    case "closed": return l10n.t("transport.diag_state_closed");
  }
}

/** `进行中 0.1 秒 → 已完成 0.4 秒`: the states of one kind, as they came. */
function timeline(a: AttemptData | undefined, k: StateKind): string | undefined {
  const events = a?.states.filter((s) => s.k === k) ?? [];
  if (!events.length) return undefined;
  const out: string[] = []; let last = "";
  for (const e of events.slice(-6)) {
    const word = stateWord(e.v);
    if (word === last) continue;
    last = word; out.push(l10n.t("transport.diag_at", { state: word, seconds: seconds(e.t) }));
  }
  return out.join(" → ");
}
const lastState = (a: AttemptData | undefined, k: StateKind) => { const e = a?.states.filter((s) => s.k === k).at(-1); return e ? stateWord(e.v) : undefined; };

function rejectWord(kind: string): string {
  switch (kind) {
    case "sdp": return l10n.t("transport.diag_rej_sdp");
    case "candidates": return l10n.t("transport.diag_rej_candidates");
    case "end": return l10n.t("transport.diag_rej_end");
    case "confirm": return l10n.t("transport.diag_rej_confirm");
    case "browser": return l10n.t("transport.diag_rej_browser");
    default: return l10n.t("transport.diag_rej_signal");
  }
}

/** One row per family pair: the groups of different candidate types over the same families are added up. */
function pairRows(a: AttemptData): DiagRow[] {
  const sums = new Map<string, { lf: Family; rf: Family; ok: number; failed: number; waiting: number; sent: number; got: number; rtt?: number }>();
  for (const p of a.pairs) {
    const key = p.lf + p.rf, g = sums.get(key) ?? { lf: p.lf, rf: p.rf, ok: 0, failed: 0, waiting: 0, sent: 0, got: 0 };
    g.ok += p.ok; g.failed += p.failed; g.waiting += p.waiting; g.sent += p.sent; g.got += p.got;
    if (p.rtt !== undefined) g.rtt = g.rtt === undefined ? p.rtt : Math.min(g.rtt, p.rtt);
    sums.set(key, g);
  }
  return [...sums.values()].map((p) => {
    const parts = [p.ok && l10n.t("transport.diag_pair_ok", { count: p.ok }), p.failed && l10n.t("transport.diag_pair_failed", { count: p.failed }), p.waiting && l10n.t("transport.diag_pair_wait", { count: p.waiting })].filter(Boolean) as string[];
    if (p.rtt !== undefined) parts.push(l10n.t("transport.diag_rtt", { ms: p.rtt }));
    parts.push(l10n.t("transport.diag_pair_traffic", { sent: p.sent, got: p.got }));
    return { label: l10n.t("transport.diag_pair", { local: familyWord(p.lf), remote: familyWord(p.rf) }), value: parts.join(SEP) };
  });
}

/** 本机地址 / 电脑地址 / 连通检查 of one PC, from its best attempt. */
function addressGroups(pc: PcData, a: AttemptData | undefined): DiagGroup[] {
  const dash = "—";
  if (!a) return [
    { title: l10n.t("transport.diag_g_local"), rows: [{ label: l10n.t("transport.diag_no_attempt"), value: dash }] },
    { title: l10n.t("transport.diag_g_peer"), rows: [{ label: l10n.t("transport.diag_no_attempt"), value: dash }] },
    { title: l10n.t("transport.diag_g_checks"), rows: [{ label: l10n.t("transport.diag_no_attempt"), value: dash }] },
  ];
  const hidden = tally(a.local, (k) => k.cls === "mdns");
  const local: DiagRow[] = [
    { label: l10n.t("transport.diag_ipv4"), value: family(a.local, "4") },
    { label: l10n.t("transport.diag_ipv6"), value: family(a.local, "6") },
  ];
  if (hidden) local.push({ label: l10n.t("transport.diag_hidden"), value: l10n.t("transport.diag_count", { count: hidden }) });
  if (a.stun.length) local.push({ label: l10n.t("transport.diag_stun_errors"), value: a.stun.map((e) => l10n.t("transport.diag_stun_error", { code: e.code, family: familyWord(e.family), count: e.n })).join(SEP) });
  const gather = timeline(a, "gather");
  if (gather) local.push({ label: l10n.t("transport.diag_gather"), value: gather });
  const peer: DiagRow[] = [
    { label: l10n.t("transport.diag_ipv4"), value: family(a.remote, "4") },
    { label: l10n.t("transport.diag_ipv6"), value: family(a.remote, "6") },
  ];
  if (pc.rejects.length) peer.push({ label: l10n.t("transport.diag_rejected"), value: pc.rejects.map((r) => { const [kind, reason] = r.k.split(":"); return l10n.t("transport.diag_rej_value", { kind: rejectWord(kind), reason, count: r.n }); }).join(SEP) });
  const checks: DiagRow[] = a.sampled && a.pairs.length ? pairRows(a) : [{ label: l10n.t("transport.diag_not_sampled"), value: dash }];
  const ice = timeline(a, "ice");
  if (ice) checks.push({ label: l10n.t("transport.diag_ice"), value: ice });
  checks.push({ label: l10n.t("transport.diag_dtls"), value: lastState(a, "dtls") ?? lastState(a, "conn") ?? dash });
  checks.push({ label: l10n.t("transport.diag_channel"), value: lastState(a, "dc") ?? dash });
  return [
    { title: l10n.t("transport.diag_g_local"), rows: local },
    { title: l10n.t("transport.diag_g_peer"), rows: peer },
    { title: l10n.t("transport.diag_g_checks"), rows: checks },
  ];
}

function stageWord(s: Stage): string {
  switch (s) {
    case "handshake": return l10n.t("transport.diag_stage_handshake");
    case "sdp": return l10n.t("transport.diag_stage_sdp");
    case "ice": return l10n.t("transport.diag_stage_ice");
    case "dtls": return l10n.t("transport.diag_stage_dtls");
    case "channel": return l10n.t("transport.diag_stage_channel");
    case "confirm": return l10n.t("transport.diag_stage_confirm");
    case "ready": return l10n.t("transport.diag_stage_ready");
  }
}

function resultWord(r: RoundData): string {
  if (r.established !== undefined) { if (r.end) return l10n.t("transport.diag_result_lost"); return l10n.t("transport.diag_result_ok"); }
  switch (r.end) {
    case undefined: return l10n.t("transport.diag_result_trying");
    case "no_response": return l10n.t("transport.diag_result_no_response");
    case "peer_unsupported": return l10n.t("transport.diag_result_refused");
    case "cancelled": return l10n.t("transport.diag_result_cancelled");
    case "retired": return l10n.t("transport.diag_result_retired");
    default: return l10n.t("transport.diag_result_timeout");
  }
}
function ageWord(ms: number): string {
  if (ms < 60_000) return l10n.t("time.just_now");
  if (ms < 3_600_000) return l10n.t("time.minutes_ago", { count: Math.floor(ms / 60_000) });
  return l10n.t("time.hours_ago", { count: Math.floor(ms / 3_600_000) });
}
const useName = (pc: PcData) => (pc.use === 1 ? l10n.t("transport.diag_use_read") : l10n.t("transport.diag_use_file"));

function signalRows(r: RoundData): DiagRow[] {
  const parts = [r.signals.unsupported && l10n.t("transport.diag_sig_refused"), r.signals.limited && l10n.t("transport.diag_sig_limited", { count: r.signals.limited }), r.signals.offline && l10n.t("transport.diag_sig_offline", { count: r.signals.offline })].filter(Boolean) as string[];
  return parts.length ? [{ label: l10n.t("transport.diag_signal"), value: parts.join(SEP) }] : [];
}

/** 结果: where the lead path stopped, what the computer said, and each recorded round. */
function resultGroup(rounds: RoundData[], chosen: RoundData | undefined, now: number): DiagGroup {
  const rows: DiagRow[] = [];
  if (chosen) {
    const { stage } = leadPc(chosen);
    if (stage !== "ready") rows.push({ label: l10n.t("transport.diag_stage"), value: stageWord(stage) });
    rows.push(...signalRows(chosen));
  }
  for (const r of rounds) {
    const took = (r.endedAt ?? r.established ?? Math.max(0, now - r.startedAt)) as number;
    rows.push({ label: ageWord(Math.max(0, now - r.startedAt)), value: l10n.t("transport.diag_round", { result: resultWord(r), seconds: seconds(took) }) });
  }
  return { title: l10n.t("transport.diag_g_result"), rows };
}

/** The round the sentence is about: the newest that finished or connected; else the one in progress. */
export function chosenRound(rounds: RoundData[]): RoundData | undefined {
  return rounds.find((r) => r.end !== undefined || r.established !== undefined) ?? rounds[0];
}

export function diagView(input: DiagInput): DiagView {
  const { rounds, now } = input;
  const round = chosenRound(rounds);
  const blocked = input.blockedAt !== undefined;
  const verdict = verdictOf(round, blocked);
  const lead = round && leadPc(round);
  const groups = round ? addressGroups(lead!.pc, lead!.attempt) : [];
  const result = resultGroup(rounds, round, now);
  const sentence = verdictText(verdict);
  const lines: string[] = [l10n.t("transport.diag_report_title"), l10n.t("transport.diag_report_verdict", { verdict: sentence })];
  if (input.browser) lines.push(l10n.t("transport.diag_report_browser", { value: input.browser }));
  const block = (g: DiagGroup, indent: string) => { lines.push(indent + g.title); for (const r of g.rows) lines.push(`${indent}  ${r.label}: ${r.value}`); };
  for (const r of rounds) {
    lines.push("", l10n.t("transport.diag_report_round", { n: r.n, age: ageWord(Math.max(0, now - r.startedAt)) }) + SEP + l10n.t("transport.diag_round", { result: resultWord(r), seconds: seconds((r.endedAt ?? r.established ?? Math.max(0, now - r.startedAt))) }));
    const extra = signalRows(r);
    for (const row of extra) lines.push(`  ${row.label}: ${row.value}`);
    for (const pc of r.pcs) {
      const best = bestAttempt(pc);
      lines.push(`  ${useName(pc)}${SEP}${l10n.t("transport.diag_stage")}: ${stageWord(stageOf(pc, best))}`);
      for (const g of addressGroups(pc, best)) block(g, "    ");
    }
  }
  return { verdict, sentence, round, groups, result, report: lines.join("\n") };
}
