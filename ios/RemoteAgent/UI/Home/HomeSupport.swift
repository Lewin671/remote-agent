// What the home page and the project pages share besides their rows: the
// scope this device remembers, fetching earlier pages of the session
// listing while a list wants them, and the line that says how that goes.
// Counterparts of Android's `HomeScopes` (ui/Home.kt) and `FetchEarlier`,
// `EarlierTail` (ui/Common.kt).
import Observation
import RACore
import SwiftUI

/// The home page's scope as this device remembers it (architecture §8
/// 「主页的范围」): kept with the device's own settings and not in the
/// account's preferences, because different devices often look at
/// different projects. Those settings are plaintext on disk, so a project
/// is kept as a mark made with the account's key (`encodeScope`) and never
/// as its path. Nothing is remembered until the user chose a scope; what
/// the page then uses is `resolveScope`'s answer.
@MainActor
@Observable
final class HomeScopes {
    /// Under the account's prefix, so it is forgotten with the account: the mark is one of its projects'.
    static let key = AccountDefaults.key("home-scope")

    private var stored: String?
    @ObservationIgnored private let mark: Data
    @ObservationIgnored private let defaults: UserDefaults

    /// `root`: the account's root key.
    init(root: Data, defaults: UserDefaults = .standard) {
        mark = scopeKey(root: root)
        self.defaults = defaults
        stored = defaults.string(forKey: Self.key)
    }

    /// The scope chosen, while the project it names is one of `state`'s lists.
    func remembered(_ state: RemoteState) -> HomeScope? {
        decodeScope(stored, key: mark, listed: listedProjects(state))
    }

    func choose(_ scope: HomeScope) {
        stored = encodeScope(scope, key: mark)
        defaults.set(stored, forKey: Self.key)
    }

    #if DEBUG
    /// A scope to look at with sample data, without storing it over the one the real account left.
    func show(_ scope: HomeScope) { stored = encodeScope(scope, key: mark) }
    #endif
}

extension AccountContext {
    /// The scope the home page was left on, for the pages that read or change it.
    var homeScopes: HomeScopes { memory.value { HomeScopes(root: account.root, defaults: preferenceDefaults) } }
}

private struct FetchEarlier: ViewModifier {
    let state: RemoteState
    let page: Bool
    let all: Bool
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context

    private struct Key: Hashable {
        var page: Bool
        var all: Bool
        var ready: Bool
        var nextMs: Int64?
        var nextId: String?
    }

    func body(content: Content) -> some View {
        // It waits for the connection, and after a failed page for a retry.
        let ready = state.moreSessions && state.conn == .online && state.sessionsError.isEmpty
        let key = Key(page: page, all: all, ready: ready, nextMs: state.sessionsNext?.endedMs, nextId: state.sessionsNext?.sessionId)
        content.task(id: key) {
            // Sample data has no server behind it, and asking would replace it with what the engine holds: nothing.
            guard ready, !model.isDemo else { return }
            // RemoteState.sessionsError says why a page failed; the line at the end offers the retry.
            if all {
                try? await context.remote.loadAllSessions()
            } else if page {
                _ = try? await context.remote.loadMoreSessions()
            }
        }
    }
}

extension View {
    /// Fetches earlier pages of the session listing while this view is shown
    /// and wants them (architecture §8 「会话列表分页」): the next one for
    /// `page`, every page left for `all` (filtering by title).
    func fetchEarlier(_ state: RemoteState, page: Bool, all: Bool = false) -> some View {
        modifier(FetchEarlier(state: state, page: page, all: all))
    }
}

/// The line that ends a list whose earlier sessions are wanted and not all fetched; after a failure a tap tries again.
struct EarlierTail: View {
    let text: String
    let retry: Bool
    @Environment(AccountContext.self) private var context

    init(_ text: String, retry: Bool) {
        self.text = text
        self.retry = retry
    }

    /// The line for the listing as it stands now.
    init(_ state: RemoteState) {
        self.init(earlierTail(state), retry: state.conn == .online && !state.sessionsError.isEmpty)
    }

    var body: some View {
        let line = Text(text).type(T.meta, Theme.muted).frame(maxWidth: .infinity, alignment: .leading)
        if retry {
            Button {
                let remote = context.remote
                Task { _ = try? await remote.loadMoreSessions() }
            } label: {
                line.hitTarget(alignment: .leading)
            }
            .buttonStyle(PressStyle())
        } else {
            line
        }
    }
}
