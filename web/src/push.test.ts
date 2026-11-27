// @vitest-environment happy-dom
import { create } from "@bufbuild/protobuf";
import { afterEach, expect, it, vi } from "vitest";
import { closeScheduleNotification, pushPrefs, restorePushPrefs, setPushPrefs } from "./push";
import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import { EventSchema } from "./gen/remoteagent/core/v1/app_pb";
import { DemoSecuritySchema, SeedDemoWorldEventSchema } from "./gen/remoteagent/core/v1/security_pb";
import { getCoreHost } from "./core/host";
import { Remote, type StreamLegCallbacks } from "./remote";

afterEach(() => { localStorage.clear(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

it("registers existing push preferences for the newly paired device without asking permission", async () => {
  localStorage.setItem("ra-push", JSON.stringify({ attention: true, turns: true }));
  const requestPermission = vi.fn();
  vi.stubGlobal("Notification", { permission: "granted", requestPermission });
  vi.stubGlobal("PushManager", class {});
  const subscription = { endpoint: "https://push.example.com/subscription" };
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ pushManager: { getSubscription: async () => subscription } }) } });
  const setPushSubscription = vi.fn(async () => undefined);
  class Probe extends Remote {
    constructor() { super({ server: "https://ra.example.com", deviceId: "new-device" } as any, undefined); }
    override securityConfig() { return undefined; }
    override openStreamLeg(legId: bigint, _token: string, callbacks: StreamLegCallbacks) {
      callbacks.onReady(legId, BigInt(Date.now()));
    }
    override relayRpc() {}
  }
  const remote = new Probe();
  remote.setPushSubscription = setPushSubscription;
  try {
    getCoreHost(remote).dispatch(
      create(EventSchema, {
        event: {
          case: "seedDemoWorld",
          value: create(SeedDemoWorldEventSchema, {
            clearDirectReports: true,
            security: create(DemoSecuritySchema, { isMember: true, selfDeviceId: "new-device", authorityDeviceId: "new-device" }),
          }),
        },
      }),
    );
    remote.start();
    await vi.waitFor(() => expect(setPushSubscription).toHaveBeenCalled());
    expect(setPushSubscription).toHaveBeenCalledWith(JSON.stringify(subscription), [NotifyKind.ATTENTION, NotifyKind.TURN_ENDED, NotifyKind.SECURITY, NotifyKind.SCHEDULED]);
    expect(requestPermission).not.toHaveBeenCalled();
  } finally {
    remote.dispose();
  }
});

it("does not register a subscription after the account was removed during the read", async () => {
  localStorage.setItem("ra-push", JSON.stringify({ attention: true }));
  vi.stubGlobal("Notification", { permission: "granted" });
  vi.stubGlobal("PushManager", class {});
  let finish!: (s: object) => void;
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ pushManager: { getSubscription: () => new Promise(resolve => { finish = resolve; }) } }) } });
  const remote = { removed: false, setPushSubscription: vi.fn() } as unknown as Remote;
  const work = restorePushPrefs(remote, () => !remote.removed);
  await vi.waitFor(() => expect(finish).toBeDefined());
  (remote as { removed: boolean }).removed = true;
  finish({ endpoint: "https://push.example.com/subscription" });
  await work;
  expect(remote.setPushSubscription).not.toHaveBeenCalled();
});

it("keeps security and scheduled notifications when session notifications are turned off during restore", async () => {
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
  const remote = { removed: false, pushKey: asked, setPushSubscription: vi.fn(async (_sub: string, kinds: number[]) => { server = kinds; }) } as unknown as Remote;
  const restoring = restorePushPrefs(remote, () => !remote.removed);
  await vi.waitFor(() => expect(asked).toHaveBeenCalled());
  // The user turns everything off while the server's key is on its way.
  const off = setPushPrefs(remote, { attention: false, turns: false });
  await new Promise((resolve) => setTimeout(resolve));
  key("AAAA");
  await Promise.all([restoring, off]);
  expect(pushPrefs()).toEqual({ attention: false, turns: false, scheduled: true });
  expect(server).toEqual([NotifyKind.SECURITY, NotifyKind.SCHEDULED]);
  expect(browser).not.toBeNull();
});

it("restores SECURITY and SCHEDULED with both session preferences disabled", async () => {
  vi.stubGlobal("Notification", { permission: "granted" });
  vi.stubGlobal("PushManager", class {});
  const subscription = { endpoint: "https://push.example.com/security" };
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ pushManager: { getSubscription: async () => subscription } }) } });
  const remote = { removed: false, setPushSubscription: vi.fn() } as unknown as Remote;
  await restorePushPrefs(remote, () => !remote.removed);
  expect(remote.setPushSubscription).toHaveBeenCalledWith(JSON.stringify(subscription), [NotifyKind.SECURITY, NotifyKind.SCHEDULED]);
});

it("allows scheduled results to be disabled without disabling security or turn results", async () => {
  vi.stubGlobal("Notification", { permission: "granted" });
  const sub = { endpoint: "https://push.example.com/results" };
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ pushManager: { getSubscription: async () => sub } }) } });
  const remote = { setPushSubscription: vi.fn() } as unknown as Remote;
  await setPushPrefs(remote, { attention: false, turns: true, scheduled: false });
  expect(remote.setPushSubscription).toHaveBeenCalledWith(JSON.stringify(sub), [NotifyKind.TURN_ENDED, NotifyKind.SECURITY]);
  expect(pushPrefs().scheduled).toBe(false);
});

it("waits for the worker and closes only exact scheduled-notification tags", async () => {
  const tag = "shared/ scheduled/4"; // Session IDs are opaque; never parse by prefix.
  const notice = { sessionId: tag, notificationSeq: 17 };
  const expected = `${tag}/scheduled:17/NOTIFY_KIND_SCHEDULED`;
  const exact = { tag: expected, close: vi.fn() };
  const other = { tag: `${tag}/scheduled:18/NOTIFY_KIND_SCHEDULED`, close: vi.fn() };
  const attention = { tag: `${tag}/scheduled:17/NOTIFY_KIND_ATTENTION`, close: vi.fn() };
  const getNotifications = vi.fn().mockResolvedValue([exact, other, attention]);
  let ready!: (value: unknown) => void;
  vi.stubGlobal("navigator", { serviceWorker: { ready: new Promise(resolve => { ready = resolve; }) } });
  const work = closeScheduleNotification(notice);
  expect(getNotifications).not.toHaveBeenCalled();
  ready({ getNotifications }); await work;
  expect(getNotifications).toHaveBeenCalledExactlyOnceWith({ tag: expected });
  expect(exact.close).toHaveBeenCalledTimes(1);
  expect(other.close).not.toHaveBeenCalled(); expect(attention.close).not.toHaveBeenCalled();
});

it.each([
  { sessionId: "", notificationSeq: 7 },
  { sessionId: "session", notificationSeq: 0 },
  { sessionId: "session", notificationSeq: -1 },
  { sessionId: "session", notificationSeq: Number.MAX_SAFE_INTEGER + 1 },
])("skips runs that cannot identify one exact notification: %j", async ref => {
  const getNotifications = vi.fn();
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ getNotifications }) } });
  await closeScheduleNotification(ref);
  expect(getNotifications).not.toHaveBeenCalled();
});

it("silently tolerates unsupported workers and failures at every notification API step", async () => {
  const notice = { sessionId: "session-r", notificationSeq: 7 };
  vi.stubGlobal("navigator", {});
  await expect(closeScheduleNotification(notice)).resolves.toBeUndefined();
  vi.stubGlobal("navigator", { serviceWorker: { get ready() { throw new Error("not available"); } } });
  await expect(closeScheduleNotification(notice)).resolves.toBeUndefined();
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.reject(new Error("worker failed")) } });
  await expect(closeScheduleNotification(notice)).resolves.toBeUndefined();
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ getNotifications: async () => { throw new Error("not allowed"); } }) } });
  await expect(closeScheduleNotification(notice)).resolves.toBeUndefined();
  const close = vi.fn();
  const tag = "session-r/scheduled:7/NOTIFY_KIND_SCHEDULED";
  vi.stubGlobal("navigator", { serviceWorker: { ready: Promise.resolve({ getNotifications: async () => [
    { tag, close: () => { throw new Error("cannot close"); } }, { tag, close },
  ] }) } });
  await expect(closeScheduleNotification(notice)).resolves.toBeUndefined();
  expect(close).toHaveBeenCalledTimes(1);
});
