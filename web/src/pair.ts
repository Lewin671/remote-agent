// Pairing crypto, matching internal/e2e/pair.go: X25519 keys, the
// initiator's commitment, the transcript, and the four-emoji SAS.

import { x25519 } from "@noble/curves/ed25519.js";
import { concat, utf8 } from "./crypto";

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
  priv: Uint8Array;
  pub: Uint8Array;
}

export function newPairKey(
  priv = crypto.getRandomValues(new Uint8Array(32)),
): PairKey {
  return { priv, pub: x25519.getPublicKey(priv) };
}

async function sha256(b: Uint8Array): Promise<Uint8Array> {
  return new Uint8Array(
    await crypto.subtle.digest("SHA-256", b as Uint8Array<ArrayBuffer>),
  );
}

export function pairCommit(initiatorPublic: Uint8Array): Promise<Uint8Array> {
  return sha256(concat(utf8("remote-agent/v1/pair-commit\0"), initiatorPublic));
}

function u32(n: number): Uint8Array {
  const b = new Uint8Array(4);
  new DataView(b.buffer).setUint32(0, n);
  return b;
}

export function pairTranscript(
  pairingId: string,
  initiatorPublic: Uint8Array,
  joinerPublic: Uint8Array,
  joinerKind: number,
): Promise<Uint8Array> {
  const parts: Uint8Array[] = [utf8("remote-agent/v1/pair\0")];
  for (const p of [utf8(pairingId), initiatorPublic, joinerPublic])
    parts.push(u32(p.length), p);
  parts.push(u32(joinerKind));
  return sha256(concat(...parts));
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
  const shared = x25519.getSharedSecret(k.priv, otherPublic);
  if (shared.every((b) => b === 0)) throw new Error("配对密钥无效");
  const base = await crypto.subtle.importKey(
    "raw",
    shared as Uint8Array<ArrayBuffer>,
    "HKDF",
    false,
    ["deriveBits"],
  );
  const bits = async (info: string, n: number) =>
    new Uint8Array(
      await crypto.subtle.deriveBits(
        {
          name: "HKDF",
          hash: "SHA-256",
          salt: transcript as Uint8Array<ArrayBuffer>,
          info: utf8(info),
        },
        base,
        n * 8,
      ),
    );
  const sas = await bits("remote-agent/v1/pair-sas", 4);
  return {
    sas,
    key: await bits("remote-agent/v1/pair-key", 32),
    emoji: [...sas].map((b) => SAS_EMOJI[b]),
  };
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
  return sha256(
    concat(
      utf8("remote-agent/v1/pair-identity\0"),
      await pairTranscript(id, initiator, joiner, kind),
      u32(device.length),
      device,
    ),
  );
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
  const inner = await pairIdentityTranscript(
    id,
    initiator,
    joiner,
    device,
    kind,
  );
  return sha256(
    concat(
      utf8("ra3/pair-domain\0"),
      u32(inner.length),
      inner,
      u32(hpke.length),
      hpke,
      u32(trust.length),
      trust,
    ),
  );
}
