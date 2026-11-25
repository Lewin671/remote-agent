import { t as message } from "../i18n";
import { demoDevices } from "../test/remote-fixture";
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
it("shows a QR code and dispatches SAS confirmation, approve, and replacement candidate toggles through Core", async () => {
  const { create } = await import("@bufbuild/protobuf");
  const { AddDeviceActionKind, AddDeviceStep, getCoreHost } = await import("../core/host");
  const { AddDeviceViewModelSchema, PairingCandidateItemViewSchema } = await import(
    "../gen/remoteagent/core/v1/security_view_pb"
  );
  const { TextSchema } = await import("../gen/remoteagent/core/v1/text_pb");

  const r = demo("role=authority");
  const coreHost = getCoreHost(r);
  vi.spyOn(coreHost, "addDeviceOpen").mockImplementation(() => undefined);
  vi.spyOn(coreHost, "addDeviceClose").mockImplementation(() => undefined);
  const actSpy = vi.spyOn(coreHost, "addDeviceAction").mockImplementation(() => undefined);
  vi.spyOn(coreHost, "viewAddDevice").mockReturnValue(
    create(AddDeviceViewModelSchema, {
      step: AddDeviceStep.COMPARE_SAS,
      formattedCode: "1234 5678",
      qrUrl: "https://app.example.com/#/pair?code=12345678",
      sasEmoji: ["🚀", "🧭", "🔑", "🧩"],
      sasConfirmedChecked: false,
      canApprove: false,
      canCancel: true,
      replaceOldLabel: create(TextSchema, { key: "security.also_remove_old_devices" }),
      replaceOldHint: create(TextSchema, { key: "security.also_remove_old_devices_hint" }),
      replacementCandidates: [
        create(PairingCandidateItemViewSchema, {
          deviceId: "old-pixel-stale",
          title: "Pixel 3",
          subtitle: create(TextSchema, {
            join: [
              create(TextSchema, { literal: "Android 14 · Pixel 3 · old-pix1" }),
              create(TextSchema, { key: "time.yesterday" }),
            ],
            separatorLiteral: " · ",
          }),
          selected: true,
        }),
        create(PairingCandidateItemViewSchema, {
          deviceId: "old-pixel-active",
          title: "Active Pixel",
          subtitle: create(TextSchema, {
            join: [
              create(TextSchema, { literal: "Android 15 · Pixel 3 · old-pix2" }),
              create(TextSchema, { key: "time.just_now" }),
            ],
            separatorLiteral: " · ",
          }),
          selected: false,
        }),
      ],
    }),
  );

  await mount(<PairCodePage remote={r} onBack={() => {}} />);
  expect(qr.generate).toHaveBeenCalledExactlyOnceWith("https://app.example.com/#/pair?code=12345678");
  expect(host.querySelector(".security-qr")?.getAttribute("src")).toBe("data:image/gif;base64,pairing-fixture");
  expect(host.textContent).toContain(message("security.also_remove_old_devices"));
  expect(host.textContent).toContain("Android 14 · Pixel 3");
  expect(host.textContent).toContain("old-pix1");
  expect(host.textContent).toContain(message("time.yesterday"));
  expect(host.textContent).toContain("old-pix2");
  expect(host.textContent).toContain(message("time.just_now"));
  expect(button(message("security.allow_to_join")).disabled).toBe(true);

  const checkboxes = [...host.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')];
  expect(checkboxes).toHaveLength(3);
  expect(checkboxes[0].checked).toBe(false);
  expect(checkboxes[1].checked).toBe(true);
  expect(checkboxes[2].checked).toBe(false);

  await act(async () => checkboxes[0].click());
  expect(actSpy).toHaveBeenCalledWith(AddDeviceActionKind.SET_SAS_CONFIRMED, { flag: true });

  await act(async () => checkboxes[1].click());
  expect(actSpy).toHaveBeenCalledWith(AddDeviceActionKind.TOGGLE_REMOVE_CANDIDATE, {
    deviceId: "old-pixel-stale",
    flag: false,
  });
});

it("labels an unfinished candidate and offers explicit removal to pair again", async () => {
  const { DevicesPage } = await import("./Settings");
  const { DeviceKind } = await import("../gen/remoteagent/v1/relay_pb");
  const { patchSyncState } = await import("../test/remote-fixture");
  const r = demo("role=authority");
  vi.spyOn(r, "loadDevices").mockResolvedValue();
  const nextDevices = new Map(demoDevices(r));
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
