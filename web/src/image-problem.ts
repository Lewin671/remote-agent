import * as l10n from "./i18n";
import * as i18n from "./i18n";
import { t } from "./i18n";
/** User-facing image errors. Never echo a codec, RPC or OS error verbatim. */
export interface ImageProblem { message: string; retryable: boolean }
export function originalImageHint(): string { return i18n.t("image.original_hint"); }
/** Stable local categories; translated messages never decide aggregation. */
export function imageProblemKind(error: string) {
  if (["original_too_large", "dimensions", "source_missing", "decode", "storage_full", "upload_failed", "processing_failed"].includes(error))
    return error as "original_too_large" | "dimensions" | "source_missing" | "decode" | "storage_full" | "upload_failed" | "processing_failed";
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
    case "original_too_large": return { message: l10n.t("image.image_exceeds_20_mb_turn_off_original_and_send"), retryable: false };
    case "dimensions": return { message: l10n.t("image.image_dimensions_are_too_large_resize_it_and_retry"), retryable: false };
    case "source_missing": return { message: l10n.t("image.this_image_has_no_local_copy_remove_it_and"), retryable: false };
    case "decode": return { message: l10n.t("image.cannot_read_this_image_try_jpeg_or_png_instead"), retryable: false };
    case "storage_full": return { message: l10n.t("image.the_computer_s_attachment_storage_is_full_clear_space"), retryable: true };
    case "upload_failed": return { message: l10n.t("image.the_image_could_not_reach_the_computer_retry_when"), retryable: true };
    default: return { message: l10n.t("image.this_image_could_not_be_processed"), retryable: false };
  }
}
export function imageFailureLine(errors: string[]): string {
  if (!errors.length) return "";
  const kinds = errors.map(imageProblemKind);
  return new Set(kinds).size === 1 ? imageProblem(errors[0]).message : t("image.attach_failed", { count: errors.length });
}
