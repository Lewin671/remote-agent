import type { Path, FrameStream } from "./path";

/** Physical WebSocket stream adapter for relay legs (unary RPCs run through Core's byte pipe). */
export class RelayPath implements Path {
  openStream(address: string): FrameStream { return new RelayStream(address); }
}

/** Physical socket events only. The logical stream listener belongs to Link. */
export class RelayStream implements FrameStream {
  private readonly socket: WebSocket;
  onOpen?: () => void;
  onFrame?: (data: ArrayBuffer) => void;
  onError?: () => void;
  onEnd?: (code?: number) => void;
  constructor(address: string) {
    const socket = this.socket = new WebSocket(address);
    socket.binaryType = "arraybuffer";
    socket.onopen = () => this.onOpen?.();
    socket.onmessage = event => this.onFrame?.(event.data as ArrayBuffer);
    socket.onerror = () => this.onError?.();
    socket.onclose = event => this.onEnd?.(event?.code);
  }
  get ready() { return this.socket.readyState === WebSocket.OPEN; }
  send(frame: Uint8Array<ArrayBuffer>) { this.socket.send(frame); }
  close() { this.socket.close(); }
}
