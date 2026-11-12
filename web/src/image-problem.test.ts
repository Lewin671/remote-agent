import { expect, it } from "vitest";
import { imageFailureLine, imageProblem } from "./image-problem";

it("separates permanent image refusals from recoverable upload failures without exposing diagnostics", () => {
  expect(imageProblem("原图超过 20 MB")).toEqual({ message: "图片超过 20 MB，关掉原图再发", retryable: false });
  expect(imageProblem("the image does not decode").retryable).toBe(false);
  expect(imageProblem("unavailable: machine offline").retryable).toBe(true);
  expect(imageProblem("opaque internal exception")).toEqual({ message: "这张图片处理失败", retryable: false });
  expect(imageFailureLine(["图片尺寸太大", "读不出这张图片"])).toBe("2 张图没加上，点图片看原因");
  expect(imageFailureLine(["decode failed", "不是规范的 PNG"])).toBe("读不出这张图片，换成 JPEG 或 PNG 再试");
});
