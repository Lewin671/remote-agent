// @vitest-environment happy-dom
import { expect, it } from "vitest";
const fs = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fs)) as { readFileSync(path: string, enc: string): string };
function design() { return new DOMParser().parseFromString(readFileSync("../docs/design/client-v57.html", "utf8"), "text/html"); }
it("shows schedule inbox groups, fixed leading dots, two kinds of badges and the wide empty state", () => {
  const doc = design(), chapter = doc.querySelector("#schedules")!;
  expect(chapter.textContent).toContain("最近");
  expect(chapter.textContent).toContain("全部标为已读");
  expect(chapter.querySelectorAll('.srow .lead [aria-label="有新结果"]').length).toBeGreaterThan(0);
  expect(chapter.querySelectorAll('.srow .lead [aria-label="需要你处理"]').length).toBeGreaterThan(0);
  expect(chapter.querySelectorAll(".schedule-unread")).toHaveLength(0);
  expect(chapter.querySelector("#schedule-wide-empty .dm")?.textContent).toBe("选一个任务查看结果");
  expect(chapter.querySelector("#schedule-wide-empty .sel")).toBeNull();
  expect(chapter.querySelectorAll("#schedule-badges + .badges .d")).toHaveLength(1);
  expect(chapter.querySelectorAll(".phone.android")).toHaveLength(4);
});
it("puts the result above history, prompt and settings, and includes running and menu examples", () => {
  const doc = design(), detail = doc.querySelector("#d-new .scroll")!;
  expect(detail.firstElementChild?.className).toBe("res");
  expect(detail.textContent!.indexOf("更早")).toBeLessThan(detail.textContent!.indexOf("让 agent 做什么"));
  expect(detail.textContent!.indexOf("让 agent 做什么")).toBeLessThan(detail.textContent!.indexOf("设置"));
  expect(detail.querySelector(".hist .lead i")).not.toBeNull();
  expect(detail.querySelector(".rep table")).not.toBeNull();
  expect(doc.querySelector("#d-run .running")?.textContent).toContain("正在运行");
  expect(doc.querySelector("#d-menu .sheet")?.textContent).toContain("标为未读");
  expect(doc.querySelector("#d-menu .sheet .red")?.textContent).toBe("删除任务…");
  expect(doc.querySelector("#schedule-create")!.nextElementSibling!.textContent).toContain("模型");
});
