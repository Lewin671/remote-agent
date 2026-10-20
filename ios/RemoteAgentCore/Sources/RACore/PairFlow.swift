import Connect
// Pairing join, as in internal/relay/pair.go, web/src/pairing.ts and
// Android's core/PairFlow.kt: the phone shows a code, this device joins with
// it, both show four emoji, and after the user checks them the phone seals
// the signed state chain (no account root) for this device.
import CryptoKit
import Foundation
import RAProto
import os

public struct PairError: Error, LocalizedError, Equatable, Sendable, CustomStringConvertible {
    public let message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
    public var description: String { message }
}

/// Retry the same approved identity after uncertain transport outcomes. A
/// definitive rejection/expiry needs a new pairing and fresh user approval.
public func canRetryPairApproval(_ error: Error) -> Bool {
    if let e = error as? ConnectError,
        [.notFound, .permissionDenied, .failedPrecondition, .invalidArgument, .canceled].contains(
            e.code)
    {
        return false
    }
    let words = (error as? PairError)?.message ?? error.localizedDescription
    return !["过期", "取消", "拒绝", "无效", "不匹配"].contains(where: words.contains)
}

/// How long a pairing waits between two looks at its state.
public typealias Sleeper = @Sendable (_ ms: Int64) async throws -> Void

public let realSleep: Sleeper = {
    try await Task.sleep(nanoseconds: UInt64(max($0, 0)) * 1_000_000)
}

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
        var allowed = CharacterSet(
            charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
        allowed.insert(charactersIn: ".-*_ ")
        return (s.addingPercentEncoding(withAllowedCharacters: allowed) ?? s).replacingOccurrences(
            of: " ", with: "+")
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

    /// Reads a link made by `url`, or by the web client and the phone app.
    /// QR contents are untrusted: anything malformed is no link, never a crash.
    public static func parse(_ raw: String) -> PairLink? {
        guard let at = raw.range(of: "#/pair?") else { return nil }
        var q = [String: String]()
        for part in raw[at.upperBound...].split(separator: "&", omittingEmptySubsequences: false) {
            let kv = part.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false)
            guard kv.count == 2 else { continue }
            guard let value = kv[1].replacingOccurrences(of: "+", with: " ").removingPercentEncoding
            else { return nil }
            q[String(kv[0])] = value
        }
        guard let code = q["code"], let server = q["s"] else { return nil }
        var commit: Data?
        if let c = q["c"] {
            guard let bytes = Data(base64URL: c), bytes.count == 32 else { return nil }
            commit = bytes
        }
        guard code.utf8.count == 8, code.utf8.allSatisfy({ $0 >= 0x30 && $0 <= 0x39 }),
            let checked = try? Secret.checkServer(server)
        else { return nil }
        return PairLink(code: code, server: checked, commit: commit)
    }
}

private func poll(
    stopped: () -> Bool, want: PBPairingState, sleep: Sleeper,
    get: () async throws -> PBGetPairingResponse
) async throws -> PBGetPairingResponse {
    while true {
        if stopped() { throw PairError("已取消") }
        let res: PBGetPairingResponse
        do {
            res = try await get()
        } catch let e as ConnectError where e.code == .notFound {
            throw PairError("配对已过期或被取消")
        }
        if res.state == .canceled { throw PairError("管理端取消了配对") }
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
    sleep: Sleeper = realSleep,
    persist: @Sendable (Account, DeviceSecurity.Record) async throws -> Void = { _, _ in
        throw DeviceSecurity.Failure.storage
    },
    persistRegistration: @Sendable (RegistrationIntent) throws -> Void = { _ in throw DeviceSecurity.Failure.storage },
    clearRegistration: @Sendable (RegistrationIntent) throws -> Void = { _ in },
    loadIdentity: @Sendable (String, String) throws -> PairingIdentity? = { _, _ in nil },
    persistIdentity: @Sendable (PairingIdentity) throws -> Void = { _ in throw DeviceSecurity.Failure.storage },
    persistDelivery: @Sendable (PairDeliveryReceipt) throws -> Void = { _ in },
    clearDelivery: @Sendable () throws -> Void = {},
    existing: Account? = nil,
    confirm: @Sendable ([String]) async -> Bool = { _ in false }
) async throws -> Account {
    if let existing, existing.server != link.server { throw PairError("安全设置必须使用原账户的服务器") }
    let api = try Api(server: link.server, http: http)
    if let existing { _ = try await login(api: api, account: existing) }
    let key = try PairKey()
    var peekAsk = PBPeekPairingRequest()
    peekAsk.code = link.code.filter { !$0.isWhitespace }
    peekAsk.protocolVersion = protocolVersion
    let peek = try await api.pairing.peekPairing(request: peekAsk).value()
    guard peek.commit.count == 32, !peek.pairingID.isEmpty,
        link.commit == nil || link.commit == peek.commit
    else { throw PairError("配对承诺不匹配") }
    let identity = try loadIdentity(link.server, peek.pairingID)
    let deviceSeed = identity?.seed ?? existing?.deviceSeed ?? randomBytes(32)
    if let identity {
        try DeviceSecurity.check(identity.server == link.server && identity.pairingID == peek.pairingID && identity.seed.count == 32 && identity.x.count == 32 && (existing == nil || existing!.deviceSeed == identity.seed), .conflict)
    }
    let devicePublic = try SigningKey(seed: deviceSeed).publicKey
    let x = try identity.map { try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: $0.x) } ?? Curve25519.KeyAgreement.PrivateKey()
    let xPublic = x.publicKey.rawRepresentation
    var join = PBJoinPairingRequest()
    join.protocolVersion = protocolVersion
    join.deviceHpkePublicKey = xPublic
    join.code = link.code.filter { !$0.isWhitespace }
    join.joinerPublic = key.publicKey
    join.joinerKind = .client
    join.devicePublicKey = devicePublic
    try persistIdentity(PairingIdentity(server: link.server, pairingID: peek.pairingID, seed: deviceSeed, x: x.rawRepresentation))
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
    guard joined.pairingID == peek.pairingID, joined.commit == peek.commit else {
        throw PairError("临时公钥公开后配对承诺发生变化")
    }
    let id = joined.pairingID
    let token = joined.candidateToken
    if let commit = link.commit, commit != joined.commit {
        throw PairError("服务器返回的不是发起配对那台设备的信息，已停止配对")
    }
    var ask = PBGetPairingRequest()
    ask.pairingID = id
    ask.candidateToken = token
    let get = { [ask] in try await api.pairing.getPairing(request: ask).value() }
    let st = try await poll(stopped: stopped, want: .revealed, sleep: sleep, get: get)
    let (pin, approver) = try DeviceSecurity.parsePairTrustDescriptor(st.trust)
    guard
        try DeviceSecurity.pairDomainCommit(ephemeral: st.initiatorPublic, trust: st.trust)
            == peek.commit
    else { throw PairError("管理端的信任信息与承诺不匹配") }
    let tr = Pairing.domainTranscript(
        pairingId: id, initiatorPublic: st.initiatorPublic, joinerPublic: key.publicKey,
        devicePublic: devicePublic, hpkePublic: xPublic, joinerKind: PBDeviceKind.client.rawValue,
        trust: st.trust)
    let s = try key.derive(otherPublic: st.initiatorPublic, transcript: tr)
    onEmoji(s.emoji)
    guard !stopped(), await confirm(s.emoji) else {
        var cancel = PBCancelPairingRequest()
        cancel.pairingID = id
        cancel.candidateToken = token
        _ = await api.pairing.cancelPairing(request: cancel)
        throw PairError("本机未确认图案，已停止安全绑定")
    }
    onRegistering()
    let done = try await poll(stopped: stopped, want: .approved, sleep: sleep, get: get)
    let (_, payload) = try Envelopes.openMessage(
        key: s.key, envelope: done.hasPayload ? done.payload : nil,
        want: Place(.pairing, requestId: id),
        as: PBPairingPayload.self)
    if payload.server != link.server { throw PairError("账户属于另一个服务器") }
    // Canceled while the other phone was allowing it: stop before registering.
    if stopped() { throw PairError("已取消本次加入") }
    guard payload.accountPublicKey.count == 32,
        deviceId(publicKey: payload.accountPublicKey) == pin.account
    else { throw PairError("配对载荷的账户与本机核对的不一致") }
    let enrollment = Enrollment(
        seed: deviceSeed, pairingId: id, approval: payload.approval,
        signature: payload.approvalSignature)
    try verifyPairApproval(
        enrollment, accountId: pin.account, deviceSeed: deviceSeed, kind: .client,
        now: Int64(Date().timeIntervalSince1970 * 1000))
    let approval = try PBPairApproval(serializedBytes: payload.approval)
    guard approval.approverDeviceID == approver,
        approval.candidateDevicePublicKey == devicePublic
    else { throw PairError("配对批准与这台设备不匹配") }
    var chain: [DeviceSecurity.State] = []
    var proofs: [DeviceSecurity.RecoveryStatement?] = []
    guard !payload.securityStates.isEmpty else { throw PairError("配对载荷的状态链不完整") }
    guard payload.securityStates.count <= DeviceSecurity.maxPairStates else {
        throw PairError("安全状态链太长，无法加入账户；请在管理端重建账户")
    }
    var chainBytes = 0
    for value in payload.securityStates {
        chainBytes += value.state.count + value.recovery.count
        guard chainBytes <= DeviceSecurity.maxPairBytes else {
            throw PairError("安全状态链太长，无法加入账户；请在管理端重建账户")
        }
        guard value.state.count <= DeviceSecurity.maxState,
            value.recovery.count <= DeviceSecurity.maxState
        else { throw PairError("配对载荷的状态链不完整") }
    }
    for value in payload.securityStates {
        chain.append(try DeviceSecurity.parseState(value.state))
        proofs.append(
            value.recovery.isEmpty
                ? nil : try DeviceSecurity.RecoveryStatement.parse(value.recovery))
    }
    try DeviceSecurity.verifyChain(pin: pin, states: chain, proofs: proofs)
    let nowMs = UInt64(Date().timeIntervalSince1970 * 1000)
    for state in chain {
        guard state.core.issuedAt <= nowMs + DeviceSecurity.clockSkewLimit else {
            throw PairError("手机的安全状态时间比本机快 5 分钟以上，请校准时间后重试")
        }
    }
    let head = chain[chain.count - 1]
    let candidate = try DeviceSecurity.member(ed: devicePublic, x: xPublic, kind: .client)
    guard head.core.authorityID == approver, head.core.member(candidate.id) != nil,
        chain.contains(where: { $0.core.member(candidate.id) != nil })
    else { throw PairError("新状态没有把这台设备加进去") }
    let secret = try DeviceSecurity.openGrant(pin, head, recipient: candidate.id, x: x)
    let acct = Account(
        server: link.server, root: existing?.root ?? Data(), deviceSeed: deviceSeed,
        deviceId: candidate.id, accountId: pin.account)
    var states: [UInt64: DeviceSecurity.State] = [:]
    var proofMap: [UInt64: DeviceSecurity.RecoveryStatement] = [:]
    for (i, state) in chain.enumerated() {
        states[state.core.epoch] = state
        if let proof = proofs[i] { proofMap[state.core.epoch] = proof }
    }
    let record = try DeviceSecurity.Record(
        pin: pin, states: states, memberID: candidate.id, xPrivate: x.rawRepresentation,
        member: true, secret: secret,
        contentKeys: [head.core.contentEpoch: secret], recovery: proofMap, lease: try DeviceSecurity.HeadLease.parse(payload.lease))
    var reg = PBDeviceRegistration()
    reg.accountPublicKey=payload.accountPublicKey;reg.devicePublicKey=try SigningKey(seed:deviceSeed).publicKey
    reg.kind = .client;reg.createdMs=Int64(Date().timeIntervalSince1970*1000)
    var request = PBRegisterDeviceRequest();request.protocolVersion=protocolVersion
    request.registration=try reg.serializedData();request.approval=enrollment.approval;request.approvalSignature=enrollment.signature
    let intent = try RegistrationIntent(account:acct,record:record,request:request)
    try persistRegistration(intent)
    let delivery = PairDeliveryReceipt(intent: intent, candidateToken: token, pairingID: id)
    try persistDelivery(delivery)
    let registered = try await intent.register(http: http, persist: persist)
    try DeviceSecurity.check(registered.deviceId == acct.deviceId, .untrusted)
    Task { await delivery.acknowledge(http: http) }
    // Success no longer depends on a best-effort receipt. Clear both private
    // retry items even when that request is lost or the pairing has expired.
    try clearDelivery()
    try clearRegistration(intent)
    return acct
}
