import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct DeviceSecurityTests {
    typealias S = DeviceSecurity
    let v: [String: Any]
    init() throws { v = try loadJSON("internal/deviceauth/testdata/domain-v3.json") }
    @Test func compressedProtocol10Envelopes() async throws {
        let genesis = try state("genesis"), head = try state("added")
        let record = try S.Record(
            pin: pin(), states: [1: genesis, 2: head], memberID: head.core.authorityID,
            xPrivate: try phoneX(), member: true, secret: try hex("content_secret_2"),
            lease: try signedLease(head))
        let sdk = try TestCoreSecurity(
            store: MemorySecurityStore(record), memberID: head.core.authorityID,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { head.core.issuedAt })
        defer { Task { await sdk.close() } }
        let fixtures = v["compressed_envelopes"] as! [[String: String]]
        #expect(fixtures.count == 3)
        func parseWireEnvelope(_ raw: Data) throws -> PBEnvelope {
            var idx = raw.startIndex
            func readLP() throws -> Data {
                guard idx + 4 <= raw.endIndex else { throw S.Failure.invalid }
                let len = Int(raw[idx]) << 24 | Int(raw[idx + 1]) << 16 | Int(raw[idx + 2]) << 8 | Int(raw[idx + 3])
                idx += 4
                guard len >= 0, idx + len <= raw.endIndex else { throw S.Failure.invalid }
                defer { idx += len }
                return Data(raw[idx..<(idx + len)])
            }
            var env = PBEnvelope()
            env.securityHeader = try readLP()
            env.nonce = try readLP()
            env.ciphertext = try readLP()
            env.signature = try readLP()
            guard idx == raw.endIndex else { throw S.Failure.invalid }
            var r = S.Reader(data: env.securityHeader)
            let version = try r.u32()
            _ = try r.str() // account
            _ = try r.raw(32) // domain
            _ = try r.u64() // epoch
            _ = try r.raw(32) // state
            _ = try r.u64() // contentEpoch
            let sender = try r.str()
            let kind = try r.u32()
            _ = try r.u32() // purpose
            let machine = try r.str()
            let session = try r.str()
            let request = try r.str()
            let seq = try r.u64()
            let deadline = try r.u64()
            let compression = try r.u32()
            try r.done()
            var proj = PBHeader()
            proj.version = version
            proj.kind = PBPayloadKind(rawValue: Int(kind)) ?? .unspecified
            proj.machineID = machine
            proj.sessionID = session
            proj.requestID = request
            proj.seq = seq
            proj.deadlineMs = Int64(bitPattern: deadline)
            proj.sender = sender
            proj.compression = PBCompression(rawValue: Int(compression)) ?? .none
            env.header = try proj.serializedData()
            return env
        }
        for fixture in fixtures {
            let env = try parseWireEnvelope(Data(base64Encoded: fixture["wire"]!)!)
            let (_, plain) = try await sdk.openHistory(
                env, sessionSeed: Data(base64Encoded: fixture["seed"]!)!)
            #expect(plain == Data(base64Encoded: fixture["plain"]!)!)
        }
    }

    @Test(arguments: [nil, 0, 1, 2] as [UInt32?])
    func opensTurnNotificationSharedFileCount(count: UInt32?) async throws {
        let genesis = try state("genesis"), head = try state("added")
        let machine = head.core.members.first { $0.kind == 1 }!.id
        let record = try S.Record(
            pin: pin(), states: [1: genesis, 2: head], memberID: machine,
            xPrivate: hex("machine_x"), member: true, secret: hex("content_secret_2"),
            lease: signedLease(head))
        let sdk = try TestCoreSecurity(
            store: MemorySecurityStore(record), memberID: machine,
            signingKey: try SigningKey(seed: hex("machine_seed")),
            clock: { head.core.issuedAt }, fetchStates: { _ in PBGetSecurityStatesResponse() })
        defer { Task { await sdk.close() } }
        var content = PBNotifyContent()
        content.machineID = machine
        content.sessionID = "session"
        content.kind = .turnEnded
        content.sourceEventSeq = 7
        if let count { content.sharedFileCount = count }
        let env = try await sdk.seal(
            purpose: .notify, machine: machine, session: "session", request: "notify:2:7",
            plaintext: content.serializedData())
        let opener = try S.NotifyRecord.parse(S.NotifyRecord(record: record).bytes())
        let opened = try opener.open(env)
        #expect(opened.sharedFileCount == (count ?? 0))
        #expect(opened.text.isEmpty && opened.kind == .turnEnded)
        var tampered = env
        tampered.signature[0] ^= 1
        #expect(throws: S.Failure.self) { _ = try opener.open(tampered) }
    }

    func hex(_ key: String) throws -> Data {
        guard let s = v[key] as? String, let b = Data(hex: s) else { throw S.Failure.invalid }
        return b
    }
    func state(_ key: String) throws -> S.State {
        guard let o = v[key] as? [String: Any], let raw = Data(base64Encoded: o.s("state")) else {
            throw S.Failure.invalid
        }
        let s = try S.parseState(raw)
        let h = try S.stateHash(s)
        #expect(h.hex == o.s("hash"))
        return s
    }
    func stmt() throws -> S.RecoveryStatement {
        guard let raw = Data(base64Encoded: v.s("recovery_statement")) else {
            throw S.Failure.invalid
        }
        return try S.RecoveryStatement.parse(raw)
    }
    func pin() throws -> S.Pin { try S.pin(for: try state("genesis")) }
    func phoneX() throws -> Data { try hex("phone_x") }
    func machineX() throws -> Data { try hex("machine_x") }

    func signedLease(_ state:S.State) throws -> S.HeadLease {
        try S.HeadLease.sign(state, signer: try SigningKey(seed: hex("phone_seed")), now: Int64(state.core.issuedAt))
    }
    @Test func committedLeaseAndKeyCacheSurviveStaleSameHeadWrites() throws {
        let genesis=try state("genesis"), head=try state("added")
        let secret=try S.openGrant(pin(),head,recipient:genesis.core.authorityID,xPrivate:phoneX())
        var old=try S.Record(pin:pin(),states:[1:genesis,2:head],memberID:genesis.core.authorityID,xPrivate:hex("phone_x"),member:true,secret:secret)
        let first=try S.HeadLease.parse(Data(base64Encoded:v.s("lease"))!)
        let renewed=try S.HeadLease.parse(Data(base64Encoded:v.s("renewed_lease"))!)
        let bootstrap = try S.Record(pin:pin(),states:[1:genesis],memberID:genesis.core.authorityID,xPrivate:hex("phone_x"),member:true,secret:S.openGrant(pin(),genesis,recipient:genesis.core.authorityID,xPrivate:phoneX()))
        #expect(try old.includesInstallation(bootstrap))
        #expect(try !bootstrap.includesInstallation(old))
        old.lease=renewed;old.floor=renewed.notAfter;old.contentKeys[1]=Data(repeating:42,count:32)
        var stale=old;stale.lease=first;stale.floor=1;stale.contentKeys=[:]
        #expect(throws:S.Failure.self) { try stale.mergingCommitted(old) }
        stale.lease=nil
        let merged=try stale.mergingCommitted(old)
        #expect(merged.lease==renewed && merged.floor==renewed.notAfter && merged.contentKeys[1]==old.contentKeys[1])
    }
    @Test func verifiesTheGoChainAndGrants() throws {
        let p = try pin()
        let genesis = try state("genesis")
        let added = try state("added")
        let renewal = try state("renewal")
        let recovery = try state("recovery")
        try S.verifyChain(pin: p, states: [genesis, added, renewal, recovery], proofs: [nil, nil, nil, try stmt()])
        #expect(try S.stateBytes(added) == {
            let o = v["added"] as! [String: Any]
            return Data(base64Encoded: o.s("state"))!
        }())
        let phone = try S.openGrant(p, added, recipient: genesis.core.authorityID, xPrivate: phoneX())
        #expect(phone == (try hex("content_secret_2")))
        let machine = try S.openGrant(p, added, recipient: (try state("added")).core.members[1].id, xPrivate: machineX())
        #expect(machine == (try hex("content_secret_2")))
        // The old authority client has no grant in the recovery state.
        #expect(throws: S.Failure.self) {
            _ = try S.openGrant(p, recovery, recipient: genesis.core.authorityID, xPrivate: phoneX())
        }
        #expect(recovery.prevSecret.isEmpty)
        #expect(recovery.core.contentEpoch == renewal.core.contentEpoch + 1)
    }

    @Test func rejectsATamperedStateAndPrevSecret() throws {
        let p = try pin()
        let genesis = try state("genesis")
        let added = try state("added")
        let renewal = try state("renewal")
        let stmt = try stmt()
        var bad = added
        bad.prevSecret[0] ^= 1
        #expect(throws: S.Failure.self) { try S.verifyTransition(parent: genesis, next: bad) }
        bad = renewal
        bad.core.issuedAt += 1
        #expect(throws: S.Failure.self) {
            try S.verifyChain(pin: p, states: [genesis, added, bad], proofs: [nil, nil, nil])
        }
        bad = renewal
        bad.signature[0] ^= 1
        #expect(throws: S.Failure.self) {
            try S.verifyChain(pin: p, states: [genesis, added, bad], proofs: [nil, nil, nil])
        }
        let recovery = try state("recovery")
        #expect(throws: S.Failure.self) {
            try S.verifyChain(pin: p, states: [genesis, added, renewal, recovery], proofs: [nil, nil, nil, nil])
        }
        var replay = recovery
        replay.core.removed = []
        #expect(throws: S.Failure.self) {
            try S.verifyChain(pin: p, states: [genesis, added, renewal, replay], proofs: [nil, nil, nil, stmt])
        }
        #expect(throws: S.Failure.self) {
            try S.verifyChain(pin: p, states: [try state("genesis"), recovery], proofs: [nil, nil])
        }
    }

    @Test func walksPrevSecretAndStopsAtTheRecoveryBoundary() throws {
        let p = try pin()
        let genesis = try state("genesis")
        let added = try state("added")
        let renewal = try state("renewal")
        let recovery = try state("recovery")
        let machine = added.core.members[1]
        let machineKey = try S.openGrant(p, recovery, recipient: machine.id, xPrivate: machineX())
        let states: [UInt64: S.State] = [
            genesis.core.epoch: genesis, added.core.epoch: added,
            renewal.core.epoch: renewal, recovery.core.epoch: recovery,
        ]
        var record = try S.Record(
            pin: p, states: states, memberID: machine.id,
            xPrivate: try hex("machine_x"), member: true,
            secret: machineKey,
            contentKeys: [
                added.core.contentEpoch: try hex("content_secret_2"),
                genesis.core.contentEpoch: try hex("content_secret_1"),
            ],
            recovery: [recovery.core.epoch: try stmt()])
        #expect(try record.contentSecret(1) == (try hex("content_secret_1")))
        #expect(try record.contentSecret(2) == (try hex("content_secret_2")))
        #expect(try record.contentSecret(3) == machineKey)
        // The old authority client was removed by the recovery. It keeps its
        // own grants from the states it belonged to, but has nothing at the
        // recovery: the empty PrevSecret is a boundary, not a retry.
        let oldPhone = genesis.core.authorityID
        let removed = try S.Record(
            pin: p, states: states, memberID: oldPhone, xPrivate: try hex("phone_x"),
            member: false, secret: try hex("content_secret_2"),
            recovery: [recovery.core.epoch: try stmt()])
        #expect(try removed.contentSecret(1) == (try hex("content_secret_1")))
        #expect(try removed.contentSecret(2) == (try hex("content_secret_2")))
        #expect(throws: S.Failure.unavailable) { try removed.contentSecret(3) }
    }

    @Test func rejectsAStateFromTheFutureBeforeInstallingIt() async throws {
        let p = try pin()
        let genesis = try state("genesis")
        var future = try state("added")
        future.core.issuedAt = 9_999_999_999_999

        let raw = try S.stateBytes(future)
        let base = try S.Record(
            pin: p, states: [1: genesis], memberID: genesis.core.authorityID,
            xPrivate: try hex("phone_x"), member: true,
            secret: try hex("content_secret_1"))
        let client = AuthoritySecurity(
            store: MemorySecurityStore(base), memberID: genesis.core.authorityID,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { 1_700_000_000_001 },
            fetchStates: { after in
                var res = PBGetSecurityStatesResponse()
                res.headEpoch = 2
                res.lease = Data()
                if after < 2 {
                    var value = PBSecurityState()
                    value.state = raw
                    res.states = [value]
                }
                return res
            })
        await #expect(throws: S.Failure.clock) { try await client.sync() }
    }

    @Test func verifyChainRefusesFutureStates() throws {
        let genesis = try state("genesis")
        var future = genesis
        future.core.issuedAt = UInt64(Date().timeIntervalSince1970 * 1000) + 10 * 60_000

        future.signature = try SigningKey(seed: try hex("phone_seed")).sign(
            S.signBytes(future))
        let p = S.Pin(
            account: genesis.core.account, domain: genesis.core.domain,
            genesis: try S.stateHash(future))
        #expect(throws: S.Failure.clock) {
            try S.verifyChain(pin: p, states: [future], proofs: [nil])
        }
    }

    @Test func liveControlAlsoChecksTheHeadsIssuedAt() async throws {
        let genesis = try state("genesis")
        let added = try state("added")
        let secret = try hex("content_secret_2")
        let signer = try SigningKey(seed: try hex("phone_seed"))
        let machine = added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id
        let phone = deviceId(publicKey: signer.publicKey)
        let lease = try S.HeadLease.parse(Data(base64Encoded: v.s("lease"))!)
        let record = try S.Record(
            pin: pin(), states: [1: genesis, 2: added], memberID: phone,
            xPrivate: try phoneX(), member: true, secret: secret, lease: lease)
        let phoneSdk = try TestCoreSecurity(
            store: MemorySecurityStore(record), memberID: phone, signingKey: signer,
            clock: { added.core.issuedAt })
        defer { Task { await phoneSdk.close() } }
        var cmd = PBCommand(); cmd.interrupt.sessionID = "s"
        let env = try await phoneSdk.seal(
            purpose: .command, machine: machine, request: randomHex(16),
            deadline: lease.notAfter, plaintext: cmd.serializedData())

        let mSeed = try hex("machine_seed")
        let mXPriv = try machineX()
        let mRecord = try S.Record(
            pin: pin(), states: [1: genesis, 2: added], memberID: machine,
            xPrivate: mXPriv, member: true, secret: secret, lease: lease)
        let earlyRunner = try CoreSecurityRunner(executor: SecurityExecutor(
            signer: { try? SigningKey(seed: mSeed) },
            xPrivate: { mXPriv }))
        defer { earlyRunner.close() }
        try earlyRunner.syncRecord(mRecord, nowMs: 1_600_000_000_000)
        #expect(throws: S.Failure.clock) {
            _ = try earlyRunner.runOp {
                $0.openEnvelope = .with {
                    $0.envelope = env
                    $0.expectedPurpose = S.Purpose.command.rawValue
                    $0.machineID = machine
                    $0.history = false
                }
            }
        }
        try earlyRunner.syncRecord(mRecord, nowMs: Int64(added.core.issuedAt))
        _ = try earlyRunner.runOp {
            $0.openEnvelope = .with {
                $0.envelope = env
                $0.expectedPurpose = S.Purpose.command.rawValue
                $0.machineID = machine
                $0.history = false
            }
        }
    }

    @Test func deviceMetaIsSelfOnly() async throws {
        let genesis = try state("genesis")
        let added = try state("added")
        let phone = added.core.authorityID
        let machine = added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id
        let record = try S.Record(
            pin: pin(), states: [1: genesis, 2: added], memberID: phone,
            xPrivate: try phoneX(), member: true, secret: try hex("content_secret_2"),
            lease: signedLease(added))
        let sdk = try TestCoreSecurity(
            store: MemorySecurityStore(record), memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt })
        defer { Task { await sdk.close() } }
        let meta = try PBDeviceMeta.with { $0.name = "phone" }.serializedData()
        let good = try await sdk.seal(purpose: .deviceMeta, request: phone, seq: 1, plaintext: meta)
        _ = try await sdk.openHistory(good, expectedPurpose: .deviceMeta, request: phone, seq: 1)
        await #expect(throws: S.Failure.self) {
            _ = try await sdk.seal(purpose: .deviceMeta, request: machine, seq: 1, plaintext: meta)
        }
    }

    private func validTestPlaintext(for purpose: S.Purpose, machine: String, session: String, seq: UInt64) throws -> Data {
        switch purpose {
        case .event:
            var ev = PBEvent(); ev.sessionID = session; ev.seq = seq; ev.agentMessage.text = "ok"
            return try ev.serializedData()
        case .sessionMeta:
            return try PBSession.with { $0.id = session }.serializedData()
        case .sessionKey:
            return Data(repeating: 7, count: 32)
        case .machineMeta:
            return try PBMachineMeta().serializedData()
        case .command:
            var cmd = PBCommand(); cmd.interrupt.sessionID = session
            return try cmd.serializedData()
        case .result:
            return try PBCommandResult().serializedData()
        case .terminalOutput:
            return try PBTerminalFrame.with { $0.output = Data("ok".utf8) }.serializedData()
        case .terminalInput:
            return try PBTerminalFrame.with { $0.input = Data("k".utf8) }.serializedData()
        case .terminalResize:
            return try PBTerminalFrame.with { $0.resize.cols = 80; $0.resize.rows = 24 }.serializedData()
        case .terminalHistory:
            return try PBTerminalFrame.with { $0.historyRequest = PBTerminalHistoryRequest() }.serializedData()
        case .notify:
            return try PBNotifyContent.with {
                $0.machineID = machine
                $0.sessionID = session
                $0.kind = .turnEnded
            }.serializedData()
        case .image:
            return try PBImagePayload.with { $0.format = .png; $0.data = Data([1, 2, 3]) }.serializedData()
        case .prefs:
            return try PBProjectPrefs.with { $0.schema = 1 }.serializedData()
        case .deviceMeta:
            return try PBDeviceMeta().serializedData()
        }
    }

    @Test(arguments: [S.Purpose.event, .sessionMeta, .sessionKey, .machineMeta, .command,
        .result, .terminalOutput, .terminalInput, .notify, .image, .prefs, .deviceMeta,
        .terminalResize, .terminalHistory])
    func everyNewPublicationNeedsALiveLease(purpose: S.Purpose) async throws {
        let genesis = try state("genesis"), head = try state("added")
        let first = try S.HeadLease.parse(Data(base64Encoded: v["lease"] as! String)!)
        let renewedRaw = Data(base64Encoded: v["renewed_lease"] as! String)!
        let clientRole = purpose.control || purpose == .prefs || purpose == .deviceMeta
        let member = head.core.members.first { $0.kind == (clientRole ? 2 : 1) }!
        let machine = head.core.members.first { $0.kind == 1 }!.id
        let key = try SigningKey(seed: hex(clientRole ? "phone_seed" : "machine_seed"))
        let record = try S.Record(pin: pin(), states: [1: genesis, 2: head], memberID: member.id,
            xPrivate: hex(clientRole ? "phone_x" : "machine_x"), member: true,
            secret: hex("content_secret_2"), lease: first)
        let store = MemorySecurityStore(record)
        let clock = PublicationClock(first.issuedAt)
        let sdk = try TestCoreSecurity(store: store, memberID: member.id, signingKey: key,
            clock: { clock.now() }, fetchStates: { _ in
                var res = PBGetSecurityStatesResponse(); res.headEpoch = 2; res.lease = renewedRaw
                return res
            })
        defer { Task { await sdk.close() } }
        let standalone = purpose == .prefs || purpose == .deviceMeta
        let noSession = standalone || [.machineMeta, .command, .result].contains(purpose)
        let noRequest = [S.Purpose.event, .sessionMeta, .sessionKey, .machineMeta].contains(purpose)
        let request = purpose == .prefs ? "project-prefs" : purpose == .deviceMeta ? member.id : noRequest ? "" : "lease-request-123456"
        let plain = try validTestPlaintext(for: purpose, machine: machine, session: "lease-session", seq: 1)
        let seed = Data(repeating:7,count:32)
        func seal() async throws -> PBEnvelope {
            try await sdk.seal(purpose: purpose, machine: standalone ? "" : machine,
                session: noSession ? "" : "lease-session", request: request, seq: 1,
                deadline: purpose.control ? first.notAfter+60000 : 0, plaintext: plain, sessionSeed: seed)
        }
        let stored = try await seal()
        clock.set(first.notAfter)
        await #expect(throws: S.Failure.lease) { try await seal() }
        if !purpose.control {
            #expect(try await sdk.openHistory(stored, expectedPurpose: purpose, sessionSeed: seed).1 == plain)
        }
        let renewed = try await sdk.sync()
        #expect(renewed.states.count == 2)
        _ = try await seal()
    }

    @Test(arguments: [false, true])
    func failedPullPersistsObservedExpiryAndRefreshesSealingCache(nearSavedFloor: Bool) async throws {
        let genesis = try state("genesis"), head = try state("added")
        let lease = try S.HeadLease.parse(Data(base64Encoded: v.s("lease"))!)
        let key = try SigningKey(seed: hex("phone_seed"))
        let phone = head.core.authorityID
        let floor = nearSavedFloor ? lease.notAfter - 1 : lease.issuedAt
        let record = try S.Record(pin: pin(), states: [1: genesis, 2: head], memberID: phone,
            xPrivate: hex("phone_x"), member: true, floor: floor, floorSaved: floor,
            secret: hex("content_secret_2"), lease: lease)
        let store = MemorySecurityStore(record)
        let clock = PublicationClock(floor)
        let sdk = try TestCoreSecurity(store: store, memberID: phone, signingKey: key,
            clock: { clock.now() }, fetchStates: { _ in
                #expect(try store.readSecurity().floor == lease.notAfter)
                throw S.Failure.unavailable
            })
        _ = try await sdk.identity() // Cache the pre-expiry record.
        clock.set(lease.notAfter)
        await #expect(throws: S.Failure.unavailable) { try await sdk.sync() }
        #expect(try store.readSecurity().floor == lease.notAfter)
        clock.set(lease.issuedAt)
        let reopened = try TestCoreSecurity(store: store, memberID: phone, signingKey: key,
            clock: { clock.now() }, fetchStates: { _ in throw S.Failure.unavailable })
        for client in [sdk, reopened] {
            await #expect(throws: S.Failure.lease) {
                try await client.seal(purpose: .prefs, request: "project-prefs", seq: 1,
                    plaintext: Data([1]))
            }
            await client.close()
        }
    }

    @Test func expiredLeaseExplainsWhyAChangeCannotBeSaved() {
        let text = errorText(S.Failure.lease)
        #expect(text.contains("无法保存"))
        #expect(text.contains("续期"))
        #expect(text.contains("历史"))
    }

    @Test func sealingUsesIndependentRandomNonces() async throws {
        let p = try pin()
        let genesis = try state("genesis")
        let added = try state("added")
        let phone = added.core.authorityID
        let machine = added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id
        let secret = try hex("content_secret_2")
        let lease = try signedLease(added)
        let record = try S.Record(
            pin: p, states: [1: genesis, 2: added], memberID: phone,
            xPrivate: try hex("phone_x"), member: true,
            secret: secret)
        let store = MemorySecurityStore(record)
        let client = try TestCoreSecurity(
            store: store, memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt },
            fetchStates: { _ in throw S.Failure.storage })
        defer { Task { await client.close() } }
        let mSeed = try hex("machine_seed")
        let mXPriv = try machineX()
        let mRecord = try S.Record(
            pin: pin(), states: [1: genesis, 2: added], memberID: machine,
            xPrivate: mXPriv, member: true, secret: secret, lease: lease)
        let mRunner = try CoreSecurityRunner(executor: SecurityExecutor(
            signer: { try? SigningKey(seed: mSeed) },
            xPrivate: { mXPriv }))
        defer { mRunner.close() }
        try mRunner.syncRecord(mRecord, nowMs: Int64(added.core.issuedAt))
        var cmd = PBCommand(); cmd.interrupt.sessionID = "s"
        let cmdPlain = try cmd.serializedData()
        var nonces: Set<Data> = []
        for _ in 1...3 {
            let env = try await client.seal(
                purpose: .command, machine: machine, request: "0123456789abcdef",
                deadline: 1_700_000_200_000, plaintext: cmdPlain)
            let res = try mRunner.runOp {
                $0.openEnvelope = .with {
                    $0.envelope = env
                    $0.expectedPurpose = S.Purpose.command.rawValue
                    $0.machineID = machine
                }
            }
            #expect(res.opened.epoch == added.core.epoch)
            #expect(nonces.insert(env.nonce).inserted)
        }
        // Reopening also uses a fresh random nonce.
        let reopened = try TestCoreSecurity(
            store: store, memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt },
            fetchStates: { _ in throw S.Failure.storage })
        defer { Task { await reopened.close() } }
        let env = try await reopened.seal(
            purpose: .command, machine: machine, request: "0123456789abcdef",
            deadline: 1_700_000_200_000, plaintext: cmdPlain)
        #expect(nonces.insert(env.nonce).inserted)
    }

    @Test func controlNeedsTheHeadLeaseAndInstallsANewContentEpoch() async throws {
        let p = try pin()
        let genesis = try state("genesis")
        let added = try state("added")
        let phone = added.core.authorityID
        let machine = added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id
        let addedLease = try signedLease(added)
        let cmd = try PBCommand.with { $0.interrupt.sessionID = "s" }.serializedData()
        let fullRecord = try S.Record(
            pin: p, states: [1: genesis, 2: added], memberID: phone, xPrivate: try hex("phone_x"),
            member: true, secret: try hex("content_secret_2"), lease: addedLease)
        let fullSdk = try TestCoreSecurity(
            store: MemorySecurityStore(fullRecord), memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt + S.lease + 1 })
        defer { Task { await fullSdk.close() } }
        // Expired head lease refuses control seal.
        await #expect(throws: S.Failure.lease) {
            _ = try await fullSdk.seal(
                purpose: .command, machine: machine, request: "0123456789abcdef",
                deadline: added.core.issuedAt + S.lease, plaintext: cmd)
        }
        // Stale content epoch sealed under added (epoch 2) cannot open live against head = handoff_parent (epoch 3).
        let addedRecord = try S.Record(
            pin: p, states: [1: genesis, 2: added], memberID: phone, xPrivate: try hex("phone_x"),
            member: true, secret: try hex("content_secret_2"), lease: addedLease)
        let addedSdk = try TestCoreSecurity(
            store: MemorySecurityStore(addedRecord), memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt })
        defer { Task { await addedSdk.close() } }
        let staleCmdEnv = try await addedSdk.seal(
            purpose: .command, machine: machine, request: "0123456789abcdef",
            deadline: added.core.issuedAt + S.lease, plaintext: cmd)
        let mSeed = try hex("machine_seed")
        let mXPriv = try machineX()
        let handoffParent = try state("handoff_parent")
        let handoffSecret = try S.openGrant(p, handoffParent, recipient: machine, xPrivate: mXPriv)
        let mHeadRecord = try S.Record(
            pin: p, states: [1: genesis, 2: added, 3: handoffParent], memberID: machine, xPrivate: mXPriv,
            member: true, secret: handoffSecret, lease: try signedLease(handoffParent))
        let mHeadRunner = try CoreSecurityRunner(executor: SecurityExecutor(
            signer: { try? SigningKey(seed: mSeed) },
            xPrivate: { mXPriv }))
        defer { mHeadRunner.close() }
        try mHeadRunner.syncRecord(mHeadRecord, nowMs: Int64(handoffParent.core.issuedAt))
        #expect(throws: S.Failure.stale) {
            _ = try mHeadRunner.runOp {
                $0.openEnvelope = .with {
                    $0.envelope = staleCmdEnv
                    $0.expectedPurpose = S.Purpose.command.rawValue
                    $0.machineID = machine
                    $0.history = false
                }
            }
        }
        let mFullRecord = try S.Record(
            pin: p, states: [1: genesis, 2: added], memberID: machine, xPrivate: mXPriv,
            member: true, secret: try hex("content_secret_2"), lease: addedLease)
        let mRunner = try CoreSecurityRunner(executor: SecurityExecutor(
            signer: { try? SigningKey(seed: mSeed) },
            xPrivate: { mXPriv }))
        defer { mRunner.close() }
        try mRunner.syncRecord(mFullRecord, nowMs: Int64(added.core.issuedAt))
        // Installing a state changes the content key.
        let base = try S.Record(
            pin: p, states: [1: genesis], memberID: phone, xPrivate: try hex("phone_x"),
            member: true,
            secret: try hex("content_secret_1"))
        let store = MemorySecurityStore(base)
        let leaseRaw = try signedLease(added).bytes()
        let client = try TestCoreSecurity(
            store: store, memberID: phone,
            signingKey: try SigningKey(seed: try hex("phone_seed")),
            clock: { added.core.issuedAt },
            fetchStates: { after in
                var res = PBGetSecurityStatesResponse()
                res.headEpoch = 2
                res.lease = leaseRaw
                if after < 2 {
                    var value = PBSecurityState()
                    value.state = try S.stateBytes(added)
                    res.states = [value]
                }
                return res
            })
        defer { Task { await client.close() } }
        let installed = try await client.sync()
        #expect(installed.headEpoch == 2)
        let env = try await client.seal(
            purpose: .command, machine: machine, request: "0123456789abcdef",
            deadline: (added.core.issuedAt + S.lease), plaintext: cmd)
        let opened = try mRunner.runOp {
            $0.openEnvelope = .with {
                $0.envelope = env
                $0.expectedPurpose = S.Purpose.command.rawValue
                $0.machineID = machine
                $0.history = false
            }
        }.opened
        #expect(opened.epoch == 2 && opened.contentEpoch == 2)
    }

    @Test func readsAStoredStateOnlyAsHistory() async throws {
        let added = try state("added")
        let p = try pin()
        let genesis = try state("genesis")
        let phone = added.core.authorityID
        let machine = added.core.members.first { $0.kind == S.Kind.machine.rawValue }!.id
        let mRecord = try S.Record(
            pin: p, states: [1: genesis, 2: added], memberID: machine, xPrivate: try machineX(),
            member: true, secret: try hex("content_secret_2"), lease: try signedLease(added))
        let mSdk = try TestCoreSecurity(
            store: MemorySecurityStore(mRecord), memberID: machine,
            signingKey: try SigningKey(seed: try hex("machine_seed")),
            clock: { added.core.issuedAt })
        defer { Task { await mSdk.close() } }
        var ev = PBEvent(); ev.sessionID = "session"; ev.seq = 1; ev.agentMessage.text = "hi"
        var bad = try await mSdk.seal(
            purpose: .event, machine: machine, session: "session", seq: 1,
            plaintext: ev.serializedData(), sessionSeed: Data(repeating: 7, count: 32))
        bad.signature[0] ^= 1
        let phoneSeed = try hex("phone_seed")
        let phoneXPriv = try hex("phone_x")
        let genRunner = try CoreSecurityRunner(executor: SecurityExecutor(
            signer: { try? SigningKey(seed: phoneSeed) },
            xPrivate: { phoneXPriv }))
        defer { genRunner.close() }
        let genRecord = try S.Record(
            pin: p, states: [1: genesis], memberID: phone, xPrivate: phoneXPriv,
            member: true, secret: try hex("content_secret_1"), lease: try signedLease(genesis))
        try genRunner.syncRecord(genRecord, nowMs: Int64(added.core.issuedAt))
        for history in [false, true] {
            #expect(throws: S.Failure.self) {
                _ = try genRunner.runOp {
                    $0.openEnvelope = .with {
                        $0.envelope = bad
                        $0.expectedPurpose = S.Purpose.event.rawValue
                        $0.machineID = machine
                        $0.sessionID = "session"
                        $0.seq = 1
                        $0.history = history
                        $0.overrideSessionSeed = Data(repeating: 7, count: 32)
                    }
                }
            }
        }
    }
}

/// A storage that keeps the whole record, for counter and chain tests.
final class MemorySecurityStore: DeviceSecurity.Storage, @unchecked Sendable {
    private let lock = NSLock()
    private var record: DeviceSecurity.Record
    init(_ record: DeviceSecurity.Record) {
        var r=record
        if r.lease == nil, let signer=try? SigningKey(seed:Data(repeating:0x11,count:32)), deviceId(publicKey:signer.publicKey)==r.state.core.authorityID {
            r.lease = try? DeviceSecurity.HeadLease.sign(r.state, signer: signer, now: Int64(r.state.core.issuedAt))
        }
        self.record=r
    }
    func readSecurity() throws -> DeviceSecurity.Record {
        lock.lock()
        defer { lock.unlock() }
        var r = record
        return r
    }
    func initializeSecurity(_ record: DeviceSecurity.Record) throws {
        throw DeviceSecurity.Failure.conflict
    }
    func writeSecurity(expectedHead: Data, _ next: DeviceSecurity.Record) throws
        -> DeviceSecurity.Record
    {
        lock.lock()
        defer { lock.unlock() }
        guard try DeviceSecurity.stateHash(record.state) == expectedHead else {
            throw DeviceSecurity.Failure.stale
        }
        record = try next.mergingCommitted(record)
        return record
    }
}

private final class PublicationClock: @unchecked Sendable {
    private let lock = NSLock()
    private var value: UInt64
    init(_ value: UInt64) { self.value = value }
    func now() -> UInt64 { lock.withLock { value } }
    func set(_ value: UInt64) { lock.withLock { self.value = value } }
}

extension DeviceSecurityTests {
 @Test func receivesDataOnlyMachineDiskNotification() async throws {
  let genesis=try state("genesis"),head=try state("added"),machine=head.core.members.first { $0.kind == 1 }!.id
  let record=try S.Record(pin:pin(),states:[1:genesis,2:head],memberID:machine,xPrivate:hex("machine_x"),member:true,secret:hex("content_secret_2"),lease:signedLease(head))
  let sdk=try TestCoreSecurity(store:MemorySecurityStore(record),memberID:machine,signingKey:try SigningKey(seed:hex("machine_seed")),clock:{head.core.issuedAt},fetchStates:{_ in PBGetSecurityStatesResponse()})
  defer { Task { await sdk.close() } }
  var c=PBNotifyContent();c.kind = .machineDisk;c.machineID=machine;c.machineName="电脑 A";c.diskStatus.observedMs=1700000000000;c.diskStatus.availableBytes=1_500_000_000;c.diskStatus.pressure = .critical
  let env=try await sdk.seal(purpose:.notify,machine:machine,session:"",request:"disk:1700000000000:3",plaintext:c.serializedData())
  let key=try S.NotifyRecord(record:record).bytes()
  let notice=try receiveMachineDiskNotification(envelope:env.serializedData(),notifyRecord:key,locale:"en")
  #expect(notice?.machineID == machine);#expect(notice?.body == "Clean up now or sessions may not start.")
  var bad=env;bad.signature[0]^=1
  #expect(throws:S.Failure.self){_ = try receiveMachineDiskNotification(envelope:bad.serializedData(),notifyRecord:key,locale:"en")}
 }
}
