export type RouteName = "relay" | "direct";
export type RouteHint = { kind: "read" } | { kind: "bulk"; file: string; upload?: "image" | "file"; bytes?: number };
type Sample = { at: number; milliseconds: number; queued: number };
type Window = { bytes: number; milliseconds: number };
type Bulk = { measured: Record<RouteName, Window>; recent: Record<RouteName, Window>; comparisons: number; preferred: RouteName; measuring: RouteName; bad: number; stableAfter: number; window: number };
const empty = (): Window => ({ bytes: 0, milliseconds: 0 });
const pair = () => ({ relay: empty(), direct: empty() });
const rate = (w: Window) => w.milliseconds > 0 ? w.bytes / w.milliseconds : 0;
const p95 = (samples: Sample[]) => [...samples].sort((a,b) => a.milliseconds - b.milliseconds)[Math.ceil(samples.length * .95) - 1]?.milliseconds ?? Infinity;

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
  private files = new Map<string, Bulk>();
  filePath: RouteName = "relay";
  /** Last file throughput per path (bytes per millisecond) over at least 4 MiB
   * of verified completions; display only, never read by route choice. */
  private fileRate: Partial<Record<RouteName, number>> = {};
  private fileTally: Record<RouteName, Window> = pair();
  constructor(private readonly now = () => performance.now()) { this.readWindow = Math.floor(now() / 5000); }
  /** Read-only view of what has actually been measured. A path with no samples
   * is absent, never zero. Throughput is in MiB/s, latency the median in ms. */
  measured(): { fileMiBps: Partial<Record<RouteName, number>>; readMs: Partial<Record<RouteName, number>> } {
    const fileMiBps: Partial<Record<RouteName, number>> = {}, readMs: Partial<Record<RouteName, number>> = {};
    for (const path of ["relay", "direct"] as const) {
      const rate = this.fileRate[path]; if (rate !== undefined) fileMiBps[path] = rate * 1000 / (1 << 20);
      const samples = this.reads[path]; if (samples.length >= 3) readMs[path] = [...samples].sort((a, b) => a.milliseconds - b.milliseconds)[Math.floor(samples.length / 2)].milliseconds;
    }
    return { fileMiBps, readMs };
  }
  /** Path that read requests use now; the same gate as choose() for reads. */
  readPath(): RouteName { return this.now() >= this.readStableAfter ? this.readPreferred : "relay"; }
  needsReadSamples() { return this.readPreferred === "relay" && this.now() >= this.readStableAfter; }
  choose(hint: RouteHint, ready: boolean): RouteName {
    if (!ready) return "relay";
    if (hint.kind === "read") {
      this.advanceReads();
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
      for (const samples of Object.values(this.reads)) while (samples.length > 512 || samples.length && now - samples[0].at > 60_000) samples.shift();
      const {relay,direct} = this.reads;
      const queueStable = direct.length > 0 && direct.at(-1)!.queued <= direct[0].queued;
      if (relay.length >= 100 && direct.length >= 100 && now - Math.max(relay[0].at,direct[0].at) >= 5000 && now >= this.readStableAfter && queueStable && p95(direct) <= p95(relay) + Math.max(5,p95(relay)*.10)) this.readPreferred = "direct";
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
    const until = this.now()+5000;
    if (!hint || hint.kind === "read") { this.readPreferred="relay";this.readStableAfter=until;this.reads={relay:[],direct:[]};this.recentReads={relay:[],direct:[]};this.readBad=0; }
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
