import { vi } from "vitest";

/** Controllable SDK callbacks; production DirectChannel still owns the PC. */
export class FakeDataChannel {
  readyState: RTCDataChannelState = "connecting";
  bufferedAmount = 0;
  onopen?: () => void;
  onclose?: () => void;
  onerror?: () => void;
  onbufferedamountlow?: () => void;
  onmessage?: (event: { data: ArrayBuffer }) => void;
  send = vi.fn();
  close() { if (this.readyState === "closed") return; this.readyState = "closed"; this.onclose?.(); }
}
export class FakePC {
  connectionState: RTCPeerConnectionState = "new";
  onicecandidate?: (event: { candidate: { type: string; candidate?: string; toJSON(): RTCIceCandidateInit } | null }) => void;
  onconnectionstatechange?: () => void;
  oniceconnectionstatechange?: () => void;
  onicecandidateerror?: (event: { errorCode: number; address: string }) => void;
  iceConnectionState = "new";
  /** What `getStats` answers: an iterable of stats entries. */
  report: Record<string, unknown>[] = [];
  getStats = vi.fn(async () => ({ forEach: (cb: (v: Record<string, unknown>) => void) => this.report.forEach(cb) }));
  channels: FakeDataChannel[] = [];
  createDataChannel() { const channel = new FakeDataChannel(); this.channels.push(channel); return channel; }
  createOffer = vi.fn();
  createAnswer = vi.fn();
  setLocalDescription = vi.fn(async () => {});
  setRemoteDescription = vi.fn(async () => {});
  addIceCandidate = vi.fn(async () => {});
  close() { this.connectionState = "closed"; for (const channel of this.channels) channel.close(); this.onconnectionstatechange?.(); }
  candidate(value: string | null) { this.onicecandidate?.({ candidate: value === null ? null : { type: value.split(" typ ")[1]?.split(" ")[0] ?? "host", candidate: value, toJSON: () => ({ candidate: value }) } }); }
  ice(value: string) { this.iceConnectionState = value; this.oniceconnectionstatechange?.(); }
  state(value: RTCPeerConnectionState) { this.connectionState = value; this.onconnectionstatechange?.(); }
}
export function fakeRTC() {
  const pcs: FakePC[] = [];
  return { pcs, factory: () => { const pc = new FakePC(); pcs.push(pc); return pc as unknown as RTCPeerConnection; } };
}
