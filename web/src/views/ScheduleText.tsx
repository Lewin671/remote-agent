import { useEffect, useRef, useState, type TextareaHTMLAttributes } from "react";
import { ChevronDown, ChevronUp } from "../icons";
import { scheduleMessageLimit, validScheduleMessage } from "../schedules";
import { Alert, Banner, useEscape } from "../ui";

/** Measure wrapped lines too, so paths and paragraphs obey the same six-line limit. */
export function ScheduleText({ text, meta }: { text: string; meta?: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [long, setLong] = useState(text.split("\n").length > 6);
  useEffect(() => {
    const el = ref.current!;
    const measure = () => {
      const style = getComputedStyle(el);
      setLong(el.scrollHeight > (parseFloat(style.lineHeight) || 24) * 6 + 1 || text.split("\n").length > 6);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [text]);
  return <>
    <div className={"schedule-text" + (expanded ? " expanded" : "")}><div ref={ref} className="schedule-text-content">{text}</div></div>
    {long ? <button className={meta ? "schedule-text-meta" : "schedule-expand"} aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>{meta && `${meta} · `}{expanded ? "收起" : "展开全文"}{expanded ? <ChevronUp /> : <ChevronDown />}</button> : meta && <small>{meta}</small>}
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
  return <div className="schedule-editor" role="dialog" aria-modal="true" aria-label="让 agent 做什么">
    <header className="schedule-editor-nav"><button disabled={busy} onClick={cancel}>取消</button><b>让 agent 做什么</b><button disabled={disabled || busy || !validScheduleMessage(draft)} onClick={() => draft === initial ? onClose() : onSave(draft)}>保存</button></header>
    <textarea autoFocus aria-label="让 agent 做什么" value={draft} onChange={e => setDraft(e.target.value)} />
    {new TextEncoder().encode(draft).length > scheduleMessageLimit && <p className="hint red">太长了，最多 32 KB</p>}
    {error && <Banner kind="err">{error}</Banner>}
    {discard && <Alert title="放弃修改？" text="这次修改不会保存。" ok="放弃修改" danger onCancel={() => setDiscard(false)} onOk={onClose} />}
  </div>;
}
