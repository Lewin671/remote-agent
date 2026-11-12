import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { agentInfo, defaultsFor } from "../agents";
import { findModel, modelLabel, STANDARD_WINDOW, type NewConfig } from "../model";
import type { DaemonSettings, ListModelsResponse } from "../gen/remoteagent/v1/local_pb";
import { Check, ChevronLeft, ChevronRight } from "../icons";
import { Banner, Segmented } from "../ui";

type ModelInfo = { models?: ListModelsResponse; settings?: DaemonSettings };

export function newTaskModel(cfg: NewConfig, info: ModelInfo) {
  const def = defaultsFor(info.settings, cfg.agent);
  const slug = cfg.model || def.model || info.models?.defaultModel || "";
  const model = findModel(info.models?.models, slug);
  const effort = cfg.effort || (cfg.model ? model?.defaultEffort : def.effort || model?.defaultEffort) || "";
  return { slug, model, effort };
}

export function chooseNewTaskModel(cfg: NewConfig, slug: string, info: ModelInfo): NewConfig {
  const next = { ...cfg, model: slug, effort: "" };
  const current = newTaskModel(cfg, info).effort;
  const target = newTaskModel(next, info);
  return { ...next, effort: target.model?.efforts.includes(current) ? current : target.effort };
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
      {all ? <button className="model-back" onClick={() => setAll(false)} aria-label="返回模型"><ChevronLeft />全部模型</button> : <b>模型</b>}
      <button className="side-act" onClick={onClose}>完成</button>
    </div>
    {info.error && <Banner kind="warn" action="重试" onAction={info.retry}>没能读取模型列表，会用 {agentInfo(cfg.agent).name} 自己的默认模型</Banner>}
    {pending || (!info.models && !info.error) ? <div className="model-loading hint">{pending ? "正在读取这个项目上次的设置…" : "读取设置…"}</div> : info.models && <>
      {!all && <div className="model-controls">
        {model && model.efforts.length > 1 && <>
          <div className="fl">推理强度</div>
          <Segmented className="full" label="推理强度" value={effort} onChange={effort => onChange({ ...cfg, effort })} options={options(model.efforts)} />
        </>}
        {model?.efforts.length === 1 && <div className="hint">推理强度固定为 {model.efforts[0]}</div>}
        {agentInfo(cfg.agent).standardContextOption && (model && model.contextWindow > 0n && model.contextWindow <= STANDARD_WINDOW ?
          <div className="hint">标准 200k · 这个模型只有标准窗口</div> : <>
            <div className="fl">上下文窗口</div>
            <Segmented className="full" label="上下文窗口" value={cfg.standardContext ? "标准 200k" : "1M"}
              onChange={value => onChange({ ...cfg, standardContext: value === "标准 200k" })} options={options(["1M", "标准 200k"])} />
          </>)}
      </div>}
      <div className="model-list-scroll" key={all ? "all" : "main"}>
        <div className="list" role="group" aria-label="模型">
          <button className="mi" disabled={!enabled} aria-pressed={!cfg.model} onClick={() => pick("")}>
            <span className="mt">默认{def ? `（${modelLabel(list, def, cfg.agent)}）` : ""}</span>
            {!cfg.model && <span className="ck"><Check /></span>}
          </button>
          {shown.map(m => <button key={m.slug} className="mi" disabled={!enabled} aria-pressed={m === picked} onClick={() => pick(m.slug)}>
            <span className="mt">{m.displayName || m.slug}</span>
            {m === picked && <span className="ck"><Check /></span>}
          </button>)}
          {!all && shown.length < list.length && <button className="mi link-row" onClick={() => setAll(true)}><span className="mt">全部模型</span><ChevronRight /></button>}
        </div>
      </div>
    </>}
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
