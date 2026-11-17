export type RouteName = "relay" | "direct";
export type RouteHint = { kind: "read" } | { kind: "bulk"; file: string; upload?: "image" | "file"; bytes?: number };
type Sample = { at: number; milliseconds: number; queued: number };
type Window = { bytes: number; milliseconds: number };
type FileSegment = { relay: Window; direct: Window };
const empty = (): Window => ({ bytes: 0, milliseconds: 0 });
const pair = (): FileSegment => ({ relay: empty(), direct: empty() });
const rate = (w: Window) => (w.milliseconds > 0 ? w.bytes / w.milliseconds : 0);
const sorted = (samples: Sample[]) => samples.map(s => s.milliseconds).sort((a, b) => a - b);
/** The one latency summary for reads: what the page shows is what the fallback rule compares. */
const median = (samples: Sample[]) => sorted(samples)[Math.floor(samples.length / 2)] ?? Infinity;

/** Direct-first routing rule (docs/transport.md 「升级与热备」). */
export const DIRECT_RULE = {
  /** Fallback for reads: direct median exceeds 1.5x relay median + 20 ms. */
  read: { windowMs: 30_000, directSamples: 8, relaySamples: 4, medianRatio: 1.5, slackMs: 20 },
  /** Fallback for bulk: direct segment throughput falls below 70% of relay. */
  bulk: { ratio: 0.7, directMinBytes: 1 << 20, relayMinBytes: 512 << 10 },
  /** Exponential wait after fallback before direct becomes preferred again. */
  backoff: { initialMs: 60_000, maxMs: 600_000 },
  /** Display / observation window bounds per path. */
  keep: 512,
  keepMs: 60_000,
  displayMinSamples: 3,
} as const;

/** Uses verified application completions. Ready DirectPaths are preferred
 * immediately; hot paths retain one relay control sample in eight. */
export class DirectRouting {
  private reads: Record<RouteName, Sample[]> = { relay: [], direct: [] };
  private readStableAfter = 0;
  private readFallbacks = 0;
  private readSlow = false;
  private readSample = 0;
  /** Display only: each path's last 60s median with >=3 samples; cleared on fallback or hard fault. */
  private lastReadMs: Partial<Record<RouteName, number>> = {};

  private files = new Map<string, FileSegment>();
  private bulkStableAfter = 0;
  private bulkFallbacks = 0;
  private bulkSlow = false;
  private bulkSample = 0;
  private fileRate: Partial<Record<RouteName, number>> = {};
  private fileTally: FileSegment = pair();

  constructor(private readonly now = () => performance.now()) {}

  private live(path: RouteName, ms: number, now = this.now()) {
    return this.reads[path].filter(s => now - s.at <= ms);
  }

  measured(): { fileMiBps: Partial<Record<RouteName, number>>; readMs: Partial<Record<RouteName, number>> } {
    const fileMiBps: Partial<Record<RouteName, number>> = {};
    const readMs: Partial<Record<RouteName, number>> = {};
    const now = this.now();
    for (const path of ["relay", "direct"] as const) {
      const r = this.fileRate[path];
      if (r !== undefined) fileMiBps[path] = (r * 1000) / (1 << 20);
      const samples = this.live(path, DIRECT_RULE.keepMs, now);
      if (samples.length >= DIRECT_RULE.displayMinSamples) this.lastReadMs[path] = median(samples);
      const shown = this.lastReadMs[path];
      if (shown !== undefined) readMs[path] = shown;
    }
    return { fileMiBps, readMs };
  }

  /** Preferred path for read requests when a read DirectPath is ready. */
  readPath(): RouteName {
    return this.now() >= this.readStableAfter ? "direct" : "relay";
  }

  /** Preferred path for bulk requests when a bulk DirectPath is ready. */
  get filePath(): RouteName {
    return this.now() >= this.bulkStableAfter ? "direct" : "relay";
  }

  readSlowFallback(): boolean {
    return this.readSlow && this.now() < this.readStableAfter;
  }

  fileSlowFallback(): boolean {
    return this.bulkSlow && this.now() < this.bulkStableAfter;
  }

  choose(hint: RouteHint, ready: boolean): RouteName {
    if (!ready) return "relay";
    const now = this.now();
    if (hint.kind === "read") {
      if (now < this.readStableAfter) return "relay";
      return ++this.readSample % 8 === 0 ? "relay" : "direct";
    }
    if (now < this.bulkStableAfter) return "relay";
    return ++this.bulkSample % 8 === 0 ? "relay" : "direct";
  }

  observe(hint: RouteHint, path: RouteName, bytes: number, milliseconds: number, queued = 0) {
    const now = this.now();
    if (!Number.isFinite(milliseconds) || milliseconds <= 0) return;
    if (hint.kind === "read") {
      const sample = { at: now, milliseconds, queued };
      this.reads[path].push(sample);
      for (const samples of Object.values(this.reads)) {
        while (samples.length > DIRECT_RULE.keep || (samples.length && now - samples[0].at > DIRECT_RULE.keepMs)) {
          samples.shift();
        }
      }
      const live = this.live(path, DIRECT_RULE.keepMs, now);
      if (live.length >= DIRECT_RULE.displayMinSamples) this.lastReadMs[path] = median(live);
      this.decideReads(now);
      return;
    }
    if (bytes <= 0) return;
    const tally = this.fileTally[path];
    tally.bytes += bytes;
    tally.milliseconds += milliseconds;
    if (tally.bytes >= 4 << 20) {
      this.fileRate[path] = rate(tally);
      this.fileTally[path] = empty();
    }
    const seg = this.file(hint.file);
    seg[path].bytes += bytes;
    seg[path].milliseconds += milliseconds;
    if (now >= this.bulkStableAfter && seg.direct.bytes >= DIRECT_RULE.bulk.directMinBytes && seg.relay.bytes >= DIRECT_RULE.bulk.relayMinBytes) {
      if (rate(seg.direct) < rate(seg.relay) * DIRECT_RULE.bulk.ratio) {
        this.fail(hint);
      } else {
        seg.direct = empty();
        seg.relay = empty();
      }
    }
  }

  private decideReads(now: number) {
    if (now < this.readStableAfter) return;
    const r = DIRECT_RULE.read;
    const relay = this.live("relay", r.windowMs, now);
    const direct = this.live("direct", r.windowMs, now);
    if (direct.length >= r.directSamples && relay.length >= r.relaySamples && median(direct) > r.medianRatio * median(relay) + r.slackMs) {
      this.fail({ kind: "read" });
    }
  }

  /** Demotes direct with exponential backoff starting at 60 s up to 10 min. */
  fail(hint?: RouteHint) {
    const now = this.now();
    if (!hint || hint.kind === "read") {
      this.readFallbacks++;
      const wait = Math.min(DIRECT_RULE.backoff.maxMs, DIRECT_RULE.backoff.initialMs * 2 ** (this.readFallbacks - 1));
      this.readStableAfter = now + wait;
      this.readSlow = true;
      this.reads = { relay: [], direct: [] };
      this.lastReadMs = {};
    }
    if (!hint || hint.kind === "bulk") {
      this.bulkFallbacks++;
      const wait = Math.min(DIRECT_RULE.backoff.maxMs, DIRECT_RULE.backoff.initialMs * 2 ** (this.bulkFallbacks - 1));
      this.bulkStableAfter = now + wait;
      this.bulkSlow = true;
      this.files.clear();
      this.fileRate = {};
      this.fileTally = pair();
    }
  }

  /** Clears observation samples on physical close without starting a slow-fallback banner. */
  hardFail(kind?: "read" | "bulk") {
    if (!kind || kind === "read") {
      this.readSlow = false;
      this.reads = { relay: [], direct: [] };
      this.lastReadMs = {};
    }
    if (!kind || kind === "bulk") {
      this.bulkSlow = false;
      this.files.clear();
      this.fileRate = {};
      this.fileTally = pair();
    }
  }

  /** Resets fallback backoff and samples on network generation change or direct rebuild. */
  reset(kind?: "read" | "bulk") {
    if (!kind || kind === "read") {
      this.readFallbacks = 0;
      this.readStableAfter = 0;
      this.readSlow = false;
      this.readSample = 0;
      this.reads = { relay: [], direct: [] };
      this.lastReadMs = {};
    }
    if (!kind || kind === "bulk") {
      this.bulkFallbacks = 0;
      this.bulkStableAfter = 0;
      this.bulkSlow = false;
      this.bulkSample = 0;
      this.files.clear();
      this.fileRate = {};
      this.fileTally = pair();
    }
  }

  private file(key: string): FileSegment {
    let seg = this.files.get(key);
    if (!seg) {
      if (this.files.size >= 32) this.files.delete(this.files.keys().next().value!);
      seg = pair();
      this.files.set(key, seg);
    }
    return seg;
  }
}
