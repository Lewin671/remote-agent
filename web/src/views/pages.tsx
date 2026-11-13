import * as l10n from "../i18n";
import { lazy, type ComponentType } from "react";
import { chunkMissing } from "../chunks";

// The pages that are not the home or a chat, each fetched when first opened
// (and ahead of that, once the page has settled: see `pagesAhead`), so that
// the first screen does not carry them. A page that cannot be fetched
// because the server has moved on to a newer build makes the page start over
// on it.

function Gone() {
  return (
    <div className="center">
      <div>
        <div className="et">{l10n.t("startup.page_updated")}</div>
        <div className="muted small">{l10n.t("startup.this_part_could_not_be_fetched_reload_to_continue")}</div>
        <div className="big">
          <button className="btn pri" onClick={() => location.reload()}>
            {l10n.t("common.reload")}</button>
        </div>
      </div>
    </div>
  );
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function page<C extends ComponentType<any>>(load: () => Promise<{ default: C }>) {
  return lazy(async () => {
    try {
      return await load();
    } catch {
      // Starting over replaces everything: until then, nothing to show.
      if (await chunkMissing(true)) return new Promise<{ default: C }>(() => undefined);
      return { default: Gone as unknown as C };
    }
  });
}

const settings = () => import("./Settings");
const security = () => import("./Security");
const schedules = () => import("./Schedules");
const machineStatus = () => import("./MachineStatus");

/** Whether a route's page is in a file of its own, which the page has to
 * wait for even if it has the file already: a lazy part suspends the first
 * time it is drawn, and React shows the fallback for 300ms at least before
 * it lets the real page replace it, unless the route changes as a transition
 * (the page being left stays until the new one is ready). */
export function inOwnFile(route: string): boolean {
  const [kind] = route.split("/");
  return kind === "tasks" || kind === "settings" || kind === "devices" || kind === "q" || kind === "c" || kind === "m" || kind === "machine-status";
}

export const SettingsPage = page(() => settings().then((m) => ({ default: m.Settings })));
export const ClientPage = page(() => settings().then((m) => ({ default: m.ClientPage })));
export const DevicesPage = page(() => settings().then((m) => ({ default: m.DevicesPage })));
export const MachinePage = page(() => settings().then((m) => ({ default: m.MachinePage })));
export const MachineStatusPage = page(() => machineStatus().then((m) => ({ default: m.MachineStatusPage })));
export const QuotaPage = page(() => settings().then((m) => ({ default: m.QuotaPage })));
export const RemovedScreen = page(() => settings().then((m) => ({ default: m.RemovedScreen })));
export const SecurityPage = page(() => security().then((m) => ({ default: m.SecurityPage })));
export const PairCodePage = page(() => security().then((m) => ({ default: m.PairCodePage })));
export const SchedulesPage = page(() => schedules().then((m) => ({ default: m.SchedulesPage })));
export const ScheduleDetail = page(() => schedules().then((m) => ({ default: m.ScheduleDetail })));
export const ScheduleCreate = page(() => schedules().then((m) => ({ default: m.ScheduleCreate })));
export const ScheduleStrip = page(() => schedules().then((m) => ({ default: m.ScheduleStrip })));

/** What to fetch ahead: the pages people go to most. */
export const pagesAhead = [settings, schedules, security];
