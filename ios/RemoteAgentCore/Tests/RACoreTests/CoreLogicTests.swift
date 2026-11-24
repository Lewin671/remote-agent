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
        // One without words is called by its type, never by NSError's sentence about it.
        struct Wordless: Error {}
        #expect(errorText(Wordless()) == "Wordless")
        #expect(detailText(Wordless()) == "Wordless")
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
