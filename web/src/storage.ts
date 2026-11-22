import { checkServer } from "./crypto";
import { clearAllSecurityV4, securityStoreV4, watchAccountLogout } from "./core/security-store-v4";
import { ApprovalMode } from "./gen/remoteagent/v1/event_pb";
import { isKnownAgent } from "./agents";
import type { NewConfig } from "./model";

export interface StoredAccount {
  server: string;
  deviceId: string;
  accountId?: string;
}

export { watchAccountLogout };

/** Forgets the local security credentials, keys, token, and cached sealed history. */
export async function forgetAccount(): Promise<void> {
  await clearAllSecurityV4(securityStoreV4);
}

// Project choices stay in this browser, scoped to its server and device.
// Only launch settings are stored, never encrypted session content.

type ProjectConfig = Omit<NewConfig, "from">;
function projectConfigKey(account: StoredAccount, project: string): string {
  return (
    "ra-project-config/" +
    JSON.stringify([checkServer(account.server), account.deviceId, project])
  );
}

export function loadProjectConfig(
  account: StoredAccount,
  project: string,
): ProjectConfig | undefined {
  try {
    const raw = localStorage.getItem(projectConfigKey(account, project));
    if (!raw) return undefined;
    const c = JSON.parse(raw);
    if (
      !c ||
      typeof c.machineId !== "string" ||
      typeof c.cwd !== "string" ||
      typeof c.model !== "string" ||
      typeof c.effort !== "string" ||
      !isKnownAgent(c.agent) ||
      ![ApprovalMode.UNSPECIFIED, ApprovalMode.ASK, ApprovalMode.AUTO].includes(
        c.mode,
      )
    )
      return undefined;
    return {
      machineId: c.machineId,
      cwd: c.cwd,
      agent: c.agent,
      model: c.model,
      effort: c.effort,
      mode: c.mode,
    };
  } catch {
    return undefined;
  }
}

export function saveProjectConfig(
  account: StoredAccount,
  project: string,
  c: NewConfig,
): void {
  const mode =
    c.mode === ApprovalMode.ASK || c.mode === ApprovalMode.AUTO
      ? c.mode
      : ApprovalMode.UNSPECIFIED;
  localStorage.setItem(
    projectConfigKey(account, project),
    JSON.stringify({
      machineId: c.machineId,
      cwd: c.cwd,
      agent: c.agent,
      model: c.model,
      effort: c.effort,
      mode,
    }),
  );
}

export function clearProjectConfig(
  account: StoredAccount,
  project: string,
): void {
  localStorage.removeItem(projectConfigKey(account, project));
}
