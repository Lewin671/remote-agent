import type { Path, PeerStatus, Forward, FrameStream } from "./path";

/** The existing relay HTTP adapter. Main-WS observations are inputs to this
 * adapter; neither a cached offline bit nor a lost WS cancels a handed-off RPC. */
export class RelayPath implements Path {
  constructor(
    readonly status: (machineId: string) => PeerStatus,
    readonly epoch: () => Promise<string>,
    readonly forward: Forward,
  ) {}
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
