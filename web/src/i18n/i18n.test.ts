// @vitest-environment happy-dom
import { afterEach, expect, it } from "vitest";
import vectors from "./locale-vectors.json";
import { localeStore, resolveLocale, t } from "./index";
import { imageFailureLine } from "../image-problem";

afterEach(() => { localeStore.overrideForTesting(); localStorage.removeItem("ra.language"); });
it("uses the shared language matching contract", () => {
  for (const vector of vectors) expect(resolveLocale(vector.preference, vector.system)).toBe(vector.expected);
});
it("pins stage 0 to Chinese, including saved English and English system preferences", () => {
  localeStore.setPreference("en");
  expect(localeStore.snapshot()).toBe("zh-CN");
  expect(t("common.cancel")).toBe("取消");
});
it("renders reviewed-source Chinese byte for byte and draft English only with an override", () => {
  expect(t("common.copy")).toBe("复制");
  expect(imageFailureLine(["pixels", "decode"])).toBe("2 张图没加上，点图片看原因");
  localeStore.overrideForTesting("en");
  expect(t("common.copy")).toBe("Copy");
  expect(imageFailureLine(["pixels", "decode"])).toBe("2 images could not be attached. Tap an image for details.");
  // Error interpretation and retryability stay independent of language.
  expect(imageFailureLine(["pixels", "pixels"])).toBe("图片尺寸太大，缩小后再试");
});
it("selects native plural categories for zero, one, two and a large count", () => {
  expect(t("image.attach_failed", { count: 1 }, "en")).toBe("1 image could not be attached. Tap it for details.");
  for (const count of [0, 2, 1000]) {
    expect(t("image.attach_failed", { count }, "en")).toBe(`${count} images could not be attached. Tap an image for details.`);
    expect(t("image.attach_failed", { count }, "zh-CN")).toBe(`${count} 张图没加上，点图片看原因`);
  }
  expect(() => t("image.attach_failed", { count: -1 })).toThrow("Invalid plural count");
  expect(() => t("image.attach_failed", { count: 1.5 })).toThrow("Invalid plural count");
});
it("notifies subscribers and updates document language", () => {
  let calls = 0;
  const stop = localeStore.subscribe(() => calls++);
  localeStore.overrideForTesting("en");
  expect(document.documentElement.lang).toBe("en");
  expect(calls).toBe(1);
  stop();
  localeStore.overrideForTesting();
  expect(calls).toBe(1);
});
// Compile-time contracts checked by the web build, without executing invalid calls.
function typeContracts() {
  // @ts-expect-error unknown key
  t("unknown.key");
  // @ts-expect-error required count
  t("image.attach_failed");
  // @ts-expect-error count type
  t("image.attach_failed", { count: "2" });
  // @ts-expect-error extra parameter
  t("common.cancel", { extra: true });
}
void typeContracts;
