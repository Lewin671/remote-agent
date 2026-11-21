import CryptoKit
import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct DirectSecuritySnapshotTests {
    private actor EventRecorder {
        var events: [PBCoreEvent] = []
        func append(_ ev: PBCoreEvent) { events.append(ev) }
        func snapshot() -> [PBCoreEvent] { events }
        func clear() { events.removeAll() }
    }

    private func makeChain(nowMs: Int64) throws -> (
        adminSigner: SigningKey,
        adminXPrivate: Data,
        record: DeviceSecurity.Record,
        nextRecord: DeviceSecurity.Record
    ) {
        let authority = Authority()
        let adminSigner = try SigningKey(seed: Data(repeating: 0x11, count: 32))
        let adminX = Data(repeating: 0x21, count: 32)
        let machineSigner = try SigningKey(seed: Data(repeating: 0x31, count: 32))
        let machineX = Data(repeating: 0x41, count: 32)
        let extraSigner = try SigningKey(seed: Data(repeating: 0x51, count: 32))
        let extraX = Data(repeating: 0x61, count: 32)
        let recoveryPub = try SigningKey(seed: Data(repeating: 0x71, count: 32)).publicKey
        let account = deviceId(publicKey: try SigningKey(seed: Data(repeating: 0x81, count: 32)).publicKey)
        let domain = Data(repeating: 0x91, count: 32)
        let s1 = Data(repeating: 0xA1, count: 32)
        let s2 = Data(repeating: 0xA2, count: 32)
        let s3 = Data(repeating: 0xA3, count: 32)

        let adminMember = try Authority.newMember(signer: adminSigner, xPrivate: adminX, kind: .client)
        let machineMember = try Authority.newMember(signer: machineSigner, xPrivate: machineX, kind: .machine)
        let extraMember = try Authority.newMember(signer: extraSigner, xPrivate: extraX, kind: .client)

        let g = try authority.genesis(
            account: account,
            domain: domain,
            member: adminMember,
            recoveryEd: recoveryPub,
            secret: s1,
            signer: adminSigner,
            now: nowMs
        )
        let p = try authority.pair(
            parent: g,
            candidate: machineMember,
            oldSecret: s1,
            newSecret: s2,
            signer: adminSigner,
            now: nowMs + 1
        )
        let lease = try DeviceSecurity.HeadLease.sign(p, signer: adminSigner, now: nowMs + 1)
        let pin = try DeviceSecurity.pin(for: g)
        let rec = try DeviceSecurity.Record(
            pin: pin,
            states: [1: g, 2: p],
            memberID: adminMember.id,
            xPrivate: adminX,
            member: true,
            floor: UInt64(nowMs + 1),
            secret: s2,
            contentKeys: [1: s1, 2: s2],
            lease: lease
        )

        let p2 = try authority.pair(
            parent: p,
            candidate: extraMember,
            oldSecret: s2,
            newSecret: s3,
            signer: adminSigner,
            now: nowMs + 2
        )
        let nextLease = try DeviceSecurity.HeadLease.sign(p2, signer: adminSigner, now: nowMs + 2)
        let nextRec = try DeviceSecurity.Record(
            pin: pin,
            states: [1: g, 2: p, 3: p2],
            memberID: adminMember.id,
            xPrivate: adminX,
            member: true,
            floor: UInt64(nowMs + 2),
            secret: s3,
            contentKeys: [1: s1, 2: s2, 3: s3],
            lease: nextLease
        )
        return (adminSigner, adminX, rec, nextRec)
    }

    @Test func tableDrivenPolicyAndFreshnessGates() throws {
        let nowMs: Int64 = 1_700_000_000_000
        let (adminSigner, _, rec, _) = try makeChain(nowMs: nowMs)
        let started = UInt64(nowMs - 5_000)
        let wrongEdPub = try SigningKey(seed: Data(repeating: 0xEE, count: 32)).publicKey

        var nonMember = rec
        nonMember.member = false

        var noLease = rec
        noLease.lease = nil

        // Mutate the head member's X25519 public key so Record's stored xPrivate no longer matches selfMember.x.
        let altX = Data(repeating: 0xCC, count: 32)
        let altMember = try Authority.newMember(signer: adminSigner, xPrivate: altX, kind: .client)
        var altState = rec.state
        altState.core.members = altState.core.members.map { $0.id == rec.memberID ? altMember : $0 }
        var badXRec = rec
        badXRec.states[rec.headEpoch] = altState

        struct Case {
            let name: String
            let record: DeviceSecurity.Record
            let expectedDeviceID: String
            let devicePub: Data
            let started: UInt64
            let nowMs: Int64
            let wantOK: Bool
        }

        let cases: [Case] = [
            Case(
                name: "valid member with machine (kind=1) and client (kind=2)",
                record: rec,
                expectedDeviceID: rec.memberID,
                devicePub: adminSigner.publicKey,
                started: started,
                nowMs: nowMs + 1_000,
                wantOK: true
            ),
            Case(
                name: "non-member rejected",
                record: nonMember,
                expectedDeviceID: rec.memberID,
                devicePub: adminSigner.publicKey,
                started: started,
                nowMs: nowMs + 1_000,
                wantOK: false
            ),
            Case(
                name: "memberID != deviceID rejected",
                record: rec,
                expectedDeviceID: "00000000000000000000000000000000",
                devicePub: adminSigner.publicKey,
                started: started,
                nowMs: nowMs + 1_000,
                wantOK: false
            ),
            Case(
                name: "missing lease rejected",
                record: noLease,
                expectedDeviceID: rec.memberID,
                devicePub: adminSigner.publicKey,
                started: started,
                nowMs: nowMs + 1_000,
                wantOK: false
            ),
            Case(
                name: "expired lease rejected",
                record: rec,
                expectedDeviceID: rec.memberID,
                devicePub: adminSigner.publicKey,
                started: UInt64(rec.lease!.notAfter) - 1_000,
                nowMs: Int64(rec.lease!.notAfter),
                wantOK: false
            ),
            Case(
                name: "20s start window expired rejected",
                record: rec,
                expectedDeviceID: rec.memberID,
                devicePub: adminSigner.publicKey,
                started: started,
                nowMs: Int64(started) + 20_000,
                wantOK: false
            ),
            Case(
                name: "mismatched Ed25519 devicePublicKey rejected",
                record: rec,
                expectedDeviceID: rec.memberID,
                devicePub: wrongEdPub,
                started: started,
                nowMs: nowMs + 1_000,
                wantOK: false
            ),
            Case(
                name: "mismatched X25519 xPrivate rejected",
                record: badXRec,
                expectedDeviceID: rec.memberID,
                devicePub: adminSigner.publicKey,
                started: started,
                nowMs: nowMs + 1_000,
                wantOK: false
            ),
        ]

        for tc in cases {
            let built = Remote.buildDirectSecuritySnapshot(
                record: tc.record,
                expectedDeviceID: tc.expectedDeviceID,
                devicePublicKey: tc.devicePub,
                started: tc.started,
                nowMs: tc.nowMs
            )
            if tc.wantOK {
                let result = try #require(built, "\(tc.name) expected non-nil")
                #expect(result.snapshot.member)
                #expect(!result.snapshot.rejected)
                #expect(result.snapshot.members.contains { $0.kind == 1 })
                #expect(result.snapshot.members.contains { $0.kind == 2 && $0.isSelf })
            } else {
                #expect(built == nil, "\(tc.name) expected nil")
            }
        }
    }

    @Test func handleDirectSecurityInstallOnlyInvalidatesNeverFabricatesMemberSnapshot() async throws {
        let nowMs = Int64(Date().timeIntervalSince1970 * 1000)
        let (adminSigner, _, rec, nextRec) = try makeChain(nowMs: nowMs)
        let account = Account(
            server: "https://example.com",
            root: Data(repeating: 1, count: 32),
            deviceSeed: Data(repeating: 0x11, count: 32),
            deviceId: rec.memberID,
            accountId: rec.pin.account
        )
        let remote = try Remote(
            account: account,
            http: HTTP(),
            clock: { nowMs + 100 }
        )
        defer { remote.close() }

        let recorder = EventRecorder()
        await remote.installCoreHooks(Remote.CoreHooks(
            onFetchSession: { _ in },
            onRefresh: {},
            onRefreshDevices: {},
            onSavePrefs: { _ in },
            onCoreEvent: { ev in await recorder.append(ev) }
        ))

        await remote.emitDirectSecuritySnapshot(rec, started: UInt64(nowMs))
        let snapBefore = remote.cachedDirectSecuritySnapshot()
        #expect(snapBefore?.member == true)

        await recorder.clear()
        await remote.handleDirectSecurityInstall(nextRec)

        let events = await recorder.snapshot()
        let installedEvents = events.compactMap { e -> PBCoreDirectSecurityInstalledEvent? in
            if case .directSecurityInstalled(let inst)? = e.event { return inst }
            return nil
        }
        let fabricatedSnapshots = events.filter {
            if case .directSecuritySnapshot(let s)? = $0.event { return s.member }
            return false
        }
        #expect(installedEvents.count == 1)
        #expect(installedEvents.first?.member == true)
        #expect(installedEvents.first?.head == (try DeviceSecurity.stateHash(nextRec.state).hex))
        #expect(fabricatedSnapshots.isEmpty)
        #expect(remote.cachedDirectSecuritySnapshot()?.rejected == true)
        _ = adminSigner
    }
}
