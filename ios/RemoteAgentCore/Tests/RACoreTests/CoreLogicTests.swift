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
        #expect(errorText(cause) == L10n.errorsIosCannotFindTheServerCheckYourNetwork())
        #expect(unresolvedHost(cause))
        #expect(detailText(cause).hasPrefix("NSURLErrorDomain -1003: "))
        #expect(!unresolvedHost(URLError(.timedOut)))
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
