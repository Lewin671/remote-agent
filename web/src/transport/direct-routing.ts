export type RouteName = "relay" | "direct";
export type RouteHint = { kind: "read" } | { kind: "bulk"; file: string };
type Sample = { at: number; milliseconds: number };
type BulkWindow = { bytes: number; milliseconds: number };
type Bulk = { relay: BulkWindow; direct: BulkWindow; comparisons: number; preferred: RouteName; measuring: RouteName; badWindows: number; stableAfter: number; windowAt: number; recent: { relay: BulkWindow; direct: BulkWindow } };
const empty = (): BulkWindow => ({ bytes: 0, milliseconds: 0 });
const rate = (window: BulkWindow) => window.milliseconds > 0 ? window.bytes / window.milliseconds : 0;
const p95 = (samples: Sample[]) => [...samples].sort((a, b) => a.milliseconds - b.milliseconds)[Math.ceil(samples.length * 0.95) - 1]?.milliseconds ?? Infinity;

/** Measurements are verified application completions, never probe RTT estimates. */
export class DirectRouting {
  private reads: Record<RouteName, Sample[]> = { relay: [], direct: [] };
  private readPreferred: RouteName = "relay";
  private readStableAfter = 0;
  private readBadWindows = 0;
  private readWindowAt = 0;
  private alternate = false;
  private files = new Map<string, Bulk>();
  filePath: RouteName = "relay";
  constructor(private readonly now = () => performance.now()) {}
  choose(hint: RouteHint, ready: boolean): RouteName {
    if (!ready) return "relay";
    if (hint.kind === "read") {
      if (this.readPreferred === "direct") return "direct";
      if (this.now() < this.readStableAfter) return "relay";
      this.alternate = !this.alternate; return this.alternate ? "relay" : "direct";
    }
    const file = this.file(hint.file);
    if (this.now() < file.stableAfter) return "relay";
    return file.preferred === "direct" ? "direct" : file.measuring;
  }
  observe(hint: RouteHint, path: RouteName, bytes: number, milliseconds: number) {
    const now = this.now();
    if (!Number.isFinite(milliseconds) || milliseconds <= 0) return;
    if (hint.kind === "read") {
      const samples = this.reads[path]; samples.push({ at: now, milliseconds });
      while (samples.length > 512 || samples.length && now - samples[0].at > 15_000) samples.shift();
      const relay = this.reads.relay, direct = this.reads.direct;
      if (relay.length >= 100 && direct.length >= 100 && now - Math.min(relay[0].at, direct[0].at) >= 5000 && now >= this.readStableAfter && p95(direct) <= p95(relay) + Math.max(5, p95(relay) * .10)) this.readPreferred = "direct";
      if (now - this.readWindowAt >= 5000 && relay.length && direct.length) {
        this.readWindowAt = now;
        this.readBadWindows = p95(direct) > p95(relay) + Math.max(10, p95(relay) * .20) ? this.readBadWindows + 1 : 0;
        if (this.readBadWindows >= 2) this.fail(hint);
      }
      return;
    }
    const file = this.file(hint.file); this.filePath = path;
    if (bytes <= 0) return;
    file[path].bytes += bytes; file[path].milliseconds += milliseconds;
    file.recent[path].bytes += bytes; file.recent[path].milliseconds += milliseconds;
    if (file.preferred === "relay") {
      if (file.measuring === "relay" && file.relay.bytes >= 4 << 20) file.measuring = "direct";
      if (file.relay.bytes >= 4 << 20 && file.direct.bytes >= 4 << 20) {
        file.comparisons = rate(file.direct) >= rate(file.relay) * .95 ? file.comparisons + 1 : 0;
        if (file.comparisons >= 2 && now >= file.stableAfter) file.preferred = "direct";
        file.relay = empty(); file.direct = empty(); file.measuring = "relay";
      }
    }
    if (now - file.windowAt >= 5000) {
      // Relay observations are actual transfers from this same file. No RTT
      // or unrelated file is allowed to stand in for the baseline.
      if (file.recent.relay.bytes && file.recent.direct.bytes) {
        file.badWindows = rate(file.recent.direct) < rate(file.recent.relay) * .90 ? file.badWindows + 1 : 0;
        if (file.badWindows >= 2) this.fail(hint);
      }
      file.windowAt = now; file.recent = { relay: empty(), direct: empty() };
    }
  }
  fail(hint?: RouteHint) {
    const until = this.now() + 5000;
    if (!hint || hint.kind === "read") { this.readPreferred = "relay"; this.readStableAfter = until; this.reads = { relay: [], direct: [] }; this.readBadWindows = 0; }
    if (!hint) { for (const file of this.files.values()) { file.preferred = "relay"; file.stableAfter = until; file.comparisons = 0; file.relay = empty(); file.direct = empty(); file.measuring = "relay"; } this.filePath = "relay"; }
    else if (hint.kind === "bulk") { const file = this.file(hint.file); file.preferred = "relay"; file.stableAfter = until; file.comparisons = 0; file.relay = empty(); file.direct = empty(); file.measuring = "relay"; this.filePath = "relay"; }
  }
  private file(key: string): Bulk {
    let file = this.files.get(key);
    if (!file) {
      if (this.files.size >= 32) this.files.delete(this.files.keys().next().value!);
      file = { relay: empty(), direct: empty(), comparisons: 0, preferred: "relay", measuring: "relay", badWindows: 0, stableAfter: 0, windowAt: this.now(), recent: { relay: empty(), direct: empty() } }; this.files.set(key, file);
    }
    return file;
  }
}
