// How a chosen file is prepared for sending: compressed by default, or the
// original with its metadata removed (images.ts).

import type { Prepared } from "./attach";

export async function prepareForSending(file: Blob, original: boolean): Promise<Prepared> {
  void file;
  void original;
  throw new Error("这个网页还不能处理图片");
}
