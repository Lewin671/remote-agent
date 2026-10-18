import {
  Writer,
  hash,
  jsonHash,
  unb64,
  b64,
  equal,
  verifyPublicSignature,
  verifyState,
  stateHash,
  stateFromJSON,
  advance,
  type Pin,
  type Member,
  type SecurityState,
} from "./device-security";

type B = Uint8Array<ArrayBuffer>;
type StateJSON = Parameters<typeof stateFromJSON>[0];
export interface DeleteRequest {
  Account: string;
  Domain: number[];
  Requester: string;
  Operation: string;
  Expected: number[];
  Nonce: number[];
  ExactIDs: boolean;
  Targets: { ID: string; MetadataCommit: number[] }[];
  Signature: string;
}
export interface GroupOperation {
  Request: DeleteRequest;
  Digest: number[];
  NewState: number[];
}
export interface FenceReceipt {
  Account: string;
  Domain: number[];
  AuthorityID: string;
  Executor: string;
  Group: string;
  Revision: number;
  Epoch: number;
  State: number[];
  Signature: string;
}
export interface RevocationGroup {
  ID: string;
  Base: StateJSON;
  Head: StateJSON;
  Operations: GroupOperation[];
  Required: string[];
  Receipts: FenceReceipt[] | null;
  Complete: boolean;
}
export interface Completion {
  Group: RevocationGroup;
  Signature: string;
}
const validID = (s: string) => /^[0-9a-f]{32}$/.test(s);
const opaque = (s: string) => /^[A-Za-z0-9_:-]{16,128}$/.test(s);
function must(ok: unknown): asserts ok {
  if (!ok) throw new Error("撤销证明无效，尚不能确认移除成功");
}
function nonzero(b: B) {
  must(b.length === 32 && b.some((x) => x !== 0));
}
export function deleteSignBytes(r: DeleteRequest): B {
  must(
    validID(r.Account) &&
      validID(r.Requester) &&
      opaque(r.Operation) &&
      r.Targets.length > 0 &&
      r.Targets.length <= 128,
  );
  const domain = jsonHash(r.Domain),
    expected = jsonHash(r.Expected),
    nonce = jsonHash(r.Nonce);
  nonzero(domain);
  nonzero(expected);
  nonzero(nonce);
  const w = new Writer()
    .raw(new TextEncoder().encode("ra2/delete\0"))
    .str(r.Account)
    .raw(domain)
    .str(r.Requester)
    .str(r.Operation)
    .raw(expected)
    .raw(nonce)
    .u32(r.ExactIDs ? 1 : 0)
    .u32(r.Targets.length);
  for (let i = 0; i < r.Targets.length; i++) {
    const t = r.Targets[i];
    must(validID(t.ID) && (!i || r.Targets[i - 1].ID < t.ID));
    const commit = jsonHash(t.MetadataCommit);
    if (!r.ExactIDs) nonzero(commit);
    w.str(t.ID).raw(commit);
  }
  return w.bytes();
}
export async function signDeleteRequest(
  r: Omit<DeleteRequest, "Signature">,
  ed: CryptoKey,
): Promise<DeleteRequest> {
  const request = { ...r, Signature: "" };
  const bytes = deleteSignBytes(request);
  request.Signature = b64(
    new Uint8Array(await crypto.subtle.sign("Ed25519", ed, bytes)),
  );
  return request;
}
export async function requestDigest(r: DeleteRequest): Promise<B> {
  return hash("ra2/delete-digest\0", deleteSignBytes(r), unb64(r.Signature));
}
export async function unnamedMemberCommit(
  state: SecurityState,
  m: Member,
): Promise<B> {
  const w = new Writer()
    .raw(await stateHash(state))
    .str(m.id)
    .raw(m.ed)
    .raw(m.x)
    .u32(m.kind);
  return hash("ra2/unnamed-member\0", w.bytes());
}
function fenceBytes(r: FenceReceipt): B {
  must(
    validID(r.Account) &&
      validID(r.AuthorityID) &&
      validID(r.Executor) &&
      opaque(r.Group) &&
      Number.isSafeInteger(r.Revision) &&
      r.Revision > 0 &&
      r.Revision <= 128 &&
      Number.isSafeInteger(r.Epoch) &&
      r.Epoch >= 2,
  );
  const domain = jsonHash(r.Domain),
    state = jsonHash(r.State);
  nonzero(domain);
  nonzero(state);
  return new Writer()
    .raw(new TextEncoder().encode("ra2/fence-receipt\0"))
    .str(r.Account)
    .raw(domain)
    .str(r.AuthorityID)
    .str(r.Group)
    .u64(BigInt(r.Revision))
    .u64(BigInt(r.Epoch))
    .raw(state)
    .str(r.Executor)
    .bytes();
}
export async function verifyCompletion(
  p: Pin,
  c: Completion,
  original: DeleteRequest,
  chain: SecurityState[],
): Promise<void> {
  const g = c.Group;
  must(
    g.Complete &&
      opaque(g.ID) &&
      g.Operations.length > 0 &&
      g.Operations.length <= 128 &&
      chain.length === g.Operations.length,
  );
  const base = await stateFromJSON(g.Base),
    head = await stateFromJSON(g.Head);
  await verifyState(p, base);
  await verifyState(p, head);
  const bh = await stateHash(base),
    hh = await stateHash(head);
  const required = base.core.members
    .filter((m) => m.kind === 1 && head.core.members.some((n) => n.id === m.id))
    .map((m) => m.id);
  must(
    required.join(",") === g.Required.join(",") &&
      (g.Receipts?.length ?? 0) === required.length,
  );
  const receipts = g.Receipts ?? [];
  const w = new Writer()
    .raw(new TextEncoder().encode("ra2/completion\0"))
    .str(p.account)
    .raw(p.domain)
    .str(p.authorityID)
    .str(g.ID)
    .raw(bh)
    .raw(hh)
    .u32(g.Operations.length);
  for (const op of g.Operations) {
    w.str(op.Request.Operation)
      .raw(jsonHash(op.Digest))
      .raw(jsonHash(op.NewState));
  }
  w.u32(required.length);
  for (let i = 0; i < required.length; i++) {
    const r = receipts[i];
    must(
      r.Executor === required[i] &&
        r.Account === p.account &&
        equal(jsonHash(r.Domain), p.domain) &&
        r.AuthorityID === p.authorityID &&
        r.Group === g.ID &&
        r.Revision === g.Operations.length &&
        r.Epoch === Number(head.core.epoch) &&
        equal(jsonHash(r.State), hh),
    );
    const m = head.core.members.find((m) => m.id === r.Executor);
    must(m?.kind === 1);
    const fb = fenceBytes(r),
      sig = unb64(r.Signature);
    await verifyPublicSignature(m.ed, sig, fb);
    w.str(r.Executor).lp(fb).lp(sig);
  }
  await verifyPublicSignature(p.authorityEd, unb64(c.Signature), w.bytes());
  let prior = base,
    found = false;
  const originalDigest = await requestDigest(original);
  const removed = new Set<string>();
  for (let i = 0; i < g.Operations.length; i++) {
    const op = g.Operations[i],
      next = chain[i];
    await advance(p, prior, next);
    const before = await stateHash(prior),
      after = await stateHash(next);
    must(
      equal(before, jsonHash(op.Request.Expected)) &&
        equal(after, jsonHash(op.NewState)) &&
        op.Request.Account === p.account &&
        equal(jsonHash(op.Request.Domain), p.domain),
    );
    const requester = prior.core.members.find(
      (m) => m.id === op.Request.Requester,
    );
    must(requester);
    await verifyPublicSignature(
      requester.ed,
      unb64(op.Request.Signature),
      deleteSignBytes(op.Request),
    );
    const digest = await requestDigest(op.Request);
    must(equal(digest, jsonHash(op.Digest)));
    const ids = op.Request.Targets.map((t) => t.ID);
    must(ids.join(",") === next.core.removed.join(","));
    ids.forEach((id) => removed.add(id));
    if (op.Request.Operation === original.Operation) {
      must(equal(digest, originalDigest));
      found = true;
    }
    prior = next;
  }
  must(found && equal(await stateHash(prior), hh));
  for (const id of removed) must(!head.core.members.some((m) => m.id === id));
}
