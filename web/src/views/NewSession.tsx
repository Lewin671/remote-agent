import { t } from "../i18n";
// Starting sessions backed by clientcore's NewSessionViewModel.

import { Fragment, useEffect, useLayoutEffect, useMemo, useRef, useState, type ComponentProps, type ReactNode } from "react";
import { AgentMark } from "../agent-mark";
import type { AttachmentExecutor } from "../attachment-executor";
import {
  newSessionDraftScope,
  routeForMachineCli,
  routeForMachineSettings,
  useAttachmentDraftView,
  useCoreHost,
  useNewSessionView,
  useProjectView,
  type DraftCandidateEntry,
  type Go,
} from "../core/host";
import { renderText } from "../core/text";
import type { AttachmentDraftView } from "../gen/remoteagent/core/v1/attachments_pb";
import { NoticeActionKind, NoticeKind, NoticeSeverity, type NewSessionNoticeView, type NewSessionViewModel } from "../gen/remoteagent/core/v1/new_session_view_pb";
import { Agent } from "../gen/remoteagent/v1/event_pb";
import { ArrowUp, Check, ChevronDown, ChevronRight, X } from "../icons";
import { useKeyboardDismiss } from "../keyboard";
import type { Remote } from "../remote";
import { anchorOf, Avatar, Banner, sendKeys, Sheet, StateMark, usePointerFine } from "../ui";
import { AttachButton, TrayView, useDropPaste } from "./Attach";
import { useAutoGrow } from "./Composer";
import { ComposerPicker, keepComposerFocus } from "./ComposerPicker";
import { DiskFullCard } from "./DiskFullCard";
import { NewTaskModelPanel, NewTaskTools } from "./NewTaskModels";
import { DirPickerSheet, ProjectItem, ProjectList } from "./Projects";

export type { Go };
type PickLayer = "" | "machine" | "model";

const NOTICE_KIND: Record<NoticeSeverity, "" | "warn" | "err"> = {
  [NoticeSeverity.UNSPECIFIED]: "",
  [NoticeSeverity.INFO]: "",
  [NoticeSeverity.WARN]: "warn",
  [NoticeSeverity.ERROR]: "err",
};

const NOTICE_ACTION_LABEL: Partial<Record<NoticeActionKind, () => string>> = {
  [NoticeActionKind.RETRY_START]: () => t("common.retry"),
  [NoticeActionKind.RETRY_READ_SETTINGS]: () => t("common.retry"),
  [NoticeActionKind.ABANDON_START]: () => t("new_session.abandon_start"),
  [NoticeActionKind.CHANGE_MACHINE]: () => t("new_session.change_machine"),
  [NoticeActionKind.CHANGE_MODEL]: () => t("new_session.change_model"),
  [NoticeActionKind.OPEN_MACHINE_SETTINGS]: () => t("new_session.change_defaults"),
  [NoticeActionKind.ENABLE_AGENT]: () => t("new_session.enable_agent"),
  [NoticeActionKind.RETRY_SAVE_CONFIG]: () => t("new_session.retry_save"),
  [NoticeActionKind.READD_PROJECT]: () => t("projects.add_again"),
};

/** Pure ViewModel renderer for the new-session composer; used by NewBox and golden UI tests. */
export function NewBoxView(p: {
  vm: NewSessionViewModel;
  draft?: AttachmentDraftView;
  attachments?: AttachmentExecutor;
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
  onSetPrompt: (prompt: string) => void;
  onSubmit: () => void;
  onRetryDiskFull?: () => void;
  onAbandonStart: () => void;
  onOpenMachineSettings: (machineId: string, agent?: Agent) => void;
  onOpenDiskUsage: (machineId: string) => void;
  onReaddProject: () => void;
  onPickFiles?: (entries: DraftCandidateEntry[], forceFile: boolean) => void;
  onRemoveAttachment?: (itemId: string) => void;
  onRetryAttachment?: (itemId: string) => void;
  onPauseAttachment?: (itemId: string) => void;
  onResumeAttachment?: (itemId: string) => void;
  onSetOriginal?: (original: boolean) => void;
  onOpenAttachmentViewer?: (index: number) => void;
  onNavigateAttachmentViewer?: (delta: number) => void;
  onCloseAttachmentViewer?: () => void;
}) {
  const { vm, variant = "sheet", onDraft, onSetPrompt, onSubmit } = p;
  const [config, setConfig] = useState<PickLayer>(p.initialConfigOpen ? "model" : "");
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
  const activeDraft = p.draft ?? vm.draft;
  const prompt = activeDraft ? activeDraft.text : vm.prompt;

  const target = `${projectKey}/${machineId}/${agent}`;
  const previousTarget = useRef(target);
  useEffect(() => {
    if (!phone || previousTarget.current === target) return;
    previousTarget.current = target;
    setChoosing(false);
    setConfig("");
  }, [target, phone]);

  useAutoGrow(input, prompt, 240);
  useEffect(() => { if (p.autoFocus) input.current?.focus(); }, [p.autoFocus]);

  const attachWhy = renderText(vm.imageUploadBlockedReason);
  const handlePickFiles = (entries: DraftCandidateEntry[], forceFile: boolean) => {
    onDraft?.();
    p.onPickFiles?.(entries, forceFile);
  };
  const dropHook = useDropPaste(handlePickFiles, attachWhy, setToast);
  const drop = p.onPickFiles ? dropHook : { over: false, props: {} };

  const frozenSigRef = useRef<string | undefined>(undefined);
  const currentSig = JSON.stringify({
    prompt: prompt.trim(),
    machineId,
    agent,
    model: vm.config?.model ?? "",
    effort: vm.config?.effort ?? "",
    standardContext: !!vm.config?.standardContext,
    items: activeDraft?.items.map((i) => `${i.itemId}:${i.state}`) ?? [],
  });
  useEffect(() => {
    if (!vm.busy && !vm.frozen) frozenSigRef.current = undefined;
    else if (frozenSigRef.current === undefined) frozenSigRef.current = currentSig;
  }, [vm.busy, vm.frozen, currentSig]);
  const changedSinceFrozen = vm.frozen && frozenSigRef.current !== undefined && frozenSigRef.current !== currentSig;

  const draftReady = activeDraft
    ? activeDraft.canSend && !activeDraft.sending && !activeDraft.restoring
    : !!prompt.trim();

  const submit = () => {
    if (changedSinceFrozen) {
      frozenSigRef.current = currentSig;
      p.onAbandonStart();
    } else if (!vm.canStart || !vm.settled) return;
    if (!draftReady) return;
    onSubmit();
  };

  const disabled = vm.busy || !vm.settled || (!changedSinceFrozen && !vm.canStart) || !draftReady;
  const openModelPanel = () => {
    if (phone) setChoosing(false);
    setConfig((c) => {
      const next = phone && c === "model" ? "" : "model";
      if (next === "model" && (vm.modelPanel?.modelsError || (!vm.modelPanel?.modelsLoading && !vm.modelPanel?.models.length))) p.onRetryModels();
      return next;
    });
  };
  const keys = sendKeys(usePointerFine());

  const movedNotice = vm.notices.find((n) => n.kind === NoticeKind.ATTACHMENTS_MOVED);
  const trayView = (activeDraft || toast || movedNotice) && (
    <TrayView
      draft={activeDraft}
      attachments={p.attachments}
      onRemove={p.onRemoveAttachment}
      onRetry={p.onRetryAttachment}
      onPause={p.onPauseAttachment}
      onResume={p.onResumeAttachment}
      onSetOriginal={p.onSetOriginal}
      onOpenViewer={p.onOpenAttachmentViewer}
      onNavigateViewer={p.onNavigateAttachmentViewer}
      onCloseViewer={p.onCloseAttachmentViewer}
      note={
        <>
          {movedNotice && activeDraft?.busy && (
            <Banner>{renderText(movedNotice.text)}</Banner>
          )}
          {toast && <Banner action={t("common.got_it")} onAction={() => setToast("")}>{toast}</Banner>}
        </>
      }
    />
  );

  const field = (
    <textarea
      ref={input}
      className={"ask" + (prompt ? " typed" : "")}
      rows={1}
      value={prompt}
      placeholder={t("new_session.prompt")}
      onChange={(e) => {
        const val = e.target.value;
        if (!prompt && val) onDraft?.();
        onSetPrompt(val);
      }}
      onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) { e.preventDefault(); submit(); } }}
      aria-label={t("new_session.first_message")}
    />
  );

  const busyNotice = vm.notices.find((n) => n.kind === NoticeKind.STARTING);
  const inlineNotices = vm.notices.filter((n) => n.kind !== NoticeKind.STARTING && n.kind !== NoticeKind.ATTACHMENTS_MOVED && !(n.kind === NoticeKind.PROJECT_REMOVED && p.notice));

  const renderNotice = (n: NewSessionNoticeView, idx: number) => {
    if (n.kind === NoticeKind.OFFLINE_DRAFT_CHOICES && n.hasDraftChoices) {
      return (
        <div key={`n-${n.kind}-${idx}`} className="offline-draft">
          <Banner kind={NOTICE_KIND[n.severity]} action={vm.machineSelectable ? t("new_session.change_machine") : undefined} onAction={vm.machineSelectable ? () => setConfig("machine") : undefined}>
            <b>{renderText(n.draftTitle) || renderText(n.text)}</b>
            {n.draftSubtitle && <div>{renderText(n.draftSubtitle)}</div>}
          </Banner>
          {n.draftChoices.length > 0 ? (
            <div className="list">
              {n.draftChoices.map((c) => (
                <button key={c.machineId} type="button" className="mi" onClick={() => p.onSelectMachine(c.machineId)}>
                  <div className="mt"><b>{renderText(c.title)}</b>{c.note && <div className="md">{renderText(c.note)}</div>}</div>
                </button>
              ))}
            </div>
          ) : n.draftNoneText && (
            <Banner kind={NOTICE_KIND[n.severity]} action={vm.machineSelectable ? t("new_session.change_machine") : undefined} onAction={vm.machineSelectable ? () => setConfig("machine") : undefined}>
              {renderText(n.draftNoneText)}
            </Banner>
          )}
        </div>
      );
    }
    if (n.kind === NoticeKind.DISK_FULL) {
      return <DiskFullCard key={`disk-${idx}`} onUsage={() => p.onOpenDiskUsage(n.targetMachineId || machineId)} onRetry={p.onRetryDiskFull ?? submit} />;
    }
    const label = NOTICE_ACTION_LABEL[n.action]?.();
    const runAction = () => {
      switch (n.action) {
        case NoticeActionKind.RETRY_START: submit(); break;
        case NoticeActionKind.ABANDON_START: p.onAbandonStart(); break;
        case NoticeActionKind.CHANGE_MACHINE: setConfig("machine"); break;
        case NoticeActionKind.CHANGE_MODEL: setChoosing(false); setConfig("model"); p.onRetryModels(); break;
        case NoticeActionKind.OPEN_MACHINE_SETTINGS: p.onOpenMachineSettings(n.targetMachineId || machineId); break;
        case NoticeActionKind.ENABLE_AGENT: p.onOpenMachineSettings(n.targetMachineId || machineId, n.targetAgent || undefined); break;
        case NoticeActionKind.RETRY_SAVE_CONFIG: p.onRetrySaveConfig(); break;
        case NoticeActionKind.READD_PROJECT: p.onReaddProject(); break;
        case NoticeActionKind.RETRY_READ_SETTINGS: p.onRetryModels(); break;
      }
    };
    const banner = <Banner key={`n-${n.kind}-${idx}`} kind={NOTICE_KIND[n.severity]} action={label} onAction={label ? runAction : undefined}>{renderText(n.text)}</Banner>;
    if (n.kind === NoticeKind.AUTO_UNAVAILABLE && n.action === NoticeActionKind.CHANGE_MODEL) {
      return (
        <div key={`n-${n.kind}-${idx}`} style={{ display: "contents" }} data-composer-picker={phone ? "model" : undefined} onPointerDown={phone ? keepComposerFocus : undefined} onMouseDown={phone ? keepComposerFocus : undefined}>
          {banner}
        </div>
      );
    }
    return banner;
  };

  const banners = (p.notice || inlineNotices.length > 0) && <div className="nt-banners">{p.notice}{inlineNotices.map(renderNotice)}</div>;
  const send = <button className={"snd" + (disabled ? " off" : "")} disabled={disabled} onClick={submit} aria-label={t("new_session.start")}><ArrowUp /></button>;
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
      onClick={() => { if (phone) setConfig(""); setChoosing((v) => (phone ? !v : true)); }}
    >
      <AgentMark agent={agent} named /><span className="cli-name">{vm.agentName}</span><ChevronDown />
    </button>
  );
  const mach = vm.machineSelectable && vm.machineName && (
    <button className="tok m" disabled={vm.busy} aria-haspopup="dialog" aria-label={t("new_session.machine_label", { name: vm.machineName })} onClick={() => afterKeyboard(() => setConfig("machine"))}>
      <span>{vm.machineName}</span><ChevronDown />
    </button>
  );
  const proj = p.fixed || !p.onPickProject ? <b>{projectName}</b> : (
    <button className="tok p" onClick={() => afterKeyboard(p.onPickProject!)} aria-label={t("new_session.project_label", { name: projectName })}>
      <Avatar name={projectName} size="sm" /><span>{projectName}</span><ChevronDown />
    </button>
  );
  const locationTemplate = vm.machineSelectable
    ? many ? t("new_session.location_machine_cli", { machine: "{machine}", project: "{project}", cli: "{cli}" }) : t("new_session.location_machine", { machine: "{machine}", project: "{project}" })
    : many ? t("new_session.location_cli", { project: "{project}", cli: "{cli}" }) : t("new_session.location", { project: "{project}" });
  const slots: Record<string, ReactNode> = { machine: mach, project: proj, cli };
  const sentence = (
    <div className="sent">
      {locationTemplate.split(/(?<=\})/).filter(Boolean).map((part, i) => (
        <span className="g" key={i}>{part.split(/\{(machine|project|cli)\}/).map((piece, j) => <Fragment key={j}>{j % 2 ? slots[piece] : piece}</Fragment>)}</span>
      ))}
    </div>
  );

  const busyNote = busyNotice && <div className="nt-note">{renderText(busyNotice.text)}</div>;
  const summaryEffort = (vm.modelPanel?.effortChoices.length ?? 0) > 1 ? (vm.config?.effort ? vm.effortLabel : t("models.default")) : "";
  const summary = (
    <ConfigSummary
      summary={{ agent: vm.agentName, model: renderText(vm.modelLabel) || t("models.default"), effort: summaryEffort }}
      bare={many}
      onClick={openModelPanel}
      data-composer-picker={phone ? "model" : undefined}
      aria-expanded={config === "model"}
      onPointerDown={phone ? keepComposerFocus : undefined}
      onMouseDown={phone ? keepComposerFocus : undefined}
    />
  );
  const attach = p.onPickFiles && (
    <AttachButton
      draft={activeDraft}
      supportsFiles={vm.supportsFiles}
      blocked={attachWhy}
      onPickFiles={handlePickFiles}
      onBlocked={setToast}
    />
  );

  const body = variant === "sheet" ? (
    <div ref={boundaryRef} className={"ntask" + (drop.over ? " drop" : "")} {...drop.props} role="group" aria-label={t("new_session.project_session_label", { name: projectName })}>
      <header className="nt-hd">
        <button className="fbtn" aria-label={t("common.close")} onClick={p.onClose}><X /></button>
        <div className="nt-where">{proj}{mach}</div>
        <span className="nt-pad" />
      </header>
      <div className="nt-body">{banners}{busyNote}</div>
      <div className="pbox" ref={composerRef}>{field}{trayView}<div className="acc">{attach}<NewTaskTools>{cli}{summary}</NewTaskTools>{send}</div></div>
    </div>
  ) : (
    <div className={"wstart" + (drop.over ? " drop" : "")} {...drop.props} role="group" aria-label={t("new_session.project_session_label", { name: projectName })}>
      {banners}{sentence}
      <div className="wbox">{field}{trayView}<div className="acc">{attach}{summary}{keys && <span className="kh">{t("new_session.start_shortcut", { keys })}</span>}{send}</div></div>
      {busyNote}
    </div>
  );

  const cliChoices = (
    <div className="list" role="menu">
      {vm.availableAgents.map((a) => (
        <button key={a.agent} className="mi" role="menuitemradio" aria-checked={a.selected} onClick={() => { setChoosing(false); if (!a.selected) p.onSelectAgent(a.agent); }}>
          <span className="mt"><AgentMark agent={a.agent} named />{a.displayName}</span>
          {a.selected && <span className="ck"><Check /></span>}
        </button>
      ))}
    </div>
  );

  const modelPanel = vm.modelPanel && (
    <NewTaskModelPanel
      key={`${machineId}/${agent}`}
      panel={vm.modelPanel}
      selectedModel={vm.config?.model ?? ""}
      configEffort={vm.config?.effort ?? ""}
      settled={vm.settled}
      onSelectModel={p.onSelectModel}
      onRetryModels={p.onRetryModels}
      onClose={() => setConfig("")}
    />
  );

  return (
    <>
      {body}
      {choosing && (phone
        ? <ComposerPicker anchor={cliRef} boundary={boundaryRef} onClose={() => setChoosing(false)}>{cliChoices}</ComposerPicker>
        : <Sheet onClose={() => setChoosing(false)} title={t("new_session.choose_cli")} anchor={cliRef}>{cliChoices}</Sheet>)}
      {config === "machine" && (
        <Sheet onClose={() => setConfig("")} title={t("common.machine")} action={{ label: t("common.done"), onClick: () => setConfig("") }} anchor={anchorOf(".wstart .sent .tok.m")} className="with-head">
          <div className="list">
            {(vm.modelPanel?.machineChoices ?? []).map((m) => (
              <button key={m.machineId} className={"mi" + (m.online ? "" : " dis")} disabled={!m.online} onClick={() => { p.onSelectMachine(m.machineId); setConfig(""); }}>
                <StateMark kind={m.online ? "online" : "off"} />
                <div className="mt">
                  {m.selected || !m.online ? m.name : <b>{t("new_session.use_machine", { name: m.name })}</b>}
                  {m.disambiguator && <span className="mc-tag mc-tag-inline">{m.disambiguator}</span>}
                  {m.note && <div className="md">{renderText(m.note)}</div>}
                </div>
                {m.selected && <span className="ck"><Check /></span>}
              </button>
            ))}
          </div>
        </Sheet>
      )}
      {config === "model" && modelPanel && (phone
        ? <ComposerPicker anchor={composerRef} boundary={boundaryRef} title={t("models.title")} onClose={() => setConfig("")}>{modelPanel}</ComposerPicker>
        : <Sheet onClose={() => setConfig("")} anchor={anchorOf(".wstart .acc .project-summary")} className="with-head new-task-model-sheet">{modelPanel}</Sheet>)}
    </>
  );
}

/** Connected new-session box backed by CoreHost. */
export function NewBox({ remote, projectKey, go, autoFocus, variant = "sheet", fixed, onPickProject, onClose, notice, settings = false, onDraft }: { remote: Remote; projectKey: string; go: Go; autoFocus?: boolean; variant?: "sheet" | "start"; fixed?: boolean; onPickProject?: () => void; onClose?: () => void; notice?: ReactNode; settings?: boolean; onDraft?: () => void }) {
  const host = useCoreHost(remote, go);
  const vm = useNewSessionView(host, projectKey);
  const activeKey = vm.projectKey || projectKey;
  const scope = useMemo(() => newSessionDraftScope(activeKey), [activeKey]);
  const draftVm = useAttachmentDraftView(host, scope);
  const draft = vm.draft ?? draftVm.draft;
  const machineId = vm.config?.machineId ?? "";

  useLayoutEffect(() => {
    host.newSessionOpen(projectKey);
    const draftMachine = projectKey ? remote.draftMachines.get(projectKey) : undefined;
    if (draftMachine) host.newSessionSelectMachine(draftMachine);
  }, [host, remote, projectKey]);

  useEffect(() => {
    if (activeKey) host.draftOpen(scope, machineId);
  }, [host, scope, activeKey, machineId]);

  return (
    <NewBoxView
      key={activeKey}
      vm={vm}
      draft={draft}
      attachments={host.attachments}
      autoFocus={autoFocus}
      variant={variant}
      fixed={fixed}
      onPickProject={onPickProject}
      onClose={onClose}
      notice={notice}
      initialConfigOpen={settings}
      onDraft={onDraft}
      onSelectMachine={host.newSessionSelectMachine}
      onSelectAgent={host.newSessionSelectAgent}
      onSelectModel={host.newSessionSelectModel}
      onRetryModels={() => host.newSessionRetryModels(activeKey)}
      onRetrySaveConfig={() => host.newSessionRetrySaveConfig(activeKey)}
      onSetPrompt={(text) => host.draftSetText(scope, text)}
      onSubmit={() => host.newSessionSubmit(activeKey)}
      onRetryDiskFull={() => host.newSessionSubmit(activeKey)}
      onAbandonStart={() => host.newSessionAbandonStart(activeKey)}
      onOpenMachineSettings={(machineId, agent) =>
        go(agent ? routeForMachineCli(machineId, agent) : routeForMachineSettings(machineId))
      }
      onOpenDiskUsage={(machineId) => go("machine-status/" + encodeURIComponent(machineId))}
      onReaddProject={() => host.projectToggleHide(activeKey)}
      onPickFiles={(entries, forceFile) => host.stageAndAddDraftItems(scope, entries, forceFile)}
      onRemoveAttachment={(id) => host.draftRemoveItem(scope, id)}
      onRetryAttachment={(id) => host.draftRetryItem(scope, id)}
      onPauseAttachment={(id) => host.attachmentPauseUpload(scope, id)}
      onResumeAttachment={(id) => host.attachmentResumeUpload(scope, id)}
      onSetOriginal={(orig) => host.draftSetOriginal(scope, orig)}
      onOpenAttachmentViewer={(idx) => host.draftImageViewerOpen(scope, idx)}
      onNavigateAttachmentViewer={(delta) => host.draftImageViewerNavigate(scope, { delta })}
      onCloseAttachmentViewer={() => host.draftImageViewerClose(scope)}
    />
  );
}

/** 选项目: the project list rendered from Core's ProjectListingView, 新项目… at the end. */
export function ProjectPicker({ remote, onPick, onNew, onClose, title = t("projects.choose") }: { remote: Remote; onPick: (key: string) => void; onNew: () => void; onClose: () => void; title?: string }) {
  const host = useCoreHost(remote);
  const manage = useProjectView(host, "", true).manage;
  return (
    <Sheet onClose={onClose} title={title} action={{ label: t("common.cancel"), onClick: onClose }} anchor={anchorOf(".wstart .sent .tok.p")}>
      {manage?.listing && (
        <ProjectList
          listing={manage.listing}
          onSearch={host.projectSetSearch}
          onNew={onNew}
          item={(p) => <ProjectItem key={p.key} p={p} onClick={() => onPick(p.key)} end={<ChevronRight className="chev" />} />}
        />
      )}
    </Sheet>
  );
}

const NEW_SESSION_PICKER_KEY = "new-session";

/** The ＋ flow: pick a project, or add a folder via Core's DirPickerView; then open its new-session box. */
export function NewFlow({ remote, go, onClose, onPicked, start }: { remote: Remote; go: Go; onClose: () => void; onPicked?: (key: string) => void; start?: "new" }) {
  const host = useCoreHost(remote, go);
  const manage = useProjectView(host, "", true, 30, "", NEW_SESSION_PICKER_KEY).manage;
  const pick = (manage?.projectCount ?? 0) > 0 && !start;
  const [step, setStep] = useState<"project" | "new">(pick ? "project" : "new");

  useEffect(() => {
    if (step !== "new") return;
    host.projectOpenDirPicker(true, "", "", NEW_SESSION_PICKER_KEY);
    return () => { host.projectCloseDirPicker(NEW_SESSION_PICKER_KEY); };
  }, [host, step]);

  const open = (key: string) => {
    if (onPicked) { onPicked(key); return; }
    remote.focusProject = key;
    go("p/" + encodeURIComponent(key));
  };

  if (step === "new" && manage?.dirPicker) {
    return (
      <DirPickerSheet
        picker={manage.dirPicker}
        onBack={pick ? () => setStep("project") : undefined}
        onClose={onClose}
        onNavigate={(mid, path) => host.projectDirNavigate(mid, path, NEW_SESSION_PICKER_KEY)}
        onConfirm={(path) => {
          if (manage.dirPicker?.machineId) remote.draftMachines.set(path, manage.dirPicker.machineId);
          host.projectAdd(path, NEW_SESSION_PICKER_KEY);
          if (onPicked) open(path);
          else { onClose(); remote.focusProject = path; }
        }}
      />
    );
  }
  return <ProjectPicker remote={remote} onPick={open} onNew={() => setStep("new")} onClose={onClose} />;
}

/** The model can shorten; the CLI prefix and effective effort stay whole. */
export function ConfigSummary({ summary, bare, ...button }: { summary: { agent: string; model: string; effort?: string }; bare?: boolean } & ComponentProps<"button">) {
  const { agent, model, effort } = summary;
  return (
    <button className="tok project-summary" aria-label={effort ? t("models.summary_effort_label", { model, effort }) : t("models.summary_label", { model })} {...button}>
      {!bare && <span className="fx">{agent} · </span>}
      <span className="mdl">{model}</span>
      {effort && <span className="effort"> · {effort}</span>}
      <ChevronRight />
    </button>
  );
}
