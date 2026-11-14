import { afterEach, expect, test, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { Link } from "./link";
import { RelayPath, RelayStream } from "./relay-path";
import { SyntheticRemote } from "../test/remote-fixture";
import { CommandResultSchema, MachineCapabilitiesSchema, MachineMetaSchema } from "../gen/remoteagent/v1/remote_pb";
import { PROTOCOL_VERSION } from "../protocol";
import { parseHeader, SecurityError } from "../device-security";
import { newSlot } from "./request";
import type { Envelope } from "../gen/remoteagent/v1/envelope_pb";

afterEach(() => vi.unstubAllGlobals());

test("operation gates and capability checks use Link despite stale relay discovery", async () => {
  const r = await SyntheticRemote.create();
  const relay = r.deviceLink.router.path;
  const forward = vi.fn(async (q: { command: Envelope }) => {
    const h = parseHeader(new Uint8Array(q.command.securityHeader));
    return { result: await r.seal({ kind: 6, requestId: h.request }, CommandResultSchema, create(CommandResultSchema)) };
  });
  (r as unknown as { api: unknown }).api = { forward };
  r.conn = "offline";
  r.machines.set(r.machine, { id: r.machine, online: false, lastSeenMs: 0n, metaRev: 0n, meta: create(MachineMetaSchema, { protocolVersion: PROTOCOL_VERSION - 1, capabilities: { fileUpload: false } }) });
  const link = new Link(new RelayPath(() => ({ reachable: true, ready: true, authorized: true, protocolVersion: PROTOCOL_VERSION, capabilities: create(MachineCapabilitiesSchema, { fileUpload: true, scheduledTasks: true }) }), relay.epoch, relay.forward));
  Object.defineProperty(r, "deviceLink", { value: link });
  try {
    const changed = vi.fn();
    const unsubscribe = link.subscribe(changed);
    link.changed();
    expect(changed).toHaveBeenCalledTimes(1);
    unsubscribe();
    Object.assign(r, { listed: true });
    expect(r.presence.get(r.machine)?.online).toBe(true);
    expect(r.machines.get(r.machine)?.online).toBe(false);
    expect(r.fileUploadIssue(r.machine)).toBe("");
    await r.machineCommand(r.machine, { case: "createSession", value: { cwd: "/work/example", fileIds: ["file"] } });
    expect(forward).toHaveBeenCalledTimes(1);
    const command = vi.spyOn(r, "machineCommand").mockResolvedValue(create(CommandResultSchema, { kind: { case: "schedule", value: { resultJson: new TextEncoder().encode("{\"id\":\"task\"}") } } }));
    await expect(r.changeSchedule(r.machine, "pause", { operation: "pause", taskId: "task" })).resolves.toMatchObject({ id: "task" });
    expect(command).toHaveBeenCalled();
    command.mockRestore();
  } finally { await r.dispose(); }
});

test("reachable capability metadata cannot authorize a command or erase prior evidence", async () => {
  const r = await SyntheticRemote.create();
  const forward = vi.fn();
  (r as unknown as { api: unknown }).api = { forward };
  const endpoint = r as unknown as { sealSecureMessage: () => Promise<Envelope> };
  const seal = vi.spyOn(endpoint, "sealSecureMessage").mockRejectedValue(new SecurityError("lease"));
  try {
    const slot = newSlot();
    const fresh = await r.machineCommand(r.machine, { case: "getSettings", value: {} }, slot).catch(error => error);
    expect(forward).not.toHaveBeenCalled();
    expect(fresh).toMatchObject({ outcome: "not_sent" });
    slot.evidence.includePrior(true);
    await expect(r.machineCommand(r.machine, { case: "getSettings", value: {} }, slot)).rejects.toMatchObject({ outcome: "unknown" });
    expect(forward).not.toHaveBeenCalled();
  } finally { seal.mockRestore(); await r.dispose(); }
});

test("physical path endings are separate from Link's relay-only stream-close policy", () => {
  let socket!: { onclose?: () => void };
  vi.stubGlobal("WebSocket", class {
    constructor() { socket = this; }
    onclose?: () => void;
    close() { this.onclose?.(); }
  });
  const path = new RelayStream("wss://example.com/v1/term");
  const pathEnd = vi.fn(), productClose = vi.fn(), stop = vi.fn();
  path.onEnd = pathEnd;
  const link = new Link(new RelayPath(() => ({ reachable: true, ready: true, authorized: true }), async () => "epoch", vi.fn()));
  const stream = link.openStream(productClose, stop);
  socket.onclose?.();
  expect(pathEnd).toHaveBeenCalledTimes(1);
  expect(productClose).not.toHaveBeenCalled();
  link.endRelayStreams("closed");
  stream.end("late path callback");
  expect(productClose).toHaveBeenCalledExactlyOnceWith("closed");
  expect(stop).toHaveBeenCalledTimes(1);
});
