// What several screens need and would otherwise each write: the bar that
// says how the connection stands with the sheet behind it, one command with
// its progress, a session's history followed while a screen shows it, and a
// clock that stops in the background. Counterparts of Android's
// ui/Common.kt (`ConnBanner`, `now`) and ui/Actions.kt (`Action`).
import Observation
import RACore
import SwiftUI

/// The connection notice of a page, under its navigation bar: nothing when
/// online, otherwise the nearest stretch that is down (architecture §9
/// 「断线时显示什么」), and 详情 when that is the server.
struct ConnectionBar: View {
    @Environment(AppModel.self) private var model
    @State private var details = false

    var body: some View {
        ConnBanner(model.state) { details = true }
            .kitSheet(isPresented: $details) {
                ServerDetails(state: model.state) { details = false }
            }
    }
}

/// Why the server is out of reach: the judgement first, the failure in the system's words last.
private struct ServerDetails: View {
    let state: RemoteState
    let onClose: () -> Void
    @Environment(AppModel.self) private var model

    var body: some View {
        SheetHead("连不上服务器")
        Text("本机的网络是通的，但服务器没有回应。可能是服务器停了或正在重启，也可能是现在的网络到不了它。会一直自动重试，连上后补齐这段时间的内容。")
            .type(T.sm, Theme.fg2)
        if let server = model.account?.server {
            Text("服务器　\(hostOf(server))").type(T.sm, Theme.muted).padding(.top, 10)
        }
        // Since start nothing was reached: when it was last is not known here.
        if state.synced {
            Ticking { now in
                Text("断开于　\(whenText(state.offlineSinceMs, now: now))").type(T.sm, Theme.muted).padding(.top, 2)
            }
        }
        if !state.error.isEmpty { Text("原因　\(state.error)").type(T.sm, Theme.muted).padding(.top, 2) }
        if !state.errorDetail.isEmpty && state.errorDetail != state.error {
            Code(state.errorDetail, maxLines: 4).frame(maxWidth: .infinity, alignment: .leading).padding(.top, 10)
        }
        Btn("立即重试") {
            model.remote?.reconnectNow()
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

extension StatusTone {
    /// A status's tone as the kit colours it.
    init(_ tone: Tone) {
        switch tone {
        case .run: self = .run
        case .wait: self = .wait
        case .idle: self = .idle
        case .off: self = .off
        }
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

/// A session's events, kept whole for as long as a screen follows them
/// (`Remote.events`). A screen keeps one in `@State` and follows it from a
/// task, which ends when the screen goes away, letting the history go:
///
///     .task(id: sid) { await history.follow(ctx.remote, sid) }
@MainActor
@Observable
final class SessionHistory {
    private(set) var events = SessionEvents()

    /// Follows the session until the task is cancelled. The feed keeps only
    /// the latest snapshot for a reader that is behind, and a frame passes
    /// between two of them.
    func follow(_ remote: Remote, _ sid: String) async {
        events = SessionEvents()
        for await snapshot in remote.events(sid).updates() {
            events = snapshot
            if (try? await Task.sleep(nanoseconds: 16_000_000)) == nil { return }
        }
    }
}
