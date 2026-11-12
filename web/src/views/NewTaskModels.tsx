import { t } from "../i18n";
import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { agentInfo, defaultsFor } from "../agents";
import { findModel, modelLabel, STANDARD_WINDOW, type NewConfig } from "../model";
import type { DaemonSettings, ListModelsResponse } from "../gen/remoteagent/v1/local_pb";
import { Check, ChevronLeft, ChevronRight } from "../icons";
import { Banner, Segmented } from "../ui";

type ModelInfo = { models?: ListModelsResponse; settings?: DaemonSettings };

export function shouldPinModelControls(bodyHeight: number, controlsHeight: number, rowHeight: number) {
  return rowHeight > 0 && bodyHeight - controlsHeight >= rowHeight * 2 + 8;
}

/** One scroll owner in both modes. Sticky controls change only their paint
 * position, so resizing never reparents content or resets the user's scroll. */
function ModelPanelBody({ children }: { children: ReactNode }) {
  const root = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const body = root.current!;
    const controls = body.querySelector<HTMLElement>(".model-controls");
    const row = body.querySelector<HTMLElement>(".mi");
    const measure = () => {
      body.dataset.pinned = String(!!controls && shouldPinModelControls(
        body.clientHeight, controls.offsetHeight, row?.getBoundingClientRect().height ?? 0,
      ));
    };
    measure();
    const observer = new ResizeObserver(measure);
    [body, controls, row].forEach(el => { if (el) observer.observe(el); });
    return () => observer.disconnect();
  });
  return <div className="model-panel-body" ref={root}>{children}</div>;
}

export function newTaskModel(cfg: NewConfig, info: ModelInfo) {
  const def = defaultsFor(info.settings, cfg.agent);
  const slug = cfg.model || def.model || info.models?.defaultModel || "";
  const model = findModel(info.models?.models, slug);
  const effort = cfg.effort;
  return { slug, model, effort };
}

export function chooseNewTaskModel(cfg: NewConfig, slug: string, info: ModelInfo): NewConfig {
  const next = { ...cfg, model: slug, effort: "" };
  const current = newTaskModel(cfg, info).effort;
  const target = newTaskModel(next, info);
  return { ...next, effort: target.model?.efforts.includes(current) ? current : "" };
}

/** Only new-task panels use this layout. Settings and schedules retain theirs. */
export function NewTaskModelPanel({ cfg, info, enabled, pending, onChange, onClose }: {
  cfg: NewConfig;
  info: ModelInfo & { error: string; retry: () => void };
  enabled: boolean;
  pending: boolean;
  onChange: (cfg: NewConfig) => void;
  onClose: () => void;
}) {
  const [all, setAll] = useState(false);
  const { model, effort } = newTaskModel(cfg, info);
  const list = info.models?.models ?? [];
  const picked = cfg.model ? model : undefined;
  const shown = all || list.length <= 5 ? list : [...list.slice(0, 3), ...(picked && !list.slice(0, 3).includes(picked) ? [picked] : [])];
  const def = defaultsFor(info.settings, cfg.agent).model || info.models?.defaultModel || "";
  const pick = (slug: string) => { onChange(chooseNewTaskModel(cfg, slug, info)); setAll(false); };
  const options = (values: string[]) => values.map(value => ({ value, label: value, disabled: !enabled }));
  return <div className="new-task-model-panel" data-page={all ? "all" : "main"}>
    <div className="sheet-head">
      {all ? <button className="model-back" onClick={() => setAll(false)} aria-label={t("models.back")}><ChevronLeft />{t("models.all")}</button> : <b>{t("models.title")}</b>}
      <button className="side-act" onClick={onClose}>{t("common.done")}</button>
    </div>
    {info.error && <Banner kind="warn" action={t("common.retry")} onAction={info.retry}>{t("models.load_failed_default", { agent: agentInfo(cfg.agent).name })}</Banner>}
    {pending || (!info.models && !info.error) ? <div className="model-loading hint">{pending ? t("new_session.reading_previous") : t("new_session.reading_settings")}</div> : info.models && <ModelPanelBody key={all ? "all" : "main"}>
      {!all && <div className="model-controls">
        {model && model.efforts.length > 1 && <>
          <div className="fl">{effort ? t("models.effort") : t("models.effort_default")}</div>
          <Segmented className="full" label={t("models.effort")} value={effort} onChange={effort => onChange({ ...cfg, effort })} options={options(model.efforts)} />
        </>}
        {model?.efforts.length === 1 && <div className="hint">{effort ? t("models.fixed_effort", { effort }) : t("models.effort_default")}</div>}
        {agentInfo(cfg.agent).standardContextOption && (model && model.contextWindow > 0n && model.contextWindow <= STANDARD_WINDOW ?
          <div className="hint">{t("models.standard_only")}</div> : <>
            <div className="fl">{t("models.context_window")}</div>
            <Segmented className="full" label={t("models.context_window")} value={cfg.standardContext ? "standard" : "extended"}
              onChange={value => onChange({ ...cfg, standardContext: value === "standard" })} options={[{ value: "extended", label: t("models.extended_window"), disabled: !enabled }, { value: "standard", label: t("models.standard_window"), disabled: !enabled }]} />
          </>)}
      </div>}
      <div className="model-list-scroll">
        <div className="list" role="group" aria-label={t("models.title")}>
          <button className="mi" disabled={!enabled} aria-pressed={!cfg.model} onClick={() => pick("")}>
            <span className="mt">{def ? t("models.default_named", { model: modelLabel(list, def, cfg.agent) }) : t("models.default")}</span>
            {!cfg.model && <span className="ck"><Check /></span>}
          </button>
          {shown.map(m => <button key={m.slug} className="mi" disabled={!enabled} aria-pressed={m === picked} onClick={() => pick(m.slug)}>
            <span className="mt">{m.displayName || m.slug}</span>
            {m === picked && <span className="ck"><Check /></span>}
          </button>)}
          {!all && shown.length < list.length && <button className="mi link-row" onClick={() => setAll(true)}><span className="mt">{t("models.all")}</span><ChevronRight /></button>}
        </div>
      </div>
    </ModelPanelBody>}
  </div>;
}

/** Measure real text, including font/zoom changes, before shortening the CLI.
 * The hidden CLI name still has an intrinsic width, avoiding a resize loop. */
export function NewTaskTools({ children }: { children: ReactNode }) {
  const root = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const box = root.current!;
    const measure = () => {
      const cli = box.querySelector<HTMLElement>(".cli");
      const name = cli?.querySelector<HTMLElement>(".cli-name");
      const summary = box.querySelector<HTMLElement>(".project-summary");
      const model = summary?.querySelector<HTMLElement>(".mdl");
      if (!cli || !name || !summary || !model) return;
      const fullCLI = cli.getBoundingClientRect().width + (box.dataset.compact === "true" ? name.scrollWidth + 4 : 0);
      const fullModel = summary.getBoundingClientRect().width + model.scrollWidth - model.clientWidth;
      box.dataset.compact = String(fullCLI + fullModel + 2 > box.clientWidth + 0.5);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(box);
    box.querySelectorAll(".cli-name, .mdl, .effort").forEach(el => observer.observe(el));
    document.fonts?.addEventListener("loadingdone", measure);
    return () => { observer.disconnect(); document.fonts?.removeEventListener("loadingdone", measure); };
  });
  return <div className="new-task-tools" ref={root}>{children}</div>;
}
