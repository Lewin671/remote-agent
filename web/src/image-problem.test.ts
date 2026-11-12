import { t as message } from "./i18n";
import { expect, it } from "vitest";
import { imageFailureLine, imageProblem } from "./image-problem";

it("separates permanent image refusals from recoverable upload failures without exposing diagnostics", () => {
  expect(imageProblem("原图超过 20 MB")).toEqual({ message: message("image.image_exceeds_20_mb_turn_off_original_and_send"), retryable: false });
  expect(imageProblem("the image does not decode").retryable).toBe(false);
  expect(imageProblem("unavailable: machine offline").retryable).toBe(true);
  expect(imageProblem("opaque internal exception")).toEqual({ message: message("image.this_image_could_not_be_processed"), retryable: false });
  expect(imageFailureLine(["图片尺寸太大", "读不出这张图片"])).toBe("2 张图没加上，点图片看原因");
  expect(imageFailureLine(["decode failed", "不是规范的 PNG"])).toBe(message("image.cannot_read_this_image_try_jpeg_or_png_instead"));
});
