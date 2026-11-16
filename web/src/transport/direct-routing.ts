export type RouteName = "relay" | "direct";
export type RouteHint = { kind: "read" } | { kind: "bulk"; file: string; upload?: "image" | "file"; bytes?: number };
type Sample = { at: number; milliseconds: number; queued: number };
type Window = { bytes: number; milliseconds: number };
type Bulk = { measured: Record<RouteName, Window>; recent: Record<RouteName, Window>; comparisons: number; preferred: RouteName; measuring: RouteName; bad: number; stableAfter: number; window: number };
const empty = (): Window => ({ bytes: 0, milliseconds: 0 });
const pair = () => ({ relay: empty(), direct: empty() });
const rate = (w: Window) => w.milliseconds > 0 ? w.bytes / w.milliseconds : 0;
const sorted = (samples: Sample[]) => samples.map(s => s.milliseconds).sort((a, b) => a - b);
const quantile = (samples: Sample[], q: number) => sorted(samples)[Math.ceil(samples.length * q) - 1] ?? Infinity;
const p95 = (samples: Sample[]) => quantile(samples, .95);
/** The one latency summary for reads: what the page shows is exactly what the
 * fast upgrade and the fallback compare. */
const median = (samples: Sample[]) => sorted(samples)[Math.floor(samples.length / 2)] ?? Infinity;
const span = (samples: Sample[]) => samples.length ? samples[samples.length - 1].at - samples[0].at : 0;

/** Read-only routing rule (docs/transport.md 「升级与热备」); every number here
 * is that document's, kept in one place for the web, and for 1d/1e to mirror. */
export const READ_RULE = {
  /** Warm-up after a connection, a network change or a fallback: authorized
   * background read samples, alternating paths, bounded in time, rate and count. */
  warmup: { windowMs: 5000, perPath: 10, minGapMs: 250 },
  /** Fast upgrade: a clear win needs few samples. */
  fast: { windowMs: 60_000, minSamples: 8, minSpanMs: 2000, medianRatio: 0.7 },
  /** Fallback: direct is clearly worse than the relay. 0.7 vs 1.2 is the gap that stops flapping. */
  back: { windowMs: 30_000, directSamples: 8, relaySamples: 4, medianRatio: 1.2, slackMs: 10 },
  /** Stability wait after any fallback before direct may be chosen again. */
  stableMs: 5000,
  /** Samples kept per path. */
  keep: 512, keepMs: 60_000,
} as const;
type Warmup = { start: number; end: number; issued: Record<RouteName, number>; inflight: Record<RouteName, boolean>; last: RouteName };

/** Uses verified application completions. Trial transfers carry disjoint file
 * chunks; a hot path retains one relay sample in eight, never duplicate bodies. */
export class DirectRouting {
  private reads: Record<RouteName, Sample[]> = { relay: [], direct: [] };
  private recentReads: Record<RouteName, Sample[]> = { relay: [], direct: [] };
  private readPreferred: RouteName = "relay";
  private readStableAfter = 0;
  private readBad = 0;
  private readWindow = 0;
  private sample = 0;
  private warm: Warmup = { start: Infinity, end: -Infinity, issued: { relay: 0, direct: 0 }, inflight: { relay: false, direct: false }, last: "relay" };
  private lastProbeAt = -Infinity;
  /** Display only: each path's last window median that had at least three samples,
   * kept when the window empties so a number does not vanish for want of new samples.
   * Routing decisions never read it. A fallback or hard failure discards it with the samples. */
  private lastReadMs: Partial<Record<RouteName, number>> = {};
  private files = new Map<string, Bulk>();
  filePath: RouteName = "relay";
  /** Last file throughput per path (bytes per millisecond) over at least 4 MiB
   * of verified completions; display only, never read by route choice. */
  private fileRate: Partial<Record<RouteName, number>> = {};
  private fileTally: Record<RouteName, Window> = pair();
  constructor(private readonly now = () => performance.now(), private readonly armed?: () => void) { this.readWindow = Math.floor(now() / 5000); }
  /** Samples of one path inside a window: the single data set that both the
   * display and the decisions read. */
  private live(path: RouteName, ms: number, now = this.now()) { return this.reads[path].filter(s => now - s.at <= ms); }
  /** Read-only view of what has actually been measured. A path with no samples
   * is absent, never zero. Throughput is in MiB/s, latency the median in ms
   * of the last minute's samples (at least three), the median the routing
   * rule compares; when the window has too few, the path's last such median. */
  measured(): { fileMiBps: Partial<Record<RouteName, number>>; readMs: Partial<Record<RouteName, number>> } {
    const fileMiBps: Partial<Record<RouteName, number>> = {}, readMs: Partial<Record<RouteName, number>> = {};
    for (const path of ["relay", "direct"] as const) {
      const rate = this.fileRate[path]; if (rate !== undefined) fileMiBps[path] = rate * 1000 / (1 << 20);
      const samples = this.live(path, READ_RULE.fast.windowMs);
      if (samples.length >= 3) this.lastReadMs[path] = median(samples);
      const shown = this.lastReadMs[path]; if (shown !== undefined) readMs[path] = shown;
    }
    return { fileMiBps, readMs };
  }
  /** Path that read requests use now; the same gate as choose() for reads. */
  readPath(): RouteName { return this.now() >= this.readStableAfter ? this.readPreferred : "relay"; }
  private snapshot(path: RouteName, now: number) {
    const samples = this.live(path, READ_RULE.fast.windowMs, now); if (samples.length >= 3) this.lastReadMs[path] = median(samples);
  }
  /** Whether the live window lacks the samples a decision needs on either path. */
  sparse() { const need = READ_RULE.fast.minSamples; return this.live("relay", READ_RULE.fast.windowMs).length < need || this.live("direct", READ_RULE.fast.windowMs).length < need; }
  /** Starts a warm-up: background samples of both paths, right away when the
   * read path is ready (the engine only asks then), after the stability wait
   * when a fallback just happened. A trigger while one is running is absorbed,
   * so triggers cannot stack budgets. `fresh` is the trigger that a new read
   * PC became ready: a window that was already running or waiting started
   * before there was a path to measure (a fallback arms one while the path is
   * still down), so absorbing this trigger would leave only the rest of that
   * window for the samples and the comparison would never complete. The new
   * window replaces it with the same bounds, never closer than the minimum gap
   * to the last probe and never before the stability wait ends. */
  arm(fresh = false) {
    const now = this.now(), w = this.warm;
    if (!fresh && now >= w.start && now < w.end) return;
    const start = Math.max(now, this.readStableAfter);
    this.warm = { start, end: start + READ_RULE.warmup.windowMs, issued: { relay: 0, direct: 0 }, inflight: w.inflight, last: "relay" };
    this.armed?.();
  }
  /** Whether a warm-up window is open or still waiting to open. */
  pending() { return this.now() < this.warm.end; }
  /** Whether a warm-up is running, or waiting out the stability period before it
   * starts: the page calls both measuring. */
  measuring() { return this.warming() || this.now() < this.warm.start && this.now() < this.warm.end; }
  /** Whether the live window holds what a decision needs on both paths: only then
   * can it be said how direct compares, whatever numbers are still displayed. */
  compared() { return !this.sparse(); }
  /** Whether the warm-up is running now: what the page calls measuring. */
  warming() {
    const now = this.now(), w = this.warm, cap = READ_RULE.warmup.perPath;
    return now >= w.start && now < w.end && !(w.issued.relay >= cap && w.issued.direct >= cap && !w.inflight.relay && !w.inflight.direct);
  }
  /** The path to take the next warm-up sample on, or nothing now. Paths
   * alternate (the less sampled first, direct before relay), one request in
   * flight per path, at most perPath per path per warm-up, and never closer
   * than minGapMs to the previous one: 4 per second in all. */
  nextProbe(): RouteName | undefined {
    const now = this.now(), w = this.warm, rule = READ_RULE.warmup;
    if (now < w.start || now >= w.end || now - this.lastProbeAt < rule.minGapMs) return;
    const open = (["direct", "relay"] as const).filter(p => w.issued[p] < rule.perPath && !w.inflight[p]);
    open.sort((a, b) => w.issued[a] - w.issued[b] || (a === w.last ? 1 : -1));
    const path = open[0]; if (!path) return;
    w.issued[path]++; w.inflight[path] = true; w.last = path; this.lastProbeAt = now;
    return path;
  }
  probeDone(path: RouteName) { this.warm.inflight[path] = false; }
  choose(hint: RouteHint, ready: boolean): RouteName {
    if (!ready) return "relay";
    if (hint.kind === "read") {
      this.advanceReads(); this.decideReads(this.now());
      if (this.now() < this.readStableAfter) return "relay";
      if (this.readPreferred === "direct") return ++this.sample % 8 === 0 ? "relay" : "direct";
      return "relay";
    }
    const file = this.file(hint.file); this.advanceBulk(hint, file);
    if (this.now() < file.stableAfter) return "relay";
    return file.preferred === "direct" ? (++this.sample % 8 === 0 ? "relay" : "direct") : file.measuring;
  }
  observe(hint: RouteHint, path: RouteName, bytes: number, milliseconds: number, queued = 0) {
    const now = this.now(); if (!Number.isFinite(milliseconds) || milliseconds <= 0) return;
    if (hint.kind === "read") {
      this.advanceReads();
      const sample = { at: now, milliseconds, queued }; this.reads[path].push(sample); this.recentReads[path].push(sample);
      for (const samples of Object.values(this.reads)) while (samples.length > READ_RULE.keep || samples.length && now - samples[0].at > READ_RULE.keepMs) samples.shift();
      this.snapshot(path, now); this.decideReads(now);
      return;
    }
    const file = this.file(hint.file); this.advanceBulk(hint,file); this.filePath = path;
    if (bytes <= 0) return;
    const tally = this.fileTally[path]; tally.bytes += bytes; tally.milliseconds += milliseconds;
    if (tally.bytes >= 4 << 20) { this.fileRate[path] = rate(tally); this.fileTally[path] = empty(); }
    for (const window of [file.measured[path],file.recent[path]]) { window.bytes += bytes; window.milliseconds += milliseconds; }
    if (file.preferred === "relay") {
      if (file.measuring === "relay" && file.measured.relay.bytes >= 4 << 20) file.measuring = "direct";
      if (file.measured.relay.bytes >= 4 << 20 && file.measured.direct.bytes >= 4 << 20) {
        file.comparisons = rate(file.measured.direct) >= rate(file.measured.relay)*.95 ? file.comparisons + 1 : 0;
        if (file.comparisons >= 2 && now >= file.stableAfter) { file.preferred = "direct"; file.bad = 0; file.recent = pair(); }
        file.measured = pair(); file.measuring = "relay";
      }
    }
  }
  /** Upgrades when direct is clearly or steadily better, falls back when it is
   * clearly worse. Fast and strict upgrades are alternatives; either suffices. */
  private decideReads(now: number) {
    if (this.readPreferred === "relay") {
      if (now < this.readStableAfter) return;
      const fast = READ_RULE.fast, relay = this.live("relay", fast.windowMs, now), direct = this.live("direct", fast.windowMs, now);
      const queueStable = direct.length > 0 && direct.at(-1)!.queued <= direct[0].queued;
      const clear = relay.length >= fast.minSamples && direct.length >= fast.minSamples && span(relay) >= fast.minSpanMs && span(direct) >= fast.minSpanMs && queueStable
        && median(direct) <= fast.medianRatio * median(relay) && quantile(direct, .9) <= median(relay);
      const steady = relay.length >= 100 && direct.length >= 100 && now - Math.max(relay[0].at, direct[0].at) >= 5000 && queueStable && p95(direct) <= p95(relay) + Math.max(5, p95(relay) * .10);
      if (clear || steady) this.readPreferred = "direct";
      return;
    }
    const back = READ_RULE.back, relay = this.live("relay", back.windowMs, now), direct = this.live("direct", back.windowMs, now);
    if (direct.length >= back.directSamples && relay.length >= back.relaySamples && median(direct) > back.medianRatio * median(relay) + back.slackMs) this.fail({ kind: "read" });
  }
  private advanceReads() {
    const window = Math.floor(this.now()/5000); if (window <= this.readWindow) return;
    const {relay,direct} = this.recentReads;
    const bad = relay.length && direct.length && p95(direct) > p95(relay) + Math.max(10,p95(relay)*.20);
    this.readBad = bad ? (window === this.readWindow+1 ? this.readBad+1 : 1) : 0;
    this.recentReads = {relay:[],direct:[]}; this.readWindow = window;
    if (this.readBad >= 2) this.fail({kind:"read"});
  }
  private advanceBulk(hint: RouteHint, file: Bulk) {
    const window=Math.floor(this.now()/5000); if(window<=file.window)return;
    const {relay,direct}=file.recent;
    const bad=relay.bytes && direct.bytes && rate(direct)<rate(relay)*.90;
    file.bad=bad ? (window===file.window+1 ? file.bad+1 : 1) : 0;
    file.recent=pair();file.window=window;
    if(file.bad>=2)this.fail(hint);
  }
  fail(hint?: RouteHint) {
    const until = this.now()+READ_RULE.stableMs;
    if (!hint || hint.kind === "read") {
      this.readPreferred="relay";this.readStableAfter=until;this.reads={relay:[],direct:[]};this.recentReads={relay:[],direct:[]};this.readBad=0;this.lastReadMs={};
      // A fallback re-measures after the stability wait; a window still open is cancelled, its samples would be wasted.
      this.warm.end = -Infinity; this.arm();
    }
    const files=!hint?[...this.files.values()]:hint.kind==="bulk"?[this.file(hint.file)]:[];
    for(const file of files){file.preferred="relay";file.stableAfter=until;file.comparisons=0;file.measured=pair();file.recent=pair();file.measuring="relay";file.bad=0;}
    if(!hint||hint.kind==="bulk"){this.filePath="relay";this.fileRate={};this.fileTally=pair();}
  }
  private file(key: string): Bulk {
    let file=this.files.get(key);
    if(!file){if(this.files.size>=32)this.files.delete(this.files.keys().next().value!);file={measured:pair(),recent:pair(),comparisons:0,preferred:"relay",measuring:"relay",bad:0,stableAfter:0,window:Math.floor(this.now()/5000)};this.files.set(key,file);}
    return file;
  }
}
