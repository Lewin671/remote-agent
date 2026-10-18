// Pairing flows, as in internal/relay/pair.go, web/src/pairing.ts and
// Android's core/PairFlow.kt: an existing device shows a code, a new one
// joins with it, both show four emoji, and after the user checks them the
// existing device sends the root key sealed for the new one.
import Connect
import Foundation
import RAProto
import os

public struct PairError: Error, LocalizedError, Equatable, Sendable, CustomStringConvertible {
    public let message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
    public var description: String { message }
}

/// How long a pairing waits between two looks at its state.
public typealias Sleeper = @Sendable (_ ms: Int64) async throws -> Void

public let realSleep: Sleeper = { try await Task.sleep(nanoseconds: UInt64(max($0, 0)) * 1_000_000) }

/// What a QR code carries: the code, the initiator's commitment and the server.
public struct PairLink: Equatable, Sendable {
    public let code: String
    public let server: String
    public let commit: Data?

    public init(code: String, server: String, commit: Data? = nil) {
        self.code = code
        self.server = server
        self.commit = commit
    }

    /// As java.net.URLEncoder writes a form value, which is what the other clients read.
    private static func enc(_ s: String) -> String {
        var allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
        allowed.insert(charactersIn: ".-*_ ")
        return (s.addingPercentEncoding(withAllowedCharacters: allowed) ?? s).replacingOccurrences(of: " ", with: "+")
    }

    /// The link for the web client at `web`; opening it starts joining.
    public func url(web: String) -> String {
        var q = ["code=" + Self.enc(code)]
        if let commit { q.append("c=" + commit.base64URL) }
        q.append("s=" + Self.enc(server))
        var base = web
        while base.hasSuffix("/") { base.removeLast() }
        return base + "/#/pair?" + q.joined(separator: "&")
    }

    /// Reads a link made by `url`, or by the web client and `remote-agent account pair`.
    /// QR contents are untrusted: anything malformed is no link, never a crash.
    public static func parse(_ raw: String) -> PairLink? {
        guard let at = raw.range(of: "#/pair?") else { return nil }
        var q = [String: String]()
        for part in raw[at.upperBound...].split(separator: "&", omittingEmptySubsequences: false) {
            let kv = part.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false)
            guard kv.count == 2 else { continue }
            guard let value = kv[1].replacingOccurrences(of: "+", with: " ").removingPercentEncoding else { return nil }
            q[String(kv[0])] = value
        }
        guard let code = q["code"], let server = q["s"] else { return nil }
        var commit: Data?
        if let c = q["c"] {
            guard let bytes = Data(base64URL: c), bytes.count == 32 else { return nil }
            commit = bytes
        }
        guard code.utf8.count == 8, code.utf8.allSatisfy({ $0 >= 0x30 && $0 <= 0x39 }),
              let checked = try? Secret.checkServer(server) else { return nil }
        return PairLink(code: code, server: checked, commit: commit)
    }
}

private func poll(
    stopped: () -> Bool, want: PBPairingState, sleep: Sleeper, get: () async throws -> PBGetPairingResponse
) async throws -> PBGetPairingResponse {
    while true {
        if stopped() { throw PairError("已取消") }
        let res: PBGetPairingResponse
        do {
            res = try await get()
        } catch let e as ConnectError where e.code == .notFound {
            throw PairError("配对已过期或被取消")
        }
        if res.state == .canceled { throw PairError("另一台设备取消了配对") }
        if res.state.rawValue >= want.rawValue { return res }
        try await sleep(1000)
    }
}

/// The kind of the device that asked to join, when it is one a device can
/// be. The server is not trusted to have checked: any number may come.
func joinerKind(_ st: PBGetPairingResponse) throws -> PBDeviceKind {
    switch st.joinerKind {
    case .machine, .client: return st.joinerKind
    default: throw PairError("服务器返回了意外的设备类型，已停止配对")
    }
}

/// Adding a device from this one.
public final class Initiation: Sendable {
    public let code: String
    public let link: PairLink
    private let remote: Remote
    private let pairingId: String
    private let key: PairKey
    private let expires: Int64
    private let candidate = OSAllocatedUnfairLock<(Data, PBDeviceKind)?>(initialState: nil)
    private let sleep: Sleeper
    private let stopped = OSAllocatedUnfairLock(initialState: false)
    private let secrets = OSAllocatedUnfairLock<PairSecrets?>(initialState: nil)

    private init(code: String, link: PairLink, remote: Remote, pairingId: String, key: PairKey, expires: Int64, sleep: @escaping Sleeper) {
        self.code = code
        self.link = link
        self.remote = remote
        self.pairingId = pairingId
        self.key = key
        self.expires = expires
        self.sleep = sleep
    }

    /// Starts adding a device to this device's account.
    public static func start(remote: Remote, sleep: @escaping Sleeper = realSleep) async throws -> Initiation {
        let key = try PairKey()
        let commit = Pairing.commit(initiatorPublic: key.publicKey)
        var req = PBCreatePairingRequest()
        req.commit = commit
        let created = try await remote.api.pairing.createPairing(
            request: req, headers: Api.headers(token: try await remote.currentToken())
        ).value()
        return Initiation(
            code: created.code, link: PairLink(code: created.code, server: remote.account.server, commit: commit),
            remote: remote, pairingId: created.pairingID, key: key, expires: created.expiresMs, sleep: sleep)
    }

    private func ask(_ token: String) async throws -> PBGetPairingResponse {
        var req = PBGetPairingRequest()
        req.pairingID = pairingId
        return try await remote.api.pairing.getPairing(request: req, headers: Api.headers(token: token)).value()
    }

    /// Waits for a device to ask to join; returns the emoji to compare and its kind.
    public func joined() async throws -> (emoji: [String], kind: PBDeviceKind) {
        let tok = try await remote.currentToken()
        let st = try await poll(stopped: { stopped.withLock { $0 } }, want: .joined, sleep: sleep) { try await ask(tok) }
        let kind = try joinerKind(st)
        var reveal = PBRevealPairingRequest()
        reveal.pairingID = pairingId
        reveal.initiatorPublic = key.publicKey
        _ = try await remote.api.pairing.revealPairing(request: reveal, headers: Api.headers(token: tok)).value()
        guard st.devicePublicKey.count == 32 else { throw PairError("无效的候选设备身份") }
        candidate.withLock { $0 = (st.devicePublicKey, kind) }
        let tr = Pairing.identityTranscript(
            pairingId: pairingId, initiatorPublic: key.publicKey, joinerPublic: st.joinerPublic,
            devicePublic: st.devicePublicKey, joinerKind: kind.rawValue)
        let s = try key.derive(otherPublic: st.joinerPublic, transcript: tr)
        secrets.withLock { $0 = s }
        return (s.emoji, kind)
    }

    /// Sends the root key; returns when the new device has it.
    public func approve() async throws {
        guard let s = secrets.withLock({ $0 }) else { throw PairError("还没有设备加入") }
        let acct = remote.account
        let tok = try await remote.currentToken()
        guard let c = candidate.withLock({ $0 }) else { throw PairError("没有候选设备") }
        let (approval, signature) = try makePairApproval(account: acct, pairingId: pairingId, candidate: c.0, kind: c.1, expires: expires)
        var h = PBHeader()
        h.kind = .pairing
        h.requestID = pairingId
        h.sender = acct.deviceId
        var payload = PBPairingPayload()
        payload.root = acct.root
        payload.server = acct.server
        payload.approval = approval; payload.approvalSignature = signature
        var req = PBApprovePairingRequest()
        req.pairingID = pairingId
        req.payload = try Envelopes.seal(key: s.key, header: h, message: payload)
        req.approval = approval; req.approvalSignature = signature
        _ = try await remote.api.pairing.approvePairing(request: req, headers: Api.headers(token: tok)).value()
        // Only the new device confirming it has the key counts as success. A
        // pairing that disappears meanwhile leaves the outcome unknown.
        for _ in 0..<120 {
            if stopped.withLock({ $0 }) { throw PairError("已取消") }
            do {
                let res = try await ask(tok)
                if res.state == .delivered { return }
                if res.state == .canceled { throw PairError("另一台设备取消了配对") }
            } catch let e as ConnectError {
                if e.code == .notFound {
                    throw PairError("配对已过期，不确定新设备有没有拿到账户密钥。请在新设备上确认，必要时重新配对。")
                }
            }
            try await sleep(1000)
        }
        throw PairError("新设备没有来取账户密钥，它可能没有加入成功。请在新设备上确认，必要时重新配对。")
    }

    public func cancel() {
        stopped.withLock { $0 = true }
    }

    /// Tells the server; best effort.
    public func cancelOnServer() async {
        cancel()
        var req = PBCancelPairingRequest()
        req.pairingID = pairingId
        // The pairing expires by itself.
        guard let tok = try? await remote.currentToken() else { return }
        _ = await remote.api.pairing.cancelPairing(request: req, headers: Api.headers(token: tok))
    }
}

/// Joins an account with a code or link from an existing device. `onEmoji`
/// gets the emoji to compare with the other device's. `onRegistering` is
/// called when registering starts: from there on the join is not canceled,
/// its outcome is reported.
public func joinByPairing(
    link: PairLink,
    http: HTTP,
    onEmoji: @Sendable ([String]) -> Void,
    stopped: @Sendable () -> Bool,
    onRegistering: @Sendable () -> Void = {},
    sleep: Sleeper = realSleep
) async throws -> Account {
    let api = try Api(server: link.server, http: http)
    let key = try PairKey()
    let deviceSeed = randomBytes(32)
    let devicePublic = try SigningKey(seed: deviceSeed).publicKey
    var join = PBJoinPairingRequest()
    join.code = link.code.filter { !$0.isWhitespace }
    join.joinerPublic = key.publicKey
    join.joinerKind = .client
    join.devicePublicKey = devicePublic
    let joined: PBJoinPairingResponse
    do {
        joined = try await api.pairing.joinPairing(request: join).value()
    } catch let e as ConnectError {
        switch e.code {
        case .notFound, .invalidArgument, .failedPrecondition: throw PairError("配对码不对，或者已经用过了")
        case .resourceExhausted: throw PairError("试得太频繁了，稍等一分钟再试")
        default: throw e
        }
    }
    let id = joined.pairingID
    let token = joined.candidateToken
    if let commit = link.commit, commit != joined.commit {
        throw PairError("服务器返回的不是扫码那台设备的信息，已停止配对")
    }
    var ask = PBGetPairingRequest()
    ask.pairingID = id
    ask.candidateToken = token
    let get = { [ask] in try await api.pairing.getPairing(request: ask).value() }
    let st = try await poll(stopped: stopped, want: .revealed, sleep: sleep, get: get)
    if Pairing.commit(initiatorPublic: st.initiatorPublic) != joined.commit {
        throw PairError("另一台设备的密钥和它的承诺对不上，已停止配对")
    }
    let tr = Pairing.identityTranscript(
        pairingId: id, initiatorPublic: st.initiatorPublic, joinerPublic: key.publicKey,
        devicePublic: devicePublic, joinerKind: PBDeviceKind.client.rawValue)
    let s = try key.derive(otherPublic: st.initiatorPublic, transcript: tr)
    onEmoji(s.emoji)
    let done = try await poll(stopped: stopped, want: .approved, sleep: sleep, get: get)
    let (_, payload) = try Envelopes.openMessage(
        key: s.key, envelope: done.hasPayload ? done.payload : nil, want: Place(.pairing, requestId: id),
        as: PBPairingPayload.self)
    if payload.server != link.server { throw PairError("账户属于另一个服务器") }
    // Canceled while the other device was allowing it: stop before registering.
    if stopped() { throw PairError("已取消本次加入") }
    onRegistering()
    let acct = try await joinWithRoot(root: payload.root, server: payload.server, http: http, enrollment: Enrollment(seed: deviceSeed, pairingId: id, approval: payload.approval, signature: payload.approvalSignature))
    // Tells the other device it went through. If this is lost, that device
    // reports the outcome as unknown; the join itself stands.
    ask.received = true
    _ = await api.pairing.getPairing(request: ask)
    return acct
}
