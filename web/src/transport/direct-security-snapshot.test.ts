import { afterEach, describe, expect, test, vi } from "vitest";
import { getCoreHost, type HostBridge } from "../core/host";
import { deviceID } from "../crypto";
import { SyntheticRemote } from "../test/remote-fixture";
import { GoSecurityFixture, webMember } from "../test/security-fixture";
import { buildDirectSecuritySnapshot } from "./direct-security-snapshot";

describe("buildDirectSecuritySnapshot", () => {
  const fixtures: GoSecurityFixture[] = [];

  afterEach(async () => {
    while (fixtures.length > 0) {
      await fixtures.pop()?.close();
    }
  });

  test("table-driven policy and freshness gates", async () => {
    const world = await GoSecurityFixture.create();
    fixtures.push(world.fixture);
    const member = await webMember(world);
    const { record, account } = member;
    const head = record.states[record.states.length - 1];
    const nowMs = Number(head.core.issuedAt) + 1_000;
    const started = nowMs - 5_000;

    const otherEd = (await crypto.subtle.generateKey({ name: "Ed25519" }, false, ["sign", "verify"]) as CryptoKeyPair).privateKey;
    const otherX = (await crypto.subtle.generateKey({ name: "X25519" }, false, ["deriveBits"]) as CryptoKeyPair).privateKey;
    const badEdBytes = new Uint8Array(32).fill(0x5a);
    const wrongDeviceId = await deviceID(badEdBytes);

    type Case = {
      name: string;
      record: typeof record;
      started: number;
      deviceId: string;
      devicePrivate: CryptoKey;
      nowMs: number;
      wantOk: boolean;
    };

    const cases: Case[] = [
      {
        name: "valid member preserves machine (kind=1) and client (kind=2)",
        record,
        started,
        deviceId: account.deviceId,
        devicePrivate: account.devicePrivate,
        nowMs,
        wantOk: true,
      },
      {
        name: "non-member rejected",
        record: { ...record, member: false },
        started,
        deviceId: account.deviceId,
        devicePrivate: account.devicePrivate,
        nowMs,
        wantOk: false,
      },
      {
        name: "memberID != deviceId rejected",
        record,
        started,
        deviceId: wrongDeviceId,
        devicePrivate: account.devicePrivate,
        nowMs,
        wantOk: false,
      },
      {
        name: "missing lease rejected",
        record: { ...record, lease: undefined },
        started,
        deviceId: account.deviceId,
        devicePrivate: account.devicePrivate,
        nowMs,
        wantOk: false,
      },
      {
        name: "expired lease rejected",
        record,
        started: Number(record.lease!.notAfter) - 1_000,
        deviceId: account.deviceId,
        devicePrivate: account.devicePrivate,
        nowMs: Number(record.lease!.notAfter),
        wantOk: false,
      },
      {
        name: "20s start window expired rejected",
        record,
        started: nowMs - 20_000,
        deviceId: account.deviceId,
        devicePrivate: account.devicePrivate,
        nowMs,
        wantOk: false,
      },
      {
        name: "mismatched Ed25519 devicePrivate rejected",
        record,
        started,
        deviceId: account.deviceId,
        devicePrivate: otherEd,
        nowMs,
        wantOk: false,
      },
      {
        name: "mismatched X25519 xPrivate rejected",
        record: { ...record, xPrivate: otherX },
        started,
        deviceId: account.deviceId,
        devicePrivate: account.devicePrivate,
        nowMs,
        wantOk: false,
      },
    ];

    for (const tc of cases) {
      const built = await buildDirectSecuritySnapshot({
        record: tc.record,
        started: tc.started,
        deviceId: tc.deviceId,
        devicePrivate: tc.devicePrivate,
        nowMs: tc.nowMs,
      });
      if (!tc.wantOk) {
        expect(built, tc.name).toBeUndefined();
      } else {
        expect(built, tc.name).toBeDefined();
        expect(built!.snapshot.member, tc.name).toBe(true);
        expect(built!.snapshot.rejected, tc.name).toBe(false);
        expect(built!.snapshot.members.some((m) => m.kind === 1), tc.name).toBe(true);
        expect(built!.snapshot.members.some((m) => m.kind === 2 && m.isSelf), tc.name).toBe(true);
      }
    }
  });

  test("handleDirectSecurityInstall invalidates stale snapshot and never fabricates member=true", async () => {
    const r = await SyntheticRemote.create();
    try {
      getCoreHost(r);
      const bridge = (r as unknown as { hostBridge: HostBridge }).hostBridge;
      const snapSpy = vi.spyOn(bridge, "dispatchDirectSecuritySnapshot");
      const instSpy = vi.spyOn(bridge, "dispatchDirectSecurityInstalled");

      const started = Number(r.member.record.states[r.member.record.states.length - 1].core.issuedAt);
      r.security.onSync!(r.member.record, started);
      await vi.waitFor(() => {
        expect(snapSpy.mock.calls.some(([s]) => s.member && !s.rejected)).toBe(true);
      });

      snapSpy.mockClear();
      instSpy.mockClear();

      // Advance the chain via another enrollment so head changes.
      await r.world.fixture.enroll(r.machine, 2);
      const { record: updatedRecord } = await r.world.fixture.recordAndChain(r.member.account.deviceId);
      r.security.onInstall!(updatedRecord);

      expect(instSpy).toHaveBeenCalledTimes(1);
      expect(instSpy.mock.calls[0][0].member).toBe(true);
      expect(snapSpy.mock.calls.some(([s]) => s.member)).toBe(false);
      expect((r as unknown as { lastDirectSecurity?: { rejected: boolean } }).lastDirectSecurity?.rejected).toBe(true);
    } finally {
      await r.dispose();
    }
  });
});
