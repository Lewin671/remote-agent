import { t } from "../i18n";
import { useEffect, useState } from "react";
import type { ListDirectoriesResponse } from "../gen/remoteagent/v1/local_pb";
import { machineName, shortPath } from "../model";
import { errorText, type Remote } from "../remote";
import { Sheet } from "../ui";
import { DirBrowserView } from "./DirBrowser";
import { getCoreHost } from "../core/host";

function pathCrumbs(p: string): string[] {
  const parts = shortPath(p).split("/").filter(Boolean);
  return parts[0] === "~" ? parts : parts.length ? ["/" + parts[0], ...parts.slice(1)] : ["/"];
}

const MACHINE_STATUS_PICKER_KEY = "machine-status";

/** Lists a folder as a project via Core's ProjectAdd action. */
export function addProject(remote: Remote, machineId: string, path: string) {
  remote.draftMachines.set(path, machineId);
  const host = getCoreHost(remote);
  host.projectOpenDirPicker(false, machineId, path, MACHINE_STATUS_PICKER_KEY);
  host.projectAdd(path, MACHINE_STATUS_PICKER_KEY);
  host.projectCloseDirPicker(MACHINE_STATUS_PICKER_KEY);
}

/** Temporary M4-deferred directory picker on a fixed machine for MachineStatus. */
export function MachineDirSheet({ remote, fixedMachine, onDone, onClose }: { remote: Remote; fixedMachine: string; onDone: (machineId: string, path: string) => void; onClose: () => void }) {
  const m = remote.machines.get(fixedMachine);
  const start = m?.meta?.home ?? "";
  const [dir, setDir] = useState<ListDirectoriesResponse>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState("");
  const open = async (path: string) => {
    setError(""); setLoading(path);
    try { const r = await remote.machineCommand(fixedMachine, { case: "listDirectories", value: { path } }); if (r.kind.case === "listDirectories") setDir(r.kind.value); }
    catch (e) { setError(t("projects.directory_failed", { reason: errorText(e) })); }
    finally { setLoading(""); }
  };
  useEffect(() => { setDir(undefined); void open(start); }, [fixedMachine]); // eslint-disable-line react-hooks/exhaustive-deps
  const parts = dir ? pathCrumbs(dir.path) : [];
  return (
    <Sheet onClose={onClose} title={t("projects.choose_directory")}>
      {m && <div className="seg machines"><button type="button" className="on" disabled>{machineName(m)}</button></div>}
      <DirBrowserView crumbs={parts.map((label) => ({ label, path: label }))} parentPath={dir?.parent ?? ""} dirs={(dir?.dirs ?? []).map((name) => ({ name, path: `${dir?.path.replace(/\/$/, "") ?? ""}/${name}` }))} loading={!!loading || (!dir && !error)} error={error} confirmLabel={t("projects.use_directory", { name: parts[parts.length - 1] || "/" })} canConfirm={!!dir && !loading} onOpenDir={(path) => void open(path)} onRetry={() => void open(loading || dir?.path || start)} onConfirm={() => dir && onDone(fixedMachine, dir.path)} />
    </Sheet>
  );
}
