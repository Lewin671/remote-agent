import Connect
import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct PingTests {
    @Test func oneSlowSampleDoesNotMakeTheLineSlow() {
        #expect(median([900, 80, 95]) == 95)
        #expect(median([70]) == 70)
    }
}

@Suite(.timeLimit(.minutes(2))) struct ErrorTextTests {
    @Test func aRequestThatNeverLeftThePhoneNamesTheNetworkNotTheResolver() {
        let cause = URLError(.cannotFindHost)
        let e = ConnectError(code: .unknown, message: cause.localizedDescription, exception: cause)
        #expect(errorText(e) == L10n.errorsIosCannotFindTheServerCheckYourNetwork())
        #expect(errorText(cause) == L10n.errorsIosCannotFindTheServerCheckYourNetwork())
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
            #expect(errorText(URLError(code)) == L10n.errorsIosCannotFindTheServerCheckYourNetwork())
            #expect(unresolvedHost(URLError(code)))
        }
        #expect(errorText(URLError(.cannotConnectToHost)) == L10n.connectionNoServer())
        #expect(errorText(URLError(.timedOut)) == L10n.errorsConnectionTimedOut())
        #expect(errorText(URLError(.secureConnectionFailed)).hasPrefix("安全连接失败："))
        #expect(errorText(URLError(.serverCertificateUntrusted)).hasPrefix("安全连接失败："))
        // A WebSocket fails with the socket's own error.
        #expect(errorText(NSError(domain: NSPOSIXErrorDomain, code: Int(ECONNREFUSED))) == L10n.connectionNoServer())
        #expect(errorText(NSError(domain: NSPOSIXErrorDomain, code: Int(ETIMEDOUT))) == L10n.errorsConnectionTimedOut())
        // Only a request that did not leave the device is known not to have run.
        #expect(neverSent(URLError(.cannotFindHost)))
        #expect(neverSent(URLError(.cannotConnectToHost)))
        #expect(!neverSent(URLError(.timedOut)))
        #expect(!neverSent(URLError(.networkConnectionLost)))
        #expect(!neverSent(nil))
    }

    @Test func anErrorOfTheAppsOwnIsSaidInItsOwnWords() {
        #expect(errorText(CommandError("unimplemented", "这台电脑不支持")) == "这台电脑不支持")
        #expect(errorText(UserError(L10nMessage.errorsConnectionTimedOutReconnecting())) == L10n.errorsConnectionTimedOutReconnecting())
        #expect(detailText(UserError(L10nMessage.errorsDisconnectedFromServer())) == L10n.errorsDisconnectedFromServer())
        #expect(errorText(OpenError("envelope does not open")) == "envelope does not open")
        #expect(
            detailText(OpenError("envelope does not open")) == "OpenError: envelope does not open")
        #expect(errorText(PairError(L10nMessage.queueCanceled())) == L10n.queueCanceled())
        #expect(errorText(PrefsConflictError()) == L10n.errorsIosOtherClientsKeptChangingProjectPreferencesChangesCouldNot())
        // One without words is called by its type, never by NSError's sentence about it.
        struct Wordless: Error {}
        #expect(errorText(Wordless()) == "Wordless")
        #expect(detailText(Wordless()) == "Wordless")
    }
}

@Suite(.timeLimit(.minutes(2))) struct PagedListTests {
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

    @Test func appendLineDoesNotConfuseBranchesAtFullPageBoundaries() {
        let common = PagedList(Array(0..<256))
        let left = common.appending([256])
        let right = common.appending([999])
        let rightTail = right.appending(Array(1000..<1300))
        #expect(left.extends(common) && rightTail.extends(common))
        #expect(!rightTail.extends(left) && !left.extends(right))
        #expect(rightTail.extends(right))
        #expect(!common.extends(left))
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

@Suite(.timeLimit(.minutes(2))) struct PairLinkSecurityTests {
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
@Suite(.timeLimit(.minutes(2))) struct ShowForTestTests {
    @Test func showsStateAndHistoryWithoutFetching() async throws {
        let remote = try fixtureRemote(
            account: Account(
                server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32),
                deviceId: "2222222222222222"),
            http: HTTP())
        defer { remote.close() }
        let m = MachineView(id: "m", online: true, lastSeenMs: 1)
        var state = RemoteState()
        state.machines = ["m": m]
        state.loaded = true
        var list = PBListModelsResponse()
        list.defaultModel = "astra"
        await remote.showForTest(
            state: state,
            modelLists: [(machineId: "m", agent: .codex, models: list)])
        #expect(remote.current.machines["m"] == m)
        #expect(try await remote.listModels("m", agent: .codex).defaultModel == "astra")
        await #expect(throws: CommandError.self) { try await remote.machineCommand("m", PBCommand()) }
    }
}

@Suite(.timeLimit(.minutes(2))) struct LoginDenialTests {
    @Test func translatesOnlyLoginDenialAndKeepsOfflineSeparate() {
        let denied = ConnectError(code: .permissionDenied, message: "login failed")
        #expect(loginDenied(denied))
        #expect(errorText(denied) == L10n.errorsTheServerDoesNotRecognizeThisDeviceItMay())
        #expect(!loginDenied(URLError(.notConnectedToInternet)))
        #expect(!loginDenied(ConnectError(code: .unavailable, message: nil)))
        #expect(!loginDenied(ConnectError(code: .notFound, message: nil)))
        #expect(errorText(ConnectError(code: .permissionDenied, message: "operation denied")) == "operation denied")
    }
}
