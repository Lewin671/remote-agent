// Public-material intake for the future creation/recovery coordinators. No
// registration, recovery submission or pending-operation lifecycle lives here.
import { accountKey, checkServer, deviceID, importRoot, newDeviceKey } from "./crypto";
import { Authority } from "./authority";
import { RecoveryCode } from "./recovery";
import {
  SecurityError, equal, newSecurityKey, pinOf, signLease, verifyChain,
  type Pin, type RecoveryStatement, type SecurityState,
} from "./device-security";
import { saveAccountPublicProfile } from "./storage";

/** Generates local creation material. The coordinator must persist its draft
 * and exact registration before contacting the relay, and confirm the code. */
export async function createAccountMaterial(server: string, now = BigInt(Date.now())) {
  server = checkServer(server);
  const rootRaw = crypto.getRandomValues(new Uint8Array(32));
  const root = await importRoot(rootRaw);
  const accountPublicKey = new Uint8Array((await accountKey(root)).publicKey);
  const device = await newDeviceKey();
  const x = await newSecurityKey();
  const member = await Authority.newMember(device.publicKey, x.publicKey, 2);
  const recovery = await Authority.newRecovery();
  try {
    const secret = Authority.newSecret();
    const genesis = await Authority.genesis(await deviceID(accountPublicKey), crypto.getRandomValues(new Uint8Array(32)), member, recovery.publicKey, secret, device.privateKey, now);
    const pin = await pinOf(genesis);
    const lease = await signLease(genesis, device.privateKey, now);
    await saveAccountPublicProfile(server, pin, accountPublicKey);
    return { server, root, rootRaw, accountPublicKey, device, x, secret, genesis, pin, lease, recoveryCode: RecoveryCode.encode(recovery.seed, pin.genesis) };
  } finally { recovery.seed.fill(0); }
}

/** Accepts an unauthenticated recovery context only after checking its account
 * key, complete chain and the offline code's genesis prefix. If a local pin
 * exists, require that pin and a strict extension of its verified chain too.
 * Callers pass decoded.genesis, then discard the input recovery seed. */
export async function acceptRecoveryPublicProfile(
  server: string,
  account: string,
  accountPublicKey: Uint8Array<ArrayBuffer>,
  states: SecurityState[],
  proofs: (RecoveryStatement | undefined)[],
  expectedGenesis: Uint8Array<ArrayBuffer>,
  local?: { pin: Pin; states: SecurityState[] },
): Promise<Pin> {
  if (accountPublicKey.length !== 32 || await deviceID(accountPublicKey) !== account ||
      states.length === 0 || expectedGenesis.length !== 10) throw new SecurityError("untrusted");
  const pin = await pinOf(states[0]);
  if (pin.account !== account || !equal(pin.genesis.slice(0, 10), expectedGenesis)) throw new SecurityError("untrusted");
  await verifyChain(pin, states, proofs);
  if (local) {
    if (local.pin.account !== pin.account || !equal(local.pin.domain, pin.domain) ||
        !equal(local.pin.genesis, pin.genesis) || local.states.length === 0 ||
        states.length < local.states.length) throw new SecurityError("conflict");
    const { stateHash } = await import("./device-security");
    if (!equal(await stateHash(states[local.states.length - 1]), await stateHash(local.states[local.states.length - 1]))) throw new SecurityError("conflict");
  }
  await saveAccountPublicProfile(server, pin, accountPublicKey);
  return pin;
}
