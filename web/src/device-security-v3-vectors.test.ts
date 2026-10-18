// The Go recovery/chain fixtures, checked by the web verifier: recovery
// statement, no pre-recovery key for the new phone, accumulated Removed,
// RecoveryEd rotation, and fork/tampered replay rejection.
import { afterEach, expect, it, vi } from "vitest";
import { ed25519 } from "@noble/curves/ed25519.js";
import recovery from "../../internal/deviceauth/testdata/recovery-v3.json";
import chain from "../../internal/deviceauth/testdata/chain-v3.json";
import { concat, deviceID, fromHex, hex } from "./crypto";
import { contentSecretOf } from "./device-security-store";
import {
  openGrant,
  openPrevSecret,
  parseRecoveryStatement,
  parseState,
  SecurityError,
  stateHash,
  verifyChain,
  verifyGenesis,
  verifyTransition,
  type Pin,
  type SecurityState,
} from "./device-security";
afterEach(() => vi.useRealTimers());
const raw = (s: { state: string }) => parseState(
  Uint8Array.from(atob(s.state), (c) => c.charCodeAt(0)),
);
async function xKey(seed: string): Promise<CryptoKey> {
  return crypto.subtle.importKey(
    "pkcs8",
    concat(fromHex("302e020100300506032b656e04220420"), fromHex(seed)),
    "X25519",
    false,
    ["deriveBits"],
  );
}
async function expectHash(s: SecurityState, want: string) {
  expect(hex(await stateHash(s))).toBe(want);
}

it("verifies the recovery-v3 fixture and the pre-recovery boundary", async () => {
  const genesis = await raw(recovery.genesis);
  const parent = await raw(recovery.parent);
  const rec = await raw(recovery.recovery);
  await expectHash(genesis, recovery.genesis.hash);
  await expectHash(parent, recovery.parent.hash);
  await expectHash(rec, recovery.recovery.hash);
  const pin: Pin = {
    account: genesis.core.account,
    domain: genesis.core.domain,
    genesis: await stateHash(genesis),
  };
  await verifyGenesis(pin, genesis);
  await verifyTransition(genesis, parent);
  const statement = await parseRecoveryStatement(
    Uint8Array.from(atob(recovery.recovery_statement), (c) => c.charCodeAt(0)),
  );
  await verifyTransition(parent, rec, statement);
  expect(rec.prevSecret.length).toBe(0);
  expect(rec.core.authorityID).not.toBe(parent.core.authorityID);
  expect(rec.core.removed).toContain(parent.core.authorityID);
  // The old recovery key signed the statement; the new one is in the state.
  expect(hex(ed25519.getPublicKey(fromHex(recovery.old_recovery_seed)))).toBe(
    hex(genesis.core.recoveryEd),
  );
  expect(hex(ed25519.getPublicKey(fromHex(recovery.new_phone_seed)))).toBe(
    hex(rec.core.members.find((m) => m.id === rec.core.authorityID)!.ed),
  );
  const newPhoneSeed = fromHex(recovery.new_phone_seed);
  expect(await deviceID(ed25519.getPublicKey(newPhoneSeed))).toBe(
    rec.core.authorityID,
  );
  // The retained machine can open the recovery grant; the old phone cannot.
  const machine = rec.core.members.find((m) => m.kind === 1)!;
  const machineSecret = await openGrant(
    pin,
    rec,
    machine.id,
    await xKey(recovery.machine_x),
  );
  expect(machineSecret.length).toBe(32);
  expect(rec.grants.some((g) => g.recipient === parent.core.authorityID)).toBe(
    false,
  );
  const newPhoneSecret = await openGrant(
    pin,
    rec,
    rec.core.authorityID,
    await xKey(recovery.new_phone_x),
  );
  await expect(
    contentSecretOf(
      { states: [genesis, parent, rec], secret: newPhoneSecret, contentKeys: [] },
      parent.core.contentEpoch,
    ),
  ).rejects.toMatchObject({ code: "unavailable" });
  // The recovery state carries no previous secret, so no key walk is possible.
  expect(rec.prevSecret.length).toBe(0);
  await expect(openPrevSecret(newPhoneSecret, rec.prevSecret)).rejects.toThrow();
});

it("rejects a chain whose states are from the future", async () => {
  const states = await Promise.all(chain.states.map(raw));
  const pin: Pin = {
    account: states[0].core.account,
    domain: states[0].core.domain,
    genesis: await stateHash(states[0]),
  };
  vi.useFakeTimers();
  vi.setSystemTime(1_600_000_000_000);
  await expect(verifyChain(pin, states)).rejects.toMatchObject({
    code: "clock",
  });
});

it("verifies the chain-v3 fixture, accumulated Removed and a rotated RecoveryEd", async () => {
  const states = await Promise.all(chain.states.map(raw));
  for (let i = 0; i < states.length; i++)
    await expectHash(states[i], chain.states[i].hash);
  const pin: Pin = {
    account: states[0].core.account,
    domain: states[0].core.domain,
    genesis: await stateHash(states[0]),
  };
  await verifyChain(pin, states);
  expect(states[states.length - 1].core.removed).toHaveLength(1);
  // The last normal state rotates the recovery key without a recovery.
  expect(hex(states[states.length - 1].core.recoveryEd)).not.toBe(
    hex(states[states.length - 2].core.recoveryEd),
  );
  // Once removed, the member is never re-added.
  const removed = states[states.length - 1].core.removed[0];
  const firstRemoved = states.findIndex((s) => s.core.removed.includes(removed));
  expect(firstRemoved).toBeGreaterThan(0);
  for (const s of states.slice(firstRemoved))
    expect(s.core.members.some((m) => m.id === removed)).toBe(false);
  // A same-parent fork is validly signed but differs from the accepted head.
  const fork = await raw(chain.fork);
  await verifyTransition(states[0], fork);
  expect(hex(await stateHash(fork))).not.toBe(chain.states[1].hash);
  // A tampered replay does not verify.
  const bad = await raw(chain.bad_replay);
  await expect(verifyTransition(states[0], bad)).rejects.toBeInstanceOf(
    SecurityError,
  );
});
