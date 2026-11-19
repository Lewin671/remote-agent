import * as l10n from "../i18n";
import { createElement, lazy, type ComponentProps, type ComponentType } from "react";
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
  let cached: C | undefined;
  let pending: Promise<{ default: C }> | undefined;
  const resolve = () =>
    (pending ??= Promise.resolve()
      .then(load)
      .then(
        (m) => {
          cached = m.default;
          return m;
        },
        async () => {
          // Starting over replaces everything: until then, nothing to show.
          if (await chunkMissing(true)) return new Promise<{ default: C }>(() => undefined);
          const gone = Gone as unknown as C;
          cached = gone;
          return { default: gone };
        },
      ));
  const Lazy = lazy(resolve);
  function Wrapper(props: ComponentProps<C>) {
    return cached ? createElement(cached, props) : createElement(Lazy, props);
  }
  return Object.assign(Wrapper, { preload: resolve });
}

const settings = () => import("./Settings");
const security = () => import("./Security");
const schedules = () => import("./Schedules");
const machineStatus = () => import("./MachineStatus");
const usage = () => import("./Usage");

/** Whether a route's page is in a file of its own, which the page has to
 * wait for even if it has the file already: a lazy part suspends the first
 * time it is drawn, and React shows the fallback for 300ms at least before
 * it lets the real page replace it, unless the route changes as a transition
 * (the page being left stays until the new one is ready). */
export function inOwnFile(route: string): boolean {
  const [kind] = route.split("/");
  return kind === "tasks" || kind === "settings" || kind === "devices" || kind === "q" || kind === "c" || kind === "m" || kind === "machine-status" || kind === "u";
}

export const SettingsPage = page(() => settings().then((m) => ({ default: m.Settings })));
export const ClientPage = page(() => settings().then((m) => ({ default: m.ClientPage })));
export const DevicesPage = page(() => settings().then((m) => ({ default: m.DevicesPage })));
export const MachinePage = page(() => settings().then((m) => ({ default: m.MachinePage })));
export const MachineStatusPage = page(() => machineStatus().then((m) => ({ default: m.MachineStatusPage })));
export const UsagePage = page(() => usage().then((m) => ({ default: m.UsagePage })));
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

/** Resolves the lazy chunk matching the current hash route before Workspace mounts
 * so a refresh on a sub-route renders the real page on frame 1 without flashing fallback. */
export function preloadRoute(route: string): Promise<void> {
  const [kind, sub] = route.split("/");
  const tasks: Array<Promise<unknown>> = [];
  if (kind === "tasks") {
    tasks.push(SchedulesPage.preload(), ScheduleDetail.preload(), ScheduleCreate.preload(), ScheduleStrip.preload());
  } else if (kind === "settings" && sub === "rejoin") {
    tasks.push(import("./Pairing"));
  } else if (kind === "settings" && (sub === "security" || sub === "add-device")) {
    tasks.push(SecurityPage.preload(), PairCodePage.preload());
  } else if (kind === "settings" || kind === "devices" || kind === "q" || kind === "c" || kind === "m") {
    tasks.push(SettingsPage.preload(), DevicesPage.preload(), QuotaPage.preload(), ClientPage.preload(), MachinePage.preload());
  } else if (kind === "machine-status") {
    tasks.push(MachineStatusPage.preload());
  } else if (kind === "u") {
    tasks.push(UsagePage.preload());
  }
  return tasks.length > 0 ? Promise.all(tasks).then(() => undefined) : Promise.resolve();
}
