export interface FrameStream {
  readonly ready: boolean;
  onOpen?: () => void;
  onFrame?: (data: ArrayBuffer) => void;
  onError?: () => void;
  onEnd?: (code?: number) => void;
  send(frame: Uint8Array<ArrayBuffer>): void;
  close(): void;
}

/** Paths open physical stream transports without decoding commands, keys, or outcomes. */
export interface Path {
  openStream(address: string): FrameStream;
}
