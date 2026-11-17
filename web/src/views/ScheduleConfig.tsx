import * as l10n from "../i18n";
import { t } from "../i18n";
import { create } from "@bufbuild/protobuf";
import { useEffect, useState } from "react";
import { ApprovalMode, type Agent } from "../gen/remoteagent/v1/event_pb";
import { DirPickerOpenActionSchema, EventSchema, ProjectAddActionSchema } from "../gen/remoteagent/core/v1/app_pb";
import type { DaemonSettings, ListDirectoriesResponse, ListModelsResponse } from "../gen/remoteagent/v1/local_pb";
import { AGENTS, agentInfo, defaultsFor } from "../agents";
import {
  agentName,
  agentOf,
  allAutoUnavailable,
  autoUnavailable,
  createdMs,
  defaultModeOf,
  endedMs,
  exited,
  findModel,
  machineAgents,
  machineName,
  modeLabel,
  modeName,
  modelLabel,
  projectName,
  projects,
  shortPath,
  type MachineLike,
  type NewConfig,
} from "../model";
import { errorText, type MachineView, type Remote } from "../remote";
import { Avatar, Banner, PathText, Segmented, Sheet, StateMark } from "../ui";
import { Check, ChevronRight } from "../icons";
import { DirBrowserView } from "./DirBrowser";
import { getCoreHost, type Go } from "../core/host";

function pathCrumbs(p: string): string[] {
  const parts = shortPath(p).split("/").filter(Boolean);
  if (parts[0] === "~") return parts;
  if (parts.length === 0) return ["/"];
  return ["/" + parts[0], ...parts.slice(1)];
}

/** Lists a folder as a project via Core's ProjectAdd action. */
export function addProject(remote: Remote, machineId: string, path: string) {
  remote.draftMachines.set(path, machineId);
  const host = getCoreHost(remote);
  host.dispatch(
    create(EventSchema, {
      event: {
        case: "projectOpenDirPicker",
        value: create(DirPickerOpenActionSchema, { machineId, path, forNewTask: false }),
      },
    }),
  );
  host.dispatch(
    create(EventSchema, {
      event: { case: "projectAdd", value: create(ProjectAddActionSchema, { path }) },
    }),
  );
}

export function useMachineInfo(remote: Remote, machineId: string, agent: Agent) {
  const key = machineId + "/" + agent;
  const [got, setGot] = useState<{ key: string; models?: ListModelsResponse; settings?: DaemonSettings; error: string; done: boolean }>();
  const [attempt, setAttempt] = useState(0);
  const online = remote.peer(machineId).reachable ?? false;
  useEffect(() => {
    let alive = true;
    setGot((g) => (g?.key === key ? { ...g, error: "", done: false } : g));
    if (!machineId || !online) return;
    Promise.allSettled([remote.listModels(machineId, agent, true), remote.settings(machineId)]).then(([m, s]) => {
      if (!alive) return;
      setGot({
        key,
        models: m.status === "fulfilled" ? m.value : undefined,
        settings: s.status === "fulfilled" ? s.value : undefined,
        error: m.status === "fulfilled" ? "" : errorText(m.reason),
        done: true,
      });
    });
    return () => { alive = false; };
  }, [remote, machineId, agent, online, attempt]);
  const mine = got?.key === key ? got : undefined;
  const models = mine?.models ?? remote.cachedModels(machineId, agent);
  const settings = mine?.settings ?? remote.cachedSettings(machineId);
  const loading = !!machineId && online && !mine?.done && !models;
  return { models, settings, error: mine?.error ?? "", loading, retry: () => setAttempt((a) => a + 1) };
}

function describeParts(cfg: NewConfig, m: MachineLike | undefined, info: ReturnType<typeof useMachineInfo>) {
  const def = defaultsFor(info.settings, cfg.agent);
  const slug = cfg.model || def.model || info.models?.defaultModel || "";
  const model = findModel(info.models?.models, slug);
  const effort = cfg.effort || (cfg.model ? model?.defaultEffort : def.effort || model?.defaultEffort) || "";
  const unset = info.loading ? t("new_session.reading_settings") : t("new_session.default_model");
  const label = slug ? modelLabel(info.models?.models, slug, cfg.agent) : unset;
  const modelText = slug ? `${label}${effort ? " · " + effort : ""}` : unset;
  return { machine: machineName(m), agent: agentName(cfg.agent), model: modelText, label, slug, mode: modeLabel(cfg.mode, defaultModeOf(m, cfg.agent), cfg.agent) };
}

function agentsOf(m: MachineLike | undefined): Agent[] {
  const have = machineAgents(m);
  return AGENTS.map((a) => a.agent).filter((a) => have.includes(a));
}

function modeTextOf(cfg: NewConfig, m: MachineLike | undefined): string {
  const dm = defaultModeOf(m, cfg.agent);
  return cfg.mode ? modeName(cfg.mode) : dm.known && dm.mode ? modeName(dm.mode) : t("new_session.follow_cli");
}

/** Temporary M3-deferred schedule config hook (until Schedules migrates to clientcore). */
export function useScheduleSessionConfig(remote: Remote, key: string, go: Go, fixedMachine?: string) {
  const p = projects(
    [...remote.sessions.values()].filter((s) => !fixedMachine || s.machineId === fixedMachine),
    remote.machines.values(),
    remote.projectPrefs().settings.filter((s) => !fixedMachine || s.machineId === fixedMachine),
  ).find((x) => x.key === key);
  const saved = remote.projectPrefs().settings.find((s) => s.path === key && (!fixedMachine || s.machineId === fixedMachine));
  const [local, setLocal] = useState<{ key: string; machineId?: string; sets: Partial<Record<Agent, NewConfig>> }>();
  const [picked, setPicked] = useState<{ key: string; agent: Agent }>();
  const ownLocal = local?.key === key ? local : undefined;
  const ownPick = picked?.key === key ? picked : undefined;

  const inherit = (a?: Agent): NewConfig | undefined => {
    const live = p ? p.sessions.filter((s) => !exited(s)).sort((a, b) => createdMs(b) - createdMs(a)) : [];
    const ended = p ? p.sessions.filter(exited).sort((a, b) => endedMs(b) - endedMs(a)) : [];
    const order = [...live, ...ended];
    const latest = order[0];
    if (!p || !latest) return undefined;
    const ag = a ?? agentOf(latest);
    const from = order.find((s) => agentOf(s) === ag);
    return { machineId: latest.machineId, cwd: key, agent: ag, model: from?.info?.model ?? "", effort: from?.info?.effort ?? "", mode: ApprovalMode.UNSPECIFIED };
  };
  const first = inherit();
  const known = p?.machines.find((id) => remote.peer(id).reachable) ?? p?.machines[0];
  const onlineFirst = [...remote.machines.values()].find((m) => remote.peer(m.id).reachable)?.id ?? [...remote.machines.keys()][0] ?? "";
  const baseMachine = fixedMachine || remote.draftMachines.get(key) || saved?.machineId || first?.machineId || known || onlineFirst;
  const machineId = fixedMachine ?? ownLocal?.machineId ?? baseMachine;
  const machine = remote.machines.get(machineId);
  const agents = agentsOf(machine);
  const remembered = saved?.agent ? agentInfo(saved.agent).agent : undefined;
  const wanted = ownPick?.agent ?? remembered ?? first?.agent ?? agents[0];
  const agent = agents.includes(wanted) ? wanted : agents[0];

  const heldEnded = p?.sessions.filter(exited).length ?? 0;
  const unreadFor = (a: Agent) =>
    !!p && remote.sessionsOpen && p.ended > heldEnded && !ownLocal?.sets[a] && !saved?.agents.some((x) => x.agent === a) &&
    !p.sessions.some((s) => agentOf(s) === a && !remote.pastPages(s));
  const unread = unreadFor(agent);
  const [watch, setWatch] = useState<Agent>();
  useEffect(() => {
    if (unread || (watch !== undefined && unreadFor(watch))) void remote.loadMoreSessions();
  });

  const configFor = (a: Agent): NewConfig => {
    const at = { machineId, cwd: key, agent: a };
    const localSet = ownLocal?.sets[a];
    if (localSet) return { ...localSet, ...at };
    const set = saved?.agents.find((x) => x.agent === a);
    if (set) return { ...at, model: set.model, effort: set.effort, mode: ApprovalMode.UNSPECIFIED, standardContext: set.standardContext };
    if (ownLocal?.machineId) return { ...at, model: "", effort: "", mode: ApprovalMode.UNSPECIFIED };
    const h = inherit(a);
    return { ...at, model: h?.model ?? "", effort: h?.effort ?? "", mode: ApprovalMode.UNSPECIFIED };
  };

  const cfg = configFor(agent);
  const info = useMachineInfo(remote, cfg.machineId, cfg.agent);
  const parts = describeParts(cfg, machine, info);
  const dm = defaultModeOf(machine, cfg.agent);
  const effectiveMode = cfg.mode || dm.mode;
  const issue =
    agentInfo(cfg.agent).autoMayBeUnavailable && effectiveMode === ApprovalMode.AUTO && autoUnavailable(info.models?.models, parts.slug)
      ? allAutoUnavailable(info.models?.models) ? i18n("modes.account_no_auto") : i18n("modes.model_no_auto")
      : "";

  const setCfg = (next: NewConfig) => {
    if (next.machineId !== machineId) {
      setLocal({ key, machineId: next.machineId, sets: { [next.agent]: next } });
    } else {
      setLocal({ key, machineId: ownLocal?.machineId, sets: { ...ownLocal?.sets, [next.agent]: next } });
    }
  };
  return {
    remote,
    go,
    p,
    cfg,
    setCfg,
    configFor,
    agents,
    setAgent: (a: Agent) => setPicked({ key, agent: a }),
    unreadFor,
    setWatch,
    reading: unread && remote.conn === "online",
    issue,
    parts,
    machine,
    info,
    modeText: modeTextOf(cfg, machine),
  };
}

function i18n(k: string) {
  return (l10n.t as (key: string) => string)(k);
}

type Pick = "" | "machine" | "model" | "mode";

/** Temporary M3-deferred config sheet for scheduled tasks. */
export function ScheduleConfigSheet({
  ns,
  onClose,
  lockMachine = false,
}: {
  ns: ReturnType<typeof useScheduleSessionConfig>;
  onClose: () => void;
  lockMachine?: boolean;
}) {
  const { remote, p, machine } = ns;
  const [pick, setPick] = useState<Pick>("");
  const [allModels, setAllModels] = useState(false);
  const back2 = () => setPick("");
  const agent = ns.cfg.agent;
  const cfg = ns.configFor(agent);
  const info = ns.info;
  const parts = describeParts(cfg, machine, info);
  const open = ns.unreadFor(agent);
  const { setWatch } = ns;
  useEffect(() => {
    setWatch(agent);
    return () => setWatch(undefined);
  }, [agent, setWatch]);
  const set = (patch: Partial<NewConfig>) => {
    if (!open) ns.setCfg({ ...cfg, ...patch });
  };
  const def = defaultsFor(info.settings, cfg.agent);
  useEffect(() => {
    if (pick === "model") info.retry();
  }, [pick]); // eslint-disable-line react-hooks/exhaustive-deps
  const choices: MachineView[] = (p && remote.peer(machine?.id).reachable ? p.machines : [...remote.machines.keys()]).map((id) => remote.machines.get(id)).filter((m): m is MachineView => !!m);

  let body;
  if (pick === "machine") {
    body = (
      <div className="list">
        {choices.map((m) => (
          <button
            key={m.id}
            className={"mi" + (remote.peer(m.id).reachable ? "" : " dis")}
            disabled={!remote.peer(m.id).reachable}
            onClick={() => {
              const agents = machineAgents(m);
              ns.setCfg({ ...cfg, machineId: m.id, agent: agents.includes(cfg.agent) ? cfg.agent : agents[0], model: "", effort: "", mode: ApprovalMode.UNSPECIFIED });
              back2();
            }}
          >
            <StateMark kind={remote.peer(m.id).reachable ? "online" : "off"} />
            <div className="mt">
              {m.id === cfg.machineId || !remote.peer(m.id).reachable ? machineName(m) : <b>{t("new_session.use_machine", { name: machineName(m) })}</b>}
            </div>
            {m.id === cfg.machineId && <span className="ck"><Check /></span>}
          </button>
        ))}
      </div>
    );
  } else if (pick === "model") {
    const chosen = findModel(info.models?.models, cfg.model || def.model || info.models?.defaultModel || "");
    const list = info.models?.models ?? [];
    const picked = cfg.model ? findModel(list, cfg.model) : undefined;
    const few = allModels || list.length <= 5;
    const shown = few ? list : [...list.slice(0, 3), ...(picked && !list.slice(0, 3).includes(picked) ? [picked] : [])];
    body = (
      <>
        {info.error && (
          <Banner kind="warn" action={l10n.t("common.retry")} onAction={info.retry}>
            {l10n.t("new_session.model_list_default", { name: agentName(cfg.agent) })}
          </Banner>
        )}
        {!info.models && !info.error && <div className="hint top">{l10n.t("models.android_reading_machine", { name: machineName(machine) })}</div>}
        <div className="list">
          <button className="mi" onClick={() => set({ model: "", effort: "" })}>
            <div className="mt">
              {l10n.t("new_session.default_model")}
              <div className="md">{l10n.t("new_session.machine_project_defaults", { name: machineName(machine) })}</div>
            </div>
            {!cfg.model && <span className="ck"><Check /></span>}
          </button>
          {shown.map((m) => (
            <button key={m.slug} className="mi" onClick={() => set({ model: m.slug, effort: "" })}>
              <div className="mt">
                {m.displayName || m.slug}
                {m.efforts.length === 1 && <div className="md">{l10n.t("new_session.fixed_effort", { effort: m.efforts[0] })}</div>}
              </div>
              {m === picked && <span className="ck"><Check /></span>}
            </button>
          ))}
          {!few && (
            <button className="mi link-row" onClick={() => setAllModels(true)}>
              <span className="mt">{l10n.t("models.all")}</span>
              <ChevronRight className="chev" />
            </button>
          )}
        </div>
        {chosen && chosen.efforts.length > 0 && (
          <>
            <div className="fl">{l10n.t("models.effort")}</div>
            <Segmented
              className="full wrap"
              label={l10n.t("models.effort")}
              value={cfg.effort || (cfg.model ? chosen.defaultEffort : def.effort || chosen.defaultEffort)}
              onChange={(e) => set({ effort: e })}
              options={chosen.efforts.map((e) => ({ value: e, label: e }))}
            />
          </>
        )}
      </>
    );
  } else if (pick === "mode") {
    const chosen = cfg.model || def.model || info.models?.defaultModel || "";
    const allNoAuto = agentInfo(cfg.agent).autoMayBeUnavailable && allAutoUnavailable(info.models?.models);
    const noAuto = allNoAuto || (agentInfo(cfg.agent).autoMayBeUnavailable && autoUnavailable(info.models?.models, chosen));
    body = (
      <div className="list">
        {[
          { mode: ApprovalMode.UNSPECIFIED, name: l10n.t("models.default"), desc: l10n.t("new_session.follow_s_configuration", { value: String(agentName(cfg.agent)) }) },
          { mode: ApprovalMode.ASK, name: l10n.t("modes.ask"), desc: l10n.t("modes.ask_desc") },
          noAuto
            ? { mode: ApprovalMode.AUTO, name: l10n.t("modes.auto"), desc: allNoAuto ? l10n.t("modes.account_unsupported") : l10n.t("new_session.this_model_has_no_auto_mode"), off: true }
            : { mode: ApprovalMode.AUTO, name: l10n.t("modes.auto"), desc: l10n.t("modes.auto_desc", { agent: String(agentName(cfg.agent)) }) },
          { mode: ApprovalMode.FULL_ACCESS, name: l10n.t("modes.full_access"), desc: l10n.t("modes.skip_all"), danger: true },
        ].map((m: { mode: ApprovalMode; name: string; desc: string; danger?: boolean; off?: boolean }) => (
          <button
            key={m.mode}
            className={"mi" + (m.danger ? " danger" : "")}
            disabled={m.off}
            onClick={() => { set({ mode: m.mode }); back2(); }}
          >
            <div className="mt">
              {m.name}
              <div className="md">{m.desc}</div>
            </div>
            {m.mode === cfg.mode && <span className="ck"><Check /></span>}
          </button>
        ))}
      </div>
    );
  }

  const titles: Record<Pick, string> = {
    "": lockMachine ? l10n.t("new_session.task_settings") : l10n.t("projects.android_settings"),
    machine: t("common.machine"),
    model: t("models.title"),
    mode: lockMachine ? l10n.t("new_session.permissions") : l10n.t("modes.title"),
  };
  const many = ns.agents.length > 1;
  if (open && pick && pick !== "machine") body = <div className="hint top">{t("new_session.reading_previous")}</div>;
  const content = (
    <>
      {body ?? (
        <>
          <div className="list">
            <div className="mi pid">
              <Avatar name={projectName(cfg.cwd)} size="sm" />
              <div className="mt">
                {projectName(cfg.cwd)}
                <div className="md mono"><PathText path={shortPath(cfg.cwd)} /></div>
              </div>
            </div>
          </div>
          <div className="fl">{l10n.t("list_state.ios_new_session")}</div>
          <div className="list">
            <button className="mi" disabled={lockMachine} onClick={() => setPick("machine")}>
              <span className="mt">{l10n.t("common.machine")}</span>
              <span className="mv">
                <StateMark kind={remote.peer(machine?.id).reachable || !remote.listed ? "online" : "off"} />
                {machineName(machine)}
              </span>
              <ChevronRight className="chev" />
            </button>
          </div>
          {many && <div className="fl">{agentName(agent)}</div>}
          {open ? (
            <div className="hint top">{l10n.t("new_session.reading_previous")}</div>
          ) : (
            <div className="list">
              <button className="mi" onClick={() => setPick("model")}>
                <span className="mt">{l10n.t("models.title")}</span>
                <span className="mv">{info.loading ? l10n.t("models.android_reading_machine", { name: String(machineName(machine)) }) : parts.model}</span>
                <ChevronRight className="chev" />
              </button>
              <button className="mi" onClick={() => setPick("mode")}>
                <span className="mt">{lockMachine ? l10n.t("new_session.permissions") : l10n.t("approval.title")}</span>
                <span className={"mv" + ((cfg.mode || defaultModeOf(machine, cfg.agent).mode) === ApprovalMode.FULL_ACCESS ? " red" : "")}>{modeTextOf(cfg, machine)}</span>
                <ChevronRight className="chev" />
              </button>
            </div>
          )}
          {info.error && (
            <Banner kind="warn" action={l10n.t("common.retry")} onAction={info.retry}>
              {l10n.t("new_session.model_list_default", { name: agentName(cfg.agent) })}
            </Banner>
          )}
        </>
      )}
    </>
  );
  return (
    <Sheet
      onClose={onClose}
      title={titles[pick]}
      action={{ label: t("common.done"), onClick: onClose }}
      back={pick ? back2 : undefined}
      className="with-head"
    >
      {content}
    </Sheet>
  );
}

/** Temporary M3-deferred directory picker on a fixed machine for MachineStatus and Schedules. */
export function MachineDirSheet({
  remote,
  fixedMachine,
  onDone,
  onClose,
}: {
  remote: Remote;
  fixedMachine: string;
  onDone: (machineId: string, path: string) => void;
  onClose: () => void;
}) {
  const m = remote.machines.get(fixedMachine);
  const start = m?.meta?.home ?? "";
  const [dir, setDir] = useState<ListDirectoriesResponse>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState("");
  const open = async (path: string) => {
    setError("");
    setLoading(path);
    try {
      const r = await remote.machineCommand(fixedMachine, { case: "listDirectories", value: { path } });
      if (r.kind.case === "listDirectories") setDir(r.kind.value);
    } catch (e) {
      setError(t("projects.directory_failed", { reason: errorText(e) }));
    } finally {
      setLoading("");
    }
  };
  useEffect(() => {
    setDir(undefined);
    void open(start);
  }, [fixedMachine]); // eslint-disable-line react-hooks/exhaustive-deps
  const parts = dir ? pathCrumbs(dir.path) : [];
  const last = parts[parts.length - 1] || "/";
  return (
    <Sheet onClose={onClose} title={t("projects.choose_directory")}>
      {m && (
        <div className="seg machines">
          <button type="button" className="on" disabled>
            {machineName(m)}
          </button>
        </div>
      )}
      <DirBrowserView
        crumbs={parts.map((label) => ({ label, path: label }))}
        parentPath={dir?.parent ?? ""}
        dirs={(dir?.dirs ?? []).map((name) => ({ name, path: `${dir?.path.replace(/\/$/, "") ?? ""}/${name}` }))}
        loading={!!loading || (!dir && !error)}
        error={error}
        confirmLabel={t("projects.use_directory", { name: last })}
        canConfirm={!!dir && !loading}
        onOpenDir={(path) => void open(path)}
        onRetry={() => void open(loading || dir?.path || start)}
        onConfirm={() => dir && onDone(fixedMachine, dir.path)}
      />
    </Sheet>
  );
}
