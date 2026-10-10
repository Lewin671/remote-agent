// @vitest-environment happy-dom
import { afterEach, expect, it, vi } from "vitest";
import { restorePushPrefs } from "./push";
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
  expect(setPushSubscription).toHaveBeenCalledWith(JSON.stringify(subscription), [NotifyKind.ATTENTION, NotifyKind.TURN_ENDED]);
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
