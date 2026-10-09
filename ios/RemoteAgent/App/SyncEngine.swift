// What the app's store needs of the sync engine and of the system, each
// behind a small protocol so that the store's rules (when to connect, when
// to stop, what a lost network means) are tested without a server, a radio
// or a Keychain prompt.
import Foundation
import Network
import RACore
import os

/// The part of `Remote` the lifecycle drives. `Remote` is the only real one;
/// tests wrap a `Remote` that is never started and count the calls.
protocol SyncEngine: AnyObject, Sendable {
    var account: Account { get }
    var state: Feed<RemoteState> { get }
    func start()
    func stop()
    func setNetwork(_ available: Bool)
    func reconnectNow()
    func retryLogin()
    func close()
    func describeSelf(platform: String, description: String) async throws
}

extension Remote: SyncEngine {}

/// What the system says of this device's network. It is evidence, not
/// proof: a path that looks fine may reach nothing, and one that looks down
/// may still reach a relay on this machine (the simulator, a VPN). So it
/// only words the notice and hurries a reconnect; the engine keeps trying
/// either way, and a connection that is up outranks it (architecture §9
/// 「断线时显示什么」).
struct NetworkPath: Equatable, Sendable {
    /// The system has a route out. `requiresConnection` counts: the route comes up when asked for.
    var available: Bool
    /// What the route goes over, such as `en0`: a change of it is a change of network.
    var interface: String

    static let unknown = NetworkPath(available: true, interface: "")
}

/// Follows the default route. Android's counterpart is the default-network callback in RaApp.kt.
protocol NetworkWatching: Sendable {
    /// Calls `onChange` for the path now and for each later one, on any thread, until `stop`.
    func start(_ onChange: @escaping @Sendable (NetworkPath) -> Void)
    func stop()
}

final class SystemNetwork: NetworkWatching {
    private let monitor = NWPathMonitor()
    private let queue = DispatchQueue(label: "ra.network")

    func start(_ onChange: @escaping @Sendable (NetworkPath) -> Void) {
        monitor.pathUpdateHandler = { path in
            onChange(NetworkPath(
                available: path.status != .unsatisfied,
                interface: path.availableInterfaces.first?.name ?? ""))
        }
        monitor.start(queue: queue)
    }

    func stop() { monitor.cancel() }
}

/// What a network change asks of the engine, by the rules of Android's
/// callback: a network that came or changed is told and reconnected on at
/// once, one that went is only told. Updates that change nothing do nothing:
/// NWPathMonitor reports far more often than the route changes, and each
/// reconnect drops the stream in use.
enum NetworkChange: Equatable {
    case none
    case lost
    case gained

    static func from(_ old: NetworkPath, to new: NetworkPath) -> NetworkChange {
        if !new.available { return old.available ? .lost : .none }
        if !old.available { return .gained }
        // Still up, over something else: Wi-Fi to cellular, or another Wi-Fi.
        return !old.interface.isEmpty && old.interface != new.interface ? .gained : .none
    }
}

/// Where the account and everything stored for it live. The real one is the
/// Keychain and the app's directories; tests give each run its own.
struct AccountStorage: Sendable {
    var vault: Vault
    var cacheRoot: URL
    var pending: PendingStore

    static func standard() -> AccountStorage {
        let vault = Vault()
        return AccountStorage(vault: vault, cacheRoot: CipherStore.defaultRoot, pending: PendingStore(vault: vault))
    }
}

/// Keeps the server's login for the account between launches (architecture
/// §10 「WebSocket」): the engine is handed the one kept, and tells of each
/// new one and of the one in use being refused. It is in the Keychain with
/// the account's keys, and goes with them on logout.
///
/// The Keychain blocks, so everything runs on a queue of its own, in the
/// order asked for. Not keeping a login costs a login at the next launch
/// and nothing else, so a Keychain that refuses is not an error here.
final class LoginKeeper: @unchecked Sendable {
    private let vault: Vault
    /// Whose logins these are: one kept for another server or device is none.
    private let server: String
    private let deviceId: String
    private let queue = DispatchQueue(label: "ra.login")
    /// Only touched on `queue`.
    private var closed = false
    private let log = Logger(subsystem: "io.github.lewin671.remoteagent", category: "login")

    init(vault: Vault, server: String, deviceId: String) {
        self.vault = vault
        self.server = server
        self.deviceId = deviceId
    }

    /// The login kept by the last run, if any. Blocks: call it off the main actor.
    func load() -> SavedLogin? {
        queue.sync {
            guard !closed, let kept = try? vault.loginToken(server: server, deviceId: deviceId) else { return nil }
            return SavedLogin(token: kept.token, expiresMs: Int64(kept.expiresAt.timeIntervalSince1970 * 1000))
        }
    }

    /// Keeps a new login, or forgets the one kept when the server refused it.
    func keep(_ login: SavedLogin?) {
        queue.async { [self] in
            guard !closed else { return }
            // Never the token: only that one was kept or dropped.
            if let login {
                let kept = LoginToken(
                    token: login.token, expiresAt: Date(timeIntervalSince1970: Double(login.expiresMs) / 1000), server: server,
                    deviceId: deviceId)
                let ok = (try? vault.saveLoginToken(kept)) != nil
                log.info("login \(ok ? "kept" : "not kept: the Keychain refused", privacy: .public)")
            } else {
                try? vault.deleteLoginToken()
                log.info("login dropped")
            }
        }
    }

    /// Ends it: once this returns nothing more is written, so that erasing
    /// the account cannot be followed by a login that was on its way. Blocks.
    func close() {
        queue.sync { closed = true }
    }
}

extension Account {
    init(_ stored: VaultAccount) {
        self.init(server: stored.server, root: stored.root, deviceSeed: stored.deviceSeed, deviceId: stored.deviceId)
    }

    var stored: VaultAccount {
        VaultAccount(server: server, root: root, deviceSeed: deviceSeed, deviceId: deviceId)
    }
}
