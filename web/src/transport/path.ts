import type { Client, ConnectError } from "@connectrpc/connect";
import type { ClientService } from "../gen/remoteagent/v1/relay_pb";
import type { Envelope } from "../gen/remoteagent/v1/envelope_pb";

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

export interface PathAttempt {
  readonly direct?: boolean;
  readonly attemptId: string;
  prepare(): Promise<void>;
  send(machineId: string, command: Envelope, signal?: AbortSignal): Promise<ForwardReply>;
  refusal(error: ConnectError): { reason: string } | undefined;
  refreshAuth(prior: boolean, signal?: AbortSignal): Promise<void>;
}

/** Paths see routing and sealed frames, never commands, keys or outcomes. */
export interface Path {
  attempt(handoff: () => void): PathAttempt;
  openStream(address: string): FrameStream;
}
