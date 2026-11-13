// Made-up status of a computer for ?demo (never compiled into a build).
//   ms=ok (default)  a quiet MacBook
//   ms=tight         busy CPU, memory under pressure, disk almost full
//   ms=low           the disk under 10 GB
//   ms=both          mac under 10 GB, iMac under 2 GB (two reminders on the home)
// tight, low, none and stale are the mac only.
//   ms=none          the status can never be read
//   ms=stale         read three times, then never again
//   ms-full          the daemon has already been sampling for two minutes
//   ms-nobattery     a desktop: no battery, no load
//   ms-slow=<ms>     each answer takes that long
//   lang=en          English copy
// ms-large-id adds names that stress the layout.

import { create } from "@bufbuild/protobuf";
import { MachineStatusSchema, ResourcePressure, type DiskStatus, type MachineStatus } from "../gen/remoteagent/v1/local_pb";
import { CommandError } from "../remote";

const GB = 1e9;
/** Memory is counted in binary units (ui-rulings 1). */
const GIB = 2 ** 30;

export type DemoScenario = "ok" | "tight" | "low" | "none" | "stale";

/** The scenario a machine is in: `both` puts the mac under 10 GB and the iMac under 2 GB. */
export function scenarioOf(opts: URLSearchParams, machineId: string): DemoScenario {
  const ms = opts.get("ms") ?? "ok";
  if (ms === "both") return machineId === "imac" ? "tight" : machineId === "mac" ? "low" : "ok";
  // The others are only the mac, so that one computer is out of the ordinary at a time.
  return machineId === "mac" && (ms === "tight" || ms === "low" || ms === "none" || ms === "stale") ? ms : "ok";
}

/** The disk the daemon reports on its last minute check (MachineMeta.disk_status). */
export function demoDisk(scenario: DemoScenario): DiskStatus {
  const available = scenario === "tight" ? 1.2 * GB : scenario === "low" ? 8.2 * GB : 42 * GB;
  return create(MachineStatusSchema, {
    disk: {
      observedMs: BigInt(Date.now()),
      totalBytes: BigInt(460 * GB),
      availableBytes: BigInt(Math.round(available)),
      pressure: scenario === "tight" ? ResourcePressure.CRITICAL : scenario === "low" ? ResourcePressure.WARNING : ResourcePressure.NORMAL,
      emergencyReserveBytes: 268_435_456n,
    },
  }).disk!;
}

const wave = (t: number, a: number, b: number) => Math.sin(t / a) + 0.5 * Math.sin(t / b);

export class DemoStatus {
  private asked = new Map<string, { start: number; at: number; calls: number }>();
  constructor(private readonly opts: URLSearchParams) {}

  read(machineId: string, sessions: string[]): MachineStatus {
    const scenario = scenarioOf(this.opts, machineId);
    const now = Date.now();
    const seen = this.asked.get(machineId);
    // Nobody looking for 30 seconds: the daemon stopped sampling, and starts again.
    const fresh = !seen || now - seen.at > 30_000;
    const state = { start: fresh ? (this.opts.has("ms-full") ? now - 125_000 : now) : seen.start, at: now, calls: (seen?.calls ?? 0) + 1 };
    this.asked.set(machineId, state);
    if (scenario === "none" || (scenario === "stale" && state.calls > 3)) throw new CommandError("unavailable", "demo");
    const tight = scenario === "tight";
    const newest = Math.max(state.start, Math.floor(now / 2000) * 2000);
    const sample = (time: number) => ({
      timeMs: BigInt(time),
      cpuPercent: Math.max(2, Math.min(99, tight ? 84 + 6 * wave(time, 7000, 2300) : 27 + 14 * wave(time, 9000, 3100))),
      memoryUsedBytes: BigInt(Math.round((tight ? 12.6 : 11.2) * GIB + 0.35 * GIB * wave(time, 11000, 4100))),
    });
    const samples = [];
    for (let time = Math.max(state.start, newest - 118_000); time <= newest; time += 2000) samples.push(sample(time));
    if (!samples.length) samples.push(sample(now));
    const last = samples.at(-1)!;
    const jitter = (k: number, spread: number) => 1 + spread * Math.sin(now / 5000 + k);
    const desktop = this.opts.has("ms-nobattery");
    const long = this.opts.has("ms-large-id");
    return create(MachineStatusSchema, {
      observedMs: last.timeMs,
      cpuPercent: last.cpuPercent,
      cores: 10,
      loadOne: desktop ? undefined : tight ? 11.4 : 3.2,
      memoryUsedBytes: last.memoryUsedBytes,
      memoryTotalBytes: BigInt(16 * GIB),
      memoryPressure: tight ? ResourcePressure.WARNING : ResourcePressure.NORMAL,
      memoryPressurePosition: tight ? 0.72 : 0.34,
      swapUsedBytes: BigInt(Math.round((tight ? 4.8 : 1.3) * GIB)),
      disk: demoDisk(scenario),
      agentBytes: BigInt(3.1 * GB),
      sessions: [
        { id: sessions[0] ?? "s-claude", executable: "claude", cpuPercent: 141 * jitter(1, 0.04), memoryBytes: BigInt(Math.round(2.4 * GIB * jitter(2, 0.01))) },
        { id: sessions[1] ?? "s-run", executable: "codex", cpuPercent: 62 * jitter(3, 0.06), memoryBytes: BigInt(Math.round(1.1 * GIB * jitter(4, 0.01))) },
        { id: sessions[2] ?? "s-agy", executable: "agy", cpuPercent: 3 * jitter(5, 0.2), memoryBytes: BigInt(380 * 2 ** 20) },
      ],
      processes: [
        { id: "p1", executable: "java", sessionId: sessions[0] ?? "s-claude", cpuPercent: 312 * jitter(6, 0.03), memoryBytes: BigInt(Math.round(2.1 * GIB)) },
        { id: "p2", executable: "xcodebuild", sessionId: sessions[0] ?? "s-claude", cpuPercent: 188 * jitter(7, 0.03), memoryBytes: BigInt(Math.round(1.2 * GIB)) },
        { id: "p3", executable: long ? "com.apple.WebKit.GPU.Helper.long-name" : "Google Chrome", cpuPercent: 41 * jitter(8, 0.08), memoryBytes: BigInt(Math.round(1.6 * GIB)) },
        { id: "p4", executable: "node", sessionId: sessions[1] ?? "s-run", cpuPercent: 18 * jitter(9, 0.1), memoryBytes: BigInt(640 * 2 ** 20) },
        { id: "p5", executable: "Slack", cpuPercent: 6 * jitter(10, 0.2), memoryBytes: BigInt(910 * 2 ** 20) },
      ],
      uptimeSeconds: BigInt(3 * 86400 + 4 * 3600),
      battery: desktop ? undefined : { percent: 81, charging: true },
      samples,
    });
  }
}
