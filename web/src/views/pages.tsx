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
        <div className="et">页面已更新</div>
        <div className="muted small">这一部分没能取到，重新加载就好。</div>
        <div className="big">
          <button className="btn pri" onClick={() => location.reload()}>
            重新加载
          </button>
        </div>
      </div>
    </div>
  );
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function page<C extends ComponentType<any>>(load: () => Promise<{ default: C }>) {
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

// Which of the three files have come, to tell a route that has to wait for its
// page from one that has it.
const fetched = { settings: false, security: false, schedules: false };
const part = <T,>(name: keyof typeof fetched, load: () => Promise<T>) => () => load().then((m) => ((fetched[name] = true), m));
const settings = part("settings", () => import("./Settings"));
const security = part("security", () => import("./Security"));
const schedules = part("schedules", () => import("./Schedules"));

/** Whether a route's page is still to be fetched. */
export function pageToFetch(route: string): boolean {
  const [kind, arg] = route.split("/");
  if (kind === "tasks") return !fetched.schedules;
  if (kind === "settings") return !(arg === "security" || arg?.startsWith("add-device") ? fetched.security : fetched.settings);
  if (kind === "devices" || kind === "q" || kind === "c" || kind === "m") return !fetched.settings;
  return false;
}

export const SettingsPage = page(() => settings().then((m) => ({ default: m.Settings })));
export const ClientPage = page(() => settings().then((m) => ({ default: m.ClientPage })));
export const DevicesPage = page(() => settings().then((m) => ({ default: m.DevicesPage })));
export const MachinePage = page(() => settings().then((m) => ({ default: m.MachinePage })));
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
