import { t } from "../i18n";
import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { renderText } from "../core/text";
import type { NewSessionModelPanelView } from "../gen/remoteagent/core/v1/new_session_view_pb";
import { Check, ChevronLeft, ChevronRight } from "../icons";
import { Banner, Segmented } from "../ui";

export function shouldPinModelControls(bodyHeight: number, controlsHeight: number, rowHeight: number) {
  return rowHeight > 0 && bodyHeight - controlsHeight >= rowHeight * 2 + 8;
}

/** One scroll owner in both modes. Sticky controls avoid reparenting content on resize. */
function ModelPanelBody({ children }: { children: ReactNode }) {
  const root = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const body = root.current!;
    const controls = body.querySelector<HTMLElement>(".model-controls");
    const row = body.querySelector<HTMLElement>(".mi");
    const measure = () => {
      body.dataset.pinned = String(!!controls && shouldPinModelControls(body.clientHeight, controls.offsetHeight, row?.getBoundingClientRect().height ?? 0));
    };
    measure();
    const observer = new ResizeObserver(measure);
    [body, controls, row].forEach((el) => el && observer.observe(el));
    return () => observer.disconnect();
  });
  return <div className="model-panel-body" ref={root}>{children}</div>;
}

/** Only new-task panels use this layout. Settings and schedules retain theirs. */
export function NewTaskModelPanel({ panel, selectedModel, configEffort, settled = true, onSelectModel, onRetryModels, onClose }: {
  panel: NewSessionModelPanelView;
  selectedModel: string;
  configEffort: string;
  settled?: boolean;
  onSelectModel: (model: string, effort: string, standardContext?: boolean) => void;
  onRetryModels: () => void;
  onClose: () => void;
}) {
  const [all, setAll] = useState(false);
  const list = panel.models;
  const picked = list.find((m) => m.checked);
  const shown = all || list.length <= 5 ? list : [...list.slice(0, 3), ...(picked && !list.slice(0, 3).includes(picked) ? [picked] : [])];
  const enabled = settled && panel.panelEnabled;
  const hasError = !panel.modelsLoading && !!panel.modelsError;
  const loading = !settled || (panel.modelsLoading && list.length === 0);
  const pick = (slug: string) => { onSelectModel(slug, ""); setAll(false); };
  const options = (values: readonly string[]) => values.map((value) => ({ value, label: value, disabled: !enabled }));
  return (
    <div className="new-task-model-panel" data-page={all ? "all" : "main"}>
      <div className="sheet-head">
        {all ? <button className="model-back" onClick={() => setAll(false)} aria-label={t("models.back")}><ChevronLeft />{t("models.all")}</button> : <b>{t("models.title")}</b>}
        <button className="side-act" onClick={onClose}>{t("common.done")}</button>
      </div>
      {hasError && <Banner kind="warn" action={t("common.retry")} onAction={onRetryModels}>{renderText(panel.modelsError)}</Banner>}
      {loading ? (
        <div className="model-loading hint">{!settled ? t("new_session.reading_previous") : renderText(panel.modelsError) || t("new_session.reading_settings")}</div>
      ) : (list.length > 0 || !hasError) && (
        <ModelPanelBody key={all ? "all" : "main"}>
          {!all && (
            <div className="model-controls">
              {panel.effortChoices.length > 1 && (
                <>
                  <div className="fl">{renderText(panel.effortTitle) || (configEffort ? t("models.effort") : t("models.effort_default"))}</div>
                  <Segmented className="full" label={t("models.effort")} value={configEffort} onChange={(effort) => onSelectModel(selectedModel, effort)} options={options(panel.effortChoices)} />
                </>
              )}
              {panel.effortChoices.length === 1 && (
                <div className="hint">{configEffort ? t("models.fixed_effort", { effort: configEffort }) : t("models.effort_default")}</div>
              )}
              {panel.supportsContextWindow && (panel.contextFixed ? (
                <div className="hint">{renderText(panel.contextNote) || t("models.standard_only")}</div>
              ) : (
                <>
                  <div className="fl">{t("models.context_window")}</div>
                  <Segmented
                    className="full"
                    label={t("models.context_window")}
                    value={panel.standardContext ? "standard" : "extended"}
                    onChange={(value) => onSelectModel(selectedModel, configEffort, value === "standard")}
                    options={[{ value: "extended", label: t("models.extended_window"), disabled: !enabled }, { value: "standard", label: t("models.standard_window"), disabled: !enabled }]}
                  />
                </>
              ))}
            </div>
          )}
          <div className="model-list-scroll">
            <div className="list" role="group" aria-label={t("models.title")}>
              <button className="mi" disabled={!enabled} aria-pressed={panel.defaultModelChecked} onClick={() => pick("")}>
                <span className="mt">{renderText(panel.defaultModelLabel) || t("models.default")}</span>
                {panel.defaultModelChecked && <span className="ck"><Check /></span>}
              </button>
              {shown.map((m) => (
                <button key={m.slug} className="mi" disabled={!enabled} aria-pressed={m.checked} onClick={() => pick(m.slug)}>
                  <span className="mt">{m.displayName || m.slug}</span>
                  {m.checked && <span className="ck"><Check /></span>}
                </button>
              ))}
              {!all && shown.length < list.length && (
                <button className="mi link-row" onClick={() => setAll(true)}><span className="mt">{t("models.all")}</span><ChevronRight /></button>
              )}
            </div>
          </div>
        </ModelPanelBody>
      )}
    </div>
  );
}

/** Measure real text before shortening the CLI badge so it avoids a resize loop. */
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
    box.querySelectorAll(".cli-name, .mdl, .effort").forEach((el) => observer.observe(el));
    document.fonts?.addEventListener("loadingdone", measure);
    return () => { observer.disconnect(); document.fonts?.removeEventListener("loadingdone", measure); };
  });
  return <div className="new-task-tools" ref={root}>{children}</div>;
}
