import { useEffect, useLayoutEffect, useState, type RefObject } from "react";
import { t } from "../i18n";
import { Banner, Skeleton } from "../ui";

/** Outside the scroller: neither placeholders nor notices enter the virtual
 * list's measurements or change a saved reading position. */
export function HistoryOpening({ waiting, scroller, error, onRetry, connected = true }: {
  waiting: boolean;
  scroller: RefObject<HTMLDivElement | null>;
  error?: string;
  onRetry?: () => void;
  connected?: boolean;
}) {
  const [held, setHeld] = useState(false);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const update = () => setHeld(el.classList.contains("mo-hold"));
    update();
    const observer = new MutationObserver(update);
    observer.observe(el, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }, [scroller]);
  const pending = waiting || held;
  const [stage, setStage] = useState(0);
  useEffect(() => {
    setStage(0);
    if (!pending) return;
    const placeholder = setTimeout(() => setStage(1), 300);
    const slow = setTimeout(() => setStage(2), 5000);
    return () => { clearTimeout(placeholder); clearTimeout(slow); };
  }, [pending]);
  if (!pending || (!error && stage === 0)) return null;
  return <div className="history-opening">
    {error || (stage === 2 && connected) ? (
      <div role="status"><Banner kind={error ? "err" : undefined} action={onRetry ? t("common.retry") : undefined} onAction={onRetry}>
        {error || t("session.opening_slow")}
      </Banner></div>
    ) : (
      <div className="history-placeholder">
        <div className="me"><Skeleton lines={2} /></div>
        <div className="ai"><Skeleton lines={4} /></div>
      </div>
    )}
  </div>;
}
