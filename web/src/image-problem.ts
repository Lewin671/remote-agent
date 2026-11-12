import * as i18n from "./i18n";
import { t } from "./i18n";
/** User-facing image errors. Never echo a codec, RPC or OS error verbatim. */
export interface ImageProblem { message: string; retryable: boolean }
export function originalImageHint(): string { return i18n.t("image.original_hint"); }
/** Stable local categories; translated messages never decide aggregation. */
export function imageProblemKind(error: string) {
  const e = error.toLowerCase();
  if (/20\s*(mb|mib)|原图超过/.test(e)) return "original_too_large" as const;
  if (/尺寸|像素|pixels|dimensions|canvas|too large|图片超过|图片太大/.test(e)) return "dimensions" as const;
  if (/本机副本|重新选择|source.*missing|source.*read/.test(e)) return "source_missing" as const;
  if (/decode|解不出|读不出|读不到|格式|规范|png|jpeg|heic|webp|metadata|exif|corrupt|unsupported/.test(e)) return "decode" as const;
  if (/quota|空间|配额|storage.*full|disk.*full/.test(e)) return "storage_full" as const;
  if (/network|offline|unavailable|fetch|timeout|deadline|expired|网络|连接|离线|断线|超时|过期|重新传/.test(e)) return "upload_failed" as const;
  return "processing_failed" as const;
}
export function imageProblem(error: string): ImageProblem {
  switch (imageProblemKind(error)) {
    case "original_too_large": return { message: "图片超过 20 MB，关掉原图再发", retryable: false };
    case "dimensions": return { message: "图片尺寸太大，缩小后再试", retryable: false };
    case "source_missing": return { message: "这张图片没有本机副本，请移除后重新选择", retryable: false };
    case "decode": return { message: "读不出这张图片，换成 JPEG 或 PNG 再试", retryable: false };
    case "storage_full": return { message: "电脑附件空间不足，清理后重试", retryable: true };
    case "upload_failed": return { message: "图片没能传到电脑，连接恢复后重试", retryable: true };
    default: return { message: "这张图片处理失败", retryable: false };
  }
}
export function imageFailureLine(errors: string[]): string {
  if (!errors.length) return "";
  const kinds = errors.map(imageProblemKind);
  return new Set(kinds).size === 1 ? imageProblem(errors[0]).message : t("image.attach_failed", { count: errors.length });
}
