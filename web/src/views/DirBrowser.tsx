import { t } from "../i18n";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { ChevronLeft, ChevronRight } from "../icons";
import { Banner } from "../ui";

export interface DirCrumbItem {
  label: string;
  path: string;
}

export interface DirEntryItem {
  name: string;
  path: string;
}

/** Pure folder browser presentation shared by Core's DirPickerView and legacy machine pickers. */
export function DirBrowserView({
  crumbs,
  parentPath,
  dirs,
  loading,
  error,
  confirmLabel,
  canConfirm,
  head,
  onOpenDir,
  onRetry,
  onConfirm,
}: {
  crumbs: readonly DirCrumbItem[];
  parentPath: string;
  dirs: readonly DirEntryItem[];
  loading: boolean;
  error: string;
  confirmLabel: string;
  canConfirm: boolean;
  head?: ReactNode;
  onOpenDir: (path: string) => void;
  onRetry: () => void;
  onConfirm: () => void;
}) {
  const listRef = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState({ up: false, down: false });
  const measure = () => {
    const el = listRef.current;
    if (!el) return;
    const up = el.scrollTop > 0;
    const down = el.scrollTop + el.clientHeight < el.scrollHeight - 1;
    setEdges((e) => (e.up === up && e.down === down ? e : { up, down }));
  };
  useEffect(measure, [dirs, loading]);
  const lastName = crumbs[crumbs.length - 1]?.label || "/";
  return (
    <>
      {head}
      <div className="crumb">
        {crumbs.length > 0 ? (
          <>
            {crumbs.slice(0, -1).map((c) => c.label).join(" / ")}
            {crumbs.length > 1 && " / "}
            <b>{lastName}</b>
          </>
        ) : (
          "…"
        )}
      </div>
      {error && (
        <Banner kind="err" action={t("common.retry")} onAction={onRetry}>
          {error}
        </Banner>
      )}
      <div
        className={"list dirs" + (edges.up ? " fade-top" : "") + (edges.down ? " fade-bottom" : "")}
        ref={listRef}
        onScroll={measure}
      >
        {parentPath && (
          <button className="mi" disabled={loading} onClick={() => onOpenDir(parentPath)}>
            <ChevronLeft className="lead" />
            <div className="mt">{t("projects.parent_directory")}</div>
          </button>
        )}
        {dirs.map((d) => (
          <button key={d.path} className="mi" disabled={loading} onClick={() => onOpenDir(d.path)}>
            <div className="mt">{d.name}</div>
            <ChevronRight className="chev" />
          </button>
        ))}
        {!loading && !error && dirs.length === 0 && (
          <div className="mi note">{t("projects.no_subdirectories")}</div>
        )}
        {loading && dirs.length === 0 && !error && (
          <div className="mi note">{t("projects.reading_directory")}</div>
        )}
      </div>
      <div className="btns sheet-foot">
        <button className="btn pri" disabled={!canConfirm || loading} onClick={onConfirm}>
          {confirmLabel}
        </button>
      </div>
    </>
  );
}
