import { t as message } from "../i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { DemoRemote } from "../dev/demo";
import {
  DeviceActions,
  ManagementEntry,
  PairCodePage,
} from "./DeviceManagement";
// This UI test exercises pairing/SAS, not Vite's cold QR-library import.
// QR loading behavior is covered in lazy-qr.test.tsx.
const qr = vi.hoisted(() => ({ generate: vi.fn(async (_url: string) => "data:image/gif;base64,pairing-fixture") }));
vi.mock("../qr-code", () => ({ pairingQRCode: qr.generate }));
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
const remotes: DemoRemote[] = [];
afterEach(() => {
  if (root) act(() => root.unmount());
  host?.remove();
  for (const r of remotes.splice(0)) r.dispose();
  qr.generate.mockClear();
});
async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(node));
}
const button = (label: string) =>
  [...host.querySelectorAll("button")].find((b) => b.textContent === label)!;
async function click(label: string) {
  await act(async () => button(label).click());
}
function demo(q: string) {
  const r = DemoRemote.create(new URLSearchParams(q), async () => {});
  remotes.push(r);
  r.start();
  return r;
}
it.each(["authority", "member", "machine"])(
  "offers add, remove and archive actions only to the authority: %s",
  async (role) => {
    const r = demo(`role=${role}&archive=1`);
    const go = vi.fn();
    await mount(<ManagementEntry remote={r} go={go} />);
    expect(!!button(message("home.add_device"))).toBe(role === "authority");
    await act(async () =>
      root.render(<DeviceActions remote={r} id="mini" machine go={go} />),
    );
    expect(!!button(message("security.rejoin_and_resume_sessions"))).toBe(role === "authority");
    expect(!!button(message("security.delete_this_computer_s_records"))).toBe(role === "authority");
  },
);
it("shows a QR code and waits for explicit candidate-first SAS confirmation", async () => {
  const r = demo("role=authority&pair=compare");
  await mount(<PairCodePage remote={r} onBack={() => {}} />);
  await click(message("security.generate_pairing_code"));
  expect(qr.generate).toHaveBeenCalledExactlyOnceWith("https://app.example.com/#/pair?code=12345678");
  expect(host.querySelector(".security-qr")?.getAttribute("src")).toBe("data:image/gif;base64,pairing-fixture");
  expect(button(message("security.allow_to_join")).disabled).toBe(true);
  await act(async () =>
    host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click(),
  );
  await click(message("security.allow_to_join"));
  expect(host.textContent).toContain(message("security.device_joined"));
});
it("leaves a failed cancellation retryable after the local SAS key is gone", async () => {
  const r = demo("role=authority");
  vi.spyOn(r, "cancelPairing").mockRejectedValueOnce(new Error("cancel acknowledgement lost"));
  await mount(<PairCodePage remote={r} onBack={() => {}} />);
  await click(message("security.generate_pairing_code"));
  await click(message("security.cancel_pairing"));
  expect(host.textContent).toContain("cancel acknowledgement lost");
  expect(button(message("security.generate_pairing_code"))).toBeTruthy();
});

it.each([false, true])("keeps approved delivery after leaving the page (approval still pending: %s)", async (pending) => {
  const r = demo("role=authority&pair=compare");
  const cancel = vi.spyOn(r, "cancelPairing");
  let release!: () => void;
  const wait = new Promise<void>(resolve => { release = resolve; });
  const origApprove = r.approvePairing.bind(r);
  vi.spyOn(r, "approvePairing").mockImplementation(async (pid, confirmed) => {
    if (pending) await wait;
    return origApprove(pid, confirmed);
  });
  await mount(<PairCodePage remote={r} onBack={() => {}} />);
  await click(message("security.generate_pairing_code"));
  await act(async () => host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
  await click(message("security.allow_to_join"));
  await act(async () => root.render(<p>设备列表</p>));
  expect(cancel).not.toHaveBeenCalled();
  await act(async () => release());
});
it("labels an unfinished candidate and offers explicit removal to pair again", async () => {
  const { DevicesPage } = await import("./Settings");
  const { DeviceKind } = await import("../gen/remoteagent/v1/relay_pb");
  const { patchSyncState } = await import("../test/remote-fixture");
  const r = demo("role=authority");
  vi.spyOn(r, "loadDevices").mockResolvedValue();
  const nextDevices = new Map(r.devices);
  nextDevices.set("candidate", { id: "candidate", kind: DeviceKind.CLIENT, createdMs: 0n, lastSeenMs: 0n, metaRev: 0n, pairingIncomplete: true });
  patchSyncState(r, { devices: nextDevices });
  await mount(<DevicesPage remote={r} go={() => {}} onBack={() => {}} />);
  expect(host.textContent).toContain(message("settings.unfinished_pairing"));
  expect(host.textContent).toContain("移除后重新配对");
  expect(host.querySelectorAll(".row.client")).toHaveLength(3);
  const remove = vi.spyOn(r, "removeDevice").mockResolvedValue();
  await click(message("security.remove_unfinished_pairing"));
  await act(async () => [...document.querySelectorAll<HTMLButtonElement>("button")].find(b => b.textContent === message("common.remove"))!.click());
  expect(remove).toHaveBeenCalledWith("candidate");
});

it("calls this device 当前 in its list and detail", async () => {
  const { DevicesPage, ClientPage } = await import("./Settings");
  const r = demo("role=authority");
  await mount(<DevicesPage remote={r} go={() => {}} onBack={() => {}} />);
  expect(host.querySelector(".row.client .v")?.textContent).toBe(message("settings.current"));
  await act(async () => root.render(<ClientPage remote={r} id={r.account.deviceId} onBack={() => {}} />));
  expect(host.querySelector(".navbar")?.textContent).toContain(message("settings.current"));
  expect(host.querySelector(".navbar")?.textContent).not.toContain("当前客户端");
});

it("offers same-device replacement candidates checked by default and honors toggling", async () => {
  const { create } = await import("@bufbuild/protobuf");
  const { SecurityPairingInfoSchema, SecurityPairingReplacementCandidateSchema } = await import(
    "../gen/remoteagent/core/v1/security_pb"
  );
  const { TextSchema } = await import("../gen/remoteagent/core/v1/text_pb");
  const { DeviceKind } = await import("../gen/remoteagent/v1/relay_pb");

  for (const keepChecked of [true, false]) {
    const r = demo("role=authority");
    vi.spyOn(r, "startPairing").mockResolvedValue(
      create(SecurityPairingInfoSchema, {
        pairingId: "pair-replace",
        code: "12345678",
        url: "https://app.example.com/#/pair?code=12345678",
        expiresMs: BigInt(Date.now() + 600_000),
      }),
    );
    vi.spyOn(r, "pollPairing").mockResolvedValue(
      create(SecurityPairingInfoSchema, {
        pairingId: "pair-replace",
        code: "12345678",
        url: "https://app.example.com/#/pair?code=12345678",
        expiresMs: BigInt(Date.now() + 600_000),
        joined: true,
        joinerKind: DeviceKind.CLIENT,
        sasEmoji: ["🚀", "🧭", "🔑", "🧩"],
        joinerMetaReceived: true,
        removeOldDefault: true,
        replaceOldLabel: create(TextSchema, { key: "security.also_remove_old_devices" }),
        replaceOldHint: create(TextSchema, { key: "security.also_remove_old_devices_hint" }),
        replacementCandidates: [
          create(SecurityPairingReplacementCandidateSchema, {
            deviceId: "old-chrome-id",
            shortId: "old-chr1",
            displayName: "Chrome · macOS",
            description: "Chrome · macOS",
          }),
        ],
      }),
    );
    const approve = vi.spyOn(r, "approvePairing").mockResolvedValue(
      create(SecurityPairingInfoSchema, { pairingId: "pair-replace", delivered: true }),
    );
    vi.spyOn(r, "finishPairingDelivery").mockResolvedValue(
      create(SecurityPairingInfoSchema, { pairingId: "pair-replace", delivered: true }),
    );

    await mount(<PairCodePage remote={r} onBack={() => {}} />);
    await click(message("security.generate_pairing_code"));
    expect(host.textContent).toContain(message("security.also_remove_old_devices"));
    expect(host.textContent).toContain("Chrome · macOS");
    expect(host.textContent).toContain("old-chr1");

    const checkboxes = [...host.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')];
    expect(checkboxes).toHaveLength(2);
    expect(checkboxes[0].checked).toBe(false);
    expect(checkboxes[1].checked).toBe(true);

    await act(async () => checkboxes[0].click());
    if (!keepChecked) {
      await act(async () => checkboxes[1].click());
      expect(checkboxes[1].checked).toBe(false);
    }
    await click(message("security.allow_to_join"));
    expect(approve).toHaveBeenCalledWith(
      "pair-replace",
      true,
      keepChecked ? ["old-chrome-id"] : [],
    );

    act(() => root.unmount());
    host.remove();
  }
});
