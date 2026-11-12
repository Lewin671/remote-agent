import { t } from "./i18n";
/** User-facing image errors. Never echo a codec, RPC or OS error verbatim. */
export interface ImageProblem { message: string; retryable: boolean }
export const ORIGINAL_IMAGE_HINT = "原图保住画质；CLI 仍可能缩小过大的图片。";
export function imageProblem(error: string): ImageProblem {
  const e = error.toLowerCase();
  if (/20\s*(mb|mib)|原图超过/.test(e)) return { message: "图片超过 20 MB，关掉原图再发", retryable: false };
  if (/尺寸|像素|pixels|dimensions|canvas|too large|图片超过|图片太大/.test(e)) return { message: "图片尺寸太大，缩小后再试", retryable: false };
  if (/本机副本|重新选择|source.*missing|source.*read/.test(e)) return { message: "这张图片没有本机副本，请移除后重新选择", retryable: false };
  if (/decode|解不出|读不出|读不到|格式|规范|png|jpeg|heic|webp|metadata|exif|corrupt|unsupported/.test(e)) return { message: "读不出这张图片，换成 JPEG 或 PNG 再试", retryable: false };
  if (/quota|空间|配额|storage.*full|disk.*full/.test(e)) return { message: "电脑附件空间不足，清理后重试", retryable: true };
  if (/network|offline|unavailable|fetch|timeout|deadline|expired|网络|连接|离线|断线|超时|过期|重新传/.test(e)) return { message: "图片没能传到电脑，连接恢复后重试", retryable: true };
  return { message: "这张图片处理失败", retryable: false };
}
export function imageFailureLine(errors: string[]): string {
  if (!errors.length) return "";
  const messages = errors.map(e => imageProblem(e).message);
  return new Set(messages).size === 1 ? messages[0] : t("image.attach_failed", { count: errors.length });
}
