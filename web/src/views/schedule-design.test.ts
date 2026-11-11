// @vitest-environment happy-dom
import { expect, it } from "vitest";
const fs = "node:fs";
const { readFileSync } = (await import(/* @vite-ignore */ fs)) as { readFileSync(path: string, enc: string): string };
function design() { return new DOMParser().parseFromString(readFileSync("../docs/design/client-v59.html", "utf8"), "text/html"); }
it("shows schedule inbox groups, fixed leading dots, two kinds of badges and the wide empty state", () => {
  const doc = design(), chapter = doc.querySelector("#schedules")!;
  const list = chapter.querySelector("#p-daily")!;
  // Stopped tasks first with an orange dot, then new results with a blue one, which can all be marked read.
  expect(list.querySelectorAll(".rc.warn .dotk.o")).toHaveLength(1);
  expect(list.querySelectorAll(".rc:not(.warn) .dotk:not(.o)").length).toBeGreaterThan(0);
  expect(list.querySelector(".sec")?.textContent).toContain("全部标为已读");
  expect(chapter.querySelectorAll(".schedule-unread")).toHaveLength(0);
  expect(chapter.querySelector("#schedule-wide-empty .main")?.textContent).toBe("选一个任务查看结果");
  expect(chapter.querySelector("#schedule-wide-empty .sel")).toBeNull();
  // A blue dot for new results only, orange numbers for what needs the user.
  const badges = chapter.querySelector("#schedule-badges + .hint + .screens")!;
  expect(badges.querySelectorAll(".tabs .d")).toHaveLength(1);
  expect(badges.querySelectorAll(".tabs .n")).toHaveLength(3);
  expect(chapter.querySelectorAll("#schedule-android ~ .screens .phone")).toHaveLength(4);
});
it("puts the result above history, prompt and settings, and includes running and menu examples", () => {
  const doc = design(), detail = doc.querySelector("#d-new .scroll")!;
  expect(detail.firstElementChild?.className).toBe("res");
  expect(detail.textContent!.indexOf("更早")).toBeLessThan(detail.textContent!.indexOf("让 agent 做什么"));
  expect(detail.textContent!.indexOf("让 agent 做什么")).toBeLessThan(detail.textContent!.indexOf("设置"));
  expect(detail.querySelector(".grp .mi.h .lead .dotk")).not.toBeNull();
  expect(detail.querySelector(".res table")).not.toBeNull();
  expect(doc.querySelector("#d-run .res.run")?.textContent).toContain("正在运行");
  expect(doc.querySelector("#d-menu .sheet")?.textContent).toContain("标为未读");
  expect(doc.querySelector("#d-menu .sheet .red")?.textContent).toBe("删除任务…");
  expect(doc.querySelector("#schedule-create")!.nextElementSibling!.textContent).toContain("模型");
});
