import { t as message } from "../i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { AccountEntry } from "./AuthoritySetup";
import { AuthoritySetup, authorityStartup } from "../authority-setup";
vi.mock("../authority-setup", async importOriginal => ({
  ...await importOriginal<typeof import("../authority-setup")>(),
  AuthoritySetup: vi.fn(),
  authorityStartup: vi.fn(async () => ({ kind: "empty" })),
}));
(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
afterEach(() => {
  if (root) act(() => root.unmount());
  host?.remove();
  vi.clearAllMocks();
  location.hash = "";
  localStorage.clear();
});
async function mount(setup = {} as AuthoritySetup) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root.render(<AccountEntry setup={setup} onDone={done} />),
  );
}
const done = vi.fn();
const button = (label: string) =>
  [...host.querySelectorAll("button")].find((b) => (b.getAttribute("aria-label") ?? b.textContent) === label)!;
async function click(label: string) {
  await act(async () => {
    button(label).click();
    if (label === "加入已有账户") await import("./Pairing");
  });
}
async function input(label: string, value: string) {
  const el = host.querySelector<HTMLInputElement>(
    `input[aria-label="${label}"]`,
  )!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
it("has three unauthenticated entries and keeps pairing direction", async () => {
  await mount();
  for (const label of ["创建账户", "加入已有账户", "用恢复码找回管理权"])
    expect(button(label)).toBeTruthy();
  const rows = [...host.querySelectorAll(".grp .entry-row")];
  expect(rows.map(row => row.querySelector("small")?.textContent)).toEqual([
    message("security.this_device_becomes_the_managing_client"), message("security.join_with_a_code_from_the_managing_client"), message("security.use_when_the_managing_client_is_lost"),
  ]);
  await click(message("security.join_an_existing_account"));
  expect(host.textContent).toContain(
    message("security.on_the_managing_client_open_settings_add_device_and"),
  );
});
it("requires offline backup before continuing creation and does not claim success on a failed save", async () => {
  vi.mocked(authorityStartup).mockResolvedValue({ kind: "empty" });
  const setup = {
    prepareCreation: vi.fn(async () => {
      vi.mocked(authorityStartup).mockResolvedValue({
        kind: "create",
        phase: "PREPARED",
        codeSaved: false,
      });
      return { code: "example-code", storagePersisted: false };
    }),
    confirmCreationCodeSaved: vi.fn(async () => {
      throw new Error("storage failed");
    }),
    continueCreation: vi.fn(),
  } as unknown as AuthoritySetup;
  await mount(setup);
  await click(message("security.create_account"));
  await input("服务器", "https://ra.example.com");
  await click(message("security.generate_recovery_code"));
  expect(host.textContent).toContain(message("security.the_browser_did_not_promise_to_retain_data"));
  expect(button(message("security.continue_creation")).disabled).toBe(true);
  await click(message("security.continue_creation"));
  expect(setup.confirmCreationCodeSaved).not.toHaveBeenCalled();
  await act(async () =>
    host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click(),
  );
  await click(message("security.continue_creation"));
  expect(host.textContent).toContain("storage failed");
  expect(setup.continueCreation).not.toHaveBeenCalled();
  expect(done).not.toHaveBeenCalled();
});
it("resumes a published creation with no replacement genesis or invented backup", async () => {
  vi.mocked(authorityStartup).mockResolvedValue({
    kind: "create",
    phase: "PUBLISHED",
    codeSaved: false,
  });
  const setup = {
    continueCreation: vi.fn(async () => {}),
    adoptCreationWithoutCode: vi.fn(async () => ({})),
    prepareCreation: vi.fn(),
  } as unknown as AuthoritySetup;
  await mount(setup);
  expect(host.textContent).toContain("恢复码不会再次显示");
  await click(message("security.continue_creation"));
  expect(setup.prepareCreation).not.toHaveBeenCalled();
  expect(setup.adoptCreationWithoutCode).toHaveBeenCalledOnce();
  expect(done).toHaveBeenCalledOnce();
});
it("queries a recovery draft on restart and permits completing it without claiming a backup", async () => {
  vi.mocked(authorityStartup).mockResolvedValue({
    kind: "recovery",
    phase: "PUBLISHED",
    codeSaved: false,
    readyMs: 10n,
  });
  const setup = {
    resumeRecovery: vi.fn(async () => ({ phase: 2, readyMs: 10n })),
    completeRecovery: vi.fn(async () => ({})),
    confirmRecoveryCodeSaved: vi.fn(),
  } as unknown as AuthoritySetup;
  await mount(setup);
  expect(host.textContent).toContain("完成恢复时会自动移除旧身份并轮换内容密钥");
  expect(host.textContent).toContain("中断后请继续原恢复");
  await click(message("security.submit_or_check_original_recovery"));
  await click(message("security.complete_recovery"));
  expect(setup.completeRecovery).toHaveBeenCalledOnce();
  expect(setup.confirmRecoveryCodeSaved).not.toHaveBeenCalled();
  expect(done).toHaveBeenCalledOnce();
});
it("finishes a retried published creation when backup confirmation already adopts and clears the draft", async () => {
  vi.mocked(authorityStartup).mockResolvedValue({ kind: "empty" });
  const setup = {
    prepareCreation: vi.fn(async () => {
      vi.mocked(authorityStartup).mockResolvedValue({
        kind: "create",
        phase: "PUBLISHED",
        codeSaved: false,
      });
      return { code: "example-code", storagePersisted: true };
    }),
    confirmCreationCodeSaved: vi.fn(async () => {
      vi.mocked(authorityStartup).mockResolvedValue({
        kind: "account",
        pending: false,
      });
    }),
    continueCreation: vi.fn(),
  } as unknown as AuthoritySetup;
  await mount(setup);
  await click(message("security.create_account"));
  await input("服务器", "https://ra.example.com");
  await click(message("security.generate_recovery_code"));
  await act(async () =>
    host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click(),
  );
  await click(message("security.continue_creation"));
  expect(setup.confirmCreationCodeSaved).toHaveBeenCalledOnce();
  expect(setup.continueCreation).not.toHaveBeenCalled();
  expect(done).toHaveBeenCalledOnce();
});
