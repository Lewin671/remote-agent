import { expect, it, vi } from "vitest";
import { NotifyKind } from "./gen/remoteagent/v1/relay_pb";
import { machineNotificationKinds } from "./machine-notifications";
import { Remote } from "./remote";

it.each([false, true])("negotiates disk push without breaking old subscriptions (capability=%s)", async (advertised) => {
  const base = [NotifyKind.ATTENTION, NotifyKind.TURN_ENDED, NotifyKind.SECURITY, NotifyKind.SCHEDULED];
  const received: NotifyKind[][] = [];
  const api = {
    getPushKey: vi.fn(async () => (advertised ? { publicKey: "fixture", machineDiskNotifications: true } : { publicKey: "old-server" })),
    setPushSubscription: vi.fn(async (q: { kinds: NotifyKind[] }) => {
      if (!advertised && q.kinds.some((k) => k > 4)) throw Error("unknown notification kind");
      received.push(q.kinds);
    }),
  };
  const remote: Remote = Object.assign(Object.create(Remote.prototype), { api });
  await remote.setPushSubscription("fixture", [...base, NotifyKind.MACHINE_DISK]);
  expect(received).toEqual([advertised ? [...base, NotifyKind.MACHINE_DISK] : base]);
  expect(machineNotificationKinds(base, false)).toEqual(base);
  await remote.setPushSubscription("", []);
  expect(received.at(-1)).toEqual([]);
});
