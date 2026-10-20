import Connect
import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct AttemptTests {
    private final class Maker {
        var n = 0
        func make() -> PreparedCommand {
            n += 1
            return PreparedCommand(
                machineId: "m", sessionId: "", requestId: "req-\(n)", deadlineMs: 1000,
                envelope: PBEnvelope())
        }
    }

    @Test func anUnknownOutcomeIsRetriedAsItWas() throws {
        let m = Maker()
        let a = Attempt()
        let first = try a.next(m.make)
        a.settle(CommandError(CommandError.unknownOutcome, "timeout"))
        #expect(a.frozen)
        #expect(try a.next(m.make) === first)
        a.settle()
        #expect(!a.frozen)
        #expect(try a.next(m.make) !== first)
    }

    @Test func aDaemonThatStoppedHalfwayLeavesTheOutcomeUnknown() throws {
        let m = Maker()
        let a = Attempt()
        let first = try a.next(m.make)
        // What the daemon answers for a request it began and has no result for (errUnknownOutcome in internal/relay).
        let stopped = CommandError(
            "aborted", "the daemon stopped while running this command", fromDaemon: true)
        #expect(uncertain(stopped))
        a.settle(stopped)
        #expect(a.frozen)
        #expect(try a.next(m.make) === first)
        // The same answer to the retry is still no answer.
        a.settle(CommandError(CommandError.unknownOutcome, "timeout"))
        a.settle(stopped)
        #expect(a.frozen)
        #expect(try a.next(m.make) === first)
        // The code alone is not the daemon's word: the app gives up on a device's name with it.
        #expect(!uncertain(CommandError("aborted", "设备名称一直在被其他客户端修改，没能保存")))
    }

    @Test func aDefiniteFailureFreesTheNextAttempt() throws {
        let m = Maker()
        let a = Attempt()
        let first = try a.next(m.make)
        a.settle(CommandError(CommandError.unavailable, "offline"))
        #expect(try a.next(m.make) !== first)
    }

    @Test func anExpiredUnknownStaysUnknownAndIsNotRenewed() throws {
        let m = Maker()
        let a = Attempt()
        let first = try a.next(m.make)
        a.settle(CommandError(CommandError.unknownOutcome, "timeout"))
        #expect(try a.next(m.make) === first)
        // The retry comes back expired: still unknown, and never sealed again.
        let e = a.settle(
            CommandError(
                "deadline_exceeded", "the command expired before it reached the machine",
                fromDaemon: true))
        #expect((e as? CommandError)?.code == CommandError.unknownOutcome)
        #expect(a.frozen && a.expired)
        for _ in 0..<2 {
            do {
                _ = try a.next(m.make)
                Issue.record("an expired unknown command was sent again")
            } catch let x as CommandError {
                #expect(x.unknown)
            }
        }
        #expect(m.n == 1)
        // Only giving up frees it.
        a.abandon()
        #expect(try a.next(m.make) !== first)
    }

    @Test func anExpiryWithoutAnUnknownOutcomeIsDefinite() throws {
        let m = Maker()
        let a = Attempt()
        let first = try a.next(m.make)
        a.settle(CommandError(CommandError.expired, "这条指令已过期，没有发出"))
        #expect(!a.frozen)
        #expect(try a.next(m.make) !== first)
    }

    @Test func anyLaterFailureKeepsAnUnknownOutcome() throws {
        let m = Maker()
        let a = Attempt()
        let first = try a.next(m.make)
        a.settle(CommandError(CommandError.unknownOutcome, "timeout"))
        // A retry that fails to log in, or on the network, says nothing about the first try.
        let later: [Error] = [
            UserError("login failed"), CommandError("network", "网络不通"),
            CommandError(CommandError.unavailable, "电脑不在线"),
        ]
        for e in later {
            let shown = a.settle(e) as? CommandError
            #expect(shown?.unknown == true)
            #expect(a.frozen)
            #expect(try a.next(m.make) === first)
        }
        // The daemon's own answer for the request does settle it.
        a.settle(CommandError("unimplemented", "no", fromDaemon: true))
        #expect(!a.frozen)
        #expect(try a.next(m.make) !== first)
    }
}

@Suite struct PingTests {
    @Test func oneSlowSampleDoesNotMakeTheLineSlow() {
        #expect(median([900, 80, 95]) == 95)
        #expect(median([70]) == 70)
    }
}

@Suite struct ErrorTextTests {
    @Test func aRequestThatNeverLeftThePhoneNamesTheNetworkNotTheResolver() {
        let cause = URLError(.cannotFindHost)
        let e = ConnectError(code: .unknown, message: cause.localizedDescription, exception: cause)
        #expect(errorText(e) == "找不到服务器，检查一下网络")
        #expect(errorText(cause) == "找不到服务器，检查一下网络")
        #expect(unresolvedHost(e))
        #expect(detailText(e).hasPrefix("NSURLErrorDomain -1003: "))
        #expect(!unresolvedHost(URLError(.timedOut)))
    }

    @Test func aServersOwnAnswerKeepsItsText() {
        #expect(
            errorText(ConnectError(code: .unauthenticated, message: "token expired"))
                == "登录失效：token expired")
        #expect(
            errorText(ConnectError(code: .unavailable, message: "machine is offline"))
                == "连不上服务器：machine is offline")
        #expect(errorText(ConnectError(code: .notFound, message: nil)) == "not_found")
    }

    /// What Android says for java.net's failures, said for URLSession's.
    @Test func theSystemsFailuresAreSaidInTheSameWords() {
        // Without a network URLSession does not look the name up: it is the same case.
        for code in [
            URLError.Code.cannotFindHost, .dnsLookupFailed, .notConnectedToInternet,
            .dataNotAllowed, .internationalRoamingOff,
        ] {
            #expect(errorText(URLError(code)) == "找不到服务器，检查一下网络")
            #expect(unresolvedHost(URLError(code)))
        }
        #expect(errorText(URLError(.cannotConnectToHost)) == "连不上服务器")
        #expect(errorText(URLError(.timedOut)) == "连接超时")
        #expect(errorText(URLError(.secureConnectionFailed)).hasPrefix("安全连接失败："))
        #expect(errorText(URLError(.serverCertificateUntrusted)).hasPrefix("安全连接失败："))
        // A WebSocket fails with the socket's own error.
        #expect(errorText(NSError(domain: NSPOSIXErrorDomain, code: Int(ECONNREFUSED))) == "连不上服务器")
        #expect(errorText(NSError(domain: NSPOSIXErrorDomain, code: Int(ETIMEDOUT))) == "连接超时")
        // Only a request that did not leave the device is known not to have run.
        #expect(neverSent(URLError(.cannotFindHost)))
        #expect(neverSent(URLError(.cannotConnectToHost)))
        #expect(!neverSent(URLError(.timedOut)))
        #expect(!neverSent(URLError(.networkConnectionLost)))
        #expect(!neverSent(nil))
    }

    @Test func anErrorOfTheAppsOwnIsSaidInItsOwnWords() {
        #expect(errorText(CommandError("unimplemented", "这台电脑不支持")) == "这台电脑不支持")
        #expect(errorText(UserError("连接超时，正在重连")) == "连接超时，正在重连")
        #expect(detailText(UserError("与 server 的连接断开")) == "与 server 的连接断开")
        #expect(errorText(OpenError("envelope does not open")) == "envelope does not open")
        #expect(
            detailText(OpenError("envelope does not open")) == "OpenError: envelope does not open")
        #expect(errorText(PairError("已取消")) == "已取消")
        #expect(errorText(PrefsConflictError()) == "项目偏好一直在被其他客户端修改，没能保存")
        // One without words is called by its type, never by NSError's sentence about it.
        struct Wordless: Error {}
        #expect(errorText(Wordless()) == "Wordless")
        #expect(detailText(Wordless()) == "Wordless")
    }
}

@Suite struct PagedListTests {
    @Test func appendsAcrossPageBoundariesWithoutMutatingPublishedSnapshots() {
        let values = Array(0..<1100)
        var list = PagedList<Int>()
        var snapshots: [PagedList<Int>] = []
        for range in [0...0, 1...254, 255...256, 257...999, 1000...1099] {
            snapshots.append(list)
            list = list.appending(Array(values[range]))
            #expect(Array(list) == Array(values[...range.upperBound]))
            for before in snapshots {
                #expect(list.extends(before))
                #expect(Array(before) == Array(values.prefix(before.count)))
            }
        }
        #expect(list.appending([]).extends(list) && list.extends(list.appending([])))
        #expect(!list.extends(PagedList(Array(0..<1101))))
        // The same values built apart are equal, though neither continues the other.
        #expect(list == PagedList(values))
        #expect(list.last == 1099 && list.count == 1100 && list[256] == 256)
    }

    @Test func replacingATailOrAppendingADifferentSiblingIsNotAnUnchangedPrefix() {
        let original = Array(0..<700)
        let common = PagedList(original.prefix(300))
        let left = common.appending(Array(original[300..<700]))
        let sibling = common.appending(Array(1000..<1400))
        #expect(!left.extends(sibling))
        #expect(!sibling.extends(left))
        #expect(unchangedPrefix(left, common))
        #expect(unchangedPrefix(sibling, common))
        #expect(!unchangedPrefix(PagedList(original.dropFirst()), PagedList(original.prefix(300))))
        // A second append to the same snapshot does not disturb the first.
        #expect(Array(left) == original)
        #expect(Array(sibling) == Array(0..<300) + Array(1000..<1400))
        // An older snapshot of the line is still a prefix of both; the two later ones go their own ways.
        let further = left.appending([7])
        #expect(further.extends(left) && further.extends(common) && !further.extends(sibling))
    }

    @Test func manySmallAppendsShareTheirFullPages() {
        var list = PagedList<Int>()
        var earlier: [PagedList<Int>] = []
        for i in 0..<2000 {
            if i % 97 == 0 { earlier.append(list) }
            list = list.appending([i])
        }
        #expect(Array(list) == Array(0..<2000))
        for e in earlier { #expect(list.extends(e)) }
    }
}

/// Looking a session up by its ID (architecture §8 「按 ID 取」): when it counts as missing, and what a server that cannot be asked means.
@Suite struct SessionLookupTests {
    private final class Run {
        let answers: [Result<Bool, UserError>]
        var waits: [Int64] = []
        var reports: [SessionLookup] = []
        var asked = 0

        init(_ answers: [Result<Bool, UserError>]) { self.answers = answers }

        func go() async throws {
            try await lookUpSession(
                fetch: {
                    asked += 1
                    return try answers[asked - 1].get()
                },
                wait: { waits.append($0) },
                report: { reports.append($0) })
        }
    }

    @Test func aSessionTheServerHasIsFoundWithoutSayingAnything() async throws {
        let r = Run([.success(true)])
        try await r.go()
        #expect(r.asked == 1)
        #expect(r.reports == [])
    }

    @Test func aSessionJustStartedIsNotMissingUntilTheServerSaidSoForTenSeconds() async throws {
        // Not uploaded yet for the first three asks, then there.
        let started = Run([.success(false), .success(false), .success(false), .success(true)])
        try await started.go()
        #expect(started.waits == [2_500, 2_500, 2_500])
        #expect(started.reports == [.reading, .reading, .reading])
        // Never there: missing after the fifth ask, ten seconds after the first.
        let none = Run(Array(repeating: .success(false), count: 5))
        try await none.go()
        #expect(none.asked == 5)
        #expect(none.waits.reduce(0, +) == 10_000)
        #expect(none.reports.last == .missing)
    }

    @Test func aServerThatCannotBeAskedIsNoAnswerAndIsAskedAgainEverMoreSlowly() async throws {
        let down = Result<Bool, UserError>.failure(UserError("连不上"))
        let r = Run(Array(repeating: down, count: 6) + [.success(false), down, .success(true)])
        try await r.go()
        // Said why each time, never as missing; the waits double up to half a minute and start over after an answer.
        #expect(r.waits == [3_000, 6_000, 12_000, 24_000, 30_000, 30_000, 2_500, 3_000])
        #expect(r.reports.first == .failed(reason: "连不上"))
        #expect(!r.reports.contains(.missing))
        // What the pages say meanwhile.
        #expect(lookupNote(.failed(reason: "连不上")) == "没能读取这个会话：连不上")
        #expect(lookupNote(.missing) == "找不到这个会话")
        #expect(lookupNote(.reading) == nil)
        #expect(lookupNote(nil) == nil)
    }
}

@Suite struct PairLinkSecurityTests {
    @Test func rejectsMalformedQrWithoutThrowing() {
        for query in [
            "code=12345678&s=%", "code=12345678&s=%GG",
            "code=12345678&s=https%3A%2F%2Frelay.example&c=%A",
            "code=bad&s=https%3A%2F%2Frelay.example",
            "code=12345678&s=https%3A%2F%2Frelay.example&c=AA",
            "code=12345678&s=http%3A%2F%2Fremote.example",
            "code=１２３４５６７８&s=https%3A%2F%2Frelay.example",
            "code=12345678", "",
        ] {
            #expect(PairLink.parse("https://app.example/#/pair?\(query)") == nil, "\(query)")
        }
        #expect(
            PairLink.parse("https://app.example/?code=12345678&s=https%3A%2F%2Frelay.example")
                == nil)
    }

    @Test func validQrRoundTrips() throws {
        let link = PairLink(
            code: "12345678", server: "https://relay.example",
            commit: Data((0..<32).map { UInt8($0) }))
        let url = link.url(web: "https://app.example/")
        #expect(url.hasPrefix("https://app.example/#/pair?code=12345678&c="))
        #expect(url.hasSuffix("&s=https%3A%2F%2Frelay.example"))
        let parsed = try #require(PairLink.parse(url))
        #expect(parsed == link)
        // Without a commitment, as a code typed by hand makes it.
        let bare = PairLink(code: "00000000", server: "http://127.0.0.1:8080")
        #expect(PairLink.parse(bare.url(web: "http://localhost:4173")) == bare)
    }
}

/// The fixed state that layout tests and screenshots render, without a server.
@Suite struct ShowForTestTests {
    @Test func showsStateAndHistoryWithoutFetching() async throws {
        let remote = try fixtureRemote(
            account: Account(
                server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32),
                deviceId: "2222222222222222"),
            http: HTTP())
        defer { remote.close() }
        let m = MachineView(id: "m", online: true, lastSeenMs: 1)
        var e = PBEvent()
        e.seq = 1
        e.agentMessage.text = "你好"
        var state = RemoteState()
        state.machines = ["m": m]
        state.loaded = true
        var list = PBListModelsResponse()
        list.defaultModel = "astra"
        await remote.showForTest(
            state: state, events: ["s": [e]],
            modelLists: [(machineId: "m", agent: .codex, models: list)])
        #expect(remote.current.machines["m"] == m)
        let feed = remote.events("s")
        await sleepMs(200)
        // Nothing was fetched over the fixed history: the session is not in the lists.
        #expect(Array(feed.value.events) == [e])
        #expect(feed.value.loaded)
        #expect(try await remote.listModels("m", agent: .codex).defaultModel == "astra")
        // Commands fail as they would offline, without a request.
        await #expect(throws: CommandError.self) { try await remote.command("s", PBCommand()) }
    }
}

@Suite struct LoginDenialTests {
    @Test func translatesOnlyLoginDenialAndKeepsOfflineSeparate() {
        let denied = ConnectError(code: .permissionDenied, message: "login failed")
        #expect(loginDenied(denied))
        #expect(errorText(denied) == "服务器不认识这台设备（可能已被移除，或服务已重建）")
        #expect(!loginDenied(URLError(.notConnectedToInternet)))
        #expect(!loginDenied(ConnectError(code: .unavailable, message: nil)))
        #expect(!loginDenied(ConnectError(code: .notFound, message: nil)))
        #expect(errorText(ConnectError(code: .permissionDenied, message: "operation denied")) == "operation denied")
    }
}
