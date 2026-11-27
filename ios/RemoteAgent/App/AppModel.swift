// The app's single graph, the counterpart of Android's RaApp.kt: the
// account on this device, the sync engine made for it, and what the engine
// knows, copied to where SwiftUI can observe it.
import Foundation
import Observation
import RACore
import RAProto
import RAWebRTC
import Security
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
    let pendingStore: PendingScope
    /// What this run keeps in memory for the account: UI state and the like.
    let memory = AccountMemory()
    let core: CoreScreens
    let coreHost: CoreHost
    var dismissNewTask: (@MainActor () -> Void)?
    var dismissNewSchedule: (@MainActor () -> Void)?
    /// Set to `true` by `AppModel` when this account runtime is torn down so late SwiftUI teardown
    /// layout passes can distinguish an unloaded context from a missing-install bug on an active one.
    @ObservationIgnored private(set) var isUnloaded = false

    func markUnloaded() {
        isUnloaded = true
    }

    /// This client's device ID.
    var me: String { account.deviceId }

    init(
        account: Account,
        remote: Remote,
        preferenceDefaults: UserDefaults = .standard,
        pendingStore: PendingScope = PendingStore.ephemeral().scoped(),
        sharedFileRoot: URL = SharedFileCenter.defaultRoot,
        demoDirectories: DirectorySource? = nil,
        demoModels: [String: PBListModelsResponse] = [:],
        demoMode: Bool = false,
        core: CoreScreens? = nil,
        coreHost: CoreHost? = nil
    ) {
        self.account = account
        self.remote = remote
        self.preferenceDefaults = preferenceDefaults
        self.pendingStore = pendingStore
        let screens = core ?? CoreScreens()
        self.core = screens
        self.coreHost = coreHost ?? (try! CoreHost.singleCore(
            store: MemorySecurityStoreV4(),
            http: AppModel.http,
            allowPlainHttp: true,
            screens: screens,
            defaults: preferenceDefaults,
            sharedFileStore: SharedFileStore(root: sharedFileRoot),
            pendingStore: pendingStore,
            demoDirectories: demoDirectories,
            demoModels: demoModels,
            demoMode: demoMode
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
        autoModeSupported: Bool
    ) {
        Task {
            await coreHost.submitNewSession(
                projectKey: projectKey,
                autoModeSupported: autoModeSupported
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

@MainActor
@Observable
final class AppModel {

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
        /// Builds the account's `Remote` sharing the single `CoreHost`'s `SecurityExecutor` & `SecurityClient`.
        var makeRemote:
            @MainActor (
                _ account: Account,
                _ cache: (any CipherCache)?,
                _ securityStore: any SecurityStoreV4,
                _ securityExecutor: SecurityExecutor,
                _ securityClient: SecurityClient,
                _ allowPlainHttp: Bool
            ) throws -> Remote = { account, cache, securityStore, securityExecutor, securityClient, allowPlainHttp in
                let log = Logger(subsystem: "io.github.lewin671.remoteagent", category: "sync")
                // The engine hands over error texts only, never content or keys.
                return try Remote(
                    account: account,
                    http: AppModel.http,
                    cache: cache,
                    log: { log.info("\($0, privacy: .public)") },
                    securityStore: securityStore,
                    securityExecutor: securityExecutor,
                    securityClient: securityClient,
                    allowPlainHttp: allowPlainHttp,
                    rtc: WebRtcFactory()
                )
            }
        /// Optional test-only hook replacing the default `Remote` executor attached to `CoreHost`.
        var makeExecutor: (@MainActor (_ account: Account, _ cache: (any CipherCache)?, _ securityStore: any SecurityStoreV4) throws -> (any CoreHostExecutor, Remote))? = nil
        /// What this client says it runs on, as Android's "Android 16 · Pixel 9".
        var deviceDescription: @MainActor @Sendable () -> String = AppModel.defaultDeviceDescription
        /// Erases what is stored for the account: the keys, the cache and the pending images. Called off the main actor.
        var erase: @Sendable (AccountStorage) throws -> Void = { storage in
            try LocalAccountData.erase(
                vault: storage.vault, cacheRoot: storage.cacheRoot, pending: storage.pending)
        }
        /// Unregisters APNs remote notifications on logout so no stale push registration remains.
        var unregisterRemoteNotifications: @MainActor () -> Void = {
            UIApplication.shared.unregisterForRemoteNotifications()
        }
        /// Notifications may show the old account's approvals in full.
        var clearNotifications: @MainActor () -> Void = {
            let center = UNUserNotificationCenter.current()
            center.removeAllDeliveredNotifications()
            center.removeAllPendingNotificationRequests()
        }
    }

    /// The app's one HTTP stack; joining uses it before there is an engine.
    nonisolated static let http = HTTP()

    // MARK: What screens read

    /// Process-wide Core view models observed by SwiftUI (`AppRootViewModel`, `OnboardingViewModel`, etc.).
    let screens = CoreScreens()

    /// The account in use and its engine; nil until one is, and after logout.
    private(set) var context: AccountContext?
    /// The app is in front. Screens that tick or hold a terminal follow it:
    /// timers pause in the background and terminals are closed there.
    private(set) var foreground = false

    /// The server of the account just left, to fill in when joining again. Only in memory.
    private(set) var lastServer = ""

    /// Returns the app's lazy singleton `CoreHost`, creating it once on first foreground/UI entry.
    var coreHost: CoreHost { ensureCoreHost() }

    @discardableResult
    func ensureCoreHost() -> CoreHost {
        if let existing = _coreHost { return existing }
        let vault = parts.storage.vault
        let host = try! CoreHost.singleCore(
            store: vault,
            http: Self.http,
            allowPlainHttp: parts.allowPlainHttp,
            screens: screens,
            defaults: parts.defaults,
            callbacks: CoreHost.SecurityHostCallbacks(
                onRequestStoragePersist: nil,
                onUnregisterPushOnExit: { [weak self] eff in
                    await self?.enqueueRuntimeWork { model in
                        await model.handleUnregisterPushOnExit(eff)
                    }
                },
                onClearAccountCaches: { [weak self] eff in
                    await self?.enqueueRuntimeWork { model in
                        await model.handleClearAccountCaches(eff)
                    }
                },
                onAccountRuntimeChanged: { [weak self] eff in
                    await self?.enqueueRuntimeWork { model in
                        await model.handleAccountRuntimeChanged(eff)
                    }
                },
                onDismissUpgradeNotice: {
                    Task.detached { vault.dismissLegacyRepairNotice() }
                },
                onPersistLastServer: { [weak self] server in
                    await MainActor.run {
                        if !server.isEmpty { self?.lastServer = server }
                    }
                }
            )
        )
        _coreHost = host
        return host
    }

    @discardableResult
    func ensureForegroundCoreHost() -> CoreHost {
        let host = ensureCoreHost()
        if !clientInitDispatched {
            clientInitDispatched = true
            let desc = parts.deviceDescription()
            let fg = foreground
            let netAvail = path.available
            let netSig = hasObservedPath ? path.signature : ""
            let defServer = lastServer
            Task {
                await host.setDeviceDescription(parts.deviceDescription)
                await host.clientInit(
                    platform: "ios",
                    deviceDescription: desc,
                    inlineCrypto: true,
                    foreground: fg,
                    networkAvailable: netAvail,
                    networkSignature: netSig,
                    defaultServer: defServer,
                    requestStoragePersist: false
                )
            }
        } else if foreground && screens.appRoot.stage == .bootstrapError && !screens.appRoot.bootstrapError.canReset {
            Task { await host.appRootRetryBootstrap() }
        }
        return host
    }

    nonisolated static func hardwareModelIdentifier() -> String {
        var sysinfo = utsname()
        uname(&sysinfo)
        let raw = withUnsafePointer(to: &sysinfo.machine) {
            $0.withMemoryRebound(to: CChar.self, capacity: Int(_SYS_NAMELEN)) {
                String(cString: $0)
            }
        }.trimmingCharacters(in: .whitespacesAndNewlines)
        if raw == "arm64" || raw == "x86_64" || raw == "i386" {
            let sim = ProcessInfo.processInfo.environment["SIMULATOR_MODEL_IDENTIFIER"]?
                .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return sim
        }
        return raw
    }

    nonisolated static func deviceDescription(
        systemVersion: String,
        hardwareModel: String,
        idiom: UIUserInterfaceIdiom
    ) -> String {
        let family = idiom == .pad ? "iPad" : "iPhone"
        let trimmed = hardwareModel.trimmingCharacters(in: .whitespacesAndNewlines)
        let model = trimmed.isEmpty ? family : trimmed
        return "iOS " + systemVersion + " · " + model
    }

    static func defaultDeviceDescription() -> String {
        deviceDescription(
            systemVersion: UIDevice.current.systemVersion,
            hardwareModel: hardwareModelIdentifier(),
            idiom: UIDevice.current.userInterfaceIdiom
        )
    }

    /// Sample data is shown and nothing is stored or sent (`-demo`, debug builds).
    private(set) var isDemo = false
    let settings: SettingsStore

    var account: Account? { context?.account }
    var remote: Remote? { context?.remote }
    func reconnectNow() { _coreHost?.reconnectNow() }
    /// The server turned this device away (architecture §13 「移除设备」): only the notice is shown.
    var removed: Bool {
        screens.appRoot.stage == .removed
    }

    /// The account's cache of ciphertext, where viewed images are kept as sealed envelopes; nil while none is
    /// open (no account, sample data, or a cache that did not open).
    var imageCache: CipherStore? { cache }
    /// Where images waiting to be sent are kept, sealed.
    var pendingImages: PendingStore { parts.storage.pending }

    // MARK: Private state

    @ObservationIgnored private let parts: Parts
    @ObservationIgnored private var _coreHost: CoreHost?
    @ObservationIgnored private var clientInitDispatched = false
    @ObservationIgnored private var runtimeTail: Task<Void, Never>?
    @ObservationIgnored private var cache: CipherStore?
    /// Bumped whenever the account in use changes: a step that awaited meanwhile finds its work unwanted.
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var path = NetworkPath.unknown
    @ObservationIgnored private var hasObservedPath = false
    @ObservationIgnored private var watching = false
    @ObservationIgnored private var terminals: [UUID: TermConn] = [:]
    @ObservationIgnored private let log = Logger(
        subsystem: "io.github.lewin671.remoteagent", category: "app")

    init(settings: SettingsStore, parts: Parts = Parts()) {
        self.settings = settings
        self.parts = parts
        Task.detached(priority: .utility) {
            LibraryVideos.discardAll()
        }
    }

    private func enqueueRuntimeWork(_ work: @escaping @MainActor (AppModel) async -> Void) async {
        let prev = runtimeTail
        let next = Task { @MainActor [weak self] in
            await prev?.value
            guard let self else { return }
            await work(self)
        }
        runtimeTail = next
        await next.value
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
        guard !isDemo else {
            if !front { closeTerminals() }
            return
        }
        if front {
            let host = ensureForegroundCoreHost()
            Task { await host.setForeground(true) }
            if context != nil {
                host.start()
            }
        } else {
            closeTerminals()
            if let host = _coreHost {
                Task { await host.setForeground(false) }
                if context != nil {
                    host.stop()
                }
            }
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
        _coreHost?.networkChanged(new.signature, available: new.available)
    }

    func dismissUpgradeNotice() {
        guard !isDemo else { return }
        let host = ensureCoreHost()
        Task { await host.appRootDismissUpgradeNotice() }
    }

    func loadAccount() {
        guard !isDemo, foreground else { return }
        let host = ensureForegroundCoreHost()
        if screens.appRoot.stage == .bootstrapError {
            Task { await host.appRootRetryBootstrap() }
        }
    }

    // MARK: Single-Core Effect Handlers

    private func handleUnregisterPushOnExit(_ eff: PBUnregisterPushOnExitEffect) async {
        parts.unregisterRemoteNotifications()
        if let host = _coreHost {
            await host.pushUnregistered(opID: eff.opID)
        }
    }

    /// Detaches any active account runtime (terminals, host attachment, media centers) and returns
    /// the prior `CipherStore` for closing or erasing.
    private func unloadAccountRuntime(clearPendingMedia: Bool = false) async -> CipherStore? {
        closeTerminals()
        let leavingContext = context
        let leavingRemote = leavingContext?.remote
        let store = cache
        leavingContext?.markUnloaded()
        context = nil
        cache = nil
        if let host = _coreHost {
            if clearPendingMedia {
                await host.clearAllSharedFiles()
                await host.clearAllDrafts()
            }
            await host.detachAccount()
        }
        if let leavingRemote {
            ImageCenter.remove(leavingRemote)
            SharedFileCenter.remove(leavingRemote)
        }
        return store
    }

    private func handleClearAccountCaches(_ eff: PBClearAccountCachesEffect) async {
        if !eff.preserveServer.isEmpty { lastServer = eff.preserveServer }
        else if let leaving = account, lastServer.isEmpty { lastServer = leaving.server }
        generation += 1
        let store = await unloadAccountRuntime(clearPendingMedia: true)
        LibraryVideos.discardAll()
        parts.unregisterRemoteNotifications()
        parts.clearNotifications()
        let storage = parts.storage
        let erase = parts.erase
        let result = await Task.detached {
            Result {
                try? store?.close()
                try erase(storage)
            }
        }.value
        var errText = ""
        switch result {
        case .success:
            settings.clear()
            AccountDefaults.clear(parts.defaults)
        case .failure(let error):
            log.error("erasing the account failed: \(String(describing: type(of: error)), privacy: .public)")
            errText = error.localizedDescription
        }
        if let host = _coreHost {
            await host.accountCachesCleared(opID: eff.opID, error: errText)
        }
    }

    private func handleAccountRuntimeChanged(_ eff: PBAccountRuntimeChangedEffect) async {
        guard let host = _coreHost, !isDemo else { return }
        if !eff.active {
            if !eff.removed {
                host.sharedSecurityExecutor?.setDefaultAccount(server: "", deviceId: "")
                generation += 1
                let store = await unloadAccountRuntime()
                await Task.detached { try? store?.close() }.value
            }
            return
        }
        guard !eff.server.isEmpty && !eff.deviceID.isEmpty else { return }
        host.sharedSecurityExecutor?.setDefaultAccount(server: eff.server, deviceId: eff.deviceID)
        let acct = Account(server: eff.server, deviceId: eff.deviceID, accountId: eff.accountID)
        if let existing = context, existing.account == acct {
            return
        }
        generation += 1
        let gen = generation
        // Detach any prior account runtime cleanly before attaching the new one.
        if context != nil || cache != nil {
            let oldStore = await unloadAccountRuntime()
            await Task.detached { try? oldStore?.close() }.value
        }
        let root = parts.storage.cacheRoot
        let open = parts.openCache
        let store = await Task.detached { try? open(root, acct) }.value
        guard gen == generation else {
            try? store?.close()
            return
        }
        if store == nil { log.error("the cache does not open; going on without it") }
        let pendingScope = parts.storage.pending.scoped()
        let executor: any CoreHostExecutor
        let remote: Remote
        do {
            if let makeExec = parts.makeExecutor {
                (executor, remote) = try makeExec(acct, store, parts.storage.vault)
            } else {
                guard let secExec = host.sharedSecurityExecutor, let secClient = host.sharedSecurityClient else {
                    try? store?.close()
                    return
                }
                let built = try parts.makeRemote(acct, store, parts.storage.vault, secExec, secClient, parts.allowPlainHttp)
                executor = built
                remote = built
            }
        } catch {
            try? store?.close()
            log.error("creating account runtime failed: \(String(describing: type(of: error)), privacy: .public)")
            return
        }
        await host.attachAccount(
            executor: executor,
            account: acct,
            defaults: ScopeDefaults(parts.defaults, scope: [acct.server, acct.deviceId]),
            pendingStore: pendingScope,
            sharedFileStore: SharedFileStore(root: SharedFileCenter.defaultRoot)
        )
        guard gen == generation else {
            await host.detachAccount()
            try? store?.close()
            return
        }
        let ctx = AccountContext(
            account: acct,
            remote: remote,
            preferenceDefaults: parts.defaults,
            pendingStore: pendingScope,
            core: screens,
            coreHost: host
        )
        let imageCenter = ImageCenter.install(remote: remote, coreHost: host, pending: pendingScope)
        let sharedCenter = SharedFileCenter.install(remote: remote, coreHost: host, root: SharedFileCenter.defaultRoot)
        await host.setDeviceDescription(parts.deviceDescription)
        let cleanup: @Sendable (String) async -> Void = { [weak ctx] sid in
            guard let ctx else { return }
            await ctx.coreHost.forgetDraftSession(sid)
            await MainActor.run {
                SharedFileCenter.of(ctx).forgetSession(sid)
            }
        }
        await host.setSessionRemovedHandler(cleanup)
        await host.setPersistClientPrefsHandler { [weak self] eff in
            self?.settings.update { $0.theme = ThemeMode.from(eff.theme) }
        }
        await host.reportAppearancePref(settings.settings.theme.proto)
        await host.reportNotificationPref(supported: false)
        await host.setAttachmentHandlers(imageCenter.handlers())
        await host.configureAttachments(persistDrafts: !isDemo, persistOutbox: !isDemo, scanStorage: !isDemo)
        await host.setSharedFileHandlers(sharedCenter.handlers())
        guard gen == generation else {
            ctx.markUnloaded()
            ImageCenter.remove(remote)
            SharedFileCenter.remove(remote)
            await host.detachAccount()
            try? store?.close()
            return
        }
        cache = store
        context = ctx
        if hasObservedPath {
            host.networkChanged(path.signature, available: path.available)
        } else {
            host.setNetwork(path.available)
        }
        if foreground {
            await host.startAsync()
        }
    }

    /// Tries the login again after the server turned this device away.
    func retryLogin() {
        _coreHost?.retryLogin()
    }

    /// Unconditional Core in-place logout + local wipe (used by `-resetAccount` launch arg and post-AccountDeletion).
    func resetLocalAccountAndLogout() async {
        if isDemo {
            await logout()
            return
        }
        if let leaving = account { lastServer = leaving.server }
        generation += 1
        // Notify Core first so in-flight sync/security writers stop before local storage is erased.
        if let host = _coreHost {
            await host.externalLogout()
        }
        let store = await unloadAccountRuntime(clearPendingMedia: true)
        LibraryVideos.discardAll()
        parts.unregisterRemoteNotifications()
        parts.clearNotifications()
        let storage = parts.storage
        let erase = parts.erase
        _ = await Task.detached {
            try? store?.close()
            try? erase(storage)
        }.value
        settings.clear()
        AccountDefaults.clear(parts.defaults)
    }

    /// Forgets the account, the cache and the keys on this device, then
    /// shows the join flow with the server filled in.
    func logout(pairing: Bool = false) async {
        if isDemo {
            closeTerminals()
            let leavingContext = context
            let leavingRemote = leavingContext?.remote
            leavingContext?.markUnloaded()
            context = nil
            if let leavingHost = leavingContext?.coreHost {
                await leavingHost.clearAllSharedFiles()
                await leavingHost.clearAllDrafts()
                await leavingHost.closeAsync()
            }
            if let leavingRemote {
                ImageCenter.remove(leavingRemote)
                SharedFileCenter.remove(leavingRemote)
            }
            screens.resetAccountViews()
            screens.appRoot = PBAppRootViewModel.with { $0.stage = .onboarding }
            screens.onboarding = PBOnboardingViewModel.with { $0.mode = pairing ? .join : .entry }
            return
        }
        await resetLocalAccountAndLogout()
        if pairing, let host = _coreHost {
            await host.onboardingSetMode(.join)
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
            account: Account, remote: Remote, world: DemoWorld,
            events: [String: [PBEvent]] = [:],
            schedules: [String: Data] = [:],
            scheduleSeen: [String: [PBScheduleSeenToken]] = [:]
        ) {
            isDemo = true
            generation += 1
            let models = Dictionary(
                Demo.models.map { ("\($0.machineId)/\($0.agent.rawValue)", $0.models) },
                uniquingKeysWith: { _, rhs in rhs }
            )
            let demoSharedRoot = FileManager.default.temporaryDirectory.appending(path: "shared-files-demo-\(ObjectIdentifier(remote).hashValue)", directoryHint: .isDirectory)
            let demoPending = PendingStore.ephemeral().scoped()
            let ctx = AccountContext(
                account: account,
                remote: remote,
                pendingStore: demoPending,
                sharedFileRoot: demoSharedRoot,
                demoDirectories: Demo.directories,
                demoModels: models,
                demoMode: true,
                core: screens
            )
            let imageCenter = ImageCenter.install(remote: remote, coreHost: ctx.coreHost, pending: demoPending, live: false)
            let sharedCenter = SharedFileCenter.install(
                remote: remote,
                coreHost: ctx.coreHost,
                root: demoSharedRoot,
                allowMissingReports: SharedFileDebug.fake
            )
            context = ctx
            screens.appRoot = PBAppRootViewModel.with {
                $0.stage = world.removed ? .removed : .ready
                $0.server = account.server
                $0.deviceID = account.deviceId
                $0.accountID = account.accountId
                $0.isManager = world.authorityDeviceId == account.deviceId
                if world.removed {
                    $0.removed = .with {
                        $0.removalVerified = true
                        $0.server = account.server
                        $0.canRetryLogin = true
                    }
                }
            }
            let host = ctx.coreHost
            let initialTheme = settings.settings.theme.proto
            Task { [weak self] in
                await host.setAttachmentHandlers(imageCenter.handlers())
                await host.setSharedFileHandlers(sharedCenter.handlers())
                await host.setPersistClientPrefsHandler { eff in
                    self?.settings.update { $0.theme = ThemeMode.from(eff.theme) }
                }
                await host.configureAttachments(
                    persistDrafts: true,
                    persistOutbox: true,
                    scanStorage: false
                )
                guard self != nil else { return }
                await host.seedDemo(
                    world: world,
                    events: events,
                    schedules: schedules,
                    scheduleSeen: scheduleSeen
                )
                await host.reportAppearancePref(initialTheme)
                await host.reportNotificationPref(supported: false)
            }
        }

        func showJoinForDemo(lastServer: String) {
            isDemo = true
            self.lastServer = lastServer
            screens.appRoot = PBAppRootViewModel.with { $0.stage = .onboarding }
            screens.onboarding = PBOnboardingViewModel.with {
                $0.mode = .join
                $0.join = .with {
                    $0.step = .form
                    $0.serverInput = lastServer
                    $0.serverEditable = true
                }
            }
        }

        enum LoadTimeoutError: Error { case timedOut }

        var hasCoreHostForTest: Bool { _coreHost != nil }

        func waitForLoadForTest(timeoutMs: Int64 = 5_000) async throws {
            let deadline = ContinuousClock.now + .milliseconds(timeoutMs)
            while ContinuousClock.now < deadline {
                try Task.checkCancellation()
                let stage = screens.appRoot.stage
                if stage != .unspecified && stage != .loading {
                    if stage != .ready || context != nil {
                        return
                    }
                }
                try await Task.sleep(nanoseconds: 10_000_000)
            }
            throw LoadTimeoutError.timedOut
        }

        func closeSyncForTest() {
            generation += 1
            closeTerminals()
            let leavingContext = context
            let leavingRemote = leavingContext?.remote
            leavingContext?.markUnloaded()
            context = nil
            let host = _coreHost
            _coreHost = nil
            clientInitDispatched = false
            leavingContext?.coreHost.close()
            host?.close()
            if let leavingRemote { ImageCenter.remove(leavingRemote); SharedFileCenter.remove(leavingRemote) }
            try? cache?.close()
            cache = nil
        }
    #endif
}
