// What the app's store needs of the sync engine and of the system, each
// behind a small protocol so that the store's rules (when to connect, when
// to stop, what a lost network means) are tested without a server, a radio
// or a Keychain prompt.
import Foundation
import Network
import RACore

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

extension Account {
    init(_ stored: VaultAccount) {
        self.init(server: stored.server, root: stored.root, deviceSeed: stored.deviceSeed, deviceId: stored.deviceId)
    }

    var stored: VaultAccount {
        VaultAccount(server: server, root: root, deviceSeed: deviceSeed, deviceId: deviceId)
    }
}
