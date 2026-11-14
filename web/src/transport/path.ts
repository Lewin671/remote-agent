import type { CallOptions, Client } from "@connectrpc/connect";
import type { ClientService } from "../gen/remoteagent/v1/relay_pb";
import type { MachineCapabilities } from "../gen/remoteagent/v1/remote_pb";

/** Discovery and path readiness are separate from permission to execute.
 * Relay-only discovery keeps the existing authenticated metadata semantics. */
export interface PeerStatus {
  reachable: boolean;
  ready: boolean;
  authorized: boolean;
  protocolVersion?: number;
  capabilities?: MachineCapabilities;
}
export type Forward = Client<typeof ClientService>["forward"];
export type ForwardFrame = Parameters<Forward>[0];
export type ForwardReply = Awaited<ReturnType<Forward>>;

export interface FrameStream {
  readonly ready: boolean;
  onOpen?: () => void;
  onFrame?: (data: ArrayBuffer) => void;
  onError?: () => void;
  onEnd?: (code?: number) => void;
  send(frame: Uint8Array<ArrayBuffer>): void;
  close(): void;
}

/** Paths see routing and sealed frames, never commands, keys or outcomes. */
export interface Path {
  status(machineId: string): PeerStatus;
  epoch(): Promise<string>;
  openStream(address: string): FrameStream;
  forward(frame: ForwardFrame, options?: CallOptions): Promise<ForwardReply>;
}
