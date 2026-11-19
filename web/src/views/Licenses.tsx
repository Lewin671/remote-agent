import { useEffect, useState } from "react";
import * as l10n from "../i18n";
import { ChevronRight, LoaderCircle } from "../icons";
import { NavBar, Sheet } from "../ui";

interface LicenseEntry {
  id: string;
  name: string;
  license: string;
  path: string;
}

const LICENSE_GROUPS: Array<{
  id: string;
  title: () => string;
  items: LicenseEntry[];
}> = [
  {
    id: "fonts",
    title: () => l10n.t("settings.licenses_web_fonts_title"),
    items: [
      { id: "inter", name: "Inter", license: "SIL Open Font License 1.1", path: "/fonts/OFL-Inter.txt" },
      { id: "jetbrains-mono", name: "JetBrains Mono", license: "SIL Open Font License 1.1", path: "/fonts/OFL-JetBrainsMono.txt" },
    ],
  },
  {
    id: "icons",
    title: () => l10n.t("settings.licenses_web_icons_title"),
    items: [
      { id: "lucide", name: "Lucide", license: "ISC License", path: "/licenses/ISC-Lucide.txt" },
    ],
  },
  {
    id: "packages",
    title: () => l10n.t("settings.licenses_web_packages_title"),
    items: [
      { id: "notices", name: "THIRD_PARTY_NOTICES.txt", license: "Web / Runtime Dependencies", path: "/licenses/THIRD_PARTY_NOTICES.txt" },
    ],
  },
];

type LoadState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; text: string }
  | { status: "error"; message: string };

async function fetchLicenseText(path: string, signal: AbortSignal): Promise<string> {
  const res = await fetch(path, { signal });
  if (!res.ok) throw new Error(`${res.status}`);
  const ct = res.headers.get("content-type")?.toLowerCase() ?? "";
  const text = await res.text();
  if (ct.includes("text/html") || /^\s*<!doctype\s+html/i.test(text) || /^\s*<html/i.test(text)) {
    throw new Error("unavailable");
  }
  return text;
}

/** 开源许可二次页: 按字体、图标、第三方依赖分组列出并按需读取 Web 随附许可原文。 */
export function LicensesPage({ onBack }: { onBack: () => void }) {
  const [selected, setSelected] = useState<LicenseEntry>();
  const [load, setLoad] = useState<LoadState>({ status: "idle" });
  const [retryToken, setRetryToken] = useState(0);

  useEffect(() => {
    if (!selected) {
      setLoad({ status: "idle" });
      return;
    }
    const ctrl = new AbortController();
    setLoad({ status: "loading" });
    void fetchLicenseText(selected.path, ctrl.signal)
      .then((text) => {
        if (!ctrl.signal.aborted) setLoad({ status: "ready", text });
      })
      .catch(() => {
        if (!ctrl.signal.aborted) {
          setLoad({ status: "error", message: l10n.t("settings.could_not_download_retry_later") });
        }
      });
    return () => ctrl.abort();
  }, [selected, retryToken]);

  return (
    <div className="page set">
      <NavBar onBack={onBack} title={l10n.t("settings.open_source_licenses")} />
      <div className="body set">
        {LICENSE_GROUPS.map((g) => (
          <div key={g.id}>
            <div className="label">{g.title()}</div>
            <div className="grp">
              {g.items.map((item) => (
                <button
                  key={item.id}
                  type="button"
                  className="row"
                  onClick={() => setSelected(item)}
                >
                  <span className="row-name">
                    <b>{item.name}</b>
                    <small>{item.license}</small>
                  </span>
                  <span className="v">{l10n.t("settings.licenses_view_full")}</span>
                  <ChevronRight className="chev" />
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>
      {selected && (
        <Sheet
          onClose={() => setSelected(undefined)}
          title={selected.name}
          action={{ label: l10n.t("common.done"), onClick: () => setSelected(undefined) }}
        >
          {load.status === "loading" && (
            <div className="license-state">
              <LoaderCircle />
              <span>{l10n.t("common.loading")}</span>
            </div>
          )}
          {load.status === "error" && (
            <div className="license-state err">
              <p>{load.message}</p>
              <button
                type="button"
                className="btn"
                onClick={() => setRetryToken((n) => n + 1)}
              >
                {l10n.t("common.retry")}
              </button>
            </div>
          )}
          {load.status === "ready" && (
            <pre className="license-pre">{load.text}</pre>
          )}
        </Sheet>
      )}
    </div>
  );
}
