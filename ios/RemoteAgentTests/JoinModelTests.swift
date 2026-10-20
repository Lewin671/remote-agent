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
        let confirm: @Sendable ([String]) async -> Bool
        let end: CheckedContinuation<Account, Error>
    }

    private let lock = NSLock()
    private var pairingList: [Pairing] = []
    private var secretList: [(Secret, CheckedContinuation<Account, Error>)] = []

    var pairings: [Pairing] { lock.withLock { pairingList } }
    var secrets: [(Secret, CheckedContinuation<Account, Error>)] { lock.withLock { secretList } }

    var calls: JoinCalls {
        JoinCalls(
            byPairing: { link, onEmoji, stopped, onRegistering, confirm in
                try await withCheckedThrowingContinuation { end in
                    self.lock.withLock {
                        self.pairingList.append(
                            Pairing(
                                link: link, onEmoji: onEmoji, stopped: stopped,
                                onRegistering: onRegistering, confirm: confirm, end: end))
                    }
                }
            })
    }
}

@MainActor
@Suite struct JoinModelTests {
    let fake = FakeJoin()
    let joined = Joined()
    let account = Account(
        server: "https://ra.example.com", root: Data(repeating: 1, count: 32),
        deviceSeed: Data(repeating: 2, count: 32), deviceId: "d1")

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

    @Test func candidateConsentWaitsForTheLocalButton() async {
        let (m, p) = await started()
        let task = Task { await p.confirm(["🦊", "🌵", "🍉", "🌙"]) }
        await eventually("local comparison") { m.comparisonPending }
        #expect(joined.accounts.isEmpty && m.canCancel)
        m.confirmPictures()
        #expect(await task.value)
        #expect(!m.canCancel && !m.comparisonPending)
        m.cancel()
        #expect(!p.stopped())
        p.end.resume(returning: account)
        await eventually("joined") { joined.accounts.count == 1 }
    }

    @Test func cancelingTheLocalComparisonRefusesTrust() async {
        let (m, p) = await started()
        let task = Task { await p.confirm(["🦊", "🌵", "🍉", "🌙"]) }
        await eventually("local comparison") { m.comparisonPending }
        m.cancel()
        #expect(await task.value == false)
        #expect(p.stopped() && joined.accounts.isEmpty)
        p.end.resume(throwing: PairError("已取消"))
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
        for bad in [
            "http://ra.example.com", "https://user:pw@ra.example.com", "ftp://ra.example.com",
        ] {
            m.server = bad
            m.joinWithCode()
            #expect(m.step == .form && m.noticeIsError && !m.notice.isEmpty, "\(bad)")
        }
        #expect(fake.pairings.isEmpty)
        // A local relay may be plain HTTP, and the address is normalised.
        m.server = "http://LOCALHOST:8080/"
        m.joinWithCode()
        await eventually("called") { fake.pairings.count == 1 }
        #expect(
            fake.pairings[0].link == PairLink(code: "48291756", server: "http://localhost:8080"))
        #expect(m.step == .working && m.notice.isEmpty)
    }

    @Test func aScannedLinkFillsTheFormAndCarriesItsCommitment() async {
        let m = model()
        let commit = Data(repeating: 9, count: 32)
        m.showScanner()
        m.scanned(
            PairLink(code: "12345678", server: "https://ra.example.com", commit: commit).url(
                web: "https://app.example.com"))
        await eventually("called") { fake.pairings.count == 1 }
        #expect(
            fake.pairings[0].link
                == PairLink(code: "12345678", server: "https://ra.example.com", commit: commit))
        #expect(m.code == "12345678" && m.server == "https://ra.example.com" && m.step == .working)
    }

    @Test func aCodeTypedAfterScanningIsNotCheckedAgainstTheScannedCommitment() async {
        let m = model()
        m.showScanner()
        m.scanned(
            PairLink(
                code: "12345678", server: "https://ra.example.com",
                commit: Data(repeating: 9, count: 32)
            ).url(web: "https://app.example.com"))
        await eventually("called") { fake.pairings.count == 1 }
        m.cancel()
        m.typeCode("87654321")
        m.joinWithCode()
        await eventually("called") { fake.pairings.count == 2 }
        #expect(fake.pairings[1].link.commit == nil)
    }

    @Test func whatIsNotAPairingCodeOfThisAppIsRefused() {
        let m = model()
        for text in [
            "https://example.com", "WIFI:S:home;;",
            "https://app.example.com/#/pair?code=12&s=https%3A%2F%2Fra.example.com", "",
        ] {
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
        #expect(
            m.step == .form && m.code.isEmpty && m.server == "https://ra.example.com" && !m.canJoin)
        // It says what this device did, and that the other one still holds the request.
        #expect(m.notice == "已取消本次加入。旧请求可能还占着配对码：请在手机 App 上拒绝它，再生成新码")
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

    @Test func aCodeTheCameraHandsOverAfterTheScannerWasLeftIsNotJoinedWith() async {
        let m = model()
        m.showScanner()
        m.typeCode("")
        // 取消 on the scanner; the camera had read a code that reaches the page afterwards.
        m.showForm()
        m.scanned(
            PairLink(
                code: "12345678", server: "https://other.example.com",
                commit: Data(repeating: 9, count: 32)
            ).url(web: "https://app.example.com"))
        try? await Task.sleep(nanoseconds: 50_000_000)
        #expect(m.step == .form && m.code.isEmpty && m.server == "https://" && m.notice.isEmpty)
        #expect(fake.pairings.isEmpty)
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
        #expect(
            m.notice.hasPrefix("不确定") && m.notice.contains("有没有加入")
                && m.notice.hasSuffix("可以在手机 App 的设置里看看客户端列表"))
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

    @Test func anAccountSecretCannotEnrollEvenWhenWellFormed() {
        let m = model()
        for value in [
            "ra1.nonsense",
            "ra1.\(Data(repeating: 7, count: 32).base64URL).\(Data("https://ra.example.com".utf8).base64URL)",
        ] {
            m.secret = value
            m.joinWithSecret()
            #expect(m.step == .form && m.noticeIsError && m.secret.isEmpty)
            #expect(m.notice.contains("在手机 App 上批准"))
            #expect(fake.secrets.isEmpty && joined.accounts.isEmpty)
        }
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
