// @vitest-environment happy-dom
import { expect, it } from "vitest";
const fs = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fs)) as { readFileSync(path: string, enc: string): string };
it("keeps the v49 schedule examples consistent with status and date wording", () => {
  const doc = new DOMParser().parseFromString(readFileSync("../docs/design/client-v49.html", "utf8"), "text/html");
  const chapter = doc.querySelector("#schedules")!;
  const create = doc.querySelector("#schedule-create")!.nextElementSibling!;
  expect([...create.querySelectorAll(".phone .tfield > span:first-child")].map(e => e.textContent)).toContain("模型");
  const offline = [...chapter.querySelectorAll(".sess")].find(row => row.textContent?.includes("每周依赖检查并且把"))!;
  expect(offline.querySelector(".dot.off")).not.toBeNull();
  expect(offline.querySelector(".row1 span")?.textContent).toBe("未连接，显示的是上次的状态");
  const running = [...chapter.querySelectorAll(".navbar .tname")].find(e => e.textContent === "盯 PR #482 的回复")!.closest(".phone")!;
  expect(running.querySelector('[aria-disabled="true"]')?.textContent).toBe("立即运行一次");
  const attention = [...chapter.querySelectorAll(".sess")].filter(row => row.querySelector(".dot.wait"));
  expect(attention.length).toBeGreaterThan(0);
  for (const row of attention) {
    expect(row.querySelector(".row1 span")?.textContent).toBe("已停下");
    expect(row.querySelector(".mc")?.textContent).not.toMatch(/今天.*失败：/);
  }
  for (const strip of chapter.querySelectorAll(".tstrip")) {
    expect(strip.textContent).not.toMatch(/下次 \d\d:/);
  }
  for (const row of chapter.querySelectorAll(".schedule-wide .sess")) {
    expect(row.textContent).not.toMatch(/下次 \d\d:|今天.*失败：/);
  }
});
