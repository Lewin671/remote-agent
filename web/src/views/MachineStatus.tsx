// 电脑状态 (ui-spec A3): one computer's CPU, memory and disk, live, and who
// is using them. A big number for the main chart, two small cards, the disk's
// actions when it is tight, the ranking. Black, white and gray; colour only
// where something needs a look.

import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { t } from "../i18n";
import { rich } from "../i18n/rich";
import { AgentMark } from "../agent-mark";
import { ResourcePressure, type MachineStatus, type ResourceUsage } from "../gen/remoteagent/v1/local_pb";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { CpuIcon, DiskIcon, MemoryIcon, TerminalIcon, ChevronRight } from "../icons";
import { agentOf, machineName, titleOf } from "../model";
import { machineCleanupPrompt, recentMachineProject, stableUsage } from "../machine-status";
import {
  RANK_ROWS, barPercent, curvePaths, curvePoints, diskBar, diskTight, diskWord, displayTime, footerText, gbFixed, gbNumber, memText, percentText,
  pressurePosition, pressureWord, sampleAt, secondsBefore, sizeText, staleText, toneOf, xOf, yOf, type Point, type RankKind,
} from "../machine-status-view";
import type { Remote } from "../remote";
import { NavBar, useRemote } from "../ui";
import { NewProjectSheet } from "./NewSession";
import { addProject } from "./Projects";
import { Roll, flipBox, useFlipList, useMachineStatus, usePageVisible, useReducedMotion } from "./machine-status-hooks";

type Go = (route: string) => void;
type Main = "cpu" | "mem";

const HEIGHT = 104;

/** The main chart (A3 ①, A4): the last two minutes, drawn from the points'
 * own times, gliding left at a constant pace; held with the mouse or a
 * finger, or stepped through with the arrow keys, it names the moment under
 * it. It draws nothing while the page is hidden. */
function Curve({ points, max, glide, describe, label, prevLabel, nextLabel, onLook }: {
  points: Point[];
  max: number;
  glide: boolean;
  describe: (p: Point) => string;
  label: string;
  prevLabel: string;
  nextLabel: string;
  onLook: (p: Point | undefined) => void;
}) {
  const gid = useId();
  const box = useRef(null as HTMLDivElement | null);
  const area = useRef(null as SVGPathElement | null);
  const line = useRef(null as SVGPathElement | null);
  const dot = useRef(null as SVGCircleElement | null);
  const rule = useRef(null as SVGLineElement | null);
  const spot = useRef(null as SVGCircleElement | null);
  const [width, setWidth] = useState(358);
  const [looking, setLooking] = useState(undefined as Point | undefined);
  // What the drawing reads each frame; React only re-renders for the width and the point looked at.
  const live = useRef({ points, max, glide, width, x: undefined as number | undefined, keyT: undefined as number | undefined, hit: undefined as Point | undefined, anchorT: -1, anchorAt: 0, onLook });
  live.current.points = points;
  live.current.max = max;
  live.current.glide = glide;
  live.current.width = width;
  live.current.onLook = onLook;
  const latest = points.at(-1)?.t ?? 0;
  if (live.current.anchorT !== latest) {
    live.current.anchorT = latest;
    live.current.anchorAt = Date.now();
  }
  const draw = useCallback(() => {
    const s = live.current;
    const at = displayTime(s.points, Date.now() - s.anchorAt, s.glide);
    const { line: l, area: a, head } = curvePaths(s.points, at, s.max, s.width, HEIGHT);
    line.current?.setAttribute("d", l);
    area.current?.setAttribute("d", a);
    if (dot.current) {
      dot.current.setAttribute("visibility", head ? "visible" : "hidden");
      if (head) {
        dot.current.setAttribute("cx", String(head.x));
        dot.current.setAttribute("cy", String(head.y));
      }
    }
    let hit: Point | undefined;
    if (s.x !== undefined) hit = sampleAt(s.points, s.x, at, s.width);
    else if (s.keyT !== undefined) hit = s.points.find((p) => p.t === s.keyT);
    if (hit && xOf(hit.t, at, s.width) < 0) hit = undefined;
    if (hit) {
      const x = xOf(hit.t, at, s.width);
      rule.current?.setAttribute("x1", String(x));
      rule.current?.setAttribute("x2", String(x));
      spot.current?.setAttribute("cx", String(x));
      spot.current?.setAttribute("cy", String(yOf(hit.v, s.max, HEIGHT)));
    }
    for (const el of [rule.current, spot.current]) el?.setAttribute("visibility", hit ? "visible" : "hidden");
    if (hit?.t !== s.hit?.t || (hit && s.hit && hit.v !== s.hit.v)) {
      s.hit = hit;
      setLooking(hit);
      s.onLook(hit);
    }
  }, []);
  useLayoutEffect(() => {
    if (typeof ResizeObserver === "undefined" || !box.current) return;
    const observer = new ResizeObserver(() => {
      const w = box.current?.clientWidth;
      if (w) setWidth(Math.round(w));
    });
    observer.observe(box.current);
    return () => observer.disconnect();
  }, []);
  // Every change of what is drawn draws once; while gliding, every frame, and nothing while hidden.
  useLayoutEffect(draw, [draw, points, max, width, glide]);
  useEffect(() => {
    let frame = 0;
    const tick = () => {
      draw();
      frame = requestAnimationFrame(tick);
    };
    const follow = () => {
      cancelAnimationFrame(frame);
      if (document.hidden || !glide) return;
      frame = requestAnimationFrame(tick);
    };
    document.addEventListener("visibilitychange", follow);
    follow();
    return () => {
      document.removeEventListener("visibilitychange", follow);
      cancelAnimationFrame(frame);
    };
  }, [draw, glide]);
  const look = (e: PointerEvent<HTMLDivElement>) => {
    live.current.keyT = undefined;
    live.current.x = e.clientX - e.currentTarget.getBoundingClientRect().left;
    draw();
  };
  const stop = () => {
    live.current.x = undefined;
    live.current.keyT = undefined;
    draw();
  };
  const step = (by: number) => {
    const list = live.current.points;
    if (!list.length) return;
    const at = live.current.keyT === undefined ? list.length - 1 : Math.max(0, list.findIndex((p) => p.t === live.current.keyT));
    live.current.x = undefined;
    live.current.keyT = list[Math.min(list.length - 1, Math.max(0, at + by))].t;
    draw();
  };
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "ArrowLeft" || e.key === "ArrowDown") step(-1);
    else if (e.key === "ArrowRight" || e.key === "ArrowUp") step(1);
    else if (e.key === "Home") step(-points.length);
    else if (e.key === "End") step(points.length);
    else if (e.key === "Escape") stop();
    else return;
    e.preventDefault();
  };
  const index = looking ? points.findIndex((p) => p.t === looking.t) : points.length - 1;
  return (
    <div
      ref={box}
      className="ms-plot"
      role="slider"
      tabIndex={0}
      aria-label={label}
      aria-orientation="horizontal"
      aria-valuemin={0}
      aria-valuemax={Math.max(0, points.length - 1)}
      aria-valuenow={Math.max(0, index)}
      aria-valuetext={looking ? describe(looking) : undefined}
      onPointerDown={(e) => { if (e.pointerType !== "mouse") look(e); }}
      onPointerMove={(e) => { if (e.pointerType === "mouse" || e.buttons) look(e); }}
      onPointerUp={stop}
      onPointerCancel={stop}
      onPointerLeave={stop}
      onKeyDown={key}
      onBlur={stop}
    >
      <svg width={width} height={HEIGHT} viewBox={`0 0 ${width} ${HEIGHT}`} aria-hidden>
        <defs>
          <linearGradient id={gid} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0" stopColor="currentColor" stopOpacity=".16" />
            <stop offset="1" stopColor="currentColor" stopOpacity="0" />
          </linearGradient>
        </defs>
        <path ref={area} fill={`url(#${gid})`} />
        <path ref={line} fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
        <line ref={rule} y1="0" y2={HEIGHT} stroke="var(--ink3)" strokeWidth="1" visibility="hidden" />
        <circle ref={spot} r="3.5" fill="currentColor" visibility="hidden" />
        <circle ref={dot} r="3.5" fill="currentColor" visibility="hidden" />
      </svg>
      <button type="button" className="ms-sr" tabIndex={-1} onClick={() => step(-1)}>{prevLabel}</button>
      <button type="button" className="ms-sr" tabIndex={-1} onClick={() => step(1)}>{nextLabel}</button>
    </div>
  );
}

const loadText = (load: number) => new Intl.NumberFormat(undefined, { minimumFractionDigits: 1, maximumFractionDigits: 1 }).format(load);

/** The big chart's card (A3 ①): CPU by default, memory once its small card is tapped. */
function Hero({ s, main, points, stale, glide, onLook, looked, heroRef }: {
  s: MachineStatus;
  main: Main;
  points: Point[];
  stale: boolean;
  glide: boolean;
  onLook: (p: Point | undefined) => void;
  looked: Point | undefined;
  heroRef: (el: HTMLElement | null) => void;
}) {
  const mem = main === "mem";
  const live = mem ? Number(s.memoryUsedBytes) : s.cpuPercent;
  const value = looked ? looked.v : live;
  const big = mem ? gbFixed(value) : String(Math.round(value));
  const pressure = pressurePosition(s) === undefined ? ResourcePressure.UNSPECIFIED : s.memoryPressure;
  const name = mem ? t("machine_status.memory") : t("machine_status.cpu");
  const readable = mem ? `${gbFixed(live)} ${t("machine_status.gb_used")}` : percentText(live);
  const total = Number(s.memoryTotalBytes);
  const describe = (p: Point) => t("machine_status.a11y_ring", { name: mem ? gbFixed(p.v) : percentText(p.v), value: t("machine_status.seconds_ago", { count: secondsBefore(p, points.at(-1) ?? p) }) });
  const max = Math.max(...points.map((p) => p.v), 0);
  return (
    <section ref={heroRef} className="ms-card ms-hero" role="group" aria-label={t("machine_status.a11y_ring", { name, value: readable })}>
      <div className="ms-top">
        <div>
          <div className="ms-kick">{mem ? <MemoryIcon /> : <CpuIcon />}{name}</div>
          <div className="ms-xl">
            <Roll text={big} />
            {mem ? <small>{t("machine_status.gb_used")}</small> : <sup>%</sup>}
          </div>
        </div>
        <div className="ms-aside">
          {mem ? (
            <>
              <div>{t("machine_status.total", { size: memText(total) })}</div>
              {s.swapUsedBytes !== undefined && <div>{t("machine_status.swap", { size: memText(s.swapUsedBytes) })}</div>}
            </>
          ) : (
            <>
              {s.loadOne !== undefined && <div>{rich(t("machine_status.load", { load: "{load}" }), { load: <b>{loadText(s.loadOne)}</b> })}</div>}
              {s.cores > 0 && <div>{t("machine_status.cores", { count: s.cores })}</div>}
            </>
          )}
        </div>
      </div>
      {!stale && (
        <>
          <Curve
            points={points}
            max={mem ? Math.max(total, max) : 100}
            glide={glide}
            describe={describe}
            label={t("machine_status.a11y_chart", { max: mem ? gbFixed(max) : percentText(max), now: mem ? gbFixed(live) : percentText(live) })}
            prevLabel={t("machine_status.a11y_prev_sample")}
            nextLabel={t("machine_status.a11y_next_sample")}
            onLook={onLook}
          />
          {mem && pressure !== ResourcePressure.UNSPECIFIED && (
            <div className="ms-pr"><b className={toneOf(pressure)}>{pressureWord(pressure)}</b></div>
          )}
          <div className="ms-axis">
            <span>{t("machine_status.axis_2min")}</span>
            <span>{t("machine_status.axis_1min")}</span>
            <span>{t("machine_status.axis_now")}</span>
          </div>
        </>
      )}
    </section>
  );
}

/** A line that may break where it has a comma and nowhere else. */
function Clauses({ text }: { text: string }) {
  return <>{(text.match(/[^，,]+[，,]?\s*/g) ?? [text]).map((part, i) => <span key={i} className="ms-clause">{part}</span>)}</>;
}

/** The other of CPU and memory as a small card: tapping it makes it the main chart. */
function Tile({ s, kind, onPick, tileRef }: { s: MachineStatus; kind: Main; onPick: () => void; tileRef: (el: HTMLElement | null) => void }) {
  if (kind === "cpu")
    return (
      <button ref={tileRef} className="ms-card ms-tile" onClick={onPick} aria-label={t("machine_status.a11y_cpu_card", { value: percentText(s.cpuPercent) })}>
        <span className="ms-kick"><CpuIcon />{t("machine_status.cpu")}</span>
        <span className="ms-lg"><Roll text={percentText(s.cpuPercent)} /></span>
        <span className="ms-say">
          {s.loadOne !== undefined && <>{t("machine_status.load_line", { load: loadText(s.loadOne) })}<br /></>}
          {s.cores > 0 && t("machine_status.cores", { count: s.cores })}
        </span>
      </button>
    );
  const position = pressurePosition(s);
  const shown = s.memoryPressure !== ResourcePressure.UNSPECIFIED;
  const used = gbFixed(s.memoryUsedBytes);
  const second = s.swapUsedBytes !== undefined
    ? t("machine_status.total_swap", { size: memText(s.memoryTotalBytes), swap: memText(s.swapUsedBytes) })
    : t("machine_status.total", { size: memText(s.memoryTotalBytes) });
  return (
    <button
      ref={tileRef}
      className="ms-card ms-tile"
      onClick={onPick}
      aria-label={shown
        ? t("machine_status.a11y_memory_card", { used: `${used} GB`, pressure: pressureWord(s.memoryPressure) })
        : t("machine_status.a11y_memory_card_plain", { used: `${used} GB` })}
    >
      <span className="ms-kick"><MemoryIcon />{t("machine_status.memory")}</span>
      <span className="ms-lg"><Roll text={used} /><small>{t("machine_status.gb_used")}</small></span>
      {shown && position !== undefined && <span className="ms-uv"><i style={{ left: `${position * 100}%` }} /></span>}
      <span className="ms-say">
        {shown && <><b className={toneOf(s.memoryPressure)}>{pressureWord(s.memoryPressure)}</b><br /></>}
        <Clauses text={second} />
      </span>
    </button>
  );
}

/** The disk's small card: free space, and what takes the volume. It has no curve, so it is
 * never the main chart: tapping it opens where remote-agent's own use is listed. */
function DiskTile({ s, onOpen }: { s: MachineStatus; onOpen: () => void }) {
  const disk = s.disk;
  if (!disk) return null;
  const tone = toneOf(disk.pressure);
  const tight = diskTight(disk.pressure);
  const critical = disk.pressure === ResourcePressure.CRITICAL;
  const bar = diskBar(disk, s.agentBytes);
  // Two lines, never folded into three: the volume, then remote-agent's share.
  const second = critical
    ? t("machine_status.disk_critical_hint")
    : <>{t("machine_status.disk_total", { size: sizeText(disk.totalBytes) })}{s.agentBytes !== undefined && <><br />{t("machine_status.disk_agent_share", { size: sizeText(s.agentBytes) })}</>}</>;
  return (
    <button className="ms-card ms-tile" onClick={onOpen} aria-label={t("machine_status.a11y_disk_card", { size: sizeText(disk.availableBytes), state: diskWord(disk.pressure) })}>
      <span className="ms-kick"><DiskIcon />{t("machine_status.disk")}</span>
      <span className={"ms-lg" + (tight ? " " + tone : "")}><Roll text={gbNumber(disk.availableBytes)} /><small>{t("machine_status.gb_available")}</small></span>
      <span className={"ms-stor" + (tight ? " " + tone : "")}><i className="a" style={{ width: `${bar.agent}%` }} /><i className="b" style={{ width: `${bar.other}%` }} /></span>
      <span className="ms-say"><b className={tone}>{diskWord(disk.pressure)}</b><br />{second}</span>
    </button>
  );
}

/** One session's CLI as a ranking row's block: the CLI's own colour and its mark. */
function SessionBlock({ agent }: { agent: Agent }) {
  const brand = agent === Agent.CODEX ? "x" : agent === Agent.ANTIGRAVITY ? "g" : "c";
  return <span className={"ms-ic " + brand}><AgentMark agent={agent} named /></span>;
}

/** 占用最多 (A3 ④): up to eight rows, by the main chart's figure, each with a bar
 * against the first. The order holds still unless a row passes its neighbour by a clear margin. */
function Ranking({ remote, s, memory, kind, stale, go }: { remote: Remote; s: MachineStatus; memory: boolean; kind: RankKind; stale: boolean; go: Go }) {
  const order = useRef<Partial<Record<RankKind, { memory: boolean; ids: string[] }>>>({});
  const list = useRef(null as HTMLDivElement | null);
  const held = order.current[kind];
  const rows = stableUsage(held && held.memory === memory ? held.ids : [], kind === "sessions" ? s.sessions : s.processes, memory).slice(0, RANK_ROWS);
  order.current[kind] = { memory, ids: rows.map((r) => r.id) };
  useFlipList(list);
  const session = (row: ResourceUsage) => remote.sessions.get(kind === "sessions" ? row.id : row.sessionId);
  const other = (row: ResourceUsage) => {
    const text = memory ? percentText(row.cpuPercent) : memText(row.memoryBytes);
    const owner = kind === "processes" ? session(row) : undefined;
    return owner ? `${text} · ${titleOf(owner)}` : text;
  };
  return (
    <div className="ms-card ms-list" ref={list}>
      {rows.length === 0 && <div className="ms-none">{t("machine_status.no_running_sessions")}</div>}
      {rows.map((row, i) => {
        const owner = kind === "sessions" ? session(row) : undefined;
        const title = kind === "sessions" ? (owner ? titleOf(owner) : row.executable) : row.executable;
        const body = (
          <>
            {kind === "sessions" && owner ? <SessionBlock agent={agentOf(owner)} /> : <span className="ms-ic pr"><TerminalIcon /></span>}
            <span className="ms-rb">
              <span className="ms-rt">
                <span>{title}</span>
                <span>{memory ? memText(row.memoryBytes) : percentText(row.cpuPercent)}</span>
              </span>
              <span className="ms-rm">
                <i className="ms-bar" style={{ width: `${barPercent(row, rows[0], memory)}%` }} />
                <small>{other(row)}</small>
              </span>
            </span>
          </>
        );
        const cls = "ms-u" + (i === 0 ? " top" : "") + (stale ? " off" : "");
        return kind === "sessions" && owner ? (
          <button key={row.id} className={cls} data-flip={row.id} onClick={() => go("s/" + owner.id)}>{body}</button>
        ) : (
          <div key={row.id} className={cls} data-flip={row.id}>{body}</div>
        );
      })}
    </div>
  );
}

/** What 让 Agent 帮我清理 does (B4): opens a new session on this computer, in its
 * most recent project, with the prompt filled in. It sends nothing. */
export function startCleanup(remote: Remote, machineId: string, go: Go, path: string) {
  remote.draftMachines.set(path, machineId);
  // What is already typed stays, with the prompt after it unless it is there already (rulings 8, 14).
  const prompt = machineCleanupPrompt();
  const draft = remote.drafts.get("new:" + path)?.trim();
  remote.drafts.set("new:" + path, !draft ? prompt : draft.includes(prompt) ? draft : `${draft}\n\n${prompt}`);
  remote.focusProject = path;
  remote.touch();
  go("p/" + encodeURIComponent(path));
}

/** The page before its first answer (ruling 6): its frame at once, dashes for numbers,
 * no curve yet and no ranking, and no spinner. */
function Skeleton({ name }: { name: string }) {
  return (
    <div className="body ms-body" role="region" aria-label={t("machine_status.title")}>
      <h1 className="ms-title">{name}</h1>
      <div className="ms-live"><span className="ms-dot idle" />{t("machine_status.loading")}</div>
      <div className="ms-cards">
        <section className="ms-card ms-hero">
          <div className="ms-top">
            <div>
              <div className="ms-kick"><CpuIcon />{t("machine_status.cpu")}</div>
              <div className="ms-xl">—</div>
            </div>
          </div>
          <div className="ms-plot" />
          <div className="ms-axis">
            <span>{t("machine_status.axis_2min")}</span>
            <span>{t("machine_status.axis_1min")}</span>
            <span>{t("machine_status.axis_now")}</span>
          </div>
        </section>
        <div className="ms-grid">
          <div className="ms-card ms-tile"><span className="ms-kick"><MemoryIcon />{t("machine_status.memory")}</span><span className="ms-lg">—</span></div>
          <div className="ms-card ms-tile"><span className="ms-kick"><DiskIcon />{t("machine_status.disk")}</span><span className="ms-lg">—</span></div>
        </div>
      </div>
    </div>
  );
}

export function MachineStatusPage({ remote, id, go, onBack }: { remote: Remote; id: string; go: Go; onBack: () => void }) {
  useRemote(remote);
  const m = remote.machines.get(id);
  const status = useMachineStatus(remote, id, 2000);
  const reduced = useReducedMotion();
  const visible = usePageVisible();
  // The main chart is remembered for this visit only.
  const [main, setMain] = useState("cpu" as Main);
  const [rank, setRank] = useState("sessions" as RankKind);
  const [looked, setLooked] = useState(undefined as Point | undefined);
  const [picking, setPicking] = useState(false);
  const [now, setNow] = useState(Date.now());
  const heroEl = useRef(null as HTMLElement | null | null);
  const tileEl = useRef(null as HTMLElement | null | null);
  const boxes = useRef<{ hero: DOMRect; tile: DOMRect } | undefined>(undefined);
  const { last } = status;
  const stale = status.state === "stale";
  // Stale: the line says how old the numbers are, so it counts.
  useEffect(() => {
    if (!stale || !visible) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [stale, visible]);
  const points = useMemo(() => (last ? curvePoints(last, main === "mem", status.curveSinceMs) : []), [last, main, status.curveSinceMs]);
  // A point being looked at belongs to one chart.
  useEffect(() => setLooked(undefined), [main]);
  useLayoutEffect(() => {
    const before = boxes.current;
    boxes.current = undefined;
    if (!before || reduced) return;
    if (heroEl.current) flipBox(heroEl.current, before.tile);
    if (tileEl.current) flipBox(tileEl.current, before.hero);
  }, [main, reduced]);
  const swap = (next: Main) => {
    if (heroEl.current && tileEl.current) boxes.current = { hero: heroEl.current.getBoundingClientRect(), tile: tileEl.current.getBoundingClientRect() };
    setMain(next);
  };
  if (!m)
    return (
      <div className="page set">
        <NavBar onBack={onBack} />
        <div className="center muted">{t("settings.device_not_found")}</div>
      </div>
    );
  if (!last)
    return (
      <div className="page set ms-page">
        <NavBar onBack={onBack} />
        {status.state === "unavailable" || !m.meta?.capabilities?.machineStatus ? (
          <div className="center ms-fail">
            <div>
              <div className="et">{t("machine_status.unavailable")}</div>
              <div className="big"><button className="link" onClick={status.retry}>{t("common.retry")}</button></div>
            </div>
          </div>
        ) : (
          <Skeleton name={machineName(m)} />
        )}
      </div>
    );
  const cleanup = () => {
    const path = m.meta && recentMachineProject(m.meta);
    if (path) startCleanup(remote, id, go, path);
    else setPicking(true);
  };
  const disk = last.disk;
  const looking = looked && points.length ? secondsBefore(looked, points.at(-1)!) : undefined;
  const words = looking !== undefined ? t("machine_status.seconds_ago", { count: looking }) : stale ? staleText(now - status.receivedAt) : t("machine_status.live");
  const tight = diskTight(disk?.pressure);
  return (
    <div className={"page set ms-page" + (stale ? " is-stale" : "")}>
      <NavBar onBack={onBack} />
      <div className="body ms-body" role="region" aria-label={t("machine_status.title")}>
        <h1 className="ms-title">{machineName(m)}</h1>
        <div className="ms-live"><span className={"ms-dot" + (stale ? " off" : "")} />{words}</div>
        <div className="ms-cards">
          <Hero s={last} main={main} points={points} stale={stale} glide={!reduced && visible} onLook={setLooked} looked={looked} heroRef={(el) => { heroEl.current = el; }} />
          <div className="ms-grid">
            <Tile s={last} kind={main === "cpu" ? "mem" : "cpu"} onPick={() => swap(main === "cpu" ? "mem" : "cpu")} tileRef={(el) => { tileEl.current = el; }} />
            {disk && <DiskTile s={last} onOpen={() => go("m/" + id + "/storage")} />}
          </div>
          {tight && (
            <div className="ms-card ms-rows">
              <button className="row act" onClick={cleanup}>{t("machine_status.cleanup_action")}</button>
              <button className="row" onClick={() => go("m/" + id + "/storage")}>
                {t("machine_status.agent_usage")}
                {last.agentBytes !== undefined && <span className="v">{sizeText(last.agentBytes)}</span>}
                <ChevronRight className="chev" />
              </button>
            </div>
          )}
          <div className="ms-sec">
            <h2>{t("machine_status.top_usage")}</h2>
            <button className="link" onClick={() => setRank(rank === "sessions" ? "processes" : "sessions")}>
              {rank === "sessions" ? t("machine_status.show_processes") : t("machine_status.show_sessions")}
            </button>
          </div>
          <Ranking remote={remote} s={last} memory={main === "mem"} kind={rank} stale={stale} go={go} />
          <div className="ms-foot">{footerText(last)}</div>
        </div>
      </div>
      {picking && (
        <NewProjectSheet
          remote={remote}
          fixedMachine={id}
          onClose={() => setPicking(false)}
          onDone={(machineId, path) => {
            addProject(remote, machineId, path);
            setPicking(false);
            startCleanup(remote, machineId, go, path);
          }}
        />
      )}
    </div>
  );
}
