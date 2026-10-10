// The app's single graph, the counterpart of Android's RaApp.kt: the
// account on this device, the sync engine made for it, and what the engine
// knows, copied to where SwiftUI can observe it. The rules are Android's:
//
//   - the account is read only in the foreground, off the main actor; a
//     Keychain that does not open (the device locked again meanwhile) is
//     said and tried again on the next return;
//   - the main stream lives only while the app is in the foreground
//     (architecture §10 「后台和内存」): going to the background stops it and
//     the terminals, coming back starts it, and the engine catches up;
//   - the system's word on the network only words the notice and hurries a
//     reconnect (`NetworkChange`);
//   - logging out forgets the keys first, then everything stored for the
//     account, and what the screens kept in memory with it.
//
// Screens read three things from the environment: this model (`state`,
// `settings`, `foreground`, `logout`), the `AccountContext` of the account
// in use (`remote`, `account`, `memory`), and the `Navigator`.
import Foundation
import Observation
import RACore
import SwiftUI
import UIKit
import UserNotifications
import os

/// What screens under the main view share for as long as one account is in
/// use. It is replaced, never changed, when the account is: a screen that
/// outlives a logout by a frame still holds the one it was drawn with.
@MainActor
@Observable
final class AccountContext {
    let account: Account
    let remote: Remote
    /// What this run keeps in memory for the account: drafts and the like.
    let memory = AccountMemory()

    /// This client's device ID.
    var me: String { account.deviceId }

    init(account: Account, remote: Remote) {
        self.account = account
        self.remote = remote
    }
}

/// Plaintext a screen keeps between visits, per account and only in memory
/// (Android's `AccountMemory`): one value per type, made on first use, all
/// gone with the account. Drafts, messages sent from here and answers whose
/// outcome is unknown belong here, never in UserDefaults or a file.
@MainActor
final class AccountMemory {
    private var values: [ObjectIdentifier: Any] = [:]

    func value<T>(_ type: T.Type = T.self, make: () -> T) -> T {
        if let v = values[ObjectIdentifier(type)] as? T { return v }
        let v = make()
        values[ObjectIdentifier(type)] = v
        return v
    }
}

/// UserDefaults keys that belong to the account in use, such as the scope
/// the home page was left on. Everything under the prefix is removed on
/// logout, so a screen that keeps such a value only has to name it here.
enum AccountDefaults {
    static let prefix = "account."

    static func key(_ name: String) -> String { prefix + name }

    static func clear(_ defaults: UserDefaults) {
        for key in defaults.dictionaryRepresentation().keys where key.hasPrefix(prefix) {
            defaults.removeObject(forKey: key)
        }
    }
}

/// The engine made for an account: what the lifecycle drives, and the `Remote` the screens use.
struct EngineParts {
    var engine: any SyncEngine
    var remote: Remote
}

@MainActor
@Observable
final class AppModel {
    enum Stage: Equatable {
        /// The stored account has not been read yet, or is being erased.
        case loading
        /// The Keychain did not open, or did not let go: the reason, to show with 重试.
        case failed(String)
        /// No account on this device: the join flow.
        case join
        /// An account is in use; `context` is set.
        case ready
    }

    /// What the model is made of; the defaults are the real things.
    struct Parts {
        var storage: AccountStorage = .standard()
        var network: any NetworkWatching = SystemNetwork()
        var defaults: UserDefaults = .standard
        /// Opens the account's cache of ciphertext. Called off the main actor.
        var openCache: @Sendable (_ root: URL, _ account: Account) throws -> CipherStore = { root, account in
            try CipherStore.open(root: root, account: CipherStore.accountKey(server: account.server, deviceId: account.deviceId))
        }
        var makeEngine: @MainActor (_ account: Account, _ cache: (any CipherCache)?) throws -> EngineParts = { account, cache in
            let log = Logger(subsystem: "io.github.lewin671.remoteagent", category: "sync")
            // The engine hands over error texts only, never content or keys.
            let remote = try Remote(account: account, http: AppModel.http, cache: cache, log: { log.info("\($0, privacy: .public)") })
            return EngineParts(engine: remote, remote: remote)
        }
        /// The pause between two copies of the engine's state to the main actor: a frame.
        var frame: Sleeper = realSleep
        /// What this client says it runs on, as Android's "Android 16 · Pixel 9".
        var deviceDescription: @MainActor () -> String = {
            "iOS \(UIDevice.current.systemVersion) · \(UIDevice.current.userInterfaceIdiom == .pad ? "iPad" : "iPhone")"
        }
        /// Notifications may show the old account's approvals in full.
        var clearNotifications: @MainActor () -> Void = {
            UNUserNotificationCenter.current().removeAllDeliveredNotifications()
        }
    }

    /// The app's one HTTP stack; joining uses it before there is an engine.
    static let http = HTTP()

    // MARK: What screens read

    private(set) var stage = Stage.loading
    /// The account in use and its engine; nil until one is, and after logout.
    private(set) var context: AccountContext?
    /// The engine's lists and connection as last copied here. A fresh, empty state while no account is in use.
    private(set) var state = RemoteState()
    /// The app is in front. Screens that tick or hold a terminal follow it:
    /// timers pause in the background and terminals are closed there.
    private(set) var foreground = false
    /// The server of the account just left, to fill in when joining again. Only in memory.
    private(set) var lastServer = ""
    /// Sample data is shown and nothing is stored or sent (`-demo`, debug builds).
    private(set) var isDemo = false
    let settings: SettingsStore

    var account: Account? { context?.account }
    var remote: Remote? { context?.remote }
    /// The server turned this device away (architecture §13 「移除设备」): only the notice is shown.
    var removed: Bool { stage == .ready && state.removed }

    /// The account's cache of ciphertext, where viewed images are kept as sealed envelopes; nil while none is
    /// open (no account, sample data, or a cache that did not open).
    var imageCache: CipherStore? { cache }
    /// Where images waiting to be sent are kept, sealed.
    var pendingImages: PendingStore { parts.storage.pending }

    // MARK: Private state

    @ObservationIgnored private let parts: Parts
    @ObservationIgnored private var engine: (any SyncEngine)?
    @ObservationIgnored private var cache: CipherStore?
    /// Where the engine's login is kept between launches; closed before the account is erased.
    @ObservationIgnored private var logins: LoginKeeper?
    @ObservationIgnored private var follower: Task<Void, Never>?
    @ObservationIgnored private var loading = false
    /// Bumped whenever the account in use changes: a step that awaited meanwhile finds its work unwanted.
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var path = NetworkPath.unknown
    @ObservationIgnored private var watching = false
    @ObservationIgnored private var terminals: [UUID: TermConn] = [:]
    /// The revision of this device's metadata and connection a description was last sent for.
    @ObservationIgnored private var described: String?
    @ObservationIgnored private let log = Logger(subsystem: "io.github.lewin671.remoteagent", category: "app")

    init(settings: SettingsStore, parts: Parts = Parts()) {
        self.settings = settings
        self.parts = parts
    }

    // MARK: Lifecycle

    /// The scene's phase, from the app. Inactive (the app switcher, a system
    /// sheet over the app) still counts as in front: only the background
    /// closes the connections.
    func scenePhase(_ phase: ScenePhase) {
        setForeground(phase != .background)
    }

    func setForeground(_ front: Bool) {
        watchNetwork()
        guard front != foreground else { return }
        foreground = front
        if front {
            loadAccount()
            engine?.start()
        } else {
            closeTerminals()
            engine?.stop()
        }
    }

    private func watchNetwork() {
        guard !watching else { return }
        watching = true
        parts.network.start { [weak self] path in
            Task { @MainActor in self?.networkChanged(path) }
        }
    }

    /// The system's default route changed, or was told for the first time.
    func networkChanged(_ new: NetworkPath) {
        let change = NetworkChange.from(path, to: new)
        path = new
        switch change {
        case .none:
            break
        case .lost:
            engine?.setNetwork(false)
        case .gained:
            engine?.setNetwork(true)
            engine?.reconnectNow()
        }
    }

    // MARK: The account

    /// Reads the stored account, only in the foreground: a process started
    /// in the background while the device is locked could not open the
    /// Keychain. A failure is shown with 重试 and tried again on the next return.
    func loadAccount() {
        guard context == nil, !loading, !isDemo, foreground else { return }
        loading = true
        let vault = parts.storage.vault
        let gen = generation
        Task {
            // The Keychain call blocks.
            let result = await Task.detached { Result { try vault.load() } }.value
            loading = false
            guard gen == generation, context == nil else { return }
            switch result {
            case .success(let stored?):
                await use(Account(stored))
            case .success(nil):
                stage = .join
            case .failure(let error):
                log.error("stored account does not open: \(String(describing: type(of: error)), privacy: .public)")
                stage = .failed(error.localizedDescription)
            }
        }
    }

    private func use(_ account: Account) async {
        generation += 1
        let gen = generation
        let root = parts.storage.cacheRoot
        let open = parts.openCache
        // Opening the database touches the disk. Without it the app still
        // works, from the server alone.
        let keeper = LoginKeeper(vault: parts.storage.vault, server: account.server, deviceId: account.deviceId)
        let (store, kept) = await Task.detached { (try? open(root, account), keeper.load()) }.value
        guard gen == generation else {
            try? store?.close()
            return
        }
        if store == nil { log.error("the cache does not open; going on without it") }
        let made: EngineParts
        do {
            made = try parts.makeEngine(account, store)
        } catch {
            try? store?.close()
            stage = .failed(errorText(error))
            return
        }
        // Before the engine starts: the stream is opened with the login the last run kept.
        log.info("login from the last run: \(kept == nil ? "none" : "found", privacy: .public)")
        made.remote.keepLogin(kept) { keeper.keep($0) }
        logins = keeper
        adopt(account: account, made: made, store: store)
        if foreground { made.engine.start() }
    }

    private func adopt(account: Account, made: EngineParts, store: CipherStore?) {
        cache = store
        engine = made.engine
        described = nil
        state = made.remote.state.value
        context = AccountContext(account: account, remote: made.remote)
        // The account's own view of the store: what is still writing when the account is erased writes nothing.
        ImageCenter.install(remote: made.remote, files: parts.storage.pending.scoped())
        stage = .ready
        made.engine.setNetwork(path.available)
        follow(made.engine.state, gen: generation)
    }

    /// Copies the engine's snapshots to the main actor, the latest only: the
    /// feed keeps one for a reader that is behind, and a frame passes
    /// between two copies, so a burst of events costs the main actor one
    /// update per frame however many there were.
    private func follow(_ feed: Feed<RemoteState>, gen: Int) {
        follower?.cancel()
        let frame = parts.frame
        follower = Task.detached { [weak self] in
            for await snapshot in feed.updates() {
                guard let self else { return }
                await self.apply(snapshot, gen: gen)
                if (try? await frame(16)) == nil { return }
            }
        }
    }

    private func apply(_ snapshot: RemoteState, gen: Int) {
        guard gen == generation else { return }
        if snapshot != state {
            #if DEBUG
            if snapshot.link != state.link || snapshot.machines.count != state.machines.count || snapshot.removed != state.removed {
                // IDs and counts only (scope §4).
                log.info("""
                    state link=\(String(describing: snapshot.link), privacy: .public) \
                    machines=\(snapshot.machines.keys.sorted().joined(separator: ","), privacy: .public) \
                    online=\(snapshot.machines.values.filter(\.online).count) sessions=\(snapshot.sessions.count) \
                    devices=\(snapshot.devices.count) removed=\(snapshot.removed)
                    """)
            }
            #endif
            state = snapshot
        }
        describeSelf(snapshot)
    }

    /// Says what this client is, sealed, so other clients can tell it apart:
    /// once it is listed and online, and again only when what is stored
    /// differs. As MainActivity.kt does it.
    private func describeSelf(_ s: RemoteState) {
        guard !isDemo, let engine, s.conn == .online, let me = s.devices[engine.account.deviceId] else { return }
        let description = parts.deviceDescription()
        guard me.meta?.platform != "ios" || me.meta?.description_p != description else { return }
        // One try per revision: a failure waits for the next change instead of spinning.
        let key = "\(me.metaRev)"
        guard described != key else { return }
        described = key
        Task {
            do {
                try await engine.describeSelf(platform: "ios", description: description)
                log.info("described this client")
            } catch {
                log.info("describing this client failed: \(errorText(error), privacy: .public)")
            }
        }
    }

    /// Stores a newly joined account and starts syncing. Throws when the
    /// Keychain refuses: the device is registered then, but nothing is kept.
    func joined(_ account: Account) async throws {
        // A real account from here on, whatever was being shown.
        isDemo = false
        let vault = parts.storage.vault
        try await Task.detached {
            try vault.save(account.stored, notifyKey: Keys.derive(root: account.root).notify)
        }.value
        await use(account)
    }

    /// Tries the login again after the server turned this device away.
    func retryLogin() {
        engine?.retryLogin()
    }

    /// Forgets the account, the cache and the keys on this device, then
    /// shows the join flow with the server filled in. When the Keychain does
    /// not let go the account is still here, and that is what is said.
    func logout() async {
        if let account { lastServer = account.server }
        generation += 1
        follower?.cancel()
        follower = nil
        closeTerminals()
        engine?.close()
        engine = nil
        // Drafts, thumbnails and viewed images held in memory go with the account.
        if let remote { ImageCenter.remove(remote) }
        context = nil
        state = RemoteState()
        described = nil
        let store = cache
        cache = nil
        let keeper = logins
        logins = nil
        if isDemo {
            // Sample data: nothing was stored, and the real account is not this one's to erase.
            stage = .join
            return
        }
        stage = .loading
        parts.clearNotifications()
        let storage = parts.storage
        let result = await Task.detached {
            Result {
                // SQLite must let go of its files before they are removed, and no login may be kept after the keys are gone.
                try? store?.close()
                keeper?.close()
                try LocalAccountData.erase(vault: storage.vault, cacheRoot: storage.cacheRoot, pending: storage.pending)
            }
        }.value
        settings.clear()
        AccountDefaults.clear(parts.defaults)
        switch result {
        case .success:
            stage = .join
        case .failure(let error):
            log.error("erasing the account failed: \(String(describing: type(of: error)), privacy: .public)")
            stage = .failed(error.localizedDescription)
        }
    }

    // MARK: Terminals

    /// A terminal a screen opened, so that going to the background closes
    /// it with the main stream. The screen reopens it when `foreground`
    /// comes back, and lets go of it here when it closes it itself.
    func holdTerminal(_ conn: TermConn) -> UUID {
        let id = UUID()
        terminals[id] = conn
        return id
    }

    func releaseTerminal(_ id: UUID) {
        terminals[id] = nil
    }

    private func closeTerminals() {
        for conn in terminals.values { conn.close() }
        terminals.removeAll()
    }

    // MARK: Sample data

    #if DEBUG
    /// Shows fixed state without a server (`-demo`). The Remote is never
    /// started; `initial` is what it is about to show, for the first frame.
    func showDemo(account: Account, remote: Remote, initial: RemoteState, load: @escaping @Sendable () async -> Void) {
        isDemo = true
        generation += 1
        let gen = generation
        state = initial
        context = AccountContext(account: account, remote: remote)
        ImageCenter.install(remote: remote, files: VolatileFiles(), live: false)
        stage = .ready
        Task {
            await load()
            if gen == generation { follow(remote.state, gen: gen) }
        }
    }

    /// The join flow without reading the Keychain, for looking at its states (`-joinStep`).
    func showJoinForDemo(lastServer: String) {
        isDemo = true
        self.lastServer = lastServer
        stage = .join
    }
    #endif
}
