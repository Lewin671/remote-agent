// @vitest-environment happy-dom
import { afterEach, expect, it, vi } from "vitest";
import { pushPrefs, restorePushPrefs, setPushPrefs } from "./push";
import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import { Remote } from "./remote";

afterEach(() => { localStorage.clear(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it("registers existing push preferences for the newly paired device without asking permission", async () => {
  localStorage.setItem("ra-push", JSON.stringify({ attention: true, turns: true }));
  const requestPermission = vi.fn();
  vi.stubGlobal("Notification", { permission: "granted", requestPermission });
  vi.stubGlobal("PushManager", class {});
  const subscription = { endpoint: "https://push.example.com/subscription" };
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ pushManager: { getSubscription: async () => subscription } }) } });
  const setPushSubscription = vi.fn(async () => undefined);
  class Probe extends Remote { constructor() { super({ server: "https://ra.example.com", deviceId: "new-device" } as any, {} as any); } }
  const remote = new Probe();
  remote.setPushSubscription = setPushSubscription;
  remote.refresh = vi.fn(async () => undefined);
  remote.loadDevices = vi.fn(async () => undefined);
  remote.loadPrefs = vi.fn(async () => undefined);
  await (remote as any).onFrame({ case: "ready", value: {} });
  await vi.waitFor(() => expect(setPushSubscription).toHaveBeenCalled());
  expect(setPushSubscription).toHaveBeenCalledWith(JSON.stringify(subscription), [NotifyKind.ATTENTION, NotifyKind.TURN_ENDED, NotifyKind.SECURITY]);
  expect(requestPermission).not.toHaveBeenCalled();
});

it("does not register a subscription after the account was removed during the read", async () => {
  localStorage.setItem("ra-push", JSON.stringify({ attention: true }));
  vi.stubGlobal("Notification", { permission: "granted" });
  vi.stubGlobal("PushManager", class {});
  let finish!: (s: object) => void;
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ pushManager: { getSubscription: () => new Promise(resolve => { finish = resolve; }) } }) } });
  const remote = { conn: "online", removed: false, setPushSubscription: vi.fn() } as unknown as Remote;
  const work = restorePushPrefs(remote);
  await vi.waitFor(() => expect(finish).toBeDefined());
  remote.removed = true;
  finish({ endpoint: "https://push.example.com/subscription" });
  await work;
  expect(remote.setPushSubscription).not.toHaveBeenCalled();
});

it("keeps security notifications when session notifications are turned off during restore", async () => {
  localStorage.setItem("ra-push", JSON.stringify({ attention: true, turns: true }));
  vi.stubGlobal("Notification", { permission: "granted" });
  vi.stubGlobal("PushManager", class {});
  // The browser's one subscription, as its push manager keeps it.
  let browser: { endpoint: string; unsubscribe(): Promise<boolean> } | null = null;
  const pushManager = {
    getSubscription: async () => browser,
    subscribe: async () => (browser = { endpoint: "https://push.example.com/subscription", unsubscribe: async () => { browser = null; return true; } }),
  };
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ pushManager }) } });
  // What the server holds for this device.
  let server: number[] = [];
  let key!: (k: string) => void;
  const asked = vi.fn(() => new Promise<string>((resolve) => { key = resolve; }));
  const remote = { conn: "online", removed: false, pushKey: asked, setPushSubscription: vi.fn(async (_sub: string, kinds: number[]) => { server = kinds; }) } as unknown as Remote;
  const restoring = restorePushPrefs(remote);
  await vi.waitFor(() => expect(asked).toHaveBeenCalled());
  // The user turns everything off while the server's key is on its way.
  const off = setPushPrefs(remote, { attention: false, turns: false });
  await new Promise((resolve) => setTimeout(resolve));
  key("AAAA");
  await Promise.all([restoring, off]);
  expect(pushPrefs()).toEqual({ attention: false, turns: false });
  expect(server).toEqual([NotifyKind.SECURITY]);
  expect(browser).not.toBeNull();
});

it("restores SECURITY alone with both session preferences disabled", async () => {
  vi.stubGlobal("Notification", { permission: "granted" });
  vi.stubGlobal("PushManager", class {});
  const subscription = { endpoint: "https://push.example.com/security" };
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ pushManager: { getSubscription: async () => subscription } }) } });
  const remote = { conn: "online", removed: false, setPushSubscription: vi.fn() } as unknown as Remote;
  await restorePushPrefs(remote);
  expect(remote.setPushSubscription).toHaveBeenCalledWith(JSON.stringify(subscription), [NotifyKind.SECURITY]);
});
