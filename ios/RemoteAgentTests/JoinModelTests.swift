import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// The server's side of a join, played by the test: each call waits until the test ends it.
final class FakeJoin: @unchecked Sendable {
    struct Pairing {
        let link: PairLink
        let onEmoji: @Sendable ([String]) -> Void
        let stopped: @Sendable () -> Bool
        let onRegistering: @Sendable () -> Void
        let end: CheckedContinuation<Account, Error>
    }

    private let lock = NSLock()
    private var pairingList: [Pairing] = []
    private var secretList: [(Secret, CheckedContinuation<Account, Error>)] = []

    var pairings: [Pairing] { lock.withLock { pairingList } }
    var secrets: [(Secret, CheckedContinuation<Account, Error>)] { lock.withLock { secretList } }

    var calls: JoinCalls {
        JoinCalls(
            byPairing: { link, onEmoji, stopped, onRegistering in
                try await withCheckedThrowingContinuation { end in
                    self.lock.withLock {
                        self.pairingList.append(Pairing(link: link, onEmoji: onEmoji, stopped: stopped, onRegistering: onRegistering, end: end))
                    }
                }
            },
            withSecret: { secret in
                try await withCheckedThrowingContinuation { end in
                    self.lock.withLock { self.secretList.append((secret, end)) }
                }
            })
    }
}

@MainActor
@Suite struct JoinModelTests {
    let fake = FakeJoin()
    let joined = Joined()
    let account = Account(server: "https://ra.example.com", root: Data(repeating: 1, count: 32), deviceSeed: Data(repeating: 2, count: 32), deviceId: "d1")

    @MainActor final class Joined {
        var accounts: [Account] = []
        var failure: Error?
    }

    func model(lastServer: String = "") -> JoinModel {
        let joined = joined
        return JoinModel(lastServer: lastServer, calls: fake.calls) { account in
            if let failure = joined.failure { throw failure }
            joined.accounts.append(account)
        }
    }

    /// A model waiting for the other device, with the pairing the test plays.
    func started() async -> (JoinModel, FakeJoin.Pairing) {
        let m = model(lastServer: "https://ra.example.com")
        m.typeCode("48291756")
        m.joinWithCode()
        await eventually("called") { fake.pairings.count == 1 }
        return (m, fake.pairings[0])
    }

    @Test func theCodeKeepsOnlyEightDigitsAndIsShownInTwoGroups() {
        let m = model()
        #expect(m.server == "https://")
        m.typeCode("48a2-9")
        #expect(m.code == "4829" && m.shownCode == "4829")
        m.typeCode("4829 1756 99")
        #expect(m.code == "48291756" && m.shownCode == "4829 1756")
        // Eight digits are not enough without a server.
        #expect(!m.canJoin)
        m.server = "https://ra.example.com"
        #expect(m.canJoin)
        m.typeCode("4829175")
        #expect(!m.canJoin)
    }

    @Test func theServerOfTheAccountJustLeftIsFilledIn() {
        #expect(model(lastServer: "https://ra.example.com").server == "https://ra.example.com")
    }

    @Test func anAddressThatIsNotAllowedIsSaidWithoutCallingAnything() async {
        let m = model()
        m.typeCode("48291756")
        for bad in ["http://ra.example.com", "https://user:pw@ra.example.com", "ftp://ra.example.com"] {
            m.server = bad
            m.joinWithCode()
            #expect(m.step == .form && m.noticeIsError && !m.notice.isEmpty, "\(bad)")
        }
        #expect(fake.pairings.isEmpty)
        // A local relay may be plain HTTP, and the address is normalised.
        m.server = "http://LOCALHOST:8080/"
        m.joinWithCode()
        await eventually("called") { fake.pairings.count == 1 }
        #expect(fake.pairings[0].link == PairLink(code: "48291756", server: "http://localhost:8080"))
        #expect(m.step == .working && m.notice.isEmpty)
    }

    @Test func aScannedLinkFillsTheFormAndCarriesItsCommitment() async {
        let m = model()
        let commit = Data(repeating: 9, count: 32)
        m.showScanner()
        m.scanned(PairLink(code: "12345678", server: "https://ra.example.com", commit: commit).url(web: "https://app.example.com"))
        await eventually("called") { fake.pairings.count == 1 }
        #expect(fake.pairings[0].link == PairLink(code: "12345678", server: "https://ra.example.com", commit: commit))
        #expect(m.code == "12345678" && m.server == "https://ra.example.com" && m.step == .working)
    }

    @Test func aCodeTypedAfterScanningIsNotCheckedAgainstTheScannedCommitment() async {
        let m = model()
        m.scanned(PairLink(code: "12345678", server: "https://ra.example.com", commit: Data(repeating: 9, count: 32)).url(web: "https://app.example.com"))
        await eventually("called") { fake.pairings.count == 1 }
        m.cancel()
        m.typeCode("87654321")
        m.joinWithCode()
        await eventually("called") { fake.pairings.count == 2 }
        #expect(fake.pairings[1].link.commit == nil)
    }

    @Test func whatIsNotAPairingCodeOfThisAppIsRefused() {
        let m = model()
        for text in ["https://example.com", "WIFI:S:home;;", "https://app.example.com/#/pair?code=12&s=https%3A%2F%2Fra.example.com", ""] {
            m.showScanner()
            m.scanned(text)
            #expect(m.step == .form && m.notice == "这不是 remote-agent 的配对二维码" && m.noticeIsError)
        }
        #expect(fake.pairings.isEmpty)
    }

    @Test func thePicturesAreShownAndAllowingHandsTheAccountOver() async {
        let (m, p) = await started()
        #expect(m.step == .working)
        p.onEmoji(["🦊", "🌵", "🍉", "🌙"])
        await eventually("emoji") { m.step == .emoji(["🦊", "🌵", "🍉", "🌙"]) }
        p.onRegistering()
        p.end.resume(returning: account)
        await eventually("joined") { joined.accounts.count == 1 }
        #expect(joined.accounts[0].deviceId == "d1")
        #expect(m.notice.isEmpty)
    }

    @Test func cancellingStopsThePairingKeepsTheServerAndClearsTheCode() async {
        let (m, p) = await started()
        p.onEmoji(["🦊", "🌵", "🍉", "🌙"])
        await eventually("emoji") { if case .emoji = m.step { true } else { false } }
        #expect(!p.stopped())
        m.cancel()
        #expect(p.stopped())
        #expect(m.step == .form && m.code.isEmpty && m.server == "https://ra.example.com" && !m.canJoin)
        // It says what this device did, and that the other one still holds the request.
        #expect(m.notice == "已取消本次加入。旧请求可能还占着配对码：请在已有设备上拒绝它，再生成新码")
        #expect(!m.noticeIsError)
        // The pairing ending because it was stopped changes nothing on the page.
        p.end.resume(throwing: PairError("已取消"))
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(m.notice.hasPrefix("已取消本次加入") && m.step == .form)
        #expect(joined.accounts.isEmpty)
        // Pictures that arrive late are not shown.
        p.onEmoji(["🐶", "🐱", "🐭", "🐹"])
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(m.step == .form)
    }

    @Test func aJoinThatFailsSaysWhyAndKeepsWhatWasTyped() async {
        let (m, p) = await started()
        p.end.resume(throwing: PairError("配对码不对，或者已经用过了"))
        await eventually("failed") { m.step == .form }
        #expect(m.notice == "配对码不对，或者已经用过了" && m.noticeIsError)
        #expect(m.code == "48291756")
    }

    @Test func onceRegisteringHasBegunTheJoinIsNotCancelledAndItsOutcomeIsShown() async {
        let (m, p) = await started()
        p.onEmoji(["🦊", "🌵", "🍉", "🌙"])
        await eventually("emoji") { if case .emoji = m.step { true } else { false } }
        p.onRegistering()
        m.cancel()
        #expect(!p.stopped())
        #expect(m.notice.hasPrefix("已经在登记") && m.notice.hasSuffix("稍等结果"))
        if case .emoji = m.step {} else { Issue.record("left the page") }
        #expect(m.code == "48291756")
        p.end.resume(returning: account)
        await eventually("joined") { joined.accounts.count == 1 }
    }

    @Test func aFailureWhileRegisteringLeavesTheOutcomeOpen() async {
        let (m, p) = await started()
        p.onRegistering()
        p.end.resume(throwing: URLError(.networkConnectionLost))
        await eventually("failed") { m.step == .form }
        #expect(m.notice.hasPrefix("不确定") && m.notice.contains("有没有加入") && m.notice.hasSuffix("可以在已有设备的设置里看看客户端列表"))
        #expect(m.noticeIsError)
    }

    @Test func aCancelThatLosesTheRaceToTheOtherDevicesAllowStillJoins() async {
        // The pairing read the flag for the last time, then the user cancelled, then it registered.
        let (m, p) = await started()
        m.cancel()
        #expect(m.step == .form)
        p.onRegistering()
        p.end.resume(returning: account)
        await eventually("joined") { joined.accounts.count == 1 }
    }

    @Test func aCancelThatLosesTheRaceAndThenFailsIsNotCalledCancelled() async {
        let (m, p) = await started()
        m.cancel()
        p.onRegistering()
        p.end.resume(throwing: URLError(.timedOut))
        await eventually("said") { m.notice.hasPrefix("不确定") }
        #expect(m.noticeIsError && joined.accounts.isEmpty)
    }

    @Test func anAccountSecretIsCheckedUsedAndForgotten() async throws {
        let m = model()
        m.showSecret()
        #expect(!m.canJoinWithSecret)
        m.secret = "ra1.nonsense"
        m.joinWithSecret()
        #expect(m.step == .secret && m.notice == "这不是 remote-agent 的账户密钥" && m.noticeIsError)
        #expect(fake.secrets.isEmpty)

        let root = Data(repeating: 7, count: 32)
        m.secret = "  ra1.\(root.base64URL).\(Data("https://ra.example.com".utf8).base64URL)\n"
        m.joinWithSecret()
        #expect(m.step == .working && !m.canCancel)
        await eventually("called") { fake.secrets.count == 1 }
        #expect(fake.secrets[0].0 == Secret(root: root, server: "https://ra.example.com"))
        // Cancel does nothing to a registration on its way.
        m.cancel()
        #expect(m.step == .working)
        fake.secrets[0].1.resume(returning: account)
        await eventually("joined") { joined.accounts.count == 1 }
        #expect(m.secret.isEmpty)
    }

    @Test func aSecretTheServerRefusesIsSaidOnItsPageAndLeavingThePageForgetsIt() async {
        let m = model()
        m.showSecret()
        m.secret = "ra1.\(Data(repeating: 7, count: 32).base64URL).\(Data("https://ra.example.com".utf8).base64URL)"
        m.joinWithSecret()
        await eventually("called") { fake.secrets.count == 1 }
        fake.secrets[0].1.resume(throwing: UserError("server 返回了意外的设备 ID"))
        await eventually("failed") { m.step == .secret }
        #expect(m.notice == "server 返回了意外的设备 ID" && m.noticeIsError && m.canCancel)
        m.showForm()
        #expect(m.secret.isEmpty && m.notice.isEmpty && m.step == .form)
    }

    @Test func anAccountTheKeychainDoesNotKeepIsSaid() async {
        joined.failure = VaultError(operation: .save, status: errSecParam)
        let (m, p) = await started()
        p.onRegistering()
        p.end.resume(returning: account)
        await eventually("said") { m.notice.hasPrefix("已经加入，但没能保存账户密钥") }
        #expect(m.step == .form && m.noticeIsError)
    }
}

/// A pairing started from this device, played by the test.
final class FakeInitiation: PairingInitiation, @unchecked Sendable {
    let code = "48291756"
    let link = PairLink(code: "48291756", server: "https://ra.example.com", commit: Data(repeating: 3, count: 32))
    private let lock = NSLock()
    private var joinEnd: CheckedContinuation<(emoji: [String], kind: PBDeviceKind), Error>?
    private var approveEnd: CheckedContinuation<Void, Error>?
    private var cancels = 0

    var cancelled: Int { lock.withLock { cancels } }
    var waitingForJoin: Bool { lock.withLock { joinEnd != nil } }
    var waitingForApprove: Bool { lock.withLock { approveEnd != nil } }

    func joined() async throws -> (emoji: [String], kind: PBDeviceKind) {
        try await withCheckedThrowingContinuation { c in lock.withLock { joinEnd = c } }
    }

    func approve() async throws {
        try await withCheckedThrowingContinuation { c in lock.withLock { approveEnd = c } }
    }

    func cancelOnServer() async { lock.withLock { cancels += 1 } }

    func join(_ result: Result<(emoji: [String], kind: PBDeviceKind), Error>) {
        lock.withLock { joinEnd.take() }?.resume(with: result)
    }

    func approved(_ result: Result<Void, Error>) {
        lock.withLock { approveEnd.take() }?.resume(with: result)
    }
}

@MainActor
@Suite struct AddDeviceModelTests {
    let emoji = ["🦊", "🌵", "🍉", "🌙"]

    @MainActor final class Made {
        var list: [FakeInitiation] = []
        var failure: Error?
    }

    let made = Made()

    func model() -> AddDeviceModel {
        let made = made
        return AddDeviceModel {
            try await MainActor.run {
                if let failure = made.failure { throw failure }
                let p = FakeInitiation()
                made.list.append(p)
                return p
            }
        }
    }

    /// A model showing a new device's pictures.
    func comparing(kind: PBDeviceKind = .client) async -> (AddDeviceModel, FakeInitiation) {
        let m = model()
        m.start()
        await eventually("waiting") { made.list.first?.waitingForJoin == true }
        let p = made.list[0]
        #expect(m.phase == .waiting(code: p.code, link: p.link))
        p.join(.success((emoji: emoji, kind: kind)))
        await eventually("compare") { m.phase == .compare(emoji: emoji, machine: kind == .machine) }
        return (m, p)
    }

    @Test func aCodeIsShownThenThePicturesAndAllowingEndsWhenTheKeyIsDelivered() async {
        #expect(model().phase == .starting)
        let (model, p) = await comparing(kind: .machine)
        model.approve()
        #expect(model.phase == .sending(emoji: emoji, machine: true))
        await eventually("approving") { p.waitingForApprove }
        p.approved(.success(()))
        await eventually("done") { model.phase == .done }
        // Done is not withdrawn by leaving the page.
        model.leave()
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(p.cancelled == 0)
    }

    @Test func aKeyOfferedAndNotConfirmedIsSaidToBeUncertain() async {
        let (m, p) = await comparing()
        m.approve()
        await eventually("approving") { p.waitingForApprove }
        p.approved(.failure(URLError(.networkConnectionLost)))
        await eventually("failed") { if case .failed = m.phase { true } else { false } }
        guard case .failed(let why, let uncertain) = m.phase else { return }
        #expect(uncertain && why.hasPrefix("不确定新设备收到没有，到新设备上看一眼是否已加入："))
    }

    @Test func thePairingsOwnWordsAreKept() async {
        let (m, p) = await comparing()
        m.approve()
        await eventually("approving") { p.waitingForApprove }
        p.approved(.failure(PairError("另一台设备取消了配对")))
        await eventually("failed") { m.phase == .failed("另一台设备取消了配对", uncertain: false) }

        let m2 = model()
        m2.start()
        await eventually("waiting") { made.list.count == 2 && made.list[1].waitingForJoin }
        made.list[1].join(.success((emoji: emoji, kind: .client)))
        await eventually("compare") { m2.phase == .compare(emoji: emoji, machine: false) }
        m2.approve()
        await eventually("approving") { made.list[1].waitingForApprove }
        let lost = "新设备没有来取账户密钥，它可能没有加入成功。请在新设备上确认，必要时重新配对。"
        made.list[1].approved(.failure(PairError(lost)))
        await eventually("failed") { m2.phase == .failed(lost, uncertain: true) }
    }

    @Test func aKeyOfferedAndNotConfirmedIsWithdrawnOnLeaving() async {
        // The server took the allowing and its answer was lost: the new device could still fetch the key.
        let (m, p) = await comparing()
        m.approve()
        await eventually("approving") { p.waitingForApprove }
        p.approved(.failure(URLError(.networkConnectionLost)))
        await eventually("failed") { if case .failed = m.phase { true } else { false } }
        #expect(p.cancelled == 0)
        m.leave()
        await eventually("cancelled") { p.cancelled == 1 }
    }

    @Test func aKeyOfferedAndNotConfirmedIsWithdrawnByAskingForAnotherCode() async {
        let (m, p) = await comparing()
        m.approve()
        await eventually("approving") { p.waitingForApprove }
        p.approved(.failure(PairError("新设备没有来取账户密钥，它可能没有加入成功。请在新设备上确认，必要时重新配对。")))
        await eventually("failed") { if case .failed = m.phase { true } else { false } }
        m.start()
        await eventually("cancelled") { p.cancelled == 1 }
        await eventually("again") { made.list.count == 2 && made.list[1].waitingForJoin }
        #expect(made.list[1].cancelled == 0)
    }

    @Test func aCodeThatExpiresCanBeReplaced() async {
        let m = model()
        m.start()
        await eventually("waiting") { made.list.first?.waitingForJoin == true }
        made.list[0].join(.failure(PairError("配对已过期或被取消")))
        await eventually("failed") { m.phase == .failed("配对已过期或被取消", uncertain: false) }
        m.start()
        await eventually("again") { made.list.count == 2 && made.list[1].waitingForJoin }
        #expect(m.phase == .waiting(code: "48291756", link: made.list[1].link))
    }

    @Test func noCodeIsAFailureToo() async {
        made.failure = URLError(.cannotConnectToHost)
        let m = model()
        m.start()
        await eventually("failed") { m.phase == .failed("连不上服务器", uncertain: false) }
    }

    @Test func rejectingOrLeavingWithdrawsThePairingOnTheServer() async {
        let (m, p) = await comparing()
        m.leave()
        await eventually("cancelled") { p.cancelled == 1 }
        // What the pairing says afterwards is not shown.
        p.approved(.success(()))
        #expect(m.phase == .compare(emoji: emoji, machine: false))
        m.approve()
        #expect(m.phase == .compare(emoji: emoji, machine: false))
    }
}
