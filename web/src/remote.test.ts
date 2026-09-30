import { fromBinary } from "@bufbuild/protobuf";
import { envelopeSigned } from "./crypto";
import { HeaderSchema, type Envelope } from "./gen/remoteagent/v1/envelope_pb";
import { Code, ConnectError } from "@connectrpc/connect";
import { expect, it, vi } from "vitest";
import { CommandError, COMMAND_TTL_MS, newSlot, Remote, uncertain, type RequestSlot } from "./remote";

it("reuses a request slot only when the outcome is unknown", () => {
  // Timed out or cut off: the command may have run; a retry must reuse
  // the slot so the daemon answers from its log.
  expect(uncertain(new CommandError("unknown", "连接超时"))).toBe(true);
  expect(uncertain(new CommandError("aborted", "outcome unknown"))).toBe(true);
  expect(uncertain(new ConnectError("reset", Code.Internal))).toBe(true);
  // A definite answer: the next attempt starts afresh.
  expect(uncertain(new CommandError("unavailable", "电脑不在线"))).toBe(false);
  expect(uncertain(new CommandError("failed_precondition", "cwd is not a directory"))).toBe(false);
  expect(uncertain(new Error("意外的回复"))).toBe(false);
});

it("gives each slot a fresh ID and a deadline a minute out", () => {
  const a = newSlot(1000);
  const b = newSlot(1000);
  expect(a.requestId).not.toBe(b.requestId);
  expect(a.requestId).toMatch(/^[0-9a-f]{32}$/);
  expect(a.deadline).toBe(1000 + COMMAND_TTL_MS);
});

it("reports a command unknown when a retry finds the machine gone", async () => {
  // A Remote whose server answers "no result from the machine" and then
  // "machine offline".
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  class Probe extends Remote {
    constructor() {
      super({ server: "https://ra.example.com", root: key, devicePrivate: device.privateKey, deviceId: "d" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key });
    }
  }
  const r = new Probe();
  const replies = [new ConnectError("no reply from the machine in time", Code.DeadlineExceeded), new ConnectError("machine is offline", Code.Unavailable)];
  (r as unknown as { api: object }).api = {
    forward: async () => {
      throw replies.shift();
    },
  };
  // Between the two tries forward waits a second.
  const e = await r.machineCommand("m", { case: "listModels", value: {} }).catch((x) => x);
  expect(e).toBeInstanceOf(CommandError);
  expect((e as CommandError).code).toBe("unknown");
}, 10_000);

it("keeps a message of unknown outcome on its request when the machine is then offline", async () => {
  const { DemoRemote } = await import("./dev/demo");
  const r = DemoRemote.create(new URLSearchParams());
  const replies = [new CommandError("unknown", "连接超时"), new CommandError("unavailable", "电脑不在线，指令没有发出")];
  const seen: string[] = [];
  r.command = async (_s: string, _c: unknown, slot?: RequestSlot) => {
    seen.push(slot!.requestId);
    throw replies.shift() ?? new CommandError("unavailable", "电脑不在线，指令没有发出");
  };
  r.send("s-idle", "hi");
  await vi.waitFor(() => expect(r.outbox("s-idle")[0].status).toBe("failed"));
  const o = r.outbox("s-idle")[0];
  r.retrySend("s-idle", o.sendId);
  await vi.waitFor(() => expect(seen).toHaveLength(2));
  await vi.waitFor(() => expect(r.outbox("s-idle")[0].status).toBe("failed"));
  r.retrySend("s-idle", o.sendId);
  await vi.waitFor(() => expect(seen).toHaveLength(3));
  expect(new Set(seen).size).toBe(1);
  expect(r.outbox("s-idle")[0].unknown).toBe(true);
});

it("signs every command it forwards with the device key, naming itself the sender", async () => {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  class Probe extends Remote {
    constructor() {
      super({ server: "https://ra.example.com", root: key, devicePrivate: device.privateKey, deviceId: "dev1" }, { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key });
    }
  }
  const r = new Probe();
  let sent: Envelope | undefined;
  (r as unknown as { api: object }).api = {
    forward: async (req: { command: Envelope }) => {
      sent = req.command;
      throw new ConnectError("machine is offline", Code.Unavailable);
    },
  };
  await r.machineCommand("m", { case: "listModels", value: {} }).catch(() => undefined);
  expect(sent).toBeTruthy();
  expect(fromBinary(HeaderSchema, sent!.header).sender).toBe("dev1");
  expect(sent!.signature.length).toBe(64);
  expect(await crypto.subtle.verify("Ed25519", device.publicKey, sent!.signature as Uint8Array<ArrayBuffer>, envelopeSigned(sent!))).toBe(true);
});

it("replays a project pin on another client's sealed copy after a conflict", async () => {
  const key = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  const device = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"])) as CryptoKeyPair;
  const keys = { wrap: key, meta: key, command: key, prefs: key, deviceMeta: key };
  class Probe extends Remote {
    constructor(id: string) {
      super({ server: "https://ra.example.com", root: key, devicePrivate: device.privateKey, deviceId: id }, keys);
    }
  }
  // One server object; two clients share the account key.
  let stored: { revision: bigint; envelope?: Envelope } = { revision: 0n };
  let before: (() => Promise<void>) | undefined;
  const api = {
    getAccountPrefs: async () => ({ object: { objectId: "project-prefs", revision: stored.revision, envelope: stored.envelope } }),
    putAccountPrefs: async (req: { expectedRevision: bigint; envelope: Envelope }) => {
      const b = before;
      before = undefined;
      await b?.();
      if (req.expectedRevision !== stored.revision) return { applied: false, object: { objectId: "project-prefs", ...stored } };
      stored = { revision: req.expectedRevision + 1n, envelope: req.envelope };
      return { applied: true, object: { objectId: "project-prefs", ...stored } };
    },
  };
  const a = new Probe("a");
  const b = new Probe("b");
  for (const r of [a, b]) {
    (r as unknown as { api: object }).api = api;
    r.conn = "online";
    await r.loadPrefs();
  }
  await a.changePrefs("pin", "/Users/me/Code/foo");
  await b.loadPrefs();
  // a hides blog from its copy while b pins billing in between.
  before = async () => {
    await b.changePrefs("pin", "/Users/me/Code/billing");
  };
  expect(await a.changePrefs("hide", "/Users/me/Code/blog")).toBe(true);
  await b.loadPrefs();
  for (const r of [a, b]) {
    const p = r.projectPrefs();
    expect(p.pinned.map((x) => x.path)).toEqual(["/Users/me/Code/foo", "/Users/me/Code/billing"]);
    expect(p.hidden.map((x) => x.path)).toEqual(["/Users/me/Code/blog"]);
  }
  expect(stored.revision).toBe(3n);
  // The server only ever held ciphertext.
  expect(new TextDecoder().decode(stored.envelope!.ciphertext)).not.toContain("billing");
});
