import * as l10n from "../i18n";
export type ImageErrorCode = "too_large" | "unsupported" | "decode_failed";

const messages: Record<ImageErrorCode, () => string> = {
  too_large: () => l10n.t("image.image_exceeds_20_mb"),
  unsupported: () => l10n.t("image.this_image_format_is_not_supported"),
  decode_failed: () => l10n.t("image.cannot_read_this_image"),
};

/**
 * An image the client refuses or cannot process. `message` is shown to
 * the user as is; `detail` says what exactly went wrong, for logs and tests.
 */
export class ImageError extends Error {
  readonly code: ImageErrorCode;
  readonly detail: string;

  constructor(code: ImageErrorCode, detail: string, message = messages[code](), readonly problem: "original_too_large" | "dimensions" | "decode" = code === "too_large" ? "original_too_large" : "decode") {
    super(message);
    this.name = "ImageError";
    this.code = code;
    this.detail = detail;
  }
}

/** A file whose structure does not parse; stripping or rotating it could only guess. */
export function malformed(detail: string): ImageError {
  return new ImageError("decode_failed", detail);
}
