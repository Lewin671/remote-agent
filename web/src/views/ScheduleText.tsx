import * as l10n from "../i18n";
import { useEffect, useRef, useState, type TextareaHTMLAttributes } from "react";
import { ChevronDown, ChevronUp } from "../icons";
import { scheduleMessageLimit, validScheduleMessage } from "../schedules";
import { Alert, Banner, useEscape } from "../ui";

/** Measure wrapped lines too, so paths and paragraphs obey the same six-line limit. */
export function ScheduleText({ text, meta, lines = 6 }: { text: string; meta?: string; lines?: number }) {
  const ref = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [long, setLong] = useState(text.split("\n").length > lines);
  useEffect(() => {
    const el = ref.current!;
    const measure = () => {
      const style = getComputedStyle(el);
      setLong(el.scrollHeight > (parseFloat(style.lineHeight) || 24) * lines + 1 || text.split("\n").length > lines);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [text, lines]);
  return <>
    <div className={"schedule-text" + (expanded ? " expanded" : "")}><div ref={ref} className="schedule-text-content" style={{ WebkitLineClamp: lines }}>{text}</div></div>
    {long ? <button className={meta ? "schedule-text-meta" : "schedule-expand"} aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>{meta && `${meta} · `}{expanded ? l10n.t("markdown.collapse") : l10n.t("markdown.expand")}{expanded ? <ChevronUp /> : <ChevronDown />}</button> : meta && <small>{meta}</small>}
  </>;
}

export function ScheduleTextarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const el = ref.current!;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, [props.value]);
  return <textarea {...props} ref={ref} rows={2} className="schedule-autosize" />;
}

export function ScheduleMessageEditor({ initial, busy, disabled, error, onClose, onSave }: {
  initial: string; busy: boolean; disabled: boolean; error: string; onClose: () => void; onSave: (text: string) => void;
}) {
  const [draft, setDraft] = useState(initial);
  const [discard, setDiscard] = useState(false);
  const cancel = () => { if (!busy) { if (draft !== initial) setDiscard(true); else onClose(); } };
  useEscape(cancel);
  return <div className="schedule-editor" role="dialog" aria-modal="true" aria-label={l10n.t("schedule.what_should_the_agent_do")}>
    <header className="schedule-editor-nav"><button disabled={busy} onClick={cancel}>{l10n.t("common.cancel")}</button><b>{l10n.t("schedule.what_should_the_agent_do")}</b><button disabled={disabled || busy || !validScheduleMessage(draft)} onClick={() => draft === initial ? onClose() : onSave(draft)}>{l10n.t("common.save")}</button></header>
    <textarea autoFocus aria-label={l10n.t("schedule.what_should_the_agent_do")} value={draft} onChange={e => setDraft(e.target.value)} />
    {new TextEncoder().encode(draft).length > scheduleMessageLimit && <p className="hint red">{l10n.t("schedule.too_long_maximum_32_kb")}</p>}
    {error && <Banner kind="err">{error}</Banner>}
    {discard && <Alert title={l10n.t("schedule.discard_changes")} text={l10n.t("schedule.these_changes_will_not_be_saved")} ok={l10n.t("schedule.discard_changes_schedule_text")} danger onCancel={() => setDiscard(false)} onOk={onClose} />}
  </div>;
}
