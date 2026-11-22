import { create } from "@bufbuild/protobuf";
import { t as message } from "../i18n";
// @vitest-environment happy-dom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { AccountEntry } from "./AuthoritySetup";
import type { SecurityClient } from "../core/security-client";
import {
  SecurityBootstrapKind,
  SecurityWorkflowStatusSchema,
  type SecurityWorkflowStatus,
} from "../gen/remoteagent/core/v1/security_pb";
import { requestAuthorityPersistence } from "../core/security-client";

vi.mock("../core/security-client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../core/security-client")>()),
  requestAuthorityPersistence: vi.fn(async () => false),
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

function makeStatus(patch: Omit<Partial<SecurityWorkflowStatus>, "$typeName" | "$unknown"> = {}): SecurityWorkflowStatus {
  return create(SecurityWorkflowStatusSchema, {
    bootstrap: SecurityBootstrapKind.EMPTY,
    ...patch,
  });
}

function makeClient(overrides: Partial<SecurityClient> = {}): SecurityClient {
  let status = makeStatus();
  return {
    bootstrap: vi.fn(async () => status),
    getStatus: vi.fn(async () => status),
    dismissUpgradeNotice: vi.fn(async () => {
      status = makeStatus({ ...status, legacyRepairNotice: false });
      return status;
    }),
    ...overrides,
  } as unknown as SecurityClient;
}

async function mount(client = makeClient()) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root.render(<AccountEntry client={client} onDone={done} />),
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

it("displays and dismisses legacy upgrade repair notice on entry screen", async () => {
  let st = makeStatus({ legacyRepairNotice: true });
  const dismissUpgradeNotice = vi.fn(async () => {
    st = makeStatus({ legacyRepairNotice: false });
    return st;
  });
  const client = makeClient({
    bootstrap: vi.fn(async () => st),
    getStatus: vi.fn(async () => st),
    dismissUpgradeNotice,
  });
  await mount(client);
  expect(host.textContent).toContain(message("security.upgrade_repair_title"));
  expect(host.textContent).toContain(message("security.upgrade_repair_notice"));
  await click(message("common.got_it"));
  expect(dismissUpgradeNotice).toHaveBeenCalledOnce();
  expect(host.textContent).not.toContain(message("security.upgrade_repair_title"));
});

it("requires offline backup before continuing creation and does not claim success on a failed save", async () => {
  vi.mocked(requestAuthorityPersistence).mockResolvedValue(false);
  let st = makeStatus();
  const client = makeClient({
    bootstrap: vi.fn(async () => st),
    getStatus: vi.fn(async () => st),
    prepareCreation: vi.fn(async () => {
      st = makeStatus({
        bootstrap: SecurityBootstrapKind.PENDING_CREATE,
        pendingOp: { kind: "create", phase: "PREPARED", codeSaved: false } as SecurityWorkflowStatus["pendingOp"],
      });
      return { recoveryCode: "example-code", resumedDraft: false, codeSaved: false };
    }),
    confirmCreationCodeSaved: vi.fn(async () => {
      throw new Error("storage failed");
    }),
    continueCreation: vi.fn(),
  });
  await mount(client);
  await click(message("security.create_account"));
  await input("服务器", "https://ra.example.com");
  await click(message("security.generate_recovery_code"));
  expect(host.textContent).toContain(message("security.the_browser_did_not_promise_to_retain_data"));
  expect(button(message("security.continue_creation")).disabled).toBe(true);
  await click(message("security.continue_creation"));
  expect(client.confirmCreationCodeSaved).not.toHaveBeenCalled();
  await act(async () =>
    host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click(),
  );
  await click(message("security.continue_creation"));
  expect(host.textContent).toContain("storage failed");
  expect(client.continueCreation).not.toHaveBeenCalled();
  expect(done).not.toHaveBeenCalled();
});
it("resumes a published creation with no replacement genesis or invented backup", async () => {
  const st = makeStatus({
    bootstrap: SecurityBootstrapKind.PENDING_CREATE,
    pendingOp: { kind: "create", phase: "PUBLISHED", codeSaved: false } as SecurityWorkflowStatus["pendingOp"],
  });
  const client = makeClient({
    bootstrap: vi.fn(async () => st),
    getStatus: vi.fn(async () => st),
    continueCreation: vi.fn(async () => ({} as never)),
    adoptCreationWithoutCode: vi.fn(async () => ({} as never)),
    prepareCreation: vi.fn(),
  });
  await mount(client);
  expect(host.textContent).toContain("恢复码不会再次显示");
  await click(message("security.continue_creation"));
  expect(client.prepareCreation).not.toHaveBeenCalled();
  expect(client.adoptCreationWithoutCode).toHaveBeenCalledOnce();
  expect(done).toHaveBeenCalledOnce();
});
it("queries a recovery draft on restart and permits completing it without claiming a backup", async () => {
  const st = makeStatus({
    bootstrap: SecurityBootstrapKind.PENDING_RECOVERY,
    pendingOp: { kind: "recovery", phase: "PUBLISHED", codeSaved: false, recoveryReadyMs: 10n } as SecurityWorkflowStatus["pendingOp"],
  });
  const client = makeClient({
    bootstrap: vi.fn(async () => st),
    getStatus: vi.fn(async () => st),
    resumeRecovery: vi.fn(async () => ({ phase: 2, readyMs: 10n } as never)),
    completeRecovery: vi.fn(async () => ({} as never)),
    confirmRecoveryCodeSaved: vi.fn(),
  });
  await mount(client);
  expect(host.textContent).toContain("完成恢复时会自动移除旧身份并轮换内容密钥");
  expect(host.textContent).toContain("中断后请继续原恢复");
  await click(message("security.submit_or_check_original_recovery"));
  await click(message("security.complete_recovery"));
  expect(client.completeRecovery).toHaveBeenCalledOnce();
  expect(client.confirmRecoveryCodeSaved).not.toHaveBeenCalled();
  expect(done).toHaveBeenCalledOnce();
});
it("finishes a retried published creation when backup confirmation already adopts and clears the draft", async () => {
  vi.mocked(requestAuthorityPersistence).mockResolvedValue(true);
  let st = makeStatus();
  const client = makeClient({
    bootstrap: vi.fn(async () => st),
    getStatus: vi.fn(async () => st),
    prepareCreation: vi.fn(async () => {
      st = makeStatus({
        bootstrap: SecurityBootstrapKind.PENDING_CREATE,
        pendingOp: { kind: "create", phase: "PUBLISHED", codeSaved: false } as SecurityWorkflowStatus["pendingOp"],
      });
      return { recoveryCode: "example-code", resumedDraft: false, codeSaved: false };
    }),
    confirmCreationCodeSaved: vi.fn(async () => {
      st = makeStatus({ bootstrap: SecurityBootstrapKind.READY, isMember: true });
    }),
    continueCreation: vi.fn(),
  });
  await mount(client);
  await click(message("security.create_account"));
  await input("服务器", "https://ra.example.com");
  await click(message("security.generate_recovery_code"));
  await act(async () =>
    host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click(),
  );
  await click(message("security.continue_creation"));
  expect(client.confirmCreationCodeSaved).toHaveBeenCalledOnce();
  expect(client.continueCreation).not.toHaveBeenCalled();
  expect(done).toHaveBeenCalledOnce();
});
