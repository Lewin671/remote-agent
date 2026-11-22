// The app's single graph, the counterpart of Android's RaApp.kt: the
// account on this device, the sync engine made for it, and what the engine
// knows, copied to where SwiftUI can observe it.
import Foundation
import Observation
import RACore
import RAProto
import RAWebRTC
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
    let preferenceDefaults: UserDefaults
    let draftFiles: (any RACore.SealedFiles)?
    /// What this run keeps in memory for the account: drafts and the like.
    let memory = AccountMemory()
    let core: CoreScreens
    let coreHost: CoreHost
    var dismissNewTask: (@MainActor () -> Void)?
    var dismissNewSchedule: (@MainActor () -> Void)?

    /// This client's device ID.
    var me: String { account.deviceId }

    init(
        account: Account,
        remote: Remote,
        preferenceDefaults: UserDefaults = .standard,
        draftFiles: (any RACore.SealedFiles)? = nil,
        demoDirectories: DirectorySource? = nil,
        demoModels: [String: PBListModelsResponse] = [:],
        demoMode: Bool = false,
        core: CoreScreens? = nil,
        coreHost: CoreHost? = nil
    ) {
        self.account = account
        self.remote = remote
        self.preferenceDefaults = preferenceDefaults
        self.draftFiles = draftFiles
        let screens = core ?? CoreScreens()
        self.core = screens
        self.coreHost = coreHost ?? (try! CoreHost.live(
            remote: remote,
            account: account,
            defaults: preferenceDefaults,
            screens: screens,
            demoDirectories: demoDirectories,
            demoModels: demoModels,
            demoMode: demoMode,
            onNavigate: { _ in }
        ))
    }

    func dispatch(_ event: PBCoreEvent) {
        Task { await coreHost.dispatch(event) }
    }

    func dispatch(_ build: (inout PBCoreEvent) -> Void) {
        var ev = PBCoreEvent()
        build(&ev)
        dispatch(ev)
    }

    func submitNewSession(
        projectKey: String,
        prompt: String,
        attachmentCount: Int32,
        attachmentsMoved: Bool,
        autoModeSupported: Bool,
        attachments: NewSessionAttachmentsSubmission?
    ) {
        Task {
            await coreHost.submitNewSession(
                projectKey: projectKey,
                prompt: prompt,
                attachmentCount: attachmentCount,
                attachmentsMoved: attachmentsMoved,
                autoModeSupported: autoModeSupported,
                attachments: attachments
            )
        }
    }

    func selectProjectsMachine(_ machineID: String) {
        Task { await coreHost.selectProjectsMachine(machineID) }
    }

    func openUsageScreen(
        machineID: String,
        focusAgent: PBAgent = .unspecified,
        focusModel: String = ""
    ) {
        Task {
            await coreHost.openUsageScreen(
                machineID: machineID,
                focusAgent: focusAgent,
                focusModel: focusModel
            )
        }
    }

    func usageSelectRange(_ preset: PBUsageRangePreset) {
        Task { await coreHost.usageSelectRange(preset) }
    }

    func usageSelectCustomDates(startMs: Int64, endMs: Int64) {
        Task { await coreHost.usageSelectCustomDates(startMs: startMs, endMs: endMs) }
    }

    func usageSelectSort(machineID: String, sortBy: PBCoreUsageSortColumn) {
        Task { await coreHost.usageSelectSort(machineID: machineID, sortBy: sortBy) }
    }

    func usageSelectSeries(machineID: String, seriesKey: String, toggle: Bool = true) {
        Task { await coreHost.usageSelectSeries(machineID: machineID, seriesKey: seriesKey, toggle: toggle) }
    }

    func usageScrub(machineID: String, bucketIndex: Int32, clear: Bool = false) {
        Task { await coreHost.usageScrub(machineID: machineID, bucketIndex: bucketIndex, clear: clear) }
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
    var coreHost: CoreHost? = nil
    var coreScreens: CoreScreens? = nil
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
        var allowPlainHttp: Bool = false
        /// Opens the account's cache of ciphertext. Called off the main actor.
        var openCache: @Sendable (_ root: URL, _ account: Account) throws -> CipherStore = {
            root, account in
            try CipherStore.open(
                root: root,
                account: CipherStore.accountKey(server: account.server, deviceId: account.deviceId))
        }
        var makeEngine:
            @MainActor (
                _ account: Account, _ cache: (any CipherCache)?,
                _ securityStore: any SecurityStoreV4
            ) throws -> EngineParts = { account, cache, securityStore in
                let log = Logger(subsystem: "io.github.lewin671.remoteagent", category: "sync")
                // The engine hands over error texts only, never content or keys.
                let remote = try Remote(
                    account: account, http: AppModel.http, cache: cache,
                    log: { log.info("\($0, privacy: .public)") },
                    securityStore: securityStore,
                    allowPlainHttp: false,
                    rtc: WebRtcFactory())
                let screens = CoreScreens()
                let host = try CoreHost.live(
                    remote: remote, account: account, defaults: .standard,
                    screens: screens, onNavigate: { _ in })
                return EngineParts(engine: host, remote: remote, coreHost: host, coreScreens: screens)
            }
        var bootstrapSecurity: @Sendable (_ vault: Vault) async throws -> PBSecurityWorkflowStatus = { vault in
            let host = try StandaloneSecurityHost(store: vault, http: AppModel.http, allowPlainHttp: false)
            defer { host.close() }
            return try await host.client.bootstrap()
        }
        var checkExitAdvice: @Sendable (_ vault: Vault) async throws -> PBSecurityExitAdvice = { vault in
            let host = try StandaloneSecurityHost(store: vault, http: AppModel.http, allowPlainHttp: false)
            defer { host.close() }
            let st = try await host.client.bootstrap()
            if st.bootstrap == .invalid {
                throw VaultError.unreadable
            }
            return try await host.client.checkExitAdvice()
        }
        var wipeLegacyCaches: @Sendable (AccountStorage) -> Void = { storage in
            try? CipherStore.eraseAll(root: storage.cacheRoot)
        }
        /// The pause between two copies of the engine's state to the main actor: a frame.
        var frame: Sleeper = realSleep
        /// What this client says it runs on, as Android's "Android 16 · Pixel 9".
        var deviceDescription: @MainActor () -> String = {
            "iOS \(UIDevice.current.systemVersion) · \(UIDevice.current.userInterfaceIdiom == .pad ? "iPad" : "iPhone")"
        }
        /// Erases what is stored for the account: the keys, the cache and the pending images. Called off the main actor.
        var erase: @Sendable (AccountStorage) throws -> Void = { storage in
            try LocalAccountData.erase(
                vault: storage.vault, cacheRoot: storage.cacheRoot, pending: storage.pending)
        }
        /// Notifications may show the old account's approvals in full.
        var clearNotifications: @MainActor () -> Void = {
            UNUserNotificationCenter.current().removeAllDeliveredNotifications()
        }
    }

    /// The app's one HTTP stack; joining uses it before there is an engine.
    nonisolated static let http = HTTP()

    // MARK: What screens read

    private(set) var stage = Stage.loading
    /// The account in use and its engine; nil until one is, and after logout.
    private(set) var context: AccountContext?
    /// The engine's lists and connection as last copied here. A fresh, empty state while no account is in use.
    private(set) var state = RemoteState()
    /// The app is in front. Screens that tick or hold a terminal follow it:
    /// timers pause in the background and terminals are closed there.
    private(set) var foreground = false
    var recoveryRotationRequired = false
    var authorityHandoffComplete = false
    private(set) var securityStatus: PBSecurityWorkflowStatus?
    private(set) var legacyRepairNotice = false

    /// The server of the account just left, to fill in when joining again. Only in memory.
    private(set) var lastServer = ""
    private(set) var retainedIdentityDenied = false
    /// Recreates JoinScreen's local flow after an account is discarded.
    private(set) var joinGeneration = 0
    private(set) var joinDirectly = false

    func makeStandaloneSecurityHost() throws -> StandaloneSecurityHost {
        try StandaloneSecurityHost(store: parts.storage.vault, http: Self.http, allowPlainHttp: parts.allowPlainHttp)
    }

    var joiningCalls: JoinCalls {
        .live(http: Self.http, existing: nil, vault: parts.storage.vault, allowPlainHttp: parts.allowPlainHttp)
    }
    func rePairingCalls(context: AccountContext) -> JoinCalls {
        .live(client: context.remote.securityClient, existing: context.account)
    }

    /// Sample data is shown and nothing is stored or sent (`-demo`, debug builds).
    private(set) var isDemo = false
    let settings: SettingsStore

    var account: Account? { context?.account }
    var remote: Remote? { context?.remote }
    func reconnectNow() { engine?.reconnectNow() }
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
    @ObservationIgnored private var follower: Task<Void, Never>?
    @ObservationIgnored private var loading = false
    @ObservationIgnored private var loadId: UUID?
    @ObservationIgnored private var loadTask: Task<Void, Never>?
    /// A logout is erasing what is stored. Nothing is read meanwhile: the account it is removing would come back.
    @ObservationIgnored private var erasing = false
    /// The last erase failed: the next read erases again first.
    @ObservationIgnored private var unerased = false
    /// Bumped whenever the account in use changes: a step that awaited meanwhile finds its work unwanted.
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var path = NetworkPath.unknown
    @ObservationIgnored private var hasObservedPath = false
    @ObservationIgnored private var watching = false
    @ObservationIgnored private var terminals: [UUID: TermConn] = [:]
    /// The revision of this device's metadata a description was last sent for on this connection.
    @ObservationIgnored private var described: String?
    @ObservationIgnored private let log = Logger(
        subsystem: "io.github.lewin671.remoteagent", category: "app")

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
            if let host = engine as? CoreHost {
                Task { await host.setForeground(true) }
            }
            engine?.start()
        } else {
            closeTerminals()
            if let host = engine as? CoreHost {
                Task { await host.setForeground(false) }
            }
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
        guard !hasObservedPath || path != new else { return }
        path = new
        hasObservedPath = true
        // Remote records the first observation and retires every later change.
        engine?.networkChanged(new.signature, available: new.available)
    }

    func dismissUpgradeNotice() {
        legacyRepairNotice = false
        securityStatus?.legacyRepairNotice = false
        let vault = parts.storage.vault
        Task.detached {
            vault.dismissLegacyRepairNotice()
        }
    }

    func reloadAfterSecuritySetup(account: Account? = nil, rotationRequired: Bool = false) async {
        recoveryRotationRequired = rotationRequired
        securityStatus = nil
        legacyRepairNotice = false
        if let account {
            do {
                try await joined(account)
            } catch {
                stage = .failed(errorText(error))
            }
        } else {
            loadAccount()
        }
    }

    // MARK: The account

    /// Reads the stored account via Go `clientcore` bootstrap, only in the foreground: a process started
    /// in the background while the device is locked could not open the Keychain.
    func loadAccount() {
        guard context == nil, !loading, !erasing, !isDemo, foreground else {
            return
        }
        loading = true
        let id = UUID()
        loadId = id
        let storage = parts.storage
        let erase = unerased ? parts.erase : nil
        let bootstrap = parts.bootstrapSecurity
        let wipeCaches = parts.wipeLegacyCaches
        let gen = generation
        loadTask = Task {
            defer {
                if self.loadId == id {
                    self.loading = false
                    self.loadTask = nil
                    self.loadId = nil
                }
            }
            let prepResult: Result<Bool, Error> = await Task.detached {
                Result {
                    try erase?(storage)
                    return try storage.vault.detectAndClearLegacy {
                        wipeCaches(storage)
                    }
                }
            }.value
            guard gen == generation, !erasing, context == nil else { return }
            switch prepResult {
            case .failure(let error):
                log.error(
                    "stored account preparation failed: \(String(describing: type(of: error)), privacy: .public)"
                )
                stage = .failed(error.localizedDescription)
                return
            case .success:
                unerased = false
            }

            let legacyWipedNow = (try? prepResult.get()) == true
            let bootResult: Result<PBSecurityWorkflowStatus, Error>
            do {
                let status = try await bootstrap(storage.vault)
                bootResult = .success(status)
            } catch {
                bootResult = .failure(error)
            }
            guard gen == generation, !erasing, context == nil else { return }
            switch bootResult {
            case .failure(let error):
                log.error(
                    "stored account does not open: \(String(describing: type(of: error)), privacy: .public)"
                )
                stage = .failed(error.localizedDescription)
            case .success(let status):
                securityStatus = status
                legacyRepairNotice = status.legacyRepairNotice || legacyWipedNow
                switch status.bootstrap {
                case .ready, .removed:
                    legacyRepairNotice = false
                    recoveryRotationRequired = status.needsRecoveryCodeRotation
                    let acct = Account(
                        server: status.server,
                        deviceId: status.deviceID,
                        accountId: status.accountID
                    )
                    await use(acct)
                case .pendingCreate, .pendingRecovery:
                    if !status.server.isEmpty { lastServer = status.server }
                    stage = .join
                case .pendingRegistration:
                    if !status.server.isEmpty { lastServer = status.server }
                    if !status.error.isEmpty {
                        stage = .failed(L10n.errorsIosTheOriginalRequestAndKeysAreRetainedRetryIf(
                            value: translateSecurityKey(status.error)))
                    } else {
                        stage = .join
                    }
                case .invalid, .clockSkewed:
                    let msg = status.error.isEmpty
                        ? VaultError.unreadable.localizedDescription
                        : translateSecurityKey(status.error)
                    stage = .failed(msg)
                case .empty, .unspecified, .UNRECOGNIZED:
                    retainedIdentityDenied = false
                    stage = .join
                }
            }
        }
    }

    private func use(_ account: Account) async {
        retainedIdentityDenied = false
        joinDirectly = false
        legacyRepairNotice = false
        securityStatus = nil
        generation += 1
        let gen = generation
        let root = parts.storage.cacheRoot
        let open = parts.openCache
        let store = await Task.detached { try? open(root, account) }.value
        guard gen == generation else {
            try? store?.close()
            return
        }
        if store == nil { log.error("the cache does not open; going on without it") }
        let made: EngineParts
        do {
            made = try parts.makeEngine(account, store, parts.storage.vault)
        } catch {
            try? store?.close()
            stage = .failed(errorText(error))
            return
        }
        await adopt(account: account, made: made, store: store)
        if foreground { made.engine.start() }
    }

    private func adopt(account: Account, made: EngineParts, store: CipherStore?) async {
        cache = store
        engine = made.engine
        described = nil
        state = made.remote.state.value
        let ctx = AccountContext(
            account: account,
            remote: made.remote,
            preferenceDefaults: parts.defaults,
            draftFiles: parts.storage.pending.scoped(),
            core: made.coreScreens,
            coreHost: made.coreHost
        )
        context = ctx
        let cleanup: @Sendable (String) async -> Void = { [weak ctx] sid in
            await MainActor.run {
                guard let ctx else { return }
                ctx.sessionStore.forgetSession(sid)
                ImageCenter.of(ctx.remote).forgetSession(sid)
                SharedFileCenter.of(ctx.remote).forgetSession(sid)
            }
        }
        await made.remote.onSessionRemoved(cleanup)
        await ctx.coreHost.setSessionRemovedHandler(cleanup)
        await ctx.coreHost.setPersistClientPrefsHandler { [weak self] eff in
            self?.settings.update { $0.theme = ThemeMode.from(eff.theme) }
        }
        await ctx.coreHost.reportAppearancePref(settings.settings.theme.proto)
        await ctx.coreHost.reportNotificationPref(supported: false)
        // The account's own view of the store: what is still writing when the account is erased writes nothing.
        ImageCenter.install(remote: made.remote, files: parts.storage.pending.scoped())
        // What the agent shared is kept in the cache, per session, and counts in 附件存储.
        SharedFileCenter.install(remote: made.remote, root: SharedFileCenter.defaultRoot)
        stage = .ready
        if hasObservedPath {
            made.engine.networkChanged(path.signature, available: path.available)
        } else {
            // The monitor has not supplied a real baseline yet.
            made.engine.setNetwork(path.available)
        }
        follow(made.engine.state, gen: generation)
    }

    /// Copies the engine's snapshots to the main actor, the latest only.
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
                if snapshot.link != state.link || snapshot.machines.count != state.machines.count
                    || snapshot.removed != state.removed
                {
                    // IDs and counts only (scope §4).
                    log.info(
                        """
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

    /// Says what this client is, sealed, so other clients can tell it apart.
    private func describeSelf(_ s: RemoteState) {
        guard s.conn == .online else {
            // A try made on the connection that is gone may not have arrived: the next one tries again.
            described = nil
            return
        }
        guard !isDemo, let engine, let me = s.devices[engine.account.deviceId] else { return }
        let description = parts.deviceDescription()
        guard me.meta?.platform != "ios" || me.meta?.description_p != description else { return }
        // One try per revision and connection: a failure waits for the next change instead of spinning.
        let key = "\(me.metaRev):\(s.leaseEndMs)"
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

    /// Starts syncing with a newly joined or re-paired account (`SecurityClient` already persisted v4 state).
    func joined(_ account: Account) async throws {
        // A real account from here on, whatever was being shown.
        isDemo = false
        if context != nil {
            // Re-pairing retains identity and history but replaces the active X key.
            generation += 1
            follower?.cancel(); follower = nil
            closeTerminals(); engine?.close(); engine = nil
            if let remote { ImageCenter.remove(remote); SharedFileCenter.remove(remote) }
            context?.prefsChanges.close(); context = nil
            let oldCache = cache; cache = nil
            try? oldCache?.close()
            stage = .loading
        }
        await use(account)
    }

    /// Tries the login again after the server turned this device away.
    func retryLogin() {
        engine?.retryLogin()
    }

    /// The confirmation captures security facts, independently of its displayed language.
    enum RejoinAdvice: Equatable {
        case sensitive(authority: Bool, unresolved: Bool)
        case unreadable
        var text: String {
            switch self {
            case .sensitive(let authority, let unresolved):
                AuthorityAccess.exitAdvice(authority: authority, unresolved: unresolved) + L10n.errorsIosThisDeletesTheAccountKeysAndCacheOnThis()
            case .unreadable: L10n.errorsIosCouldNotReadThisPhoneSAccountStateManagement()
            }
        }
    }

    /// Confirmation is needed only for management keys or unfinished work.
    func rejoinAdvice() async -> RejoinAdvice? {
        do {
            let adv: PBSecurityExitAdvice
            if let remote = context?.remote {
                adv = try await remote.checkExitAdvice()
            } else {
                adv = try await parts.checkExitAdvice(parts.storage.vault)
            }
            if adv.authority || adv.unresolved {
                return .sensitive(authority: adv.authority, unresolved: adv.unresolved)
            }
            return nil
        } catch {
            // An unreadable vault is not proof that there is no management key
            // or unfinished work. Keep an explicit, warned way to erase it.
            return .unreadable
        }
    }

    /// Forgets the account, the cache and the keys on this device, then
    /// shows the join flow with the server filled in.
    func logout(pairing: Bool = false) async {
        if let leaving = account { lastServer = leaving.server }
        else if let st = securityStatus, !st.server.isEmpty { lastServer = st.server }
        retainedIdentityDenied = false
        joinDirectly = pairing
        joinGeneration += 1
        recoveryRotationRequired = false
        authorityHandoffComplete = false
        securityStatus = nil
        legacyRepairNotice = false
        generation += 1
        loadTask?.cancel()
        loadTask = nil
        loadId = nil
        loading = false
        follower?.cancel()
        follower = nil
        closeTerminals()
        engine?.close()
        engine = nil
        // Drafts, thumbnails and viewed images held in memory go with the account, and the videos copied for its drafts.
        if let remote { ImageCenter.remove(remote); SharedFileCenter.remove(remote) }
        LibraryVideos.discardAll()
        context?.prefsChanges.close()
        if let host = context?.coreHost {
            Task { await host.closeAsync() }
        }
        context = nil
        state = RemoteState()
        described = nil
        let store = cache
        cache = nil
        if isDemo {
            // Sample data: nothing was stored, and the real account is not this one's to erase.
            stage = .join
            return
        }
        stage = .loading
        erasing = true
        parts.clearNotifications()
        let storage = parts.storage
        let erase = parts.erase
        let result = await Task.detached {
            Result {
                // SQLite must let go of its files before they are removed.
                try? store?.close()
                try erase(storage)
            }
        }.value
        erasing = false
        if case .failure = result { unerased = true } else { unerased = false }
        settings.clear()
        AccountDefaults.clear(parts.defaults)
        switch result {
        case .success:
            stage = .join
        case .failure(let error):
            log.error(
                "erasing the account failed: \(String(describing: type(of: error)), privacy: .public)"
            )
            stage = .failed(error.localizedDescription)
        }
    }

    // MARK: Terminals

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
        func showDemo(
            account: Account, remote: Remote, initial: RemoteState,
            events: [String: [PBEvent]] = [:],
            schedules: [String: Data] = [:],
            scheduleSeen: [String: [PBScheduleSeenToken]] = [:],
            load: @escaping @Sendable () async -> Void
        ) {
            isDemo = true
            generation += 1
            let gen = generation
            state = initial
            let models = Dictionary(
                Demo.models.map { ("\($0.machineId)/\($0.agent.rawValue)", $0.models) },
                uniquingKeysWith: { _, rhs in rhs }
            )
            context = AccountContext(
                account: account,
                remote: remote,
                demoDirectories: Demo.directories,
                demoModels: models,
                demoMode: true
            )
            ImageCenter.install(remote: remote, files: VolatileFiles(), live: false)
            stage = .ready
            let host = context?.coreHost
            let initialTheme = settings.settings.theme.proto
            Task { [weak self] in
                await host?.setPersistClientPrefsHandler { eff in
                    self?.settings.update { $0.theme = ThemeMode.from(eff.theme) }
                }
                guard let self else { return }
                await load()
                if gen == self.generation { self.follow(remote.state, gen: gen) }
                await host?.seedDemo(
                    state: remote.current,
                    events: events,
                    schedules: schedules,
                    scheduleSeen: scheduleSeen
                )
                await host?.reportAppearancePref(initialTheme)
                await host?.reportNotificationPref(supported: false)
            }
        }

        func showJoinForDemo(lastServer: String) {
            isDemo = true
            self.lastServer = lastServer
            stage = .join
        }

        func waitForLoadForTest() async {
            await loadTask?.value
        }

        func closeSyncForTest() {
            generation += 1
            loadTask?.cancel()
            loadTask = nil
            loadId = nil
            loading = false
            follower?.cancel()
            follower = nil
            closeTerminals()
            engine?.close()
            engine = nil
            if let remote { ImageCenter.remove(remote); SharedFileCenter.remove(remote) }
            context?.prefsChanges.close()
            context?.coreHost.close()
            context = nil
            try? cache?.close()
            cache = nil
        }
    #endif
}
