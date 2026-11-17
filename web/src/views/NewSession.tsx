import { t } from "../i18n";
// Starting sessions. There is one way: type in a project's new-session
// box and send. Everything about config inheritance, model defaults,
// notices, and start attempt lifecycle lives in clientcore.

import { create } from "@bufbuild/protobuf";
import { Fragment, useEffect, useRef, useState, type ComponentProps, type ReactNode } from "react";
import { AgentMark } from "../agent-mark";
import {
  useCoreHost,
  useNewSessionView,
  useProjectView,
  type Go,
  type StartTraySnapshot,
} from "../core/host";
import { renderText } from "../core/text";
import {
  DirNavigateActionSchema,
  DirPickerOpenActionSchema,
  EventSchema,
  NewSessionOpenActionSchema,
  NewSessionProjectActionSchema,
  NewSessionSelectAgentActionSchema,
  NewSessionSelectMachineActionSchema,
  NewSessionSelectModelActionSchema,
  NewSessionSetPromptActionSchema,
  NewSessionSubmitActionSchema,
  ProjectAddActionSchema,
  ProjectKeyActionSchema,
  ProjectSetSearchActionSchema,
} from "../gen/remoteagent/core/v1/app_pb";
import {
  NoticeActionKind,
  NoticeKind,
  NoticeSeverity,
  type NewSessionNoticeView,
  type NewSessionViewModel,
} from "../gen/remoteagent/core/v1/new_session_view_pb";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { ArrowUp, Check, ChevronDown, ChevronRight, X } from "../icons";
import { useKeyboardDismiss } from "../keyboard";
import type { Remote } from "../remote";
import {
  anchorOf,
  Avatar,
  Banner,
  sendKeys,
  Sheet,
  StateMark,
  usePointerFine,
  useRemote,
} from "../ui";
import { AttachButton, attachBlocked, maxImages, TrayView, useDropPaste, useTray } from "./Attach";
import { useAutoGrow } from "./Composer";
import { ComposerPicker, keepComposerFocus } from "./ComposerPicker";
import { DiskFullCard } from "./DiskFullCard";
import { NewTaskModelPanel, NewTaskTools } from "./NewTaskModels";
import { DirPickerSheet, ProjectItem, ProjectList } from "./Projects";

export type { Go };

type PickLayer = "" | "machine" | "model";

function noticeBannerKind(sev: NoticeSeverity): "" | "warn" | "err" {
  switch (sev) {
    case NoticeSeverity.WARN:
      return "warn";
    case NoticeSeverity.ERROR:
      return "err";
    default:
      return "";
  }
}

function noticeActionLabel(n: NewSessionNoticeView): string | undefined {
  switch (n.action) {
    case NoticeActionKind.RETRY_START:
    case NoticeActionKind.RETRY_READ_SETTINGS:
      return t("common.retry");
    case NoticeActionKind.ABANDON_START:
      return t("new_session.abandon_start");
    case NoticeActionKind.CHANGE_MACHINE:
      return t("new_session.change_machine");
    case NoticeActionKind.CHANGE_MODEL:
      return t("new_session.change_model");
    case NoticeActionKind.OPEN_MACHINE_SETTINGS:
      return t("new_session.change_defaults");
    case NoticeActionKind.RETRY_SAVE_CONFIG:
      return t("new_session.retry_save");
    case NoticeActionKind.READD_PROJECT:
      return t("projects.add_again");
    default:
      return undefined;
  }
}

/** Pure ViewModel renderer for the new-session composer; used by NewBox and golden UI tests. */
export function NewBoxView({
  vm,
  remote,
  autoFocus,
  variant = "sheet",
  fixed,
  onPickProject,
  onClose,
  notice,
  initialConfigOpen = false,
  onDraft,
  onSelectMachine,
  onSelectAgent,
  onSelectModel,
  onRetryModels,
  onRetrySaveConfig,
  onSetPrompt,
  onSubmit,
  onRetryDiskFull,
  onAbandonStart,
  onOpenMachineSettings,
  onOpenDiskUsage,
  onReaddProject,
  onRegisterTray,
}: {
  vm: NewSessionViewModel;
  remote?: Remote;
  autoFocus?: boolean;
  variant?: "sheet" | "start";
  fixed?: boolean;
  onPickProject?: () => void;
  onClose?: () => void;
  notice?: ReactNode;
  initialConfigOpen?: boolean;
  onDraft?: () => void;
  onSelectMachine: (machineId: string) => void;
  onSelectAgent: (agent: Agent) => void;
  onSelectModel: (model: string, effort: string, standardContext?: boolean) => void;
  onRetryModels: () => void;
  onRetrySaveConfig: () => void;
  onSetPrompt: (prompt: string, attachmentCount: number, attachmentsMoved: boolean) => void;
  onSubmit: () => void;
  onRetryDiskFull?: () => void;
  onAbandonStart: () => void;
  onOpenMachineSettings: (machineId: string) => void;
  onOpenDiskUsage: (machineId: string) => void;
  onReaddProject: () => void;
  onRegisterTray?: (projectKey: string, getter: () => StartTraySnapshot) => () => void;
}) {
  const [config, setConfig] = useState<PickLayer>(initialConfigOpen ? "model" : "");
  const [choosing, setChoosing] = useState(false);
  const [toast, setToast] = useState("");
  const afterKeyboard = useKeyboardDismiss();
  const cliRef = useRef<HTMLButtonElement>(null);
  const composerRef = useRef<HTMLDivElement>(null);
  const boundaryRef = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);

  const phone = variant === "sheet";
  const machineId = vm.config?.machineId ?? "";
  const agent = vm.config?.agent ?? Agent.CODEX;
  const projectKey = vm.projectKey;
  const projectName = renderText(vm.projectName) || t("projects.unknown");

  const target = `${projectKey}/${machineId}/${agent}`;
  const previousTarget = useRef(target);
  useEffect(() => {
    if (!phone || previousTarget.current === target) return;
    previousTarget.current = target;
    setChoosing(false);
    setConfig("");
  }, [target, phone]);

  useAutoGrow(input, vm.prompt, 240);
  useEffect(() => {
    if (autoFocus) input.current?.focus();
  }, [autoFocus]);

  // Attachment tray stays in the Web shell (M6); only its count/move flags sync to Core.
  const machineObj = remote?.machines.get(machineId);
  const tray = remote
    ? // eslint-disable-next-line react-hooks/rules-of-hooks
      useTray(remote, "new:" + projectKey, machineId, maxImages(remote, machineObj))
    : undefined;
  const attachWhy = remote ? attachBlocked(remote, machineObj) : "";
  const drop = remote && tray
    ? // eslint-disable-next-line react-hooks/rules-of-hooks
      useDropPaste(tray, attachWhy, setToast)
    : { over: false, props: {} };

  if (tray) {
    tray.onAdd = () => onDraft?.();
  }

  const trayCount = tray?.items.length ?? 0;
  const lastTrayCount = useRef(trayCount);
  const lastMachine = useRef(machineId);
  useEffect(() => {
    const moved = lastMachine.current !== machineId && trayCount > 0;
    const countChanged = lastTrayCount.current !== trayCount;
    lastMachine.current = machineId;
    lastTrayCount.current = trayCount;
    if (moved || countChanged) {
      onSetPrompt(vm.prompt, trayCount, moved);
    }
  }, [machineId, trayCount]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!onRegisterTray || !tray || !projectKey) return;
    return onRegisterTray(projectKey, () => ({
      tray,
      imageIds: tray.imageIds(),
      fileIds: tray.fileIds(),
      items: [...tray.items],
    }));
  }, [onRegisterTray, tray, projectKey]);

  // Track frozen attempt inputs so editing prompt/config/attachments before retry abandons the stale request.
  const frozenSigRef = useRef<string | undefined>(undefined);
  const currentSig = JSON.stringify({
    prompt: vm.prompt.trim(),
    machineId,
    agent,
    model: vm.config?.model ?? "",
    effort: vm.config?.effort ?? "",
    standardContext: !!vm.config?.standardContext,
    tray: tray?.items.map((i) => `${i.key}:${i.prepared ?? ""}`) ?? [],
  });
  useEffect(() => {
    if (!vm.busy && !vm.frozen) {
      frozenSigRef.current = undefined;
    } else if (frozenSigRef.current === undefined) {
      frozenSigRef.current = currentSig;
    }
  }, [vm.busy, vm.frozen, currentSig]);
  const changedSinceFrozen =
    vm.frozen && frozenSigRef.current !== undefined && frozenSigRef.current !== currentSig;

  const submit = () => {
    if (changedSinceFrozen) {
      frozenSigRef.current = currentSig;
      onAbandonStart();
    } else if (!vm.canStart || !vm.settled) {
      return;
    }
    if (tray) {
      tray.refreshExpired();
      if (tray.items.length ? !tray.ready() : !vm.prompt.trim()) return;
    } else if (!vm.prompt.trim()) {
      return;
    }
    onSubmit();
  };

  const disabled =
    vm.busy ||
    !vm.settled ||
    (!changedSinceFrozen && !vm.canStart) ||
    (tray ? (trayCount ? !tray.ready() : !vm.prompt.trim()) : !vm.prompt.trim());

  const openModelPanel = () => {
    if (phone) setChoosing(false);
    setConfig((c) => {
      const next = phone && c === "model" ? "" : "model";
      if (
        next === "model" &&
        (vm.modelPanel?.modelsError ||
          (!vm.modelPanel?.modelsLoading && (vm.modelPanel?.models.length ?? 0) === 0))
      ) {
        onRetryModels();
      }
      return next;
    });
  };

  const keys = sendKeys(usePointerFine());

  const trayView = remote && tray && (
    <TrayView
      tray={tray}
      remote={remote}
      note={
        <>
          {vm.notices.some((n) => n.kind === NoticeKind.ATTACHMENTS_MOVED) &&
            tray.busy() && (
              <Banner>
                {renderText(
                  vm.notices.find((n) => n.kind === NoticeKind.ATTACHMENTS_MOVED)!.text,
                )}
              </Banner>
            )}
          {toast && (
            <Banner action={t("common.got_it")} onAction={() => setToast("")}>
              {toast}
            </Banner>
          )}
        </>
      }
    />
  );

  const field = (
    <textarea
      ref={input}
      className={"ask" + (vm.prompt ? " typed" : "")}
      rows={1}
      value={vm.prompt}
      placeholder={t("new_session.prompt")}
      onChange={(e) => {
        const val = e.target.value;
        if (!vm.prompt && val) onDraft?.();
        if (remote && projectKey) {
          remote.drafts.set("new:" + projectKey, val);
          remote.touch();
        }
        onSetPrompt(val, trayCount, false);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
          e.preventDefault();
          submit();
        }
      }}
      aria-label={t("new_session.first_message")}
    />
  );

  const busyNotice = vm.notices.find((n) => n.kind === NoticeKind.STARTING);
  const inlineNotices = vm.notices.filter(
    (n) =>
      n.kind !== NoticeKind.STARTING &&
      n.kind !== NoticeKind.ATTACHMENTS_MOVED &&
      !(n.kind === NoticeKind.PROJECT_REMOVED && notice),
  );

  const renderNotice = (n: NewSessionNoticeView, idx: number) => {
    if (n.kind === NoticeKind.OFFLINE_DRAFT_CHOICES && n.hasDraftChoices) {
      return (
        <div key={`n-${n.kind}-${idx}`} className="offline-draft">
          <Banner
            kind={noticeBannerKind(n.severity)}
            action={vm.machineSelectable ? t("new_session.change_machine") : undefined}
            onAction={vm.machineSelectable ? () => setConfig("machine") : undefined}
          >
            <b>{renderText(n.draftTitle) || renderText(n.text)}</b>
            {n.draftSubtitle && <div>{renderText(n.draftSubtitle)}</div>}
          </Banner>
          {n.draftChoices.length > 0 ? (
            <div className="list">
              {n.draftChoices.map((c) => (
                <button
                  key={c.machineId}
                  type="button"
                  className="mi"
                  onClick={() => onSelectMachine(c.machineId)}
                >
                  <div className="mt">
                    <b>{renderText(c.title)}</b>
                    {c.note && <div className="md">{renderText(c.note)}</div>}
                  </div>
                </button>
              ))}
            </div>
          ) : (
            n.draftNoneText && (
              <Banner
                kind={noticeBannerKind(n.severity)}
                action={vm.machineSelectable ? t("new_session.change_machine") : undefined}
                onAction={vm.machineSelectable ? () => setConfig("machine") : undefined}
              >
                {renderText(n.draftNoneText)}
              </Banner>
            )
          )}
        </div>
      );
    }
    if (n.kind === NoticeKind.DISK_FULL) {
      return (
        <DiskFullCard
          key={`disk-${idx}`}
          onUsage={() => onOpenDiskUsage(n.targetMachineId || machineId)}
          onRetry={onRetryDiskFull ?? submit}
        />
      );
    }
    const label = noticeActionLabel(n);
    const runAction = () => {
      switch (n.action) {
        case NoticeActionKind.RETRY_START:
          submit();
          break;
        case NoticeActionKind.ABANDON_START:
          onAbandonStart();
          break;
        case NoticeActionKind.CHANGE_MACHINE:
          setConfig("machine");
          break;
        case NoticeActionKind.CHANGE_MODEL:
          setChoosing(false);
          setConfig("model");
          onRetryModels();
          break;
        case NoticeActionKind.OPEN_MACHINE_SETTINGS:
          onOpenMachineSettings(n.targetMachineId || machineId);
          break;
        case NoticeActionKind.RETRY_SAVE_CONFIG:
          onRetrySaveConfig();
          break;
        case NoticeActionKind.READD_PROJECT:
          onReaddProject();
          break;
        case NoticeActionKind.RETRY_READ_SETTINGS:
          onRetryModels();
          break;
      }
    };
    const banner = (
      <Banner
        key={`n-${n.kind}-${idx}`}
        kind={noticeBannerKind(n.severity)}
        action={label}
        onAction={label ? runAction : undefined}
      >
        {renderText(n.text)}
      </Banner>
    );
    if (n.kind === NoticeKind.AUTO_UNAVAILABLE && n.action === NoticeActionKind.CHANGE_MODEL) {
      return (
        <div
          key={`n-${n.kind}-${idx}`}
          style={{ display: "contents" }}
          data-composer-picker={phone ? "model" : undefined}
          onPointerDown={phone ? keepComposerFocus : undefined}
          onMouseDown={phone ? keepComposerFocus : undefined}
        >
          {banner}
        </div>
      );
    }
    return banner;
  };

  const banners = (notice || inlineNotices.length > 0) && (
    <div className="nt-banners">
      {notice}
      {inlineNotices.map(renderNotice)}
    </div>
  );

  const send = (
    <button
      className={"snd" + (disabled ? " off" : "")}
      disabled={disabled}
      onClick={submit}
      aria-label={t("new_session.start")}
    >
      <ArrowUp />
    </button>
  );

  const many = vm.availableAgents.length > 1;
  const cli = many && (
    <button
      ref={cliRef}
      className="tok cli"
      disabled={vm.busy}
      aria-haspopup="dialog"
      aria-label={t("new_session.cli_label", { agent: vm.agentName })}
      aria-expanded={choosing}
      data-composer-picker={phone ? "cli" : undefined}
      onPointerDown={phone ? keepComposerFocus : undefined}
      onMouseDown={phone ? keepComposerFocus : undefined}
      onClick={() => {
        if (phone) setConfig("");
        setChoosing((v) => (phone ? !v : true));
      }}
    >
      <AgentMark agent={agent} named />
      <span className="cli-name">{vm.agentName}</span>
      <ChevronDown />
    </button>
  );

  const mach = vm.machineSelectable && vm.machineName && (
    <button
      className="tok m"
      disabled={vm.busy}
      aria-haspopup="dialog"
      aria-label={t("new_session.machine_label", { name: vm.machineName })}
      onClick={() => afterKeyboard(() => setConfig("machine"))}
    >
      <span>{vm.machineName}</span>
      <ChevronDown />
    </button>
  );

  const proj =
    fixed || !onPickProject ? (
      <b>{projectName}</b>
    ) : (
      <button
        className="tok p"
        onClick={() => afterKeyboard(onPickProject)}
        aria-label={t("new_session.project_label", { name: projectName })}
      >
        <Avatar name={projectName} size="sm" />
        <span>{projectName}</span>
        <ChevronDown />
      </button>
    );

  const locationTemplate = vm.machineSelectable
    ? many
      ? t("new_session.location_machine_cli", {
          machine: "{machine}",
          project: "{project}",
          cli: "{cli}",
        })
      : t("new_session.location_machine", { machine: "{machine}", project: "{project}" })
    : many
      ? t("new_session.location_cli", { project: "{project}", cli: "{cli}" })
      : t("new_session.location", { project: "{project}" });
  const slots: Record<string, ReactNode> = { machine: mach, project: proj, cli };
  const sentence = (
    <div className="sent">
      {locationTemplate
        .split(/(?<=\})/)
        .filter(Boolean)
        .map((part, i) => {
          const pieces = part.split(/\{(machine|project|cli)\}/);
          return (
            <span className="g" key={i}>
              {pieces.map((piece, j) => (
                <Fragment key={j}>{j % 2 ? slots[piece] : piece}</Fragment>
              ))}
            </span>
          );
        })}
    </div>
  );

  const busyNote = busyNotice && <div className="nt-note">{renderText(busyNotice.text)}</div>;
  const effortChoicesCount = vm.modelPanel?.effortChoices.length ?? 0;
  const summaryEffort =
    effortChoicesCount > 1
      ? vm.config?.effort
        ? vm.effortLabel
        : t("models.default")
      : "";
  const summary = (
    <ConfigSummary
      summary={{
        agent: vm.agentName,
        model: renderText(vm.modelLabel) || t("models.default"),
        effort: summaryEffort,
      }}
      bare={many}
      onClick={openModelPanel}
      data-composer-picker={phone ? "model" : undefined}
      aria-expanded={config === "model"}
      onPointerDown={phone ? keepComposerFocus : undefined}
      onMouseDown={phone ? keepComposerFocus : undefined}
    />
  );

  const attach = remote && tray && (
    <AttachButton tray={tray} blocked={attachWhy} onBlocked={setToast} />
  );

  const body =
    variant === "sheet" ? (
      <div
        ref={boundaryRef}
        className={"ntask" + (drop.over ? " drop" : "")}
        {...drop.props}
        role="group"
        aria-label={t("new_session.project_session_label", { name: projectName })}
      >
        <header className="nt-hd">
          <button className="fbtn" aria-label={t("common.close")} onClick={onClose}>
            <X />
          </button>
          <div className="nt-where">
            {proj}
            {mach}
          </div>
          <span className="nt-pad" />
        </header>
        <div className="nt-body">
          {banners}
          {busyNote}
        </div>
        <div className="pbox" ref={composerRef}>
          {field}
          {trayView}
          <div className="acc">
            {attach}
            <NewTaskTools>
              {cli}
              {summary}
            </NewTaskTools>
            {send}
          </div>
        </div>
      </div>
    ) : (
      <div
        className={"wstart" + (drop.over ? " drop" : "")}
        {...drop.props}
        role="group"
        aria-label={t("new_session.project_session_label", { name: projectName })}
      >
        {banners}
        {sentence}
        <div className="wbox">
          {field}
          {trayView}
          <div className="acc">
            {attach}
            {summary}
            {keys && <span className="kh">{t("new_session.start_shortcut", { keys })}</span>}
            {send}
          </div>
        </div>
        {busyNote}
      </div>
    );

  const cliChoices = (
    <div className="list" role="menu">
      {vm.availableAgents.map((a) => (
        <button
          key={a.agent}
          className="mi"
          role="menuitemradio"
          aria-checked={a.selected}
          onClick={() => {
            setChoosing(false);
            if (!a.selected) onSelectAgent(a.agent);
          }}
        >
          <span className="mt">
            <AgentMark agent={a.agent} named />
            {a.displayName}
          </span>
          {a.selected && (
            <span className="ck">
              <Check />
            </span>
          )}
        </button>
      ))}
    </div>
  );

  const machineChoices = vm.modelPanel?.machineChoices ?? [];
  const machineSheet = config === "machine" && (
    <Sheet
      onClose={() => setConfig("")}
      title={t("common.machine")}
      action={{ label: t("common.done"), onClick: () => setConfig("") }}
      anchor={anchorOf(".wstart .sent .tok.m")}
      className="with-head"
    >
      <div className="list">
        {machineChoices.map((m) => (
          <button
            key={m.machineId}
            className={"mi" + (m.online ? "" : " dis")}
            disabled={!m.online}
            onClick={() => {
              onSelectMachine(m.machineId);
              setConfig("");
            }}
          >
            <StateMark kind={m.online ? "online" : "off"} />
            <div className="mt">
              {m.selected || !m.online ? (
                m.name
              ) : (
                <b>{t("new_session.use_machine", { name: m.name })}</b>
              )}
              {m.note && <div className="md">{renderText(m.note)}</div>}
            </div>
            {m.selected && (
              <span className="ck">
                <Check />
              </span>
            )}
          </button>
        ))}
      </div>
    </Sheet>
  );

  const modelPanel = vm.modelPanel && (
    <NewTaskModelPanel
      key={`${machineId}/${agent}`}
      panel={vm.modelPanel}
      selectedModel={vm.config?.model ?? ""}
      configEffort={vm.config?.effort ?? ""}
      settled={vm.settled}
      onSelectModel={onSelectModel}
      onRetryModels={onRetryModels}
      onClose={() => setConfig("")}
    />
  );

  const modelLayer =
    config === "model" &&
    modelPanel &&
    (phone ? (
      <ComposerPicker
        anchor={composerRef}
        boundary={boundaryRef}
        title={t("models.title")}
        onClose={() => setConfig("")}
      >
        {modelPanel}
      </ComposerPicker>
    ) : (
      <Sheet
        onClose={() => setConfig("")}
        anchor={anchorOf(".wstart .acc .project-summary")}
        className="with-head new-task-model-sheet"
      >
        {modelPanel}
      </Sheet>
    ));

  return (
    <>
      {body}
      {choosing &&
        (phone ? (
          <ComposerPicker
            anchor={cliRef}
            boundary={boundaryRef}
            onClose={() => setChoosing(false)}
          >
            {cliChoices}
          </ComposerPicker>
        ) : (
          <Sheet onClose={() => setChoosing(false)} title={t("new_session.choose_cli")} anchor={cliRef}>
            {cliChoices}
          </Sheet>
        ))}
      {machineSheet}
      {modelLayer}
    </>
  );
}

/** Connected new-session box backed by CoreHost. */
export function NewBox({
  remote,
  projectKey,
  go,
  autoFocus,
  variant = "sheet",
  fixed,
  onPickProject,
  onClose,
  notice,
  settings = false,
  onDraft,
}: {
  remote: Remote;
  projectKey: string;
  go: Go;
  autoFocus?: boolean;
  variant?: "sheet" | "start";
  fixed?: boolean;
  onPickProject?: () => void;
  onClose?: () => void;
  notice?: ReactNode;
  settings?: boolean;
  onDraft?: () => void;
}) {
  useRemote(remote);
  const host = useCoreHost(remote, go);
  const vm = useNewSessionView(host, projectKey);
  const activeKey = vm.projectKey || projectKey;

  useEffect(() => {
    host.dispatch(
      create(EventSchema, {
        event: {
          case: "newSessionOpen",
          value: create(NewSessionOpenActionSchema, { projectKey }),
        },
      }),
    );
  }, [host, projectKey]);

  useEffect(() => {
    if (!vm.settled && activeKey && remote.moreSessions) {
      void remote.loadMoreSessions();
    }
  }, [vm.settled, activeKey, remote, remote.moreSessions]);

  // Restore any persisted browser draft for this project if Core's draft is still empty.
  useEffect(() => {
    if (!projectKey || vm.busy || vm.frozen) return;
    const saved = remote.drafts.get("new:" + projectKey);
    if (saved && vm.prompt === "") {
      host.dispatch(
        create(EventSchema, {
          event: {
            case: "newSessionSetPrompt",
            value: create(NewSessionSetPromptActionSchema, {
              prompt: saved,
              attachmentCount: 0,
              attachmentsMoved: false,
            }),
          },
        }),
      );
    }
  }, [host, remote, projectKey]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <NewBoxView
      key={activeKey}
      vm={vm}
      remote={remote}
      autoFocus={autoFocus}
      variant={variant}
      fixed={fixed}
      onPickProject={onPickProject}
      onClose={onClose}
      notice={notice}
      initialConfigOpen={settings}
      onDraft={onDraft}
      onSelectMachine={(machineId) =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "newSessionSelectMachine",
              value: create(NewSessionSelectMachineActionSchema, { machineId }),
            },
          }),
        )
      }
      onSelectAgent={(agent) =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "newSessionSelectAgent",
              value: create(NewSessionSelectAgentActionSchema, { agent }),
            },
          }),
        )
      }
      onSelectModel={(model, effort, standardContext) =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "newSessionSelectModel",
              value: create(NewSessionSelectModelActionSchema, {
                model,
                effort,
                ...(standardContext !== undefined ? { standardContext } : {}),
              }),
            },
          }),
        )
      }
      onRetryModels={() =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "newSessionRetryModels",
              value: create(NewSessionProjectActionSchema, { projectKey: activeKey }),
            },
          }),
        )
      }
      onRetrySaveConfig={() =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "newSessionRetrySaveConfig",
              value: create(NewSessionProjectActionSchema, { projectKey: activeKey }),
            },
          }),
        )
      }
      onSetPrompt={(prompt, attachmentCount, attachmentsMoved) =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "newSessionSetPrompt",
              value: create(NewSessionSetPromptActionSchema, {
                prompt,
                attachmentCount,
                attachmentsMoved,
              }),
            },
          }),
        )
      }
      onSubmit={() =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "newSessionSubmit",
              value: create(NewSessionSubmitActionSchema, { projectKey: activeKey }),
            },
          }),
        )
      }
      onRetryDiskFull={() => {
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "newSessionSubmit",
              value: create(NewSessionSubmitActionSchema, { projectKey: activeKey }),
            },
          }),
        );
      }}
      onAbandonStart={() =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "newSessionAbandonStart",
              value: create(NewSessionProjectActionSchema, { projectKey: activeKey }),
            },
          }),
        )
      }
      onOpenMachineSettings={(machineId) => go("m/" + machineId)}
      onOpenDiskUsage={(machineId) => go("machine-status/" + encodeURIComponent(machineId))}
      onReaddProject={() =>
        host.dispatch(
          create(EventSchema, {
            event: {
              case: "projectToggleHide",
              value: create(ProjectKeyActionSchema, { projectKey: activeKey }),
            },
          }),
        )
      }
      onRegisterTray={(k, getter) => host.registerTray(k, getter)}
    />
  );
}

/** 选项目: the project list rendered from Core's ProjectListingView, 新项目… at the end. */
export function ProjectPicker({
  remote,
  onPick,
  onNew,
  onClose,
  title = t("projects.choose"),
}: {
  remote: Remote;
  onPick: (key: string) => void;
  onNew: () => void;
  onClose: () => void;
  title?: string;
}) {
  const host = useCoreHost(remote);
  const manage = useProjectView(host, "", true).manage;
  return (
    <Sheet
      onClose={onClose}
      title={title}
      action={{ label: t("common.cancel"), onClick: onClose }}
      anchor={anchorOf(".wstart .sent .tok.p")}
    >
      {manage?.listing && (
        <ProjectList
          listing={manage.listing}
          onSearch={(query) =>
            host.dispatch(
              create(EventSchema, {
                event: {
                  case: "projectSetSearch",
                  value: create(ProjectSetSearchActionSchema, { query }),
                },
              }),
            )
          }
          onNew={onNew}
          item={(p) => (
            <ProjectItem
              key={p.key}
              p={p}
              onClick={() => onPick(p.key)}
              end={<ChevronRight className="chev" />}
            />
          )}
        />
      )}
    </Sheet>
  );
}

/** The ＋ flow: pick a project, or add a folder via Core's DirPickerView; then open its new-session box. */
export function NewFlow({
  remote,
  go,
  onClose,
  onPicked,
  start,
}: {
  remote: Remote;
  go: Go;
  onClose: () => void;
  onPicked?: (key: string) => void;
  start?: "new";
}) {
  const host = useCoreHost(remote, go);
  const manage = useProjectView(host, "", true).manage;
  const hasProjects = (manage?.projectCount ?? 0) > 0;
  const pick = hasProjects && !start;
  const [step, setStep] = useState<"project" | "new">(pick ? "project" : "new");

  useEffect(() => {
    if (step !== "new") return;
    host.dispatch(
      create(EventSchema, {
        event: {
          case: "projectOpenDirPicker",
          value: create(DirPickerOpenActionSchema, { forNewTask: true }),
        },
      }),
    );
    return () => {
      host.dispatch(
        create(EventSchema, { event: { case: "projectCloseDirPicker", value: {} } }),
      );
    };
  }, [host, step]);

  const open = (key: string) => {
    onClose();
    if (onPicked) {
      onPicked(key);
      return;
    }
    remote.focusProject = key;
    go("p/" + encodeURIComponent(key));
  };

  if (step === "new" && manage?.dirPicker) {
    return (
      <DirPickerSheet
        picker={manage.dirPicker}
        onBack={pick ? () => setStep("project") : undefined}
        onClose={onClose}
        onNavigate={(machineId, path) =>
          host.dispatch(
            create(EventSchema, {
              event: {
                case: "projectDirNavigate",
                value: create(DirNavigateActionSchema, { machineId, path }),
              },
            }),
          )
        }
        onConfirm={(path) => {
          if (manage.dirPicker?.machineId) {
            remote.draftMachines.set(path, manage.dirPicker.machineId);
          }
          host.dispatch(
            create(EventSchema, {
              event: {
                case: "projectAdd",
                value: create(ProjectAddActionSchema, { path }),
              },
            }),
          );
          open(path);
        }}
      />
    );
  }
  return (
    <ProjectPicker
      remote={remote}
      onPick={open}
      onNew={() => setStep("new")}
      onClose={onClose}
    />
  );
}

/** The model can shorten; the CLI prefix and effective effort stay whole. */
export function ConfigSummary({
  summary,
  bare,
  ...button
}: {
  summary: { agent: string; model: string; effort?: string };
  bare?: boolean;
} & ComponentProps<"button">) {
  const { agent, model, effort } = summary;
  return (
    <button
      className="tok project-summary"
      aria-label={
        effort
          ? t("models.summary_effort_label", { model, effort })
          : t("models.summary_label", { model })
      }
      {...button}
    >
      {!bare && <span className="fx">{agent} · </span>}
      <span className="mdl">{model}</span>
      {effort && <span className="effort"> · {effort}</span>}
      <ChevronRight />
    </button>
  );
}
