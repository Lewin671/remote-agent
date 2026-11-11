import { Sheet } from "../ui";
import { canAnimate, Swap, useExit, useFresh, useHeightFlip, useKnob, useSettle } from "../motion";
import { agentInfo } from "../agents";
import { FileChips, withoutFileLines } from "./Files";
import { ChevronDown, ChevronLeft } from "../icons";
import { ScheduleText } from "./ScheduleText";
import { ScheduleCreate, ScheduleStrip } from "./pages";
import { clockTime, foldScheduled, type ScheduledItem } from "../schedules";
import { isSkeleton, skeletonMessage } from "../skeleton";
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";
import type { Event, ImageRef } from "../gen/remoteagent/v1/event_pb";
import { sessionImages, splitPlaceholders } from "../imageview";
import {
  GenCard,
  ImageStrip,
  ImageViewer,
  LocalThumbs,
  OpenImage,
} from "./Images";
import {
  AttachButton,
  attachBlocked,
  maxImages,
  TrayView,
  useDropPaste,
  useTray,
} from "./Attach";
import {
  ApprovalDecision,
  InputStatus,
  SessionStatus,
  type Agent,
} from "../gen/remoteagent/v1/event_pb";
import { Markdown } from "../markdown";
import { getView, setView, type View } from "../view";
import {
  agentName,
  agentOf,
  alwaysText,
  ago,
  decisionText,
  dockItems,
  duration,
  eventTime,
  exitReason,
  forkBlocked,
  dayTime,
  hhmm,
  inputFailure,
  machineName,
  modeName,
  modeOf,
  contextPercent,
  CONTEXT_WARN,
  phaseOf,
  plain,
  projectKey,
  projectName,
  queuedIds,
  shortPath,
  shortenPath,
  shortenPaths,
  shownStateOf,
  timeline,
  titleOf,
  type Derived,
  type Item,
  type Outgoing,
} from "../model";
import { errorText, type Remote, type SessionView } from "../remote";
import { ModeSheet, RenameDialog, SessionSheet } from "../sheets";
import {
  ActionSheet,
  Alert,
  Banner,
  ConnBar,
  StateMark,
  Exp,
  fromHere,
  NavBar,
  PathText,
  Skeleton,
  useRemote,
} from "../ui";
import { useDerived } from "./actions";
import { Attempt } from "../attempt";
import {
  elsewhereLabel,
  originLabel,
  terminalLabel,
  type OriginContext,
} from "../devices";
import { PlanChoice } from "../gen/remoteagent/v1/local_pb";
import { Composer, promptIdOf } from "./Composer";
import { ApprovalPage, Dock, PlanPage, QuestionPage } from "./Dock";
import { PendingBubbles, queueLine } from "./Queue";

// The terminal brings xterm, a third of the page's script: fetched when a
// terminal is first shown, or ahead of that once the page has settled.
const TerminalView = lazy(() =>
  import("./Terminal").then((m) => ({ default: m.TerminalView })),
);
import {
  ActiveTool,
  BackgroundTool,
  ToolGroup,
  ToolRow,
  toolTitle,
  type Short,
} from "./Tools";

import { RowState, RememberDetails } from "./RowState";
import { HistoryRows } from "./HistoryRows";
import { followsEnd, type Scrolled } from "./scroll-follow";
import {
  ArrowDown,
  Check,
  ChevronRight,
  Ellipsis,
  LoaderCircle,
  Minus,
  X,
} from "../icons";

type Tab = View;

/** Tracks an element's width, for choices that depend on the space the
 * main area has rather than the window. Through a callback ref: the element
 * is gone while a plan, question or approval page stands in its place, and
 * the one that comes back must be watched again. */
function useWidth<T extends HTMLElement>(): [(el: T | null) => void, number] {
  const [w, setW] = useState(0);
  const ro = useRef<ResizeObserver>(undefined);
  const ref = useCallback((el: T | null) => {
    ro.current?.disconnect();
    ro.current = undefined;
    if (!el) return;
    ro.current = new ResizeObserver(() => setW(el.clientWidth));
    ro.current.observe(el);
    setW(el.clientWidth);
  }, []);
  return [ref, w];
}

/** Tracks an element's height through a callback ref, for an element that
 * comes and goes (the chat column when switching to the terminal). */
function useHeight(): [(el: HTMLElement | null) => void, number] {
  const [h, setH] = useState(0);
  const ro = useRef<ResizeObserver>(undefined);
  const ref = useCallback((el: HTMLElement | null) => {
    ro.current?.disconnect();
    if (!el) return;
    const o = new ResizeObserver(() => setH(el.clientHeight));
    o.observe(el);
    ro.current = o;
    setH(el.clientHeight);
  }, []);
  return [ref, h];
}

/** The chat / terminal switch: a tablist whose knob slides to the picked view. */
function ViewSwitch({ className, tabs, tab, inactive, popup, onPick }: { className: string; tabs: [Tab, string][]; tab: Tab; inactive: boolean; popup: boolean; onPick: (t: Tab) => void }) {
  const ref = useRef<HTMLDivElement>(null);
  useKnob(ref, tab);
  return (
    <div className={className} role="tablist" aria-label="视图" ref={ref}>
      <i className="mo-knob" aria-hidden />
      {tabs.map(([t, name]) => (
        <button
          key={t}
          role="tab"
          aria-selected={t === tab}
          className={t === tab ? "on" : ""}
          disabled={t !== "chat" && inactive}
          onClick={() => onPick(t)}
        >
          {name}
          {t === "term" && popup && <span className="n" />}
        </button>
      ))}
    </div>
  );
}

export function SessionPage({
  remote,
  session,
  onBack,
  wide,
}: {
  remote: Remote;
  session: SessionView;
  onBack: () => void;
  wide: boolean;
}) {
  useRemote(remote);
  useEffect(() => {
    remote.setScheduleSession(session.id);
    return () => remote.setScheduleSession();
  }, [remote, session.id]);
  const sid = session.id;
  const events = remote.events(sid);
  const d = useDerived(remote, sid);
  const machine = remote.machines.get(session.machineId);
  // A computer's presence is what this connection has heard of it; until the
  // lists are in, the copy kept in this browser (which says offline) is not it,
  // and the input must not be replaced by 电脑不在线 for the moment it takes.
  // Nor is a word for the connection due then: the last state heard shows,
  // rather than 未连接 and 电脑离线 and then the state.
  const st = shownStateOf(session, remote.presence.get(session.machineId), remote.unlinked && !remote.awaiting);
  const agent = agentOf(session);
  const title = titleOf(session);
  const project = projectName(projectKey(session));
  const mname = machineName(machine);
  const ended = st.kind === "ended";
  const inactive = ended || st.kind === "offline";

  const [rootRef, width] = useWidth<HTMLDivElement>();
  const [colRef, colHeight] = useHeight();
  // The open dock may take what the input and about 120 px of chat leave.
  const room = colHeight > 0 ? colHeight - 230 : Infinity;
  const canSplit = wide && width >= 840;
  // A new session opens on its terminal once; otherwise the view is the one
  // last picked on this device. A view that cannot show falls back to the
  // chat without changing the pick.
  const [tabPick, setTab] = useState<Tab>(() =>
    remote.termFirst.delete(session.id) ? (wide ? "split" : "term") : getView(),
  );
  const pickTab = (t: Tab) => {
    setTab(t);
    setView(t);
  };
  let tab: Tab = tabPick === "split" && !canSplit ? "chat" : tabPick;
  if (inactive && tab !== "chat") tab = "chat";
  const [sheet, setSheet] = useState<"" | "mode" | "model">("");
  const [menu, setMenu] = useState(false);
  const [scheduling, setScheduling] = useState(false);
  const [confirmEnd, setConfirmEnd] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [forking, setForking] = useState(false);
  const [forkError, setForkError] = useState("");
  // A fork whose outcome is unknown is sent again as the same request,
  // also after the menu was closed and opened again, so the computer
  // answers with the session it already made instead of forking a second
  // time. The page is one session's (it is keyed by the session).
  const forkTry = useRef(new Attempt<null>()).current;
  const [forkUnknown, setForkUnknown] = useState<"" | "unknown" | "expired">(
    "",
  );
  const menuOpen = useRef(false);
  menuOpen.current = menu;
  const [page, setPage] = useState<
    "" | "plan" | "question" | { approval: string }
  >("");
  const [toast, setToast] = useState("");
  const [sent, setSent] = useState(0);
  const setToastRef = useRef(setToast);
  const composer = useRef<HTMLTextAreaElement>(null);
  // Remounting the input shows a draft put back from the chat.
  const [composerKey, setComposerKey] = useState(0);
  const items = dockItems(session, d, machine);
  const tray = useTray(remote, sid, session.machineId, maxImages(machine));
  /** Puts a message back into the input: its text, and its images, still
   * on the computer, with this device's copies of them when it has them. */
  const restore = (text: string, from?: string) => {
    const o = from
      ? remote.outbox(sid).find((x) => x.sendId === from)
      : undefined;
    const acc = from ? d.accepted.get(from) : undefined;
    // An uploaded image can be sent only by the device that uploaded it:
    // another client's images are not put into this input.
    const ours = !!o || acc?.origin?.deviceId === remote.account.deviceId;
    const ids = ours ? (o?.imageIds ?? acc?.imageIds ?? []) : [];
    const sourceFiles = o?.files ?? acc?.files ?? [];
    const fileIssue = sourceFiles.length ? remote.fileUploadIssue(session.machineId) : "";
    if (fileIssue) return setToast(fileIssue);
    const files = ours ? sourceFiles : [];
    const foreign = !ours && ((acc?.imageIds.length ?? 0) > 0 || (acc?.files?.length ?? 0) > 0);
    if (tray.files().length + files.length > tray.maxFiles) return setToast(`输入框放不下这条消息的 ${files.length} 个文件；先移除一些再放回`);
    // All of the message or none of it: never a message short of images.
    if (
      ids.length &&
      !tray.restore(
        ids.map((imageId, i) => ({
          imageId,
          thumb: o?.thumbs?.[i],
          file: o?.attachments?.files[i],
        })),
        o?.attachments?.original,
      )
    )
      return setToast(
        `输入框里已有 ${tray.images().length} 张图，放不下这条消息的 ${ids.length} 张；先移除一些再放回`,
      );
    tray.restoreFiles(files.map((f, i) => ({ fileId: f.fileId, name: f.name, size: Number(f.sizeBytes), file: o?.files?.[i]?.file })));
    if (o) remote.dismissSend(sid, o.sendId);
    const draft = remote.drafts.get(sid) ?? "";
    remote.drafts.set(
      sid,
      draft.trim() ? draft.trimEnd() + "\n\n" + text : text,
    );
    if (foreign)
      setToast(
        acc?.files?.length ? "这条消息的附件是另一客户端上传的，只放回了文字；需要的话在这里重新选择附件" : "这条消息的图片是另一客户端上传的，只放回了文字；需要的话在这里重新选择图片",
      );
    setComposerKey((k) => k + 1);
    setTimeout(() => composer.current?.focus());
  };
  const attachWhy = attachBlocked(remote, machine);
  const drop = useDropPaste(tray, attachWhy, setToastRef.current);
  const pictures = useMemo(
    () => sessionImages(events),
    [events, events.length],
  ); // eslint-disable-line react-hooks/exhaustive-deps
  const [viewing, setViewing] = useState<number>();
  const openImage = useCallback(
    (ref: ImageRef) => {
      const i = pictures.findIndex((p) => p.ref === ref);
      if (i >= 0) setViewing(i);
    },
    [pictures],
  );
  // Display names for the model chip; the list is cached per machine.
  useEffect(() => {
    if (machine?.online && !inactive)
      remote.listModels(session.machineId, agent).catch(() => undefined);
  }, [remote, session.machineId, agent, machine?.online, inactive]);

  const run = async (label: string, fn: () => Promise<unknown>) => {
    try {
      await fn();
      return true;
    } catch (e) {
      setToast(label + "失败：" + errorText(e));
      return false;
    }
  };
  const interrupt = () =>
    void run("中断", () =>
      remote.command(sid, { case: "interrupt", value: { sessionId: sid } }),
    );
  const toTerminal = () => setTab(canSplit ? "split" : "term");
  const agentLabel = agentName(agent);

  // Detail pages replace the whole session view.
  if (page === "plan" && d.plan && session.info?.planOpen)
    return (
      <PlanPage
        remote={remote}
        sid={sid}
        plan={d.plan}
        title={title}
        onBack={() => setPage("")}
      />
    );
  const question = [...d.questions.values()][0];
  if (page === "question" && question)
    return (
      <QuestionPage
        remote={remote}
        sid={sid}
        q={question}
        agent={agent}
        title={title}
        onBack={() => setPage("")}
        onTerminal={() => {
          setPage("");
          toTerminal();
        }}
      />
    );
  if (typeof page === "object" && "approval" in page) {
    const a = d.approvals.get(page.approval);
    if (a)
      return (
        <ApprovalPage
          remote={remote}
          sid={sid}
          a={a}
          agent={agent}
          title={title}
          onBack={() => setPage("")}
        />
      );
  }

  const tabs: [Tab, string][] = [
    ["chat", "聊天"],
    ["term", "终端"],
    ...(canSplit ? ([["split", "并排"]] as [Tab, string][]) : []),
  ];
  // Chat / terminal: a glass segmented control under the title on a phone
  // (dark glass over the terminal), the toolbar's own on a wide window.
  const seg = (
    <ViewSwitch
      className={wide ? "seg" : "vseg glass" + (tab === "term" ? " dark" : "")}
      tabs={tabs}
      tab={tab}
      inactive={inactive}
      popup={st.kind === "popup"}
      onPick={pickTab}
    />
  );
  // The mode follows the status only once it is known: a starting CLI has
  // not reported it, and one nobody could read is left unsaid.
  const known = modeOf(session);
  const modeTail = !ended && known && (
    <>
      {" "}
      · <span className={known.text === "完全放行" ? "red" : ""}>{known.text}</span>
    </>
  );
  const sub = (
    <>
      {st.text}
      {modeTail}
    </>
  );
  // The session's settings live in the ⋯ menu; a high context shows as
  // an orange dot on it.
  const mode = modeOf(session);
  const pct = contextPercent(session);
  const high = pct !== undefined && pct > CONTEXT_WARN;
  const model = session.info?.model
    ? [
        remote.modelName(session.machineId, agent, session.info.model),
        session.info.effort,
      ]
        .filter(Boolean)
        .join(" · ")
    : "";
  const moreLabel = high ? `更多，上下文 ${pct}%` : "更多";
  const openMenu = () => {
    setForkError("");
    setMenu(true);
  };
  /** Forks the session on its computer and opens the new one. The menu
   * stays open meanwhile and says how it went. */
  const fork = async () => {
    setForking(true);
    setForkError("");
    const { slot } = forkTry.next(null);
    const open = fromHere((id: string) => {
      location.hash = "/s/" + encodeURIComponent(id);
    });
    try {
      const res = await remote.command(
        sid,
        { case: "forkSession", value: { sessionId: sid } },
        slot,
      );
      forkTry.settle();
      setForkUnknown("");
      if (res.kind.case !== "forkSession" || !res.kind.value.session)
        throw new Error("意外的回复");
      const id = res.kind.value.session.id;
      remote.created.add(id);
      setMenu(false);
      open(id);
    } catch (e) {
      forkTry.settle(e);
      setForkUnknown(
        forkTry.expired ? "expired" : forkTry.frozen ? "unknown" : "",
      );
      if (menuOpen.current) setForkError(errorText(e));
      else setToast("没能分叉：" + errorText(e));
    } finally {
      setForking(false);
    }
  };
  /** Gives up a fork that expired with its outcome unknown: the next one
   * is a new request. */
  const abandonFork = () => {
    forkTry.abandon();
    setForkUnknown("");
    setForkError("");
  };
  const forkWhy = forkBlocked(session, machine, d.running);
  const more = (
    <button
      className={"more" + (high ? " context-high" : "")}
      onClick={openMenu}
      aria-label={moreLabel}
    >
      <Ellipsis />
    </button>
  );

  const header = wide ? (
    <header className="dh">
      {session.info?.taskOwned && <button className="schedule-run-back" aria-label="返回" title="返回任务" onClick={onBack}><ChevronLeft /></button>}
      <b>{title}</b>
      <span className="st">
        <StateMark kind={st.tone} />
        <span>
          {st.text}
          {modeTail}
        </span>
      </span>
      <span className="sp" />
      {seg}
      <button
        className={"ib mr" + (high ? " context-high" : "")}
        onClick={openMenu}
        aria-label={moreLabel}
      >
        <Ellipsis />
      </button>
    </header>
  ) : (
    <NavBar
      onBack={onBack}
      title={title}
      sub={sub}
      right={more}
      className={tab === "term" ? "dark" : ""}
    />
  );

  const chat = (
    <div className="ccol" ref={colRef}>
      <ChatBody
        remote={remote}
        session={session}
        events={events}
        d={d}
        agent={agent}
        machine={mname}
        sent={sent}
        onRestore={restore}
        onTerminal={inactive ? undefined : toTerminal}
      />
      {toast && (
        <div className="toast" onClick={() => setToast("")}>
          {toast}
        </div>
      )}
      {!inactive && (
        <Dock
          remote={remote}
          sid={sid}
          items={items}
          agent={agent}
          drafting={!!(remote.drafts.get(sid) ?? "").trim() || tray.items.length > 0}
          room={room}
          height={colHeight}
          onExpand={() => composer.current?.blur()}
          onDetail={(id) => setPage({ approval: id })}
          onPlan={() => setPage("plan")}
          onQuestion={() => setPage("question")}
          onTerminal={toTerminal}
        />
      )}
      {session.info?.taskOwned && <div className="schedule-reply-hint">回复后，这个对话会移到会话列表</div>}
      {inactive ? (
        <EndCard
          remote={remote}
          session={session}
          ended={ended}
          scheduledClosed={d.scheduledClosed}
          idleClosed={d.idleClosed}
          machineOnline={machine?.online ?? false}
          agent={agent}
          mname={mname}
          lastSeen={machine?.lastSeenMs ?? 0n}
        />
      ) : (
        <>
          <Composer
            key={composerKey}
            ref={composer}
            remote={remote}
            session={session}
            d={d}
            onSend={(text, mode) => {
              setSent((n) => n + 1);
              remote.send(
                sid,
                text,
                tray.imageIds(),
                tray.images().map((i) => i.thumb!).filter(Boolean),
                mode,
                {
                  original: tray.original,
                  files: tray.images().map((i) => i.file),
                },
                tray.files().map((i) => ({ fileId: i.fileId!, name: i.name!, sizeBytes: BigInt(i.bytes ?? 0), path: "", file: i.file })),
              );
              tray.clear();
            }}
            onInterrupt={interrupt}
            tray={tray}
            trayView={<TrayView tray={tray} />}
            attach={
              <AttachButton
                tray={tray}
                blocked={attachWhy}
                onBlocked={setToast}
              />
            }
            cardProps={wide ? drop.props : { onPaste: drop.props.onPaste }}
            dropping={drop.over}
            draft={remote.drafts.get(sid) ?? ""}
            onDraft={(text) => remote.drafts.set(sid, text)}
          />
        </>
      )}
    </div>
  );

  return (
    <OpenImage.Provider value={openImage}>
      <div
        className={"session" + (tab === "term" && !wide ? " termmode" : "")}
        ref={rootRef}
      >
        {header}
        <ConnBar remote={remote} />
        {/* The strip is only drawn for a session that has tasks; the room it takes is kept while it is fetched. */}
        {remote.sessionTasks(session.id).length > 0 && (
          <Suspense fallback={<div className="schedule-strip" aria-hidden />}>
            <ScheduleStrip remote={remote} session={session} go={route => { location.hash = "/" + route; }} />
          </Suspense>
        )}
        {!wide && <div className="segrow">{seg}</div>}
        <div className={"panes p-" + tab}>
          {tab !== "term" && chat}
          {tab !== "chat" && (
            <div className="tcol">
              <Suspense fallback={null}>
                <TerminalView
                  remote={remote}
                  session={session}
                  compact={tab === "split"}
                  wide={wide}
                  running={!ended}
                />
              </Suspense>
            </div>
          )}
        </div>
        {menu && (
          <ActionSheet
            info={
              <>
                <b>
                  {[agentLabel, project, remote.machines.size > 1 ? mname : ""].filter(Boolean).join(" · ")}
                </b>
                <span className="mono">
                  <PathText path={shortPath(session.info?.cwd ?? "")} />
                </span>
              </>
            }
            // Three groups (design-system §6 弹层与对话框): the session's
            // settings, what can be done to it, and ending it.
            actions={[
              ...(!inactive && mode
                ? [
                    {
                      label: "审批模式",
                      value: mode.text,
                      red: mode.text === "完全放行",
                      onClick: () => setSheet("mode"),
                    },
                  ]
                : []),
              ...(!inactive
                ? [
                    {
                      label: model ? "模型" : "会话设置",
                      value: model,
                      onClick: () => setSheet("model"),
                    },
                  ]
                : []),
              ...(!inactive && pct !== undefined
                ? [
                    {
                      label: "上下文",
                      value: `已用 ${pct}%`,
                      warn: high,
                      onClick: () => setSheet("model"),
                    },
                  ]
                : []),
              ...(machine?.online && remote.conn === "online"
                ? [{ label: "重命名", group: 1, onClick: () => setRenaming(true) }]
                : []),
              ...(agentInfo(agent).forks && machine?.online && remote.conn === "online"
                ? [
                    forking
                      ? {
                          label: "正在分叉…",
                          busy: true,
                          keep: true,
                          group: 1,
                          onClick: () => undefined,
                        }
                      : forkWhy
                        ? {
                            label: "分叉会话",
                            disabled: true,
                            note: forkWhy,
                            group: 1,
                            onClick: () => undefined,
                          }
                        : forkUnknown === "expired"
                          ? {
                              label: "放弃上次的分叉",
                              keep: true,
                              note: "不确定上次有没有分叉出新会话，它已过期，不会再重发。请先看看会话列表",
                              noteRed: true,
                              group: 1,
                              onClick: abandonFork,
                            }
                          : {
                              label: "分叉会话",
                              keep: true,
                              // It may have happened: only the same request is sent again.
                              note: forkUnknown
                                ? "上次的分叉结果不确定。再点会重发同一个请求，电脑收到过就不会再分叉一次"
                                : forkError && "没能分叉：" + forkError,
                              noteRed: true,
                              group: 1,
                              onClick: () => void fork(),
                            },
                  ]
                : []),
              { label: "定时发送…", group: 1, disabled: !machine?.online || remote.conn !== "online" || !remote.supportsSchedules(session.machineId), note: !remote.supportsSchedules(session.machineId) ? "请更新电脑上的 remote-agent" : undefined, onClick: () => setScheduling(true) },
              // Stopping the turn stays in its place while there is none,
              // grayed, saying why.
              ...(!inactive
                ? [
                    d.running
                      ? { label: "中断本轮", red: true, group: 1, onClick: interrupt }
                      : { label: "中断本轮", disabled: true, note: "现在没有在运行", group: 1, onClick: () => undefined },
                  ]
                : []),
              ...(!ended && machine?.online
                ? [
                    {
                      label: "结束会话…",
                      red: true,
                      group: 2,
                      onClick: () => setConfirmEnd(true),
                    },
                  ]
                : []),
            ].map((a) =>
              "busy" in a || !forking ? a : { ...a, disabled: true },
            )}
            pop={wide}
            onClose={() => setMenu(false)}
          />
        )}
        {scheduling && <Sheet className="schedule-session-sheet" onClose={() => setScheduling(false)}><div className="schedule-session-overlay"><Suspense fallback={null}><ScheduleCreate remote={remote} session={session} go={route => { setScheduling(false); location.hash = "/" + route; }} onBack={() => setScheduling(false)} /></Suspense></div></Sheet>}
        {confirmEnd && (
          <Alert
            title="结束这个会话？"
            text={`${mname} 上的 ${agentLabel} 会被关掉，聊天记录保留，之后可以恢复。`}
            ok="结束"
            danger
            onCancel={() => setConfirmEnd(false)}
            onOk={() => {
              setConfirmEnd(false);
              void run("结束会话", () =>
                remote.command(sid, {
                  case: "stopSession",
                  value: { sessionId: sid },
                }),
              );
            }}
          />
        )}
        {renaming && (
          <RenameDialog
            remote={remote}
            s={session}
            onClose={() => setRenaming(false)}
          />
        )}
        {sheet === "mode" && (
          <ModeSheet remote={remote} s={session} onClose={() => setSheet("")} />
        )}
        {sheet === "model" && (
          <SessionSheet
            remote={remote}
            s={session}
            running={d.running}
            onClose={() => setSheet("")}
          />
        )}
        {viewing !== undefined && (
          <ImageViewer
            remote={remote}
            sid={sid}
            images={pictures}
            start={viewing}
            agent={agentLabel}
            title={title}
            wide={wide}
            onClose={() => setViewing(undefined)}
            showPath={(p) =>
              shortenPath(p, session.info?.cwd ?? "", machine?.meta?.home ?? "")
            }
          />
        )}
      </div>
    </OpenImage.Provider>
  );
}

/** The chat: history, what runs now, and messages on their way. Follows
 * the bottom unless scrolled up, then offers a way back. */
function ChatBody({
  remote,
  session,
  events,
  d,
  agent,
  machine,
  sent,
  onRestore,
  onTerminal,
}: {
  remote: Remote;
  session: SessionView;
  events: Event[];
  d: Derived;
  agent: Agent;
  machine: string;
  /** Counts the messages sent from this view. */
  sent: number;
  onRestore: (text: string, from?: string) => void;
  onTerminal?: () => void;
}) {
  const sid = session.id;
  const scroller = useRef<HTMLDivElement>(null);
  const rowStates = useRef(new Map<string, boolean>());
  // Back from another view, the chat is where it was left.
  const [saved] = useState(() => remote.scroll.get(sid));
  const [focus] = useState(() => { const value = remote.scheduleFocus.get(sid); remote.scheduleFocus.delete(sid); return value; });
  const stick = useRef(focus ? false : saved?.stick ?? true);
  const userScrollAt = useRef(0);
  const lastScroll = useRef<Scrolled | undefined>(undefined);
  const [unseen, setUnseen] = useState(0);
  const seen = useRef<number | undefined>(undefined);
  const hist = remote.history(sid);
  const restoringHistory = useRef(!hist.loaded);
  const [expandedQuiet, setExpandedQuiet] = useState(new Set<string>());
  const rawItems = useMemo(() => timeline(events, d), [events, events.length, d]);
  const focusSeq = focus && (events.find(e => e.kind.case === "userMessage" && e.origin?.scheduledRunId === focus.runId)?.seq ?? focus.seq);
  const items = useMemo(() => foldScheduled(rawItems, events, expandedQuiet, focusSeq), [rawItems, events, events.length, expandedQuiet, focusSeq]);
  const focusKey = focus && (rawItems.find(it => it.kind === "event" && it.event.kind.case === "userMessage" && it.event.origin?.scheduledRunId === focus.runId) ?? rawItems.find(it => it.kind === "event" && it.event.seq >= focus.seq))?.key;
  const outbox = remote.outbox(sid);
  const prompt = remote.prompts.get(sid);
  const cwd = session.info?.cwd ?? "";
  // Models by the names the chips use: Sonnet, not sonnet or its full ID.
  const nameModel = (id: string) =>
    remote.modelName(session.machineId, agent, id);
  const home = remote.machines.get(session.machineId)?.meta?.home ?? "";
  const short: Short = {
    text: (title) => shortenPaths(title, cwd, home),
    path: (path) => shortenPath(path, cwd, home),
  };

  // Text of a message this page knows, by request ID; the daemon says
  // what it accepted from any client.
  const textOf = new Map([
    ...[...d.accepted].map(([id, a]) => [id, a.text] as const),
    ...outbox.map((o) => [o.sendId, o.text] as const),
  ]);
  const who: Who = {
    ctx: { me: remote.account.deviceId, devices: remote.devices, machine },
  };
  const promptId = promptIdOf(remote, sid, d);
  if (promptId && prompt) textOf.set(promptId, prompt);

  const count = items.length + outbox.length;
  // What has only now arrived plays its entrance; what was there, or loads
  // in above or behind a long open, plays nothing.
  const byKey = new Map(items.map((it) => [it.key, it] as const));
  const freshRow = useFresh(
    items.map((it) => it.key),
    hist.loaded,
    {
      replaces: (key, gone) => {
        const it = byKey.get(key);
        const tool = it?.kind === "tools" || it?.kind === "unsettled" || it?.kind === "background" || (it?.kind === "event" && it.event.kind.case === "toolCall");
        return tool && gone.some((g) => g[0] === "r" || g[0] === "u");
      },
    },
  );
  const freshOut = useFresh(
    outbox.map((o) => o.sendId),
    hist.loaded,
    { bulk: 3 },
  );
  // Messages this page sent: their line in the chat takes over from the
  // bubble that was waiting, in the same place.
  const sentHere = useRef(new Map<string, Sent>());
  useLayoutEffect(() => {
    for (const o of outbox) if (!sentHere.current.has(o.sendId)) sentHere.current.set(o.sendId, { text: "已送达电脑", queued: false });
  });
  const rowClass = (it: ScheduledItem) => {
    if (!freshRow(it.key)) return undefined;
    if (it.kind !== "event") return it.kind === "quiet" ? undefined : "mo-fade";
    const k = it.event.kind;
    if (k.case === "userMessage") return k.value.requestId && sentHere.current.has(k.value.requestId) ? undefined : "mo-rise";
    return k.case === "toolCall" || k.case === "approvalRequested" ? "mo-fade" : undefined;
  };
  // Following the bottom means what the chat holds may change height as it
  // likes (a queue line comes in, a running tool becomes a row, a picture
  // loads) and the column may give way as the dock or the input grows: the
  // bottom stays in view in the same frame. Not for what the person has just
  // opened (a tap or a key inside the chat), which grows where it was
  // touched, and never once they have scrolled up.
  const pressedAt = useRef(0);
  useEffect(() => {
    const el = scroller.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    let h = -1;
    // The observer reports once a frame, after the page has been laid out:
    // the sizes it hands over and the page's own height are read without
    // asking for another layout, and the one write is the scroll.
    const ro = new ResizeObserver((entries) => {
      let shrank = false;
      for (const e of entries) {
        if (e.target !== el) continue;
        const now = e.contentRect.height;
        if (h >= 0 && now < h) shrank = true;
        h = now;
      }
      // What the next scroll event is compared with: where the chat stands.
      lastScroll.current = { top: el.scrollTop, height: el.scrollHeight, client: el.clientHeight };
      if (!stick.current) return;
      if (shrank || Date.now() - pressedAt.current > 800) el.scrollTop = el.scrollHeight;
    });
    ro.observe(el);
    // The rows (or the virtual list that holds them) are the scroller's children.
    const watched = new Set<Element>();
    const sync = () => {
      for (const c of el.children) if (!watched.has(c)) (watched.add(c), ro.observe(c));
      for (const c of watched) if (c.parentElement !== el) (watched.delete(c), ro.unobserve(c));
    };
    const mo = new MutationObserver(sync);
    mo.observe(el, { childList: true });
    sync();
    return () => {
      mo.disconnect();
      ro.disconnect();
    };
  }, []);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (hist.loaded && items.length <= 100 && el && saved && !saved.stick)
      el.scrollTop = saved.top;
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  // Sending is asking to see the answer: back to the end, even from far up.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!sent || !el) return;
    stick.current = true;
    el.scrollTop = el.scrollHeight;
  }, [sent]);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (restoringHistory.current) {
      seen.current = count;
      if (hist.loaded || hist.error || hist.broken)
        restoringHistory.current = false;
    }
    seen.current ??= count;
    if (stick.current) {
      if (items.length <= 100) el.scrollTop = el.scrollHeight;
      seen.current = count;
      setUnseen(0);
    } else if (count > seen.current) setUnseen(count - seen.current);
  }, [count, hist.loaded]);

  // The daemon names the request a recorded message came from; until
  // then, a message it marked submitted (Codex takes one mid-turn and
  // records it later) stays in view as written to the terminal.
  const recorded = useMemo(() => new Set(
    events.flatMap((e) =>
      e.kind.case === "userMessage" && e.kind.value.requestId
        ? [e.kind.value.requestId]
        : [],
    ),
  ), [events, events.length]);
  const awaiting = (o: Outgoing) =>
    phaseOf(o, d.inputs.get(o.sendId)) === "done" &&
    d.accepted.has(o.sendId) &&
    !recorded.has(o.sendId);
  const queued = queuedIds(d);
  const pending = outbox.filter(
    (o) =>
      ["sending", "delivered", "queued", "failed"].includes(
        phaseOf(o, d.inputs.get(o.sendId)),
      ) || awaiting(o),
  );
  // Whether a bubble that unmounts goes without a sound: the chat now holds
  // its message, or it is still waiting and only drawn again (the long chat
  // swaps to its virtual list).
  const quiet = useRef<(id: string) => boolean>(() => false);
  quiet.current = (id) => recorded.has(id) || pending.some((o) => o.sendId === id);
  const started = session.info?.createdAt
    ? Number(session.info.createdAt.seconds) * 1000
    : events[0]
      ? eventTime(events[0])
      : 0;
  // The first message of a session started here, until the CLI takes it.
  const promptState = promptId ? d.inputs.get(promptId) : undefined;
  const showPrompt =
    prompt &&
    !events.some((e) => e.kind.case === "userMessage") &&
    (promptState === undefined || promptState === InputStatus.SUBMITTED);

  const header = (
    <>
      <div className="sysline">
        {started ? dayTime(started) + " · " : ""}
        <PathText path={shortPath(cwd)} />
      </div>
      {!hist.loaded && events.length === 0 && !hist.error && <Skeleton />}
      {hist.backfilling && !hist.error && !hist.broken && (
        <div className="sysline">正在加载更早的记录…</div>
      )}
    </>
  );
  const footer = (
    <>
      {remote.liveSnapshotError(sid) && (
        <Banner
          kind="warn"
          action="重试"
          onAction={() => void remote.refreshLiveSnapshot(sid)}
        >
          {remote.liveSnapshotError(sid)}
        </Banner>
      )}
      {(hist.unavailable ?? 0) > 0 && (
        <Banner>
          恢复前有 {hist.unavailable} 条记录不可读；后续可读记录照常显示。
        </Banner>
      )}
      {hist.broken && (
        <Banner>
          {hist.error || "有一条记录读不出来，历史停在这里，后面的内容和状态可能不全"}
        </Banner>
      )}
      {/* Offline, the connection banner says the content may be stale and the
            next connection fetches again: a failed fetch adds nothing. */}
      {hist.error && !hist.broken && remote.conn === "online" && (
        <Banner
          kind="err"
          action="重试"
          onAction={() => remote.retryEvents(sid)}
        >
          后面的记录没读到
        </Banner>
      )}
      {showPrompt && (
        <div className="me">
          <div>{prompt}</div>
          <small>已送达电脑</small>
        </div>
      )}
      {session.info?.status === SessionStatus.STARTING && (
        <div className="sysline">
          正在 {machine} 上启动 {agentName(agent)}…
        </div>
      )}
      {events.flatMap((e) => {
        if (
          e.kind.case !== "approvalRequested" ||
          !d.resolved.has(e.kind.value.approvalId)
        )
          return [];
        const reason = remote.approvalReasons.get(e.kind.value.approvalId);
        return reason
          ? [
              <div className="record" key={"reason:" + e.kind.value.approvalId}>
                <span className="muted">这条审批已经处理了。</span>
                <pre>{reason}</pre>
                <button
                  className="link"
                  onClick={() =>
                    void navigator.clipboard
                      .writeText(reason)
                      .catch(() => undefined)
                  }
                >
                  复制
                </button>
              </div>,
            ]
          : [];
      })}
      {pending.map((o) => (
        <Pending
          key={o.sendId}
          remote={remote}
          session={session}
          d={d}
          rank={queued.indexOf(o.sendId) + 1}
          count={queued.length}
          sentHere={sentHere.current}
          quiet={quiet}
          reached={items.length > 100 && stick.current ? () => { const e = scroller.current; return !e || e.scrollHeight - e.scrollTop - e.clientHeight < 4; } : undefined}
          o={o}
          phase={awaiting(o) ? "written" : phaseOf(o, d.inputs.get(o.sendId))}
          enter={freshOut(o.sendId)}
          agent={agent}
          onRestore={onRestore}
        />
      ))}
      <PendingBubbles remote={remote} session={session} d={d} sentHere={sentHere.current} own={new Set(pending.map((o) => o.sendId))} />
    </>
  );

  return (
    <div className="chatwrap">
      <div
        className="body chat"
        ref={scroller}
        onPointerDownCapture={() => {
          pressedAt.current = Date.now();
        }}
        onKeyDownCapture={() => {
          pressedAt.current = Date.now();
        }}
        onWheel={() => {
          userScrollAt.current = Date.now();
        }}
        onTouchMove={() => {
          userScrollAt.current = Date.now();
        }}
        onKeyDown={() => {
          userScrollAt.current = Date.now();
        }}
        onPointerDown={(e) => {
          if (e.target === e.currentTarget) userScrollAt.current = Date.now();
        }}
        onPointerMove={(e) => {
          if (e.buttons) userScrollAt.current = Date.now();
        }}
        onScroll={(e) => {
          const el = e.currentTarget;
          const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
          // Virtual measurement and pagination also produce scroll events;
          // only a scroll away from the end turns following off.
          const now = { top: el.scrollTop, height: el.scrollHeight, client: el.clientHeight };
          stick.current = followsEnd(stick.current, lastScroll.current, now, userScrollAt.current, Date.now());
          lastScroll.current = now;
          // The virtual list follows its last row, which stops short of the
          // chat's own bottom padding: where nobody scrolled it, the last step
          // is taken, so that older records that join above, or a row measured,
          // do not leave the end a few pixels away.
          if (stick.current && gap > 0 && gap < 40 && Date.now() - userScrollAt.current >= 1000) el.scrollTop = el.scrollHeight;
          remote.scroll.set(sid, { top: el.scrollTop, stick: stick.current });
          if (stick.current) {
            seen.current = count;
            setUnseen(0);
          }
        }}
      >
        <HistoryRows
          items={items}
          focusKey={focusKey}
          scroller={scroller}
          remote={remote}
          sid={sid}
          follow={() => stick.current}
          rowClass={rowClass}
          long={Number(session.lastSeq ?? 0) > LONG_HISTORY}
          loaded={hist.loaded || !!hist.error || hist.broken}
          ready={hist.ready || !!hist.error || hist.broken}
          header={header}
          footer={footer}
          render={(it) => it.kind === "quiet" ? <button className="schedule-quiet" onClick={() => setExpandedQuiet(old => new Set(old).add(it.key))}>{it.times.join("、")} 定时检查 · 没有新回复 <ChevronDown /></button> : (
            <RowState id={it.key} states={rowStates.current}>
              <Row
                it={it}
                d={d}
                short={short}
                machine={machine}
                agent={agent}
                textOf={textOf}
                onRestore={onRestore}
                nameModel={nameModel}
                remote={remote}
                onTerminal={onTerminal}
                who={who}
                sid={sid}
                settle={freshRow(it.key)}
                sentHere={sentHere.current}
              />
            </RowState>
          )}
        />
      </div>
      {unseen > 0 && (
        <NewMessages
          n={unseen}
          onClick={() => {
            const el = scroller.current;
            if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
            stick.current = true;
            seen.current = count;
            setUnseen(0);
          }}
        />
      )}
    </div>
  );
}

/** 「回到最新」: rises in, fades out. */
function NewMessages({ n, onClick }: { n: number; onClick: () => void }) {
  const ref = useRef<HTMLButtonElement>(null);
  useExit(ref);
  return (
    <button ref={ref} className="newmsg" onClick={onClick}>
      <ArrowDown /> {n} 条新消息
    </button>
  );
}

/** Sessions with more events than this are drawn with the virtual list from the
 * start: past it the chat is, or soon will be, over 100 rows (a row is about
 * half an event). */
const LONG_HISTORY = 170;

/** How long a queued message with no reason to wait may be on its way before
 * its bubble says it is queued: an idle session takes it in about half a
 * second (measured on a real Claude Code session), which must not flash. */
const QUEUE_SHOWN_AFTER = 1000;

/** A message this page sent, from the moment it is sent until the chat holds
 * it: one bubble throughout. Sending, delivered, written to the terminal, and
 * waiting in the queue are the same element under the same key; only the line
 * under it changes, and only a message that really waits (a turn runs, it is
 * paused, or it has waited a moment) takes the queue's outline, with its
 * 取消 line coming in by height and its fill and frame by a fade. It goes by
 * collapsing when it is canceled or dismissed, and without a sound when the
 * chat has the recorded message to put in its place. */
function Pending({
  remote,
  session,
  d,
  o,
  phase,
  enter,
  agent,
  onRestore,
  rank,
  count,
  sentHere,
  quiet,
  reached,
}: {
  remote: Remote;
  session: SessionView;
  d: Derived;
  o: Outgoing;
  phase: string;
  enter: boolean;
  // In a long (virtual) chat, the bottom is reached a frame or two after the
  // bubble is drawn: its entrance waits for that, hidden until then.
  reached?: () => boolean;
  agent: Agent;
  onRestore: (text: string, from?: string) => void;
  rank: number;
  count: number;
  sentHere: Map<string, Sent>;
  quiet: RefObject<(id: string) => boolean>;
}) {
  const sid = session.id;
  const root = useRef<HTMLDivElement>(null);
  const tail = useRef<HTMLElement>(null);
  const [waiting, setWaiting] = useState(() => !!reached);
  useEffect(() => {
    if (!waiting) return;
    let frames = 0;
    let id = requestAnimationFrame(function poll() {
      if (reached?.() || ++frames > 12) return setWaiting(false);
      id = requestAnimationFrame(poll);
    });
    return () => cancelAnimationFrame(id);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  useExit(root, { collapse: true, skip: () => quiet.current(o.sendId) });
  useHeightFlip(tail, { fade: false });
  // A queued message that is only on its way to the terminal shows nothing of
  // the queue; one that has waited a moment does.
  const queued = phase === "queued";
  const [late, setLate] = useState(false);
  useEffect(() => {
    setLate(false);
    if (!queued) return;
    const t = setTimeout(() => setLate(true), QUEUE_SHOWN_AFTER);
    return () => clearTimeout(t);
  }, [queued]);
  const failed = phase === "failed";
  const line = queued ? queueLine(remote, session, d, o.sendId, rank, count) : undefined;
  const asQueue = !!line && (line.needed || late);
  const lineKey = asQueue ? "queue" : phase === "queued" ? "delivered" : phase;
  const said = phase === "sending" ? "发送中…" : phase === "written" ? `已送入终端，等 ${agentName(agent)} 记录` : "已送达电脑";
  // What the chat's own line for it will start from.
  sentHere.set(o.sendId, { text: asQueue ? line.text : said, queued: asQueue });
  // Either it surely did not go, or it may have: the computer may have
  // taken it before the answer was lost.
  const cls = failed ? (o.unknown ? " warn" : " fail") : asQueue ? " pending-bubble mo-queued" : phase === "sending" ? " pending" : "";
  return (
    <div ref={root} className={"me" + cls + (enter ? (waiting ? " mo-wait" : " mo-rise") : "")}>
      <LocalThumbs blobs={o.thumbs} />
      <FileChips files={o.files} />
      {o.text && <div className="mo-text">{o.text}</div>}
      <small ref={tail}>
        {failed ? (
          <>
            {o.unknown ? "可能没发出去：" : "没发出去："}
            {o.error}
            {!o.unknown && (!!o.imageIds?.length || !!o.files?.length) && <button onClick={() => onRestore(o.text, o.sendId)}>放回输入框</button>}
            {!o.missingImages && <button onClick={() => remote.retrySend(sid, o.sendId)}>重试</button>}
            <button onClick={() => remote.dismissSend(sid, o.sendId)}>移除此提示</button>
          </>
        ) : (
          /* Where it has got to: each word fades into the next. */
          <Swap k={lineKey}>
            {asQueue ? (
              <span className="pending-meta">
                <span>{line.text}</span>
                {line.buttons}
              </span>
            ) : (
              <span className="mo-small">
                {phase === "sending" && <LoaderCircle />} {said}
              </span>
            )}
          </Swap>
        )}
      </small>
    </div>
  );
}

/** What a sent bubble last said under it, for the line that closes up in its place. */
type Sent = { text: string; queued: boolean };

/** A message of the chat. One that takes over from a bubble that was waiting
 * in the queue (`from`) starts in the queue's frame and fill, which fade to
 * the message's own. */
function MessageRow({ from, children }: { from?: boolean; children: ReactNode }) {
  const [queued, setQueued] = useState(!!from);
  useEffect(() => {
    if (!queued) return;
    const id = requestAnimationFrame(() => requestAnimationFrame(() => setQueued(false)));
    return () => cancelAnimationFrame(id);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps
  return <div className={"me" + (queued ? " mo-queued" : "")}>{children}</div>;
}

/** The line the sent bubble had, closing up. */
function Settled({ text, tall }: { text: string; tall: boolean }) {
  const ref = useRef<HTMLElement>(null);
  useSettle(ref, true);
  // Where nothing can play it, it is not there.
  return canAnimate() ? <small ref={ref} className={tall ? "mo-tall" : undefined}>{text}</small> : null;
}

function Row({
  it,
  d,
  short,
  machine,
  agent,
  textOf,
  onRestore,
  nameModel,
  remote,
  onTerminal,
  who,
  sid,
  settle,
  sentHere,
}: {
  it: Item;
  d: Derived;
  short: Short;
  machine: string;
  agent: Agent;
  textOf: Map<string, string>;
  onRestore: (text: string, from?: string) => void;
  nameModel: (id: string) => string;
  remote: Remote;
  onTerminal?: () => void;
  who: Who;
  sid: string;
  // The row has only now arrived; the ids of messages this page sent.
  settle: boolean;
  sentHere: Map<string, Sent>;
}) {
  if (it.kind === "tools") return <ToolGroup calls={it.calls} short={short} />;
  if (it.kind === "background")
    return (
      <BackgroundTool
        call={it.call}
        short={short}
        remote={remote}
        onTerminal={onTerminal}
      />
    );
  if (it.kind === "running" || it.kind === "unsettled")
    return (
      <ActiveTool
        tool={{ ...it.tool, title: toolTitle(it.tool, short) }}
        d={d}
        unsettled={it.kind === "unsettled"}
        aborted={it.kind === "unsettled" && it.aborted}
        remote={remote}
      />
    );
  if (it.kind === "turn")
    return <div className="sep">本轮用时 {duration(it.ms)}</div>;
  const e = it.event;
  if (isSkeleton(e))
    return <div className="record muted">{skeletonMessage}</div>;
  const k = e.kind;
  switch (k.case) {
    case "userMessage": {
      // This client's own messages carry no name.
      const label = originLabel(e.origin, who.ctx);
      const from =
        label === undefined
          ? d.typedLocally.has(e.seq)
            ? terminalLabel(machine)
            : ""
          : label === "本客户端"
            ? ""
            : label;
      const images = k.value.images;
      const segs = splitPlaceholders(withoutFileLines(k.value.text, k.value.files), images);
      const sent = k.value.requestId ? sentHere.get(k.value.requestId) : undefined;
      return (
        <MessageRow from={settle && sent?.queued}>
          <ImageStrip refs={images} numbered />
          <FileChips files={k.value.files} />
          {e.origin?.scheduledTaskId ? <ScheduleText key={e.seq.toString()} text={k.value.text} meta={`定时任务 · ${clockTime(Number(e.time?.seconds ?? 0n) * 1000)}`} /> : segs.length > 0 && (
            <div className="mo-text">
              {segs.map((x, i) =>
                "text" in x ? (
                  <span key={i}>{x.text}</span>
                ) : (
                  <span key={i} className="ichip">
                    图 {x.image}
                  </span>
                ),
              )}
            </div>
          )}
          {!e.origin?.scheduledTaskId && from && <small>{from}</small>}
          {/* Taking over from the bubble that waited for it: that had its
              status line under it, which now closes up instead of going. */}
          {!from && settle && !!sent && <Settled text={sent.text} tall={sent.queued} />}
        </MessageRow>
      );
    }
    case "scheduledNotification":
      return <div className="sysline schedule-notification">通知你：{k.value.text}</div>;
    case "imageGenerated":
      return <GenCard refs={k.value.images} prompt={k.value.revisedPrompt} />;
    case "agentMessage":
      return (
        <div
          className={
            "ai" + (k.value.phase === "commentary" ? " commentary" : "")
          }
        >
          <Markdown text={k.value.text} />
        </div>
      );
    case "reasoning":
      return (
        <RememberDetails name="details" className="think">
          <summary>
            思考 <Exp />
          </summary>
          <div className="thinktext">{k.value.text}</div>
        </RememberDetails>
      );
    case "toolCall":
      return (
        <ToolRow
          t={k.value}
          short={short}
          decision={it.decision}
          resultText={
            it.decision === ApprovalDecision.ALLOW_ALWAYS && it.approvalId
              ? alwaysText(d.resolved.get(it.approvalId)?.message ?? "")
              : undefined
          }
          by={
            it.approvalId && otherThanMe(d.by.get(it.approvalId), who)
              ? elsewhereLabel(d.by.get(it.approvalId), who.ctx)
              : undefined
          }
        />
      );
    case "turnEnded":
      return (
        <div className="sysline">
          已中断
          {otherThanMe(e.origin, who)
            ? ` · ${originLabel(e.origin, who.ctx)}`
            : ""}
        </div>
      );
    case "planAnswered": {
      const choices: Record<number, string> = {
        [PlanChoice.IMPLEMENT]: "批准执行计划",
        [PlanChoice.KEEP_PLANNING]: "继续规划",
        [PlanChoice.CLEAR_AND_IMPLEMENT]: "清空上下文后执行计划",
      };
      const choice = choices[k.value.choice] ?? "回答了计划";
      return (
        <div className="sysline">
          {choice}
          {otherThanMe(e.origin, who) &&
            ` · ${elsewhereLabel(e.origin, who.ctx)}`}
        </div>
      );
    }
    case "approvalRequested": {
      const r = d.resolved.get(k.value.approvalId);
      if (!r) return null;
      const ok =
        r.decision === ApprovalDecision.ALLOW ||
        r.decision === ApprovalDecision.ALLOW_ALWAYS;
      // Canceled, or answered in the CLI's own prompt: what happened shows
      // in the tool call that follows.
      const canceled =
        r.decision === ApprovalDecision.CANCELED ||
        r.decision === ApprovalDecision.TERMINAL;
      return (
        <div className="record">
          <span className={canceled ? "muted" : ok ? "ok" : "x"}>
            {canceled ? <Minus /> : ok ? <Check /> : <X />}
          </span>
          <code>{k.value.command || k.value.toolName}</code>
          <span className="muted">
            {r.decision === ApprovalDecision.ALLOW_ALWAYS
              ? alwaysText(r.message)
              : decisionText(r.decision)}
            {otherThanMe(d.by.get(k.value.approvalId), who) &&
              r.decision !== ApprovalDecision.TERMINAL &&
              ` · ${elsewhereLabel(d.by.get(k.value.approvalId), who.ctx)}`}
          </span>
        </div>
      );
    }
    case "questionAsked": {
      // Async questions use the ordinary composer; there is no terminal
      // answer card or answer command for them.
      if (k.value.async)
        return (
          <div className="ai">
            {k.value.questions.map((q, i) => (
              <div key={i}>
                {q.header && <b>{q.header}</b>}
                <Markdown text={q.text} />
                {q.options.length > 0 && (
                  <ul>
                    {q.options.map((o, j) => (
                      <li key={j}>
                        <b>{o.label}</b>
                        {o.description && ` · ${o.description}`}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            ))}
          </div>
        );
      const a = d.answered.get(k.value.questionId);
      const by = d.by.get(k.value.questionId);
      return a ? (
        <div className="sysline">
          回答了 <b>{a.join("、")}</b>
          {otherThanMe(by, who) && ` · ${elsewhereLabel(by, who.ctx)}`}
        </div>
      ) : null;
    }
    case "inputState": {
      if (d.unsent.has(k.value.requestId)) {
        // The session ended before writing it. Not sent again by itself:
        // the text can go back into the input, to send after resuming.
        const text = textOf.get(k.value.requestId);
        const images = d.accepted.get(k.value.requestId)?.imageIds.length ?? 0;
        const files = d.accepted.get(k.value.requestId)?.files;
        return text !== undefined && (text || images || files?.length) ? (
          <div className="me fail unsent">
            <FileChips files={files} />
            <div>{text || (images ? `${images} 张图片` : "")}</div>
            <small>
              没发出去：会话已结束
              <button onClick={() => onRestore(text, k.value.requestId)}>
                放回输入框
              </button>
            </small>
          </div>
        ) : (
          <div className="sysline fail">有一条消息没发出去：会话已结束</div>
        );
      }
      if (k.value.status === InputStatus.CANCELED)
        return <div className="sysline">取消了一条排队的消息</div>;
      if (
        k.value.status === InputStatus.FAILED ||
        k.value.status === InputStatus.NEEDS_TERMINAL
      ) {
        const text = textOf.get(k.value.requestId) ?? "";
        const why = inputFailure(k.value.reason, agent);
        const images = d.accepted.get(k.value.requestId)?.imageIds ?? [];
        const files = d.accepted.get(k.value.requestId)?.files;
        const mine = remote
          .outbox(sid)
          .some((o) => o.sendId === k.value.requestId);
        if (k.value.status === InputStatus.NEEDS_TERMINAL)
          return (
            <div className="me warn">
              <FileChips files={files} />
              <div>{text || (files?.length ? "" : "一条消息")}</div>
              <small>
                可能没发出去：{why}
                {onTerminal && <button onClick={onTerminal}>去终端</button>}
              </small>
            </div>
          );
        return (
          <div className="me fail">
            <FileChips files={files} />
            {!text && images.length > 0 ? (
              <div>{images.length} 张图片</div>
            ) : (
              <div>{text || (files?.length ? "" : "一条消息")}</div>
            )}
            <small>
              没发出去：{why}
              {mine && (text || images.length > 0 || !!files?.length) && (
                <button
                  disabled={remote.replaced.has(k.value.requestId)}
                  onClick={() =>
                    remote.resend(sid, k.value.requestId, text, images, [], remote.outbox(sid).find((o) => o.sendId === k.value.requestId)?.files ?? files)
                  }
                >
                  {remote.replaced.has(k.value.requestId)
                    ? "已重新发送"
                    : "重试"}
                </button>
              )}
              {(text || images.length > 0 || !!files?.length) && (
                <button onClick={() => onRestore(text, k.value.requestId)}>
                  放回输入框
                </button>
              )}
            </small>
          </div>
        );
      }
      const text = textOf.get(k.value.requestId);
      const note = "可能没发出去：已送到终端但没确认";
      return text ? (
        <div className="me warn">
          <div>{text}</div>
          <small>{note}</small>
        </div>
      ) : (
        <div className="sysline warn">有一条消息{note}</div>
      );
    }
    case "settingsChanged": {
      const c = k.value;
      if (c.compacted) {
        const x = d.compactions.get(e.seq);
        return (
          <div className="sysline">
            上下文已压缩
            {x?.before !== undefined && x.after !== undefined
              ? ` · ${x.before}% → ${x.after}%`
              : ""}
          </div>
        );
      }
      if (c.model)
        return (
          <div className="sysline">
            {otherThanMe(e.origin, who)
              ? `${originLabel(e.origin, who.ctx)} 将模型切到 `
              : "切换到 "}<b>{nameModel(c.model)}</b>
            {c.effort && ` · ${c.effort}`}
          </div>
        );
      return (
        <div className="sysline">
          {otherThanMe(e.origin, who)
            ? `${originLabel(e.origin, who.ctx)} 将模式切到 `
            : "模式切换到 "}
          <b>{modeName(c.mode)}</b>
        </div>
      );
    }
    case "turnContext":
      return (
        <div className="sysline">
          {machine} 终端把
          {(d.terminalChanges.get(e.seq) ?? "").split("，").map((c, i) => {
            const [what, to] = c.split("切到了 ");
            return (
              <span key={i}>
                {i > 0 && "，"}
                {what}切到了{" "}
                <b>
                  {k.value.model
                    ? to.split(k.value.model).join(nameModel(k.value.model))
                    : to}
                </b>
              </span>
            );
          })}
        </div>
      );
    case "planProposed":
      return (
        <RememberDetails name="details" className="think">
          <summary>
            计划 <Exp />
          </summary>
          <div className="ai">
            <Markdown text={k.value.text} />
          </div>
        </RememberDetails>
      );
    case "sessionState": {
      const t = eventTime(e);
      return (
        <div className="sep">
          {t
            ? `${new Date(t).getMonth() + 1}月${new Date(t).getDate()}日 ${hhmm(t)} `
            : ""}
          结束
        </div>
      );
    }
    case "sessionForked": {
      const t = eventTime(e);
      // The source as it is titled now, or, while it is not among the
      // pages of the listing held here, as it was titled at the fork. The
      // link asks for it by its ID.
      const src = remote.sessions.get(k.value.sourceSessionId);
      return (
        <div className="sep fork">
          <span>
            {t
              ? `${new Date(t).getMonth() + 1}月${new Date(t).getDate()}日 ${hhmm(t)} `
              : ""}
            分叉自
            {k.value.sourceSessionId ? (
              <a
                className="src"
                href={"#/s/" + encodeURIComponent(k.value.sourceSessionId)}
              >
                {src
                  ? titleOf(src)
                  : plain(k.value.sourceTitle) || "另一个会话"}
                <ChevronRight />
              </a>
            ) : (
              <span className="src">
                {plain(k.value.sourceTitle) || "另一个会话"}
              </span>
            )}
          </span>
        </div>
      );
    }
  }
  return null;
}

/** How the chat names who did what (devices.ts). */
interface Who {
  ctx: OriginContext;
}

/** Whether an origin is someone other than this client. */
function otherThanMe(o: Event["origin"], who: Who): boolean {
  const l = originLabel(o, who.ctx);
  return l !== undefined && l !== "本客户端" && l !== "";
}

/** The input's place when the session cannot take messages: ended, or the
 * computer is offline. */
function EndCard({
  remote,
  session,
  ended,
  machineOnline,
  agent,
  mname,
  lastSeen,
  scheduledClosed,
  idleClosed,
}: {
  remote: Remote;
  session: SessionView;
  ended: boolean;
  machineOnline: boolean;
  agent: Agent;
  mname: string;
  lastSeen: bigint;
  scheduledClosed: boolean;
  idleClosed: boolean;
}) {
  const [resuming, setResuming] = useState(false);
  const [error, setError] = useState("");
  const status = session.info?.status;
  const exited = status === SessionStatus.EXITED;
  useEffect(() => {
    if (status !== SessionStatus.EXITED) setResuming(false);
  }, [status]);
  if (!ended)
    return (
      <div className="endcard">
        <div className="h">
          <StateMark kind="off" />
          {mname} 不在线
        </div>
        <div className="d">
          {lastSeen ? `${ago(lastSeen)}断开，` : ""}可能是合盖睡眠、断网，或
          remote-agent
          没在运行。电脑上的会话可能还在跑，重新连上后这里会自动更新，现在只能看历史。
        </div>
      </div>
    );
  const resume = async () => {
    setResuming(true);
    setError("");
    try {
      await remote.command(session.id, {
        case: "resumeSession",
        value: { sessionId: session.id },
      });
    } catch (e) {
      setError(errorText(e));
      setResuming(false);
    }
  };
  if (resuming)
    return (
      <div className="endcard">
        <div className="h">
          <StateMark kind="run" />
          正在恢复…
        </div>
        <div className="d">
          在 {mname} 上重新打开 {agentName(agent)}
        </div>
      </div>
    );
  if (error)
    return (
      <div className="endcard">
        <div className="h">
          <StateMark kind="off" />
          恢复失败
        </div>
        <div className="d">{error}</div>
        <div className="btns">
          <button
            className="btn"
            onClick={() => void resume()}
            disabled={!machineOnline || remote.conn !== "online"}
          >
            再试一次
          </button>
        </div>
      </div>
    );
  return (
    <div className="endcard">
      <div className="h">
        <StateMark kind="off" />
        会话已结束
      </div>
      {remote.unlinked ? (
        <div className="d">连上服务器后才能恢复。</div>
      ) : machineOnline ? (
        <>
          <div className="d">
            {exitReason(
              session.info?.statusDetail ?? "",
              exited && scheduledClosed,
              exited && idleClosed,
            )}
            恢复会在 {mname}{" "}
            上用同样的设置重新打开它，聊天记录都在。
          </div>
          <div className="btns">
            <button
              className="btn pri"
              onClick={() => void resume()}
              disabled={remote.conn !== "online"}
            >
              恢复会话
            </button>
          </div>
        </>
      ) : (
        <div className="d">{mname} 不在线，连上后才能恢复。</div>
      )}
    </div>
  );
}
