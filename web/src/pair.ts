import * as l10n from "./i18n";
// Pairing crypto, matching internal/e2e/pair.go: X25519 keys, the
// initiator's commitment, the transcript, and the four-emoji SAS.

import { create } from "@bufbuild/protobuf";
import { concat, utf8 } from "./crypto";
import {
  SecDerivePairSecretsCallSchema,
  SecPairDomainTranscriptCallSchema,
  SecPairIdentityTranscriptCallSchema,
  SecPairTranscriptCallSchema,
} from "./gen/remoteagent/core/v1/seccall_pb";
import {
  secCall,
  x25519DeriveWithPrivateKey,
  x25519PrivateKeyFromSeed,
  x25519SeedPublicKey,
} from "./core/sec-call";

export const SAS_EMOJI: readonly string[] = [
  "🐶",
  "🐱",
  "🐭",
  "🐹",
  "🐰",
  "🦊",
  "🐻",
  "🐼",
  "🐨",
  "🐯",
  "🦁",
  "🐮",
  "🐷",
  "🐸",
  "🐵",
  "🐔",
  "🐧",
  "🐦",
  "🐤",
  "🦆",
  "🦅",
  "🦉",
  "🦇",
  "🐺",
  "🐗",
  "🐴",
  "🦄",
  "🐝",
  "🐛",
  "🦋",
  "🐌",
  "🐞",
  "🐜",
  "🦗",
  "🕷",
  "🦂",
  "🐢",
  "🐍",
  "🦎",
  "🦖",
  "🦕",
  "🐙",
  "🦑",
  "🦐",
  "🦞",
  "🦀",
  "🐡",
  "🐠",
  "🐟",
  "🐬",
  "🐳",
  "🐋",
  "🦈",
  "🐊",
  "🐅",
  "🐆",
  "🦓",
  "🦍",
  "🦧",
  "🐘",
  "🦛",
  "🦏",
  "🐪",
  "🐫",
  "🦒",
  "🦘",
  "🐃",
  "🐂",
  "🐄",
  "🐎",
  "🐖",
  "🐏",
  "🐑",
  "🦙",
  "🐐",
  "🦌",
  "🐕",
  "🐩",
  "🦮",
  "🐈",
  "🐓",
  "🦃",
  "🦚",
  "🦜",
  "🦢",
  "🦩",
  "🕊",
  "🐇",
  "🦝",
  "🦨",
  "🦡",
  "🦦",
  "🦥",
  "🐁",
  "🐀",
  "🐿",
  "🦔",
  "🌵",
  "🎄",
  "🌲",
  "🌳",
  "🌴",
  "🌱",
  "🌿",
  "☘",
  "🍀",
  "🎍",
  "🎋",
  "🍃",
  "🍂",
  "🍁",
  "🍄",
  "🐚",
  "🌾",
  "💐",
  "🌷",
  "🌹",
  "🥀",
  "🌺",
  "🌸",
  "🌼",
  "🌻",
  "🌞",
  "🌝",
  "🌛",
  "🌜",
  "🌚",
  "🌕",
  "🌖",
  "🌗",
  "🌘",
  "🌑",
  "🌒",
  "🌓",
  "🌔",
  "🌙",
  "🌎",
  "🌍",
  "🌏",
  "🪐",
  "💫",
  "⭐",
  "🌟",
  "✨",
  "⚡",
  "☄",
  "💥",
  "🔥",
  "🌪",
  "🌈",
  "☀",
  "🌤",
  "⛅",
  "🌥",
  "☁",
  "🌦",
  "🌧",
  "⛈",
  "🌩",
  "🌨",
  "❄",
  "☃",
  "⛄",
  "🌬",
  "💨",
  "💧",
  "💦",
  "☔",
  "☂",
  "🌊",
  "🍏",
  "🍎",
  "🍐",
  "🍊",
  "🍋",
  "🍌",
  "🍉",
  "🍇",
  "🍓",
  "🫐",
  "🍈",
  "🍒",
  "🍑",
  "🥭",
  "🍍",
  "🥥",
  "🥝",
  "🍅",
  "🍆",
  "🥑",
  "🥦",
  "🥬",
  "🥒",
  "🌶",
  "🫑",
  "🌽",
  "🥕",
  "🧄",
  "🧅",
  "🥔",
  "🍠",
  "🥐",
  "🥯",
  "🍞",
  "🥖",
  "🥨",
  "🧀",
  "🥚",
  "🍳",
  "🧈",
  "🥞",
  "🧇",
  "🥓",
  "🥩",
  "🍗",
  "🍖",
  "🌭",
  "🍔",
  "🍟",
  "🍕",
  "🥪",
  "🥙",
  "🧆",
  "🌮",
  "🌯",
  "🥗",
  "🥘",
  "🥫",
  "🍝",
  "🍜",
  "🍲",
  "🍛",
  "🍣",
  "🍱",
  "🥟",
  "🦪",
  "🍤",
  "🍙",
  "🍚",
  "🍘",
  "🍥",
  "🥠",
  "🥮",
  "🍢",
  "🍡",
  "🍧",
  "🍨",
  "🍦",
  "🥧",
  "🧁",
  "🍰",
  "🎂",
  "🍮",
  "🍭",
  "🍬",
  "🍫",
];

export interface PairKey {
  privKey: CryptoKey;
  pub: Uint8Array;
}

export async function newPairKey(priv?: Uint8Array): Promise<PairKey> {
  if (priv) {
    return {
      privKey: await x25519PrivateKeyFromSeed(priv, false),
      pub: await x25519SeedPublicKey(priv),
    };
  }
  const kp = (await crypto.subtle.generateKey({ name: "X25519" }, false, ["deriveBits"])) as CryptoKeyPair;
  return {
    privKey: kp.privateKey,
    pub: new Uint8Array(await crypto.subtle.exportKey("raw", kp.publicKey)),
  };
}

export async function pairTranscript(
  pairingId: string,
  initiatorPublic: Uint8Array,
  joinerPublic: Uint8Array,
  joinerKind: number,
): Promise<Uint8Array> {
  const resp = secCall({
    case: "pairTranscript",
    value: create(SecPairTranscriptCallSchema, {
      pairingId,
      initiatorPublic: new Uint8Array(initiatorPublic),
      joinerPublic: new Uint8Array(joinerPublic),
      joinerKind,
    }),
  });
  return new Uint8Array(resp.data);
}

export interface PairSecrets {
  sas: Uint8Array;
  key: Uint8Array;
  emoji: string[];
}

export async function derivePair(
  k: PairKey,
  otherPublic: Uint8Array,
  transcript: Uint8Array,
): Promise<PairSecrets> {
  let shared: Uint8Array<ArrayBuffer>;
  try {
    shared = await x25519DeriveWithPrivateKey(k.privKey, otherPublic);
  } catch {
    throw new Error(l10n.t("security.invalid_pairing_key"));
  }
  try {
    if (shared.every((b) => b === 0)) throw new Error(l10n.t("security.invalid_pairing_key"));
    const resp = secCall({
      case: "derivePairSecrets",
      value: create(SecDerivePairSecretsCallSchema, { shared, transcript: new Uint8Array(transcript) }),
    });
    return {
      sas: new Uint8Array(resp.secondaryData),
      key: new Uint8Array(resp.data),
      emoji: [...resp.strings],
    };
  } catch {
    throw new Error(l10n.t("security.invalid_pairing_key"));
  } finally {
    shared.fill(0);
  }
}

// The inner transcript is the ephemeral exchange; the current pairing also
// commits to the permanent signing identity that the user will authorize.
export async function pairIdentityTranscript(
  id: string,
  initiator: Uint8Array,
  joiner: Uint8Array,
  device: Uint8Array,
  kind: number,
): Promise<Uint8Array> {
  const resp = secCall({
    case: "pairIdentityTranscript",
    value: create(SecPairIdentityTranscriptCallSchema, {
      pairingId: id,
      initiatorPublic: new Uint8Array(initiator),
      joinerPublic: new Uint8Array(joiner),
      deviceEdPublic: new Uint8Array(device),
      joinerKind: kind,
    }),
  });
  return new Uint8Array(resp.data);
}

export async function pairDomainTranscript(
  id: string,
  initiator: Uint8Array,
  joiner: Uint8Array,
  device: Uint8Array,
  hpke: Uint8Array,
  kind: number,
  trust: Uint8Array,
): Promise<Uint8Array> {
  const resp = secCall({
    case: "pairDomainTranscript",
    value: create(SecPairDomainTranscriptCallSchema, {
      pairingId: id,
      initiatorPublic: new Uint8Array(initiator),
      joinerPublic: new Uint8Array(joiner),
      deviceEdPublic: new Uint8Array(device),
      deviceXPublic: new Uint8Array(hpke),
      joinerKind: kind,
      trustDescriptor: new Uint8Array(trust),
    }),
  });
  return new Uint8Array(resp.data);
}
export { concat, utf8 };
