// @vitest-environment happy-dom
import { expect, it } from "vitest";
const fs = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fs)) as { readFileSync(path: string, enc: string): string };
it("keeps the v55 schedule examples consistent with status and date wording", () => {
  const doc = new DOMParser().parseFromString(readFileSync("../docs/design/client-v55.html", "utf8"), "text/html");
  const chapter = doc.querySelector("#schedules")!;
  const create = doc.querySelector("#schedule-create")!.nextElementSibling!;
  expect([...create.querySelectorAll(".phone .tfield > span:first-child")].map(e => e.textContent)).toContain("模型");
  const offline = [...chapter.querySelectorAll(".sess")].find(row => row.textContent?.includes("每周依赖检查并且把"))!;
  expect(offline.querySelector(".dot.off")).not.toBeNull();
  expect(offline.querySelector(".mc")?.textContent).toContain("未连接");
  const running = [...chapter.querySelectorAll(".navbar .tname")].find(e => e.textContent === "盯 PR #482 的回复")!.closest(".phone")!;
  expect(running.querySelector('[aria-disabled="true"]')?.textContent).toBe("立即运行一次");
  const attention = [...chapter.querySelectorAll(".sess")].filter(row => row.querySelector(".dot.wait"));
  expect(attention.length).toBeGreaterThan(0);
  for (const row of attention) {
    expect(row.querySelector(".mc")?.textContent).toContain("已停下");
    expect(row.querySelector(".pv")?.textContent).not.toMatch(/今天.*失败：/);
  }
  // Stopped tasks say 已停下 everywhere; 已暂停 is reserved for manual pauses.
  for (const row of chapter.querySelectorAll("#schedule-shared-copy tbody td")) {
    expect(row.textContent).not.toMatch(/任务已暂停/);
  }
  for (const sub of chapter.querySelectorAll(".phone.gray .navbar .title > span")) {
    expect(sub.querySelector(".dot")).toBeNull();
  }
  expect(chapter.querySelector(".tasknew")).toBeNull();
  for (const strip of chapter.querySelectorAll(".tstrip")) {
    expect(strip.textContent).not.toMatch(/下次 \d\d:/);
  }
  for (const row of chapter.querySelectorAll(".desk .sess")) {
    expect(row.textContent).not.toMatch(/下次 \d\d:|今天.*失败：/);
  }
});

it("includes unread, crowded and no-notification examples for phone, Android and wide layouts", () => {
  const doc = new DOMParser().parseFromString(readFileSync("../docs/design/client-v55.html", "utf8"), "text/html");
  const list = doc.querySelector("#schedule-list")!.nextElementSibling!;
  expect(list.querySelectorAll(".sess.schedule-line.unread").length).toBeGreaterThan(0);
  expect(list.querySelector('[aria-label="104 条未读通知"]')?.textContent).toBe("99+");
  const android = doc.querySelector("#schedule-android")!.nextElementSibling!;
  expect(android.querySelectorAll(".phone.android")).toHaveLength(4);
  expect(android.querySelector(".schedule-unread-dot")).not.toBeNull();
  expect(doc.querySelector("#schedule-detail")!.nextElementSibling!.querySelector(".schedule-unread-dot")).not.toBeNull();
  const wideCrowded = doc.querySelector("#schedule-wide-crowded")!;
  expect(wideCrowded.querySelector(".tail")).toBeNull();
  expect(wideCrowded.querySelectorAll(".sess")).toHaveLength(11); // 2 attention + 9 ongoing
  expect(wideCrowded.querySelector(".stabs .schedule-unread")?.getAttribute("aria-label")).toBe("121 条待查看");
  for (const example of [doc.querySelector("#schedule-no-notifications")!, doc.querySelector("#schedule-wide-no-notifications")!]) {
    expect(example.querySelector(".schedule-unread")).toBeNull();
    expect(example.querySelector(".schedule-unread-dot")).toBeNull();
    for (const row of example.querySelectorAll(".sess")) {
      expect(row.querySelector(".row1 > span")?.textContent).toBe("");
      expect(row.querySelector(".mc")?.textContent).toContain("下次");
    }
  }
});
