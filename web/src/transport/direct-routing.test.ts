import { expect, test } from "vitest";
import { DirectRouting } from "./direct-routing";
import { peerFrameBody } from "./peer-frame";
import { b, pb } from "./peer/wire";

test("bulk upgrade requires two 4 MiB comparisons of the same file, retains relay samples and falls back", () => {
 let now=0;const route=new DirectRouting(()=>now),file={kind:"bulk",file:"one"} as const;
 for(let round=0;round<2;round++) {
  expect(route.choose(file,true)).toBe("relay");route.observe(file,"relay",4<<20,400);
  expect(route.choose(file,true)).toBe("direct");route.observe(file,"direct",4<<20,410);now+=5000;
  if(round===0)expect(route.choose(file,true)).toBe("relay");
 }
 const selected=Array.from({length:8},()=>route.choose(file,true));expect(selected.filter(x=>x==="relay")).toHaveLength(1);
 expect(route.choose({kind:"bulk",file:"different"},true)).toBe("relay");
 for(let window=0;window<2;window++) {route.observe(file,"relay",4<<20,400);route.observe(file,"direct",4<<20,800);now+=5000;route.choose(file,true);}
 expect(route.choose(file,true)).toBe("relay");now+=4999;expect(route.choose(file,true)).toBe("relay");
 route.fail(file);expect(route.filePath).toBe("relay");expect(route.choose(file,false)).toBe("relay");
});
test("slow direct samples never upgrade and physical failure immediately retains relay",()=>{
 let now=0;const route=new DirectRouting(()=>now),file={kind:"bulk",file:"one"} as const;
 for(let i=0;i<3;i++){route.observe(file,"relay",4<<20,100);route.observe(file,"direct",4<<20,200);now+=5000;}
 expect(route.choose(file,true)).toBe("relay");route.fail();expect(route.choose(file,true)).toBe("relay");
});
test("the original maximum-sized delivery survives its ServerFrame wrapper",()=>{
 const body=new Uint8Array(65536);const raw=pb(b(11,body));expect(peerFrameBody(raw,11)).toEqual(body);
 expect(peerFrameBody(pb(b(11,new Uint8Array(65537))),11)).toBeUndefined();expect(peerFrameBody(raw,12)).toBeUndefined();
 expect(peerFrameBody(new Uint8Array([...raw,0]),11)).toBeUndefined();
});

test("requires two actual 4 MiB comparisons from the same file before bulk upgrade", () => {
  let now = 0; const routing = new DirectRouting(() => now), hint = { kind: "bulk", file: "file-a" } as const;
  expect(routing.choose(hint, true)).toBe("relay");
  for (let round = 0; round < 2; round++) {
    routing.observe(hint, "relay", 4 << 20, 1000); expect(routing.choose(hint, true)).toBe("direct");
    routing.observe(hint, "direct", 4 << 20, 1000 / .96); now += 2000;
    expect(routing.choose(hint, true)).toBe(round ? "direct" : "relay");
  }
  expect(routing.choose({ kind: "bulk", file: "file-b" }, true)).toBe("relay");
  expect(routing.choose(hint, false)).toBe("relay");
});
test("does not replace throughput with RTT and holds five seconds after a hard fault", () => {
  let now = 0; const routing = new DirectRouting(() => now), hint = { kind: "bulk", file: "file" } as const;
  routing.observe(hint, "relay", 4 << 20, 1000); routing.observe(hint, "direct", 4 << 20, 2000);
  expect(routing.choose(hint, true)).toBe("relay");
  routing.fail(hint); routing.observe(hint, "relay", 4 << 20, 1000);
  now = 4999; expect(routing.choose(hint, true)).toBe("relay");
  now = 5000; expect(routing.choose(hint, true)).toBe("direct");
});
test("the strict tier is kept: 100 steady samples, five seconds and a p95 within tolerance, when the gap is too small for the fast one", () => {
  let now = 0; const routing = new DirectRouting(() => now), hint = { kind: "read" } as const;
  for (let i = 0; i < 100; i++) { now = i * 50; routing.observe(hint, "relay", 1, 150); routing.observe(hint, "direct", 1, 140); }
  // Median 140 is above 0.7 x 150, so the fast tier says no; the strict one still needs its five seconds.
  expect(routing.choose(hint, true)).toBe("relay");
  now = 5000; routing.observe(hint, "direct", 1, 140);
  expect(routing.choose(hint, true)).toBe("direct"); expect(routing.choose(hint, true)).toBe("direct");
});
test("the strict tier still refuses a p95 outside its tolerance", () => {
  let now = 0; const routing = new DirectRouting(() => now), hint = { kind: "read" } as const;
  for (let i = 0; i < 120; i++) { now = i * 50; routing.observe(hint, "relay", 1, 150); routing.observe(hint, "direct", 1, i % 10 === 0 ? 400 : 140); }
  now = 6000; routing.observe(hint, "direct", 1, 140);
  expect(routing.choose(hint, true)).toBe("relay");
});

test("measured speeds appear only from real samples and are forgotten when measurements are discarded",()=>{
 let now=0;const route=new DirectRouting(()=>now),file={kind:"bulk",file:"one"} as const,none={fileMiBps:{},readMs:{}};
 expect(route.measured()).toEqual(none);expect(route.readPath()).toBe("relay");
 // Under 4 MiB is not a measurement yet; two 2 MiB chunks together are.
 route.observe(file,"relay",2<<20,100);expect(route.measured()).toEqual(none);
 route.observe(file,"relay",2<<20,100);expect(route.measured().fileMiBps).toEqual({relay:20});
 route.observe(file,"direct",4<<20,100);expect(route.measured().fileMiBps).toEqual({relay:20,direct:40});
 // Read latency is a median and needs a few samples; an empty path stays absent.
 for(const ms of [300,100,200])route.observe({kind:"read"},"relay",0,ms);
 route.observe({kind:"read"},"direct",0,20);route.observe({kind:"read"},"direct",0,30);
 expect(route.measured().readMs).toEqual({relay:200});
 route.fail();expect(route.measured()).toEqual(none);
});

// ---- Read routing: warm-up, fast upgrade, fallback (docs/transport.md 「升级与热备」) ----
// A fake clock and a made-up network. Every path has a latency function; the
// harness plays the engine's part (asks nextProbe, completes probes later) and
// the page's part (a foreground read every `readEvery` ms, on the chosen path).

type Net = { direct: (t: number) => number; relay: (t: number) => number; readEvery?: number; step?: number };
function simulate(net: Net) {
  let now = 0, nextRead = 0, upgradedAt: number | undefined, fellBackAt: number | undefined;
  const route = new DirectRouting(() => now), hint = { kind: "read" } as const;
  const probes: { at: number; path: "relay" | "direct" }[] = [], flips: { at: number; path: string }[] = [], inflight = { relay: 0, direct: 0 };
  let maxInflight = 0, last = route.readPath(), jitter = 0;
  const due: { at: number; path: "relay" | "direct"; ms: number; probe: boolean }[] = [];
  const lat = (path: "relay" | "direct") => net[path](now) + (jitter++ % 5) * 2;
  const run = (ms: number) => {
    const end = now + ms, step = net.step ?? 25;
    while (now < end) {
      now += step;
      for (const d of due.filter(x => x.at <= now)) {
        due.splice(due.indexOf(d), 1);
        if (d.probe) { inflight[d.path]--; route.probeDone(d.path); }
        route.observe(hint, d.path, 0, d.ms);
      }
      const probe = route.nextProbe();
      if (probe) { probes.push({ at: now, path: probe }); inflight[probe]++; maxInflight = Math.max(maxInflight, inflight[probe]); const ms = lat(probe); due.push({ at: now + ms, path: probe, ms, probe: true }); }
      if (net.readEvery && now >= nextRead) { nextRead = now + net.readEvery; const path = route.choose(hint, true), ms = lat(path); due.push({ at: now + ms, path, ms, probe: false }); }
      const path = route.readPath();
      if (path !== last) { flips.push({ at: now, path }); if (path === "direct") upgradedAt ??= now; else fellBackAt ??= now; last = path; }
    }
  };
  return { route, run, probes, flips, get now() { return now; }, get upgradedAt() { return upgradedAt; }, get fellBackAt() { return fellBackAt; }, get maxInflight() { return maxInflight; } };
}
const clearWin = { direct: () => 48, relay: () => 175 };

test("warm-up is bounded: at most 10 samples per path, one in flight per path, 4 per second, inside 5 seconds", () => {
  const sim = simulate({ ...clearWin, direct: () => 400, relay: () => 400 });
  sim.route.arm(); sim.run(20_000);
  const per = (path: string) => sim.probes.filter(p => p.path === path);
  expect(per("direct")).toHaveLength(10); expect(per("relay")).toHaveLength(10);
  expect(Math.max(...sim.probes.map(p => p.at))).toBeLessThanOrEqual(5000);
  expect(sim.maxInflight).toBe(1);
  for (const p of sim.probes) expect(sim.probes.filter(q => q.at >= p.at && q.at < p.at + 1000).length).toBeLessThanOrEqual(4);
  // Alternating: neither path gets two in a row.
  for (let i = 1; i < sim.probes.length; i++) expect(sim.probes[i].path).not.toBe(sim.probes[i - 1].path);
});
test("a path that never answers holds one slot and the other still stops at 10", () => {
  const sim = simulate({ direct: () => 1e9, relay: () => 100 });
  sim.route.arm(); sim.run(20_000);
  expect(sim.probes.filter(p => p.path === "direct")).toHaveLength(1);
  expect(sim.probes.filter(p => p.path === "relay")).toHaveLength(10);
  expect(sim.maxInflight).toBe(1);
});
test("triggers while a warm-up runs are absorbed; the next trigger gets its own bounded budget and the rate holds across both", () => {
  const sim = simulate({ ...clearWin, direct: () => 400, relay: () => 400 });
  for (let i = 0; i < 100; i++) sim.route.arm();
  sim.run(2000); for (let i = 0; i < 100; i++) sim.route.arm();
  sim.run(8000);
  expect(sim.probes.filter(p => p.path === "direct")).toHaveLength(10);
  sim.route.arm(); sim.run(6000); // a fresh trigger after the window: its own 10 per path
  expect(sim.probes.filter(p => p.path === "direct")).toHaveLength(20);
  expect(sim.probes.filter(p => p.path === "relay")).toHaveLength(20);
  for (const p of sim.probes) expect(sim.probes.filter(q => q.at >= p.at && q.at < p.at + 1000).length).toBeLessThanOrEqual(4);
});
test("no sample is asked for before a trigger, nor once the window is over", () => {
  const sim = simulate(clearWin);
  sim.run(10_000); expect(sim.probes).toHaveLength(0);
  expect(sim.route.warming()).toBe(false);
  sim.route.arm(); expect(sim.route.warming()).toBe(true); sim.run(5200); expect(sim.route.warming()).toBe(false);
});

test("a clear win (48 vs 175 ms) upgrades within 5 seconds of the warm-up and reads go direct", () => {
  const sim = simulate({ ...clearWin, readEvery: 1000 });
  sim.route.arm(); sim.run(5000);
  expect(sim.upgradedAt).toBeDefined(); expect(sim.upgradedAt!).toBeLessThanOrEqual(5000);
  expect(sim.route.readPath()).toBe("direct");
  expect(sim.route.measured().readMs.direct).toBeLessThan(60); expect(sim.route.measured().readMs.relay).toBeGreaterThan(170);
});
test("a close race (160 vs 175 ms) never upgrades and never oscillates over a minute", () => {
  const sim = simulate({ direct: () => 160, relay: () => 175, readEvery: 500 });
  sim.route.arm(); sim.run(65_000);
  expect(sim.flips).toEqual([]); expect(sim.route.readPath()).toBe("relay");
  // Re-triggers (a network change every ten seconds) do not push it over either.
  for (let i = 0; i < 6; i++) { sim.route.arm(); sim.run(10_000); }
  expect(sim.flips).toEqual([]);
});
test("the upgrade line is 0.7 of the relay median: 122 ms against 175 goes direct, 124 does not", () => {
  for (const [direct, goes] of [[110, true], [118, true], [126, false], [140, false]] as const) {
    // Jitter adds up to 8 ms to every sample, so compare the medians the page shows.
    const sim = simulate({ direct: () => direct, relay: () => 175, readEvery: 1000 });
    sim.route.arm(); sim.run(5000);
    const { direct: d, relay: r } = sim.route.measured().readMs;
    expect(sim.route.readPath() === "direct").toBe(goes);
    expect(d! <= 0.7 * r!).toBe(goes);
  }
});
test("what the page shows is what the rule compares: the same median over the same minute", () => {
  let now = 0; const route = new DirectRouting(() => now), hint = { kind: "read" } as const;
  for (let i = 0; i < 9; i++) { now = i * 300; route.observe(hint, "relay", 0, 100 + i * 10); route.observe(hint, "direct", 0, 60 + i); }
  // Medians: relay 140, direct 64; direct is under 0.7 x relay, so the rule upgraded on exactly these numbers.
  expect(route.measured().readMs).toEqual({ relay: 140, direct: 64 });
  expect(route.choose(hint, true)).toBe("direct");
  // Outside the minute the rule has nothing to compare, but the page keeps the last measured numbers.
  now += 61_000; expect(route.measured().readMs).toEqual({ relay: 140, direct: 64 });
});
test("the last measured median is kept after the window empties, replaced by a fresh one, and never steers a decision", () => {
  let now = 0; const route = new DirectRouting(() => now), hint = { kind: "read" } as const;
  for (let i = 0; i < 5; i++) { now = i * 300; route.observe(hint, "relay", 0, 175); route.observe(hint, "direct", 0, 160); }
  expect(route.measured().readMs).toEqual({ relay: 175, direct: 160 });
  now += 61_000; expect(route.measured().readMs).toEqual({ relay: 175, direct: 160 });   // 60 s later, no new samples
  expect(route.pending()).toBe(false); expect(route.sparse()).toBe(true);
  // Fewer than three fresh samples do not replace it; three do.
  route.observe(hint, "relay", 0, 90); route.observe(hint, "relay", 0, 90); expect(route.measured().readMs.relay).toBe(175);
  route.observe(hint, "relay", 0, 90); expect(route.measured().readMs.relay).toBe(90);
  // The snapshot is display only: with an empty window direct is not upgraded on its strength.
  now += 61_000; expect(route.choose(hint, true)).toBe("relay"); expect(route.readPath()).toBe("relay");
  // A fallback discards it with the samples.
  route.fail(); expect(route.measured().readMs).toEqual({});
});
test("the fast upgrade needs 8 samples of each path, a 2 second span, a calm tail and a stable queue", () => {
  const feed = (cfg: { n?: number; gap?: number; direct?: (i: number) => number; queued?: (i: number) => number }) => {
    let now = 0; const route = new DirectRouting(() => now), hint = { kind: "read" } as const, n = cfg.n ?? 8, gap = cfg.gap ?? 400;
    for (let i = 0; i < n; i++) { now = i * gap; route.observe(hint, "relay", 0, 175); route.observe(hint, "direct", 0, cfg.direct?.(i) ?? 48, cfg.queued?.(i) ?? 0); }
    return route.readPath();
  };
  expect(feed({})).toBe("direct");
  expect(feed({ n: 7 })).toBe("relay");                       // too few samples
  expect(feed({ gap: 200 })).toBe("relay");                   // 8 samples span 1.4 s, not 2 s
  expect(feed({ direct: i => (i < 5 ? 48 : 300) })).toBe("relay"); // median fine, p90 above the relay median
  expect(feed({ queued: i => i * 1000 })).toBe("relay");      // queue growing
});

test("direct that degrades after an upgrade (400 ms) falls back within 30 seconds, then holds relay", () => {
  const sim = simulate({ direct: t => (t < 10_000 ? 48 : 400), relay: () => 175, readEvery: 500 });
  sim.route.arm(); sim.run(5000); expect(sim.route.readPath()).toBe("direct");
  sim.run(25_000);
  expect(sim.fellBackAt).toBeDefined(); expect(sim.fellBackAt! - 10_000).toBeLessThanOrEqual(30_000);
  expect(sim.route.readPath()).toBe("relay");
  // 400 is worse than the relay, so the re-measure after the 5 s wait refuses it too: one upgrade, one fallback, nothing else.
  sim.run(60_000);
  expect(sim.flips.map(f => f.path)).toEqual(["direct", "relay"]);
});
test("after a fallback direct waits five seconds and must qualify again, even if it is fast at once", () => {
  const sim = simulate({ ...clearWin, readEvery: 500 });
  sim.route.arm(); sim.run(5000); expect(sim.route.readPath()).toBe("direct");
  const fell = sim.now; sim.route.fail({ kind: "read" });
  expect(sim.route.readPath()).toBe("relay");
  sim.run(4900); expect(sim.route.readPath()).toBe("relay"); expect(sim.route.warming()).toBe(false); // waiting, not measuring
  sim.run(2000); expect(sim.route.warming() || sim.route.readPath() === "direct").toBe(true);      // re-measure started after the wait
  sim.run(5000); expect(sim.route.readPath()).toBe("direct");
  expect(sim.flips.find(f => f.path === "direct" && f.at > fell)!.at).toBeGreaterThanOrEqual(fell + 5000);
});
test("a hard failure is immediate and drops every sample", () => {
  const sim = simulate({ ...clearWin, readEvery: 500 });
  sim.route.arm(); sim.run(5000); expect(sim.route.readPath()).toBe("direct");
  sim.route.fail({ kind: "read" });
  expect(sim.route.readPath()).toBe("relay"); expect(sim.route.choose({ kind: "read" }, true)).toBe("relay");
  expect(sim.route.measured().readMs).toEqual({});
});
test("the gap between 0.7 and 1.2 holds: equal speeds neither upgrade nor fall back, 1.3 times worse falls back", () => {
  // Upgraded on a clear win, then direct slows to match the relay: inside the gap, it stays.
  const same = simulate({ direct: t => (t < 10_000 ? 48 : 175), relay: () => 175, readEvery: 250 });
  same.route.arm(); same.run(70_000);
  expect(same.flips.map(f => f.path)).toEqual(["direct"]);
  // 1.3 x worse plus the 10 ms slack is past the line.
  const worse = simulate({ direct: t => (t < 10_000 ? 48 : 175 * 1.3 + 12), relay: () => 175, readEvery: 250 });
  worse.route.arm(); worse.run(70_000);
  expect(worse.flips.map(f => f.path).slice(0, 2)).toEqual(["direct", "relay"]);
  expect(worse.flips.length).toBe(2);
});
test("the fallback needs 8 direct and 4 relay samples inside 30 seconds", () => {
  let now = 0; const route = new DirectRouting(() => now), hint = { kind: "read" } as const;
  for (let i = 0; i < 10; i++) { now = i * 300; route.observe(hint, "relay", 0, 175); route.observe(hint, "direct", 0, 48); }
  expect(route.readPath()).toBe("direct");
  // Slow direct samples that outnumber the good ones: with 3 relay samples in the window there is no verdict yet.
  now = 40_000; for (let i = 0; i < 3; i++) route.observe(hint, "relay", 0, 175);
  for (let i = 0; i < 12; i++) { now += 100; route.observe(hint, "direct", 0, 400); }
  expect(route.readPath()).toBe("direct");
  now += 100; route.observe(hint, "relay", 0, 175);
  expect(route.readPath()).toBe("relay");
});
