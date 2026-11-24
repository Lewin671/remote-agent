import CryptoKit
import Foundation
import RACore
import RAProto
import RAWebRTC
import Testing

@testable import RemoteAgent

/// Acceptance of the direct path in the real app stack on a simulator: the real SDK, a real relay
/// server, a real STUN service and a daemon-side relay on a controllable backend, all started by
/// `tmp/ra-transport-1e/e2e/run.py`, which hands this test a directory (RA_E2E_DIR) and a
/// scenario (RA_E2E_SCENARIO). Without them the suite is skipped, so the ordinary checks never
/// need a server. The test and the script talk through files in that directory only.
enum E2E {
    static let dir = ProcessInfo.processInfo.environment["RA_E2E_DIR"]
    static let scenario = ProcessInfo.processInfo.environment["RA_E2E_SCENARIO"] ?? "positive"
}

/// The real factory, with two faults a network could cause: every path cut at once, and every
/// local candidate swallowed (what UDP being dropped looks like to the other side).
final class E2EFactory: RtcFactory, @unchecked Sendable {
    private let inner = WebRtcFactory()
    private let lock = NSLock()
    private var pcs: [any RtcPeerConnection] = []
    private var swallowing = false
    private(set) var made = 0
    var log: @Sendable (String) -> Void = { _ in }
    var swallow: Bool {
        get { lock.withLock { swallowing } }
        set { lock.withLock { swallowing = newValue } }
    }
    struct Filter: RtcPeerListener {
        let inner: any RtcPeerListener
        let owner: E2EFactory
        func onIceCandidate(_ c: RtcIceCandidate) { if !owner.swallow { inner.onIceCandidate(c) } }
        func onIceGatheringComplete() { inner.onIceGatheringComplete() }
        func onConnectionStateChange(_ s: RtcPeerState) { owner.log("sdk connection state \(s)"); inner.onConnectionStateChange(s) }
        func onUnexpectedDataChannel() { inner.onUnexpectedDataChannel() }
        func onIceConnectionStateChange(_ state: String) { inner.onIceConnectionStateChange(state) }
        func onIceGatheringStateChange(_ state: String) { inner.onIceGatheringStateChange(state) }
        func onIceCandidateError(code: Int, address: String) { inner.onIceCandidateError(code: code, address: address) }
    }
    func create(stun: [String], listener: any RtcPeerListener) throws -> any RtcPeerConnection {
        let real = try inner.create(stun: stun, listener: Filter(inner: listener, owner: self))
        let pc = Spy(real: real, owner: self, n: lock.withLock { made + 1 })
        lock.withLock { pcs.append(pc); made += 1 }
        return pc
    }
    /// Says what happens on the channels: opened, closed, how many bytes each way.
    final class Spy: RtcPeerConnection, @unchecked Sendable {
        let real: any RtcPeerConnection, owner: E2EFactory, n: Int
        private let flag = NSLock()
        private var _dead = false
        /// Everything on this path's channels goes nowhere, in both directions, and nothing says so.
        var dead: Bool { get { flag.withLock { _dead } } set { flag.withLock { _dead = newValue } } }
        init(real: any RtcPeerConnection, owner: E2EFactory, n: Int) { self.real = real; self.owner = owner; self.n = n }
        var maxMessageSize: Int? { real.maxMessageSize }
        struct Channel: RtcDataChannelListener {
            let inner: any RtcDataChannelListener, owner: E2EFactory, name: String, pc: Spy
            func onStateChange() { owner.log("pc\(name) state change"); inner.onStateChange() }
            func onBufferedAmountChange() { inner.onBufferedAmountChange() }
            func onMessage(_ data: Data) { if pc.dead { return }; inner.onMessage(data) }
        }
        func createDataChannel(id: Int, ordered: Bool, maxRetransmits: Int?, listener: any RtcDataChannelListener) throws -> any RtcDataChannel {
            let c = try real.createDataChannel(id: id, ordered: ordered, maxRetransmits: maxRetransmits, listener: Channel(inner: listener, owner: owner, name: "\(n).ch\(id)", pc: self))
            return SpyChannel(real: c, owner: owner, name: "\(n).ch\(id)", pc: self)
        }
        func createOffer(iceRestart: Bool) async throws -> String { try await real.createOffer(iceRestart: iceRestart) }
        func createAnswer() async throws -> String { try await real.createAnswer() }
        func setLocalDescription(offer: Bool, sdp: String) async throws { try await real.setLocalDescription(offer: offer, sdp: sdp) }
        func setRemoteDescription(offer: Bool, sdp: String) async throws {
            do { try await real.setRemoteDescription(offer: offer, sdp: sdp) }
            catch { owner.log("pc\(n) remote description FAILED \(error)"); throw error }
        }
        func addIceCandidate(_ candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) async throws {
            do { try await real.addIceCandidate(candidate, sdpMid: sdpMid, sdpMLineIndex: sdpMLineIndex, usernameFragment: usernameFragment) }
            catch { owner.log("pc\(n) addIceCandidate FAILED \(error)"); throw error }
        }
        func readStats() async -> PBCoreDirectStatsReport { await real.readStats() }
        func close() { real.close() }
    }
    final class SpyChannel: RtcDataChannel, @unchecked Sendable {
        let real: any RtcDataChannel, owner: E2EFactory, name: String, pc: Spy
        init(real: any RtcDataChannel, owner: E2EFactory, name: String, pc: Spy) { self.real = real; self.owner = owner; self.name = name; self.pc = pc }
        var state: RtcChannelState { real.state }
        var bufferedAmount: Int { pc.dead ? 0 : real.bufferedAmount }
        func send(_ data: Data) -> Bool { if pc.dead { return true }; return real.send(data) }
        func close() { real.close() }
    }
    /// The network goes away under every open path, with no word to this side: a black hole.
    func cut() {
        let all = lock.withLock { () -> [any RtcPeerConnection] in let c = pcs; pcs = []; return c }
        for pc in all { (pc as? Spy)?.dead = true }
    }
}

struct E2EOff: DirectPreferenceStore {
    func isEnabled(server: String, deviceId: String, machineId: String) -> Bool { false }
    func setEnabled(server: String, deviceId: String, machineId: String, enabled: Bool) throws {}
}

private func expectedBytes(_ count: Int) -> Data {
    var d = Data(count: count)
    d.withUnsafeMutableBytes { raw in for i in 0..<count { raw[i] = UInt8(truncatingIfNeeded: i &* 31 &+ 17) } }
    return d
}

private func percentile(_ xs: [Double], _ p: Double) -> Double {
    let s = xs.sorted()
    guard !s.isEmpty else { return 0 }
    return s[min(s.count - 1, Int((p * Double(s.count)).rounded(.up)) - 1 < 0 ? 0 : Int((p * Double(s.count)).rounded(.up)) - 1)]
}

private func median(_ xs: [Double]) -> Double { percentile(xs, 0.5) }

@MainActor
@Suite(.serialized, .enabled(if: E2E.dir != nil))
struct DirectE2ETests {
    let dir = E2E.dir ?? "/nonexistent"
    let started = Date()

    nonisolated func say(_ s: String) { e2eSay(dir, started, s) }
    func save(_ r: [String: Any]) {
        if let data = try? JSONSerialization.data(withJSONObject: r, options: [.prettyPrinted, .sortedKeys]) { try? data.write(to: URL(fileURLWithPath: dir + "/result.json")) }
    }
    func read(_ name: String) throws -> String { try String(contentsOfFile: dir + "/" + name, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines) }

    /// Asks the script for the accepted-record count and the relay's byte counters, now.
    func mark(_ name: String) async throws -> [String: Int] {
        try Data().write(to: URL(fileURLWithPath: dir + "/mark-" + name))
        let ack = dir + "/mark-" + name + ".ack"
        for _ in 0..<600 {
            if let text = try? String(contentsOfFile: ack, encoding: .utf8), text.hasSuffix("\n") {
                var out: [String: Int] = [:]
                for pair in text.split(separator: " ") { let kv = pair.split(separator: "="); if kv.count == 2, let n = Int(kv[1].trimmingCharacters(in: .whitespacesAndNewlines)) { out[String(kv[0])] = n } }
                return out
            }
            try await Task.sleep(nanoseconds: 100_000_000)
        }
        throw UserError("the script did not answer the mark \(name)")
    }
    func setRelayDelay(ms: Int) throws { try String(ms).write(toFile: dir + "/proxy-delay-ms", atomically: true, encoding: .utf8) }

    func wait(_ what: String, seconds: Double, _ cond: @MainActor () async throws -> Bool) async throws -> Double {
        let t0 = Date()
        while Date().timeIntervalSince(t0) < seconds {
            if try await cond() { return Date().timeIntervalSince(t0) }
            try await Task.sleep(nanoseconds: 100_000_000)
        }
        throw UserError("timed out waiting for \(what)")
    }

    func make() async throws -> (Remote, CoreHost, CoreScreens, String, E2EFactory) {
        let server = try read("url"), token = try read("setup-token")
        let vault = Vault(service: "io.github.lewin671.remoteagent.e2e.\(UUID().uuidString)")
        let http = HTTP()
        var setupHost: StandaloneSecurityHost? = try StandaloneSecurityHost(store: vault, http: http, allowPlainHttp: true)
        defer { setupHost?.close() }
        guard let hostClient = setupHost?.client else { throw UserError("missing host") }
        _ = try await hostClient.bootstrap()
        _ = try await hostClient.prepareCreation(server: server)
        try await hostClient.confirmCreationCodeSaved()
        let created = try await hostClient.continueCreation(setupToken: token)
        let st = created.status
        let account = Account(
            server: !created.accountAdopted.server.isEmpty ? created.accountAdopted.server : st.server,
            deviceId: !created.accountAdopted.deviceID.isEmpty ? created.accountAdopted.deviceID : st.deviceID,
            accountId: !created.accountAdopted.accountID.isEmpty ? created.accountAdopted.accountID : st.accountID
        )
        setupHost?.close()
        setupHost = nil
        say("account made")
        let factory = E2EFactory()
        factory.log = { [dir, started] in e2eSay(dir, started, $0) }
        factory.swallow = E2E.scenario == "udpdrop"
        let prefs: any DirectPreferenceStore = E2E.scenario == "switchoff" ? E2EOff() : UserDefaultsDirectPreferences(UserDefaults(suiteName: "e2e-\(UUID().uuidString)")!)
        let remote = try Remote(account: account, http: http, log: { [dir, started] line in e2eSay(dir, started, "remote: " + line) }, securityStore: vault, allowPlainHttp: true, rtc: factory, directPreferences: prefs, directStun: [try read("stun-url")])
        let screens = await MainActor.run { CoreScreens() }
        let host = try CoreHost.live(remote: remote, account: account, defaults: .standard, screens: screens, onNavigate: { _ in })
        await host.startAsync()
        let initiation = try await remote.securityClient.startPairing(webOrigin: "https://example.com")
        try initiation.url.write(toFile: dir + "/link", atomically: true, encoding: .utf8)
        say("pairing code written")
        var polled = initiation
        _ = try await wait("a joining computer", seconds: 90) {
            polled = try await remote.securityClient.pollPairing(pairingId: initiation.pairingID)
            return polled.joined
        }
        _ = try await remote.securityClient.approvePairing(pairingId: initiation.pairingID, sasConfirmed: true)
        for _ in 0..<100 {
            try await remote.resumeSecurityOperation()
            _ = try await remote.finishPairingDelivery(pairingId: initiation.pairingID)
            let cur = try await remote.getSecurityStatus()
            if !cur.hasPendingOp { break }
            try await Task.sleep(nanoseconds: 100_000_000)
        }
        say("paired")
        var machine = ""
        _ = try await wait("the computer online", seconds: 90) {
            if let m = await MainActor.run(body: { screens.syncState.machines.first(where: \.reachable) }) {
                machine = m.machineID
                return true
            }
            return false
        }
        say("computer online \(machine.prefix(8))")
        return (remote, host, screens, machine, factory)
    }

    func report(_ host: CoreHost, _ screens: CoreScreens, _ machine: String) async -> PBDirectRouteReport {
        await host.openDirectDiagnostics(machineID: machine)
        return await MainActor.run { screens.directDiagnostics.report }
    }

    func call(_ remote: Remote, _ machine: String, session: String = "", _ cmd: PBCommand) async throws -> PBCommandResult {
        try await e2eCall(remote, machine, session, cmd)
    }
    func settings() -> PBCommand { var c = PBCommand(); c.getSettings = PBGetSettingsRequest(); return c }
    func chunk(_ offset: Int64) -> PBCommand { e2eChunk(offset) }

    @Test func scenario() async throws {
        do { try await run() } catch { say("FAILED: \(error)"); throw error }
    }

    func run() async throws {
        let (remote, host, screens, machine, factory) = try await make()
        defer { Task { await host.stopAsync() } }
        var r: [String: Any] = ["scenario": E2E.scenario]
        defer { save(r) }
        let watcher = Task {
            var last = ""
            while !Task.isCancelled {
                let s = await self.report(host, screens, machine)
                let rMs = s.hasRelayReadMs_p ? "\(s.relayReadMs)" : "-"
                let dMs = s.hasDirectReadMs_p ? "\(s.directReadMs)" : "-"
                let d = "status \(s.state) reason=\(s.reason) file=\(s.fileRoute == .direct) read=\(s.readRoute == .direct) warming=\(s.warming) compared=\(s.compared) pcs=\(factory.made) readMs=\(dMs)/\(rMs)"
                if d != last { say(d); last = d }
                try? await Task.sleep(nanoseconds: 250_000_000)
            }
        }
        defer { watcher.cancel() }
        switch E2E.scenario {
        case "positive": try await positive(remote, host, screens, machine, factory, &r)
        case "cut": try await cutOnly(remote, host, screens, machine, factory, &r)
        case "p99":
            try setRelayDelay(ms: 60)
            await host.retryDirectNow(machineID: machine)
            _ = try await wait("direct to establish", seconds: 120) { await self.report(host, screens, machine).state == .connected }
            try await download(remote, host, screens, machine, rounds: 3, &r)
            try setRelayDelay(ms: 0)
            try await approvals(remote, host, screens, machine, &r)
        default: try await negative(remote, host, screens, machine, factory, &r)
        }
        say("done")
    }

    // MARK: staying on the relay

    func negative(_ remote: Remote, _ host: CoreHost, _ screens: CoreScreens, _ machine: String, _ factory: E2EFactory, _ r: inout [String: Any]) async throws {
        var reads = 0, failures = 0, connected = false
        let t0 = Date()
        while Date().timeIntervalSince(t0) < 25 {
            do { _ = try await call(remote, machine, settings()); reads += 1 } catch { failures += 1; if failures == 1 { say("first read failure: \(error)") } }
            let s = await report(host, screens, machine)
            if s.state == .connected || s.readRoute == .direct || s.fileRoute == .direct { connected = true }
            try await Task.sleep(nanoseconds: 400_000_000)
        }
        let s = await report(host, screens, machine)
        say("\(E2E.scenario): reads=\(reads) failures=\(failures) state=\(s.state) reason=\(s.reason) pcs=\(factory.made)")
        r["reads"] = reads; r["failures"] = failures; r["everDirect"] = connected
        r["finalState"] = "\(s.state)"; r["finalReason"] = "\(s.reason)"; r["peerConnectionsMade"] = factory.made
        #expect(!connected, "stays on relay")
        #expect(failures == 0 && reads > 10, "relay reads keep working")
    }

    func cutOnly(_ remote: Remote, _ host: CoreHost, _ screens: CoreScreens, _ machine: String, _ factory: E2EFactory, _ r: inout [String: Any]) async throws {
        try setRelayDelay(ms: 60)
        await host.retryDirectNow(machineID: machine)
        _ = try await wait("direct to establish", seconds: 60) { await self.report(host, screens, machine).state == .connected }
        _ = try await wait("reads to go direct", seconds: 60) {
            _ = try? await call(remote, machine, settings())
            return await self.report(host, screens, machine).readRoute == .direct
        }
        let made = factory.made
        factory.cut(); say("cut")
        let t = try? await wait("a rebuilt path", seconds: 60) {
            _ = try? await call(remote, machine, settings())
            let st = await self.report(host, screens, machine)
            return factory.made > made && st.state == .connected
        }
        r["rebuiltSeconds"] = t ?? -1
        #expect(t != nil)
    }

    // MARK: the whole of it

    func positive(_ remote: Remote, _ host: CoreHost, _ screens: CoreScreens, _ machine: String, _ factory: E2EFactory, _ r: inout [String: Any]) async throws {
        try setRelayDelay(ms: 60)
        await host.retryDirectNow(machineID: machine)
        // 1. direct establishes
        let establish = try await wait("direct to establish", seconds: 60) { await self.report(host, screens, machine).state == .connected }
        r["directEstablishedSeconds"] = establish
        say("direct established after \(establish) s")

        // 2. reads upgrade fast
        var samples = 0
        let upgrade = try await wait("reads to go direct", seconds: 150) {
            _ = try? await call(remote, machine, settings()); samples += 1
            return await self.report(host, screens, machine).readRoute == .direct
        }
        let s2 = await report(host, screens, machine)
        r["readUpgradeSeconds"] = upgrade; r["readUpgradeSamples"] = samples
        r["readMsDirect"] = s2.hasDirectReadMs_p ? Int(s2.directReadMs) : -1
        r["readMsRelay"] = s2.hasRelayReadMs_p ? Int(s2.relayReadMs) : -1
        say("reads direct after \(upgrade) s, \(samples) test reads; median ms direct=\(r["readMsDirect"] ?? -1) relay=\(r["readMsRelay"] ?? -1)")

        try await download(remote, host, screens, machine, rounds: 2, &r)

        // 4. a cut falls back to the relay and rebuilds by itself
        let made = factory.made
        factory.cut()
        let cutAt = Date()
        var okReads = 0, badReads = 0, fellBack: Double?, rebuilt: Double?
        while Date().timeIntervalSince(cutAt) < 90, rebuilt == nil {
            do { _ = try await call(remote, machine, settings()); okReads += 1 } catch { badReads += 1 }
            let s = await report(host, screens, machine)
            if fellBack == nil, !(s.state == .connected && s.readRoute == .direct) { fellBack = Date().timeIntervalSince(cutAt) }
            if fellBack != nil, s.state == .connected, factory.made > made { rebuilt = Date().timeIntervalSince(cutAt) }
            try await Task.sleep(nanoseconds: 250_000_000)
        }
        r["cutFellBackSeconds"] = fellBack ?? -1; r["cutRebuiltSeconds"] = rebuilt ?? -1; r["cutReads"] = okReads; r["cutFailedReads"] = badReads
        say("cut: fell back after \(String(describing: fellBack)), rebuilt after \(String(describing: rebuilt)); reads ok=\(okReads) failed=\(badReads)")
        #expect(fellBack != nil && rebuilt != nil && badReads == 0)
        _ = try? await wait("reads direct again", seconds: 90) {
            _ = try? await call(remote, machine, settings())
            return await self.report(host, screens, machine).readRoute == .direct
        }

        // 5. an upload resumes at the persisted offset
        try await upload(remote, host, screens, machine, factory, &r)

        // 6. the approval flow under full load
        try setRelayDelay(ms: 0)
        try await approvals(remote, host, screens, machine, &r)
    }

    func download(_ remote: Remote, _ host: CoreHost, _ screens: CoreScreens, _ machine: String, rounds count: Int, _ r: inout [String: Any]) async throws {
        // Over 16 MiB, not in the accept log, hash equal.
        let total = 64 << 20, want = Data(SHA256.hash(data: expectedBytes(total)))
        var rounds: [[String: Any]] = []
        for round in 1...count {
            let before = try await mark("download\(round)-start")
            var hash = SHA256(); var offset: Int64 = 0
            let t0 = Date()
            while offset < Int64(total) {
                let c = try await call(remote, machine, session: "fx-session", chunk(offset)).getSharedFileChunk
                hash.update(data: c.data.prefix(Int(min(Int64(c.data.count), Int64(total) - offset))))
                offset += Int64(c.data.count)
            }
            let secs = Date().timeIntervalSince(t0)
            let after = try await mark("download\(round)-end")
            let ok = Data(hash.finalize()) == want
            let st = await report(host, screens, machine)
            let row: [String: Any] = ["round": round, "bytes": total, "seconds": secs, "MiBps": Double(total >> 20) / secs, "hashEqual": ok,
                "acceptedBefore": before["accepted"] ?? -1, "acceptedAfter": after["accepted"] ?? -1,
                "relayBytes": (after["proxy_bytes"] ?? 0) - (before["proxy_bytes"] ?? 0), "fileDirect": st.fileRoute == .direct]
            rounds.append(row); say("download \(row)")
            #expect(ok && before["accepted"] == after["accepted"])
        }
        r["downloads"] = rounds
    }

    func upload(_ remote: Remote, _ host: CoreHost, _ screens: CoreScreens, _ machine: String, _ factory: E2EFactory, _ r: inout [String: Any]) async throws {
        let total = 8 << 20, block = 256 << 10
        let data = Data((0..<total).map { UInt8(truncatingIfNeeded: ($0 &* 7) ^ ($0 >> 9)) })
        let sum = Data(SHA256.hash(data: data))
        let id = (0..<16).map { _ in String(format: "%02x", UInt8.random(in: 0...255)) }.joined()
        var begin = PBCommand(); begin.beginImageUpload.transferID = id; begin.beginImageUpload.totalBytes = Int64(total); begin.beginImageUpload.sha256 = sum; begin.beginImageUpload.fileName = "e2e.bin"
        var status = try await call(remote, machine, begin).beginImageUpload.status
        var offset = status.persistedBytes, puts = 0, cut = false, persistedAtCut: Int64 = -1, resumedAt: Int64 = -1
        var offsets: [Int64] = []
        while offset < Int64(total) {
            var put = PBCommand()
            put.putImageChunk.transferID = id; put.putImageChunk.offset = offset
            put.putImageChunk.data = data.subdata(in: Int(offset)..<Int(min(Int64(total), offset + Int64(block))))
            status = try await call(remote, machine, put).putImageChunk.status
            offsets.append(offset); puts += 1
            offset = status.persistedBytes
            if puts == 12, !cut {
                cut = true; factory.cut()
                var ask = PBCommand(); ask.getImageUploadStatus.transferID = id
                persistedAtCut = try await call(remote, machine, ask).getImageUploadStatus.status.persistedBytes
                resumedAt = persistedAtCut
                #expect(persistedAtCut == offset, "the durable offset is what the last acknowledged chunk said")
            }
        }
        var done = PBCommand(); done.completeImageUpload.transferID = id
        let final = try await call(remote, machine, done).completeImageUpload.status
        let st = await report(host, screens, machine)
        r["upload"] = ["bytes": total, "puts": puts, "persistedAtCut": persistedAtCut, "resumedAt": resumedAt, "complete": final.state == .complete,
            "monotonic": zip(offsets, offsets.dropFirst()).allSatisfy { $0 < $1 }, "fileDirect": st.fileRoute == .direct]
        say("upload: puts=\(puts) persistedAtCut=\(persistedAtCut) complete=\(final.state == .complete)")
        #expect(final.state == .complete && persistedAtCut == 12 * Int64(block))
    }

    func approvals(_ remote: Remote, _ host: CoreHost, _ screens: CoreScreens, _ machine: String, _ r: inout [String: Any]) async throws {
        let stop = Flag()
        var snapshot = PBCommand(); snapshot.getLiveSnapshot.sessionID = "fx-session"
        let snap = snapshot
        var slowCommand = PBCommand(); slowCommand.setModel.sessionID = "fx-session"; slowCommand.setModel.model = "x"
        let slow = slowCommand
        let bulk = Count()
        var tasks: [Task<Void, Never>] = []
        // Two bulk streams, the ordinary pool held full, and a flood of snapshots from the same peer.
        for _ in 0..<2 {
            tasks.append(Task.detached {
                var off: Int64 = 0
                while !stop.set { if (try? await e2eCall(remote, machine, "fx-session", e2eChunk(off))) != nil { bulk.add(768 << 10) }; off = (off + (768 << 10)) % Int64(60 << 20) }
            })
        }
        for _ in 0..<6 { tasks.append(Task.detached { while !stop.set { _ = try? await e2eCall(remote, machine, "", slow) } }) }
        for _ in 0..<4 { tasks.append(Task.detached { while !stop.set { _ = try? await e2eCall(remote, machine, "fx-session", snap) } }) }
        try await Task.sleep(nanoseconds: 3_000_000_000)
        let before = try await mark("approvals-start")
        var times: [Double] = [], failures = 0
        for _ in 0..<100 {
            let t0 = ContinuousClock.now
            do {
                _ = try await call(remote, machine, session: "fx-session", snap)
                // The approval step: a control-class command, which the same queue serves ahead of snapshots.
                // (ResolveApproval itself needs the session in the app's own state; the fixture's session
                // metadata does not open on the phone, so Interrupt stands in; see the report.)
                var control = PBCommand(); control.interrupt.sessionID = "fx-session"
                _ = try await call(remote, machine, session: "fx-session", control)
            } catch { failures += 1; if failures == 1 { say("first approval failure: \(error)") } }
            let d = ContinuousClock.now - t0
            times.append(Double(d.components.seconds) * 1000 + Double(d.components.attoseconds) / 1e15)
            try await Task.sleep(nanoseconds: 30_000_000)
        }
        let after = try await mark("approvals-end")
        let st = await report(host, screens, machine)
        stop.set = true
        for t in tasks { _ = await t.value }
        let p99 = percentile(times, 0.99)
        r["approvals"] = ["n": times.count, "failures": failures, "p50": median(times), "p95": percentile(times, 0.95), "p99": p99, "max": times.max() ?? 0,
            "relayBytesDuringLoad": (after["proxy_bytes"] ?? 0) - (before["proxy_bytes"] ?? 0), "bulkBytesDuringLoad": bulk.value, "fileDirect": st.fileRoute == .direct, "readDirect": st.readRoute == .direct]
        say("approvals: n=\(times.count) failures=\(failures) p50=\(median(times)) p99=\(p99) max=\(times.max() ?? 0) fileDirect=\(st.fileRoute == .direct)")
        #expect(failures == 0 && p99 <= 100)
    }
}

final class Count: @unchecked Sendable {
    private let lock = NSLock(); private var v = 0
    func add(_ n: Int) { lock.withLock { v += n } }
    var value: Int { lock.withLock { v } }
}

final class Flag: @unchecked Sendable {
    private let lock = NSLock(); private var v = false
    var set: Bool { get { lock.withLock { v } } set { lock.withLock { v = newValue } } }
}

func e2eSay(_ dir: String, _ started: Date, _ s: String) {
    let line = String(format: "[%6.1fs] ", Date().timeIntervalSince(started)) + s + "\n"
    if let h = FileHandle(forWritingAtPath: dir + "/test.log") { h.seekToEndOfFile(); h.write(Data(line.utf8)); try? h.close() }
    else { try? line.write(toFile: dir + "/test.log", atomically: true, encoding: .utf8) }
}

func e2eCall(_ remote: Remote, _ machine: String, _ session: String, _ cmd: PBCommand) async throws -> PBCommandResult {
    try await remote.runMachineCommand(machineId: machine, sessionId: session, cmd, skipConnectivityPreflight: true)
}

func e2eChunk(_ offset: Int64) -> PBCommand {
    var c = PBCommand()
    c.getSharedFileChunk.sessionID = "fx-session"; c.getSharedFileChunk.fileID = "fx-file"; c.getSharedFileChunk.offset = offset
    return c
}
