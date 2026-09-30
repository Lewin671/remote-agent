export type ImageErrorCode = "too_large" | "unsupported" | "decode_failed";

const messages: Record<ImageErrorCode, string> = {
  too_large: "图片超过 20 MB",
  unsupported: "不支持这种图片格式",
  decode_failed: "读不出这张图片",
};

/**
 * An image the client refuses or cannot process. `message` is shown to
 * the user as is; `detail` says what exactly went wrong, for logs and tests.
 */
export class ImageError extends Error {
  readonly code: ImageErrorCode;
  readonly detail: string;

  constructor(code: ImageErrorCode, detail: string, message = messages[code]) {
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
