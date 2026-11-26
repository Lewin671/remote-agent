// What several screens need and would otherwise each write: the bar that
// says how the connection stands with the sheet behind it, one command with
// its progress, a session's history followed while a screen shows it, and a
// clock that stops in the background. Counterparts of Android's
// ui/Common.kt (`ConnBanner`, `now`) and ui/Actions.kt (`Action`).
import Observation
import RACore
import RAProto
import SwiftUI

/// Resolved banner presentation derived from Core's `ConnectionViewModel` and clock-skew banner text.
struct ConnectionBannerPresentation: Equatable {
    enum Tone: Equatable { case plain, attention, error }
    let text: String
    let symbol: String?
    let spinning: Bool
    let tone: Tone
    let showDetails: Bool

    static func resolve(connection: PBConnectionViewModel) -> ConnectionBannerPresentation? {
        if connection.hasBanner && connection.banner.visible {
            let banner = connection.banner
            switch banner.kind {
            case .noNetwork:
                return ConnectionBannerPresentation(
                    text: banner.text.rendered(),
                    symbol: "wifi.slash",
                    spinning: false,
                    tone: .plain,
                    showDetails: banner.showDetails
                )
            case .waiting:
                return ConnectionBannerPresentation(
                    text: banner.text.rendered(),
                    symbol: nil,
                    spinning: true,
                    tone: .plain,
                    showDetails: banner.showDetails
                )
            case .noServer:
                if banner.errorTone {
                    let detail = banner.hasDetailText ? banner.detailText.rendered() : ""
                    return ConnectionBannerPresentation(
                        text: detail.or(banner.text.rendered()),
                        symbol: "xmark",
                        spinning: false,
                        tone: .error,
                        showDetails: banner.showDetails
                    )
                }
                return ConnectionBannerPresentation(
                    text: banner.text.rendered(),
                    symbol: "icloud.slash",
                    spinning: false,
                    tone: .plain,
                    showDetails: banner.showDetails
                )
            case .none, .unspecified, .UNRECOGNIZED:
                break
            }
        }
        if connection.hasClockSkewNotice {
            let clockText = connection.clockSkewNotice.rendered()
            if !clockText.isEmpty {
                return ConnectionBannerPresentation(
                    text: clockText, symbol: "clock", spinning: false, tone: .attention, showDetails: false)
            }
        }
        return nil
    }
}

/// The connection notice of a page, under its navigation bar, in from the page's sides.
struct ConnectionBar: View {
    @Environment(AccountContext.self) private var context: AccountContext?

    var body: some View {
        if let context,
           ConnectionBannerPresentation.resolve(connection: context.core.connection) != nil
        {
            ConnectionStrip().padding(.horizontal, DS.Space.l).padding(.vertical, 6)
        }
    }
}

/// How the way to the server stands, as a strip of glass under a page's
/// title (design-system §6 「连接状态」, architecture §9 「断线时显示什么」):
/// driven by Core's `ConnectionViewModel`.
struct ConnectionStrip: View {
    @Environment(AccountContext.self) private var context: AccountContext?
    @State private var details = false

    var body: some View {
        if let context {
            let connection = context.core.connection
            let banner = ConnectionBannerPresentation.resolve(connection: connection)
            Group {
                if let banner {
                    let stripTone: NoticeTone = switch banner.tone {
                    case .plain: .plain
                    case .attention: .attention
                    case .error: .error
                    }
                    if banner.showDetails {
                        GlassStrip(
                            text: banner.text,
                            symbol: banner.symbol,
                            spinning: banner.spinning,
                            tone: stripTone,
                            action: L10n.commonDetails()
                        ) { details = true }
                    } else {
                        GlassStrip(
                            text: banner.text,
                            symbol: banner.symbol,
                            spinning: banner.spinning,
                            tone: stripTone
                        )
                    }
                }
            }
            .kitSheet(isPresented: $details) {
                ServerDetails(connection: connection) { details = false }
            }
        }
    }
}

/// Why the server is out of reach: the judgement first, the failure in the system's words last.
struct ServerDetails: View {
    let connection: PBConnectionViewModel
    let onClose: () -> Void
    @Environment(AppModel.self) private var model

    var body: some View {
        let details = connection.serverDetails
        let reason = details.hasReasonText ? details.reasonText.rendered() : ""
        SheetHead(L10n.connectionNoServer())
        Text(L10n.connectionIosExplanation())
            .font(.subheadline).ink(DS.ink2)
        if let server = model.account?.server {
            Text(L10n.connectionIosHost(host: hostOf(server))).font(.subheadline).ink(DS.ink2).padding(.top, 10)
        }
        // Since start nothing was reached: when it was last is not known here.
        if details.showDisconnectedAt {
            Ticking { now in
                Text(L10n.connectionIosDisconnectedAt(time: whenText(details.disconnectedAtMs, now: now))).font(.subheadline).ink(DS.ink2).padding(.top, 2)
            }
        }
        if !reason.isEmpty { Text(L10n.connectionIosReason(reason: reason)).font(.subheadline).ink(DS.ink2).padding(.top, 2) }
        Btn(L10n.connectionIosRetryNow()) {
            model.reconnectNow()
            onClose()
        }
        .padding(.top, 14)
    }
}

/// A server address without its scheme, as rows show it.
func hostOf(_ server: String) -> String {
    server.range(of: "://").map { String(server[$0.upperBound...]) } ?? server
}

private struct Tick: ViewModifier {
    @Binding var now: Int64
    let seconds: TimeInterval
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        // Nothing ticks in the background (architecture §10 「后台和内存」); coming back starts with the time it is then.
        content.task(id: model.foreground) {
            guard model.foreground else { return }
            while !Task.isCancelled {
                now = nowMs()
                try? await Task.sleep(for: .seconds(seconds))
            }
        }
    }
}

extension View {
    /// Keeps `now` at the current time in milliseconds, renewed every
    /// `seconds` while the app is in front (Android's `now()`). The view
    /// stays what it was when the app comes back: a list keeps where it was
    /// scrolled to, a field what was typed.
    func ticking(_ now: Binding<Int64>, every seconds: TimeInterval = 30) -> some View {
        modifier(Tick(now: now, seconds: seconds))
    }
}

/// The current time in milliseconds, handed to `content` again every
/// `seconds` while the app is in front; for where only a part of a page
/// needs the time. `content` is drawn again with the new time, never built
/// anew: what it holds (scroll position, focus, its own state) stays.
struct Ticking<Content: View>: View {
    private let seconds: TimeInterval
    private let content: (Int64) -> Content
    @State private var now = nowMs()

    init(every seconds: TimeInterval = 30, @ViewBuilder content: @escaping (_ now: Int64) -> Content) {
        self.seconds = seconds
        self.content = content
    }

    var body: some View {
        content(now).ticking($now, every: seconds)
    }
}

/// One command with its progress: running, and the error of the last try.
/// Keep one in `@State` per button; `run` does nothing while one is running.
/// The command goes on when its page is left, and `onDone` is called all the
/// same: what it does to the stack goes through `Navigator.back(from:)` or
/// `open(_:from:)`, which leave a stack the user has moved on in alone.
@MainActor
@Observable
final class ActionRunner {
    private(set) var running = false
    var error = ""
    /// The last failure left the outcome unknown: it may have run.
    private(set) var unknown = false

    func run(_ work: @escaping @MainActor () async throws -> Void, onDone: @escaping @MainActor () -> Void = {}) {
        guard !running else { return }
        running = true
        error = ""
        unknown = false
        Task {
            do {
                try await work()
                onDone()
            } catch is CancellationError {
                // The screen went away.
            } catch {
                self.error = errorText(error)
                unknown = (error as? CommandError)?.unknown == true
            }
            running = false
        }
    }
}
