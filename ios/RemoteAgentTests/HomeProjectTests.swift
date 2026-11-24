#if DEBUG
import Foundation
import os
@testable import RACore
import RAProto
import SwiftProtobuf
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

private actor FakeCoreExecutor: CoreHostExecutor {
    nonisolated let account: Account
    nonisolated let machineID: String

    private var hooks: Remote.CoreHooks?
    var appliedSyncStates: [PBCoreSyncStateView] = []
    var describedCalls: [String] = []
    var describeFailure: (any Error)?
    var openedLegs: [UInt64] = []
    var closedLegs: [UInt64] = []
    var activeLeg: UInt64?
    var streamCallbacks: [UInt64: Remote.StreamCallbacks] = [:]
    var fetchListsCount = 0
    var savedPrefsRequests: [PBCorePutAccountPrefsRpc] = []
    var createdRequests: [PBCreateSessionRequest] = []
    var autoReadyOnOpen = true
    var dispatchCommandHandler: (@Sendable (PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent)?

    func setDispatchCommandHandler(_ handler: (@Sendable (PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent)?) {
        dispatchCommandHandler = handler
    }

    var machines: [PBCorePlainMachine]
    var sessions: [PBCorePlainSession]
    var prefs = PBProjectPrefs.with { $0.schema = 1 }
    var prefsRev: UInt64 = 1
    var nowMs: Int64 = 1_791_000_000_000

    init() {
        let clientId = "1111111111111111"
        let machineId = "2222222222222222"
        let acctId = String(repeating: "0", count: 31) + "1"
        self.machineID = machineId
        self.account = Account(
            server: "http://127.0.0.1:1",
            deviceId: clientId,
            accountId: acctId
        )
        self.machines = [
            .with {
                $0.machineID = machineId
                $0.online = true
                $0.lastSeenMs = 1_791_000_000_000
                $0.meta = .with {
                    $0.hostname = "MacBook"
                    $0.protocolVersion = peerProtocolVersion
                    $0.agents = [.with { $0.agent = .codex }]
                }
            }
        ]
        self.sessions = [
            .with {
                $0.sessionID = "s1"
                $0.machineID = machineId
                $0.updatedMs = 1_790_999_000_000
                $0.meta = .with {
                    $0.id = "s1"
                    $0.cwd = "/Users/me/Code/foo"
                    $0.title = "Inspect API"
                    $0.agent = .codex
                    $0.status = .idle
                    $0.createdAt = .with { $0.seconds = 1_790_990_000 }
                }
            },
            .with {
                $0.sessionID = "s2"
                $0.machineID = machineId
                $0.updatedMs = 1_790_998_000_000
                $0.meta = .with {
                    $0.id = "s2"
                    $0.cwd = "/Users/me/Code/bar"
                    $0.title = "Fix docs"
                    $0.agent = .codex
                    $0.status = .exited
                    $0.endedAt = .with { $0.seconds = 1_790_998_000 }
                }
            }
        ]
    }

    func setSessions(_ next: [PBCorePlainSession]) {
        sessions = next
    }

    func appendSession(_ session: PBCorePlainSession) {
        sessions.append(session)
    }

    func installCoreHooks(_ hooks: Remote.CoreHooks) async {
        self.hooks = hooks
    }

    var latestSyncState: PBCoreSyncStateView { appliedSyncStates.last ?? PBCoreSyncStateView() }

    func applyCoreSyncState(_ snapshot: PBCoreSyncStateView) async {
        appliedSyncStates.append(snapshot)
    }

    func setDescribeFailure(_ err: (any Error)?) {
        describeFailure = err
    }

    func clearDescribedCalls() {
        describedCalls.removeAll()
    }

    func setNetworkGeneration(_ gen: UInt64) async {}

    func ensureToken(opId: UInt64, forceLogin: Bool, timeoutMs: Int64) async -> PBCoreTokenResultEvent {
        PBCoreTokenResultEvent.with {
            $0.opID = opId
            $0.token = "tok-\(opId)"
            $0.expiresMs = nowMs + 86_400_000
        }
    }

    func discardToken(_ rejected: String) async {}

    func openStream(legId: UInt64, token: String, callbacks: Remote.StreamCallbacks) async {
        openedLegs.append(legId)
        activeLeg = legId
        streamCallbacks[legId] = callbacks
        if autoReadyOnOpen {
            await callbacks.onReady(legId, nowMs)
        }
    }

    func closeStream(legId: UInt64) async {
        closedLegs.append(legId)
        if activeLeg == legId {
            activeLeg = nil
        }
    }

    func dropActiveStream(errorCode: String = "unavailable", reason: String = "connection lost") async {
        guard let legId = activeLeg, let cb = streamCallbacks[legId] else { return }
        activeLeg = nil
        await cb.onClosed(PBCoreStreamClosedEvent.with {
            $0.legID = legId
            $0.errorCode = errorCode
            $0.error = reason
        })
    }

    func executeCacheRead(_ effect: PBCoreCacheReadEffect) async -> PBCoreEvent {
        var event = PBCoreEvent()
        switch effect.kind {
        case .bootstrap(let req):
            event.cacheBootstrapLoaded = .with { $0.opID = req.opID }
        case .events(let req):
            event.cacheEventsLoaded = .with {
                $0.opID = req.opID
                $0.sessionID = req.sessionID
            }
        case nil:
            break
        }
        return event
    }

    func executeCacheWrite(_ effect: PBCoreCacheWriteEffect) async {}

    func executeCacheDelete(_ effect: PBCoreCacheDeleteEffect, onSessionRemoved: (@Sendable (String) async -> Void)?) async {}

    func executeRpc(_ effect: PBCoreRpcEffect) async -> PBCoreEvent {
        var event = PBCoreEvent()
        switch effect.kind {
        case .fetchLists:
            fetchListsCount += 1
            event.listsFetched = .with {
                $0.opID = effect.opID
                $0.machines = machines
                $0.sessions = sessions
            }
        case .fetchSessionsPage:
            event.sessionsPageFetched = .with {
                $0.opID = effect.opID
                $0.sessions = sessions
            }
        case .getAccountPrefs:
            event.accountPrefsFetched = .with {
                $0.opID = effect.opID
                $0.prefs = .with {
                    $0.revision = prefsRev
                    $0.prefs = prefs
                }
            }
        case .putAccountPrefs(let req):
            savedPrefsRequests.append(req)
            prefs = req.nextPrefs
            prefsRev += 1
            event.accountPrefsPutResult = .with {
                $0.opID = effect.opID
                $0.applied = true
                $0.current = .with {
                    $0.revision = prefsRev
                    $0.prefs = prefs
                }
            }
        case .fetchDevices:
            event.devicesFetched = .with { $0.opID = effect.opID }
        default:
            break
        }
        return event
    }

    func dispatchCommandAttempt(_ effect: PBCoreDispatchCommandEffect) async -> PBCoreCommandOutcomeEvent {
        if let handler = dispatchCommandHandler {
            return await handler(effect)
        }
        var outcome = PBCoreCommandOutcomeEvent()
        outcome.requestID = effect.requestID
        outcome.attemptID = effect.attemptID
        outcome.source = .daemon
        outcome.attemptBound = true
        outcome.signed = true
        outcome.hashesMatch = true
        outcome.outerMatchesInner = true
        outcome.outerOutcome = "executed"
        outcome.outerReason = "ok"

        if let cmd = try? PBCommand(serializedBytes: effect.commandBytes) {
            switch cmd.kind {
            case .listModels:
                outcome.result = .with {
                    $0.outcome = "executed"
                    $0.reason = "ok"
                    $0.resultAvailable = true
                    $0.listModels = .with {
                        $0.defaultModel = "gpt-5"
                        $0.models = [
                            .with {
                                $0.slug = "gpt-5"
                                $0.displayName = "GPT-5"
                                $0.efforts = ["low", "medium", "high"]
                                $0.defaultEffort = "medium"
                            }
                        ]
                    }
                }
            case .getSettings:
                outcome.result = .with {
                    $0.outcome = "executed"
                    $0.reason = "ok"
                    $0.resultAvailable = true
                    $0.getSettings = .with { $0.settings = PBDaemonSettings() }
                }
            case .listDirectories(let req):
                outcome.result = .with {
                    $0.outcome = "executed"
                    $0.reason = "ok"
                    $0.resultAvailable = true
                    $0.listDirectories = .with {
                        $0.path = req.path.isEmpty ? "/Users/me" : req.path
                        $0.dirs = ["Code"]
                    }
                }
            case .createSession(let req):
                createdRequests.append(req)
                outcome.result = .with {
                    $0.outcome = "executed"
                    $0.reason = "ok"
                    $0.resultAvailable = true
                    $0.createSession = .with {
                        $0.session.id = "sess-created"
                    }
                }
            default:
                outcome.result = .with {
                    $0.outcome = "executed"
                    $0.reason = "ok"
                    $0.resultAvailable = true
                }
            }
        }
        return outcome
    }

    func setWatchedSessions(_ sessionIds: [String]) async {}
    func describeSelf(platform: String, description: String) async throws {
        describedCalls.append("\(platform) \(description)")
        if let describeFailure { throw describeFailure }
    }
    func renameDevice(_ id: String, name: String) async throws {}
    func close() async {}
}

private actor NavRecorder {
    var navigated: [PBCoreNavigateEffect] = []
    func record(_ eff: PBCoreNavigateEffect) { navigated.append(eff) }
}

@MainActor
@Suite struct HomeProjectTests {
    @Test func coreHostDrivesRealCoreAndPublishesObservableScreens() async throws {
        let screens = CoreScreens()
        let executor = FakeCoreExecutor()
        let navRecorder = NavRecorder()
        let client = try CoreClient()
        let suiteName = "corehost-test-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)
        let key = scopeHmacKey(localKey: Demo.account.localKey)

        let host = CoreHost(
            client: client,
            executor: executor,
            screens: screens,
            defaults: ScopeDefaults(defaults),
            scopeHmacKey: key,
            draftStorageQueue: DraftStorageQueue(),
            clock: { 1_791_000_000_000 },
            sleep: { _ in try? await Task.sleep(for: .milliseconds(5)) },
            onNavigate: { eff in Task { await navRecorder.record(eff) } }
        )
        await host.wireExecutorHooks()
        await host.startAsync()
        await host.showHome()

        await waitUntil { screens.home.canStart && screens.home.running.count == 1 && screens.home.scopeMenu.count >= 2 }

        await host.dispatch(PBCoreEvent.with { $0.homeSetScope.scopeKey = "/Users/me/Code/foo" })
        let expectedToken = encodeScopeToken(scopeKey: "/Users/me/Code/foo", hmacKey: key)
        await waitUntil { defaults.string(forKey: AccountDefaults.key("homeScopeToken")) == expectedToken }
        #expect(screens.home.scopeKey == "/Users/me/Code/foo")

        await host.setManageActive(true)
        await host.dispatch(PBCoreEvent.with { $0.projectTogglePin.projectKey = "/Users/me/Code/foo" })
        await waitUntil {
            await executor.savedPrefsRequests.contains(where: { req in
                req.nextPrefs.pinned.contains(where: { $0.path == "/Users/me/Code/foo" })
            })
        }

        await host.showNewSession(projectKey: "/Users/me/Code/foo")
        await waitUntil { !screens.newSession.modelPanel.models.isEmpty }
        #expect(screens.newSession.modelPanel.models.first?.slug == "gpt-5")

        await host.setDraftText(scope: .newSession("/Users/me/Code/foo"), text: "Run checks")
        await waitUntil {
            screens.newSession.draft.text == "Run checks"
                && screens.newSession.draft.canSend
                && !screens.newSession.draft.restoring
        }
        await host.submitNewSession(
            projectKey: "/Users/me/Code/foo",
            autoModeSupported: false
        )
        await waitUntil { await executor.createdRequests.contains { $0.prompt == "Run checks" && $0.cwd == "/Users/me/Code/foo" } }
        await waitUntil { await navRecorder.navigated.contains { $0.route == .session && $0.sessionID == "sess-created" } }
        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    @Test func coreHostSmokeLifecycleConnectListsDisconnectAndReconnectCatchUp() async throws {
        let screens = CoreScreens()
        let executor = FakeCoreExecutor()
        let client = try CoreClient()
        let suiteName = "corehost-smoke-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let host = CoreHost(
            client: client,
            executor: executor,
            screens: screens,
            defaults: ScopeDefaults(defaults),
            scopeHmacKey: scopeHmacKey(localKey: Demo.account.localKey),
            clock: { 1_791_000_000_000 },
            sleep: { _ in try? await Task.sleep(for: .milliseconds(5)) }
        )
        await host.wireExecutorHooks()

        // 1. Start -> online -> lists -> home rows
        await host.startAsync()
        await host.showHome()
        await waitUntil {
            let conn = await executor.latestSyncState.connection
            let fetches = await executor.fetchListsCount
            return conn == .connected
                && screens.syncState.connection == .connected
                && fetches >= 1
                && screens.home.running.contains { $0.sessionID == "s1" }
        }

        // 2. Disconnect (streamClosed) -> state reflects offline/reconnecting
        await executor.dropActiveStream(errorCode: "unavailable", reason: "server restarting")
        await waitUntil {
            let conn = await executor.latestSyncState.connection
            return conn != .connected && screens.syncState.connection != .connected
        }

        // 3. Reconnect -> catch-up fetchLists delivers newly added session on reconnect
        let initialFetches = await executor.fetchListsCount
        await executor.appendSession(.with {
            $0.sessionID = "s3"
            $0.machineID = executor.machineID
            $0.updatedMs = 1_791_000_001_000
            $0.meta = .with {
                $0.id = "s3"
                $0.cwd = "/Users/me/Code/foo"
                $0.title = "Catch-up task"
                $0.agent = .claude
                $0.status = .idle
                $0.createdAt = .with { $0.seconds = 1_791_000_000 }
            }
        })
        await waitUntil {
            let conn = await executor.latestSyncState.connection
            let legs = await executor.openedLegs.count
            let fetches = await executor.fetchListsCount
            return conn == .connected
                && legs >= 2
                && fetches > initialFetches
                && screens.home.running.contains { $0.sessionID == "s3" }
        }

        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    @Test func coreHostDescribesSelfOnConnectReconnectAndLeaseRenewal() async throws {
        let screens = CoreScreens()
        let executor = FakeCoreExecutor()
        let client = try CoreClient()
        let suiteName = "corehost-describe-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let host = CoreHost(
            client: client,
            executor: executor,
            screens: screens,
            defaults: ScopeDefaults(defaults),
            scopeHmacKey: scopeHmacKey(localKey: Demo.account.localKey),
            clock: { 1_791_000_000_000 },
            sleep: { _ in try? await Task.sleep(for: .milliseconds(5)) }
        )
        await host.setDeviceDescription { "iOS 26.0 · iPhone" }

        let meID = executor.account.deviceId
        var sync = PBCoreSyncStateView.with {
            $0.connection = .offline
            $0.leaseEndMs = 1
            $0.devices = [.with { $0.deviceID = meID; $0.kind = .client; $0.trusted = true }]
        }
        // Offline: does not send.
        await host.maybeDescribeSelf(sync)
        #expect(await executor.describedCalls.isEmpty)

        // Online with failure: sends once, does not spin on unchanged key.
        await executor.setDescribeFailure(URLError(.networkConnectionLost))
        sync.connection = .connected
        await host.maybeDescribeSelf(sync)
        await waitUntil { await executor.describedCalls.count == 1 }
        await host.maybeDescribeSelf(sync)
        #expect(await executor.describedCalls.count == 1)

        // Disconnect + reconnect retries.
        sync.connection = .offline
        await host.maybeDescribeSelf(sync)
        await executor.setDescribeFailure(SecurityFailure.lease)
        sync.connection = .connected
        await host.maybeDescribeSelf(sync)
        await waitUntil { await executor.describedCalls.count == 2 }

        // Lease renewal retries without disconnect or metaRev change.
        await executor.setDescribeFailure(nil)
        sync.leaseEndMs = 2
        await host.maybeDescribeSelf(sync)
        await waitUntil { await executor.describedCalls.count == 3 }

        // Once server reflects matching metadata, no further calls.
        await executor.clearDescribedCalls()
        sync.devices[0].metaRev = 1
        sync.devices[0].meta = .with {
            $0.platform = "ios"
            $0.description_p = "iOS 26.0 · iPhone"
        }
        await host.maybeDescribeSelf(sync)
        #expect(await executor.describedCalls.isEmpty)

        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    @Test func cancelDispatchEffectCancelsInFlightAttemptByRequestId() async throws {
        let executor = FakeCoreExecutor()
        let screens = CoreScreens()
        let client = try CoreClient()
        let suiteName = "corehost-cancel-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)

        let started = OSAllocatedUnfairLock(initialState: false)
        let cancelled = OSAllocatedUnfairLock(initialState: false)
        await executor.setDispatchCommandHandler { eff in
            started.withLock { $0 = true }
            await withTaskCancellationHandler {
                try? await Task.sleep(for: .seconds(60))
            } onCancel: {
                cancelled.withLock { $0 = true }
            }
            return PBCoreCommandOutcomeEvent.with {
                $0.requestID = eff.requestID
                $0.attemptID = eff.attemptID
                $0.source = .transport
                $0.notForwarded = true
                $0.transportCode = "canceled"
            }
        }

        let host = CoreHost(
            client: client,
            executor: executor,
            screens: screens,
            defaults: ScopeDefaults(defaults),
            scopeHmacKey: scopeHmacKey(localKey: Demo.account.localKey),
            clock: { 1_791_000_000_000 },
            sleep: { _ in try? await Task.sleep(for: .milliseconds(5)) }
        )
        await host.wireExecutorHooks()
        await host.startAsync()
        await waitUntil { screens.syncState.connection == .connected }

        await host.dispatch(PBCoreEvent.with {
            $0.runMachineCommand = .with {
                $0.opID = 77
                $0.machineID = executor.machineID
                $0.command = .with { $0.getSettings = PBGetSettingsRequest() }
                $0.skipConnectivityPreflight = true
            }
        })
        await waitUntil { started.withLock { $0 } }

        await host.dispatch(PBCoreEvent.with {
            $0.cancelMachineCommand = .with { $0.opID = 77 }
        })
        await waitUntil { cancelled.withLock { $0 } }
        #expect(cancelled.withLock { $0 })

        await host.closeAsync()
        defaults.removePersistentDomain(forName: suiteName)
    }

    private func waitUntil(_ cond: @escaping () async -> Bool) async {
        for _ in 0..<100 {
            if await cond() { return }
            try? await Task.sleep(for: .milliseconds(20))
        }
        Issue.record("condition not met before timeout")
    }

    @Test func goldenViewModelsRenderHomeProjectManageAndNewSessionCleanly() {
        let context = AccountContext(account: Demo.account, remote: Demo.remote())
        let settings = SettingsStore(defaults: UserDefaults(suiteName: "home-golden-" + UUID().uuidString)!)
        let model = AppModel(settings: settings)
        let nav = Navigator()

        let home = ScreenGoldens.home("home_timeline", index: 0)
        #expect(!home.attention.isEmpty || home.topAttentionActionable)
        #expect(!home.running.isEmpty)
        #expect(!home.endedGroups.isEmpty)
        #expect(!home.headline.title.rendered().isEmpty)

        var allAtt = false
        var allRun = false
        var filter = ""
        render(
            HomeContent(
                home: home,
                allAttention: Binding(get: { allAtt }, set: { allAtt = $0 }),
                allRunning: Binding(get: { allRun }, set: { allRun = $0 }),
                filterText: Binding(get: { filter }, set: { filter = $0 })
            )
            .environment(context)
            .environment(model)
            .environment(nav)
        )

        let emptyHome = ScreenGoldens.home("home_empty_errors", index: 0)
        render(
            HomeContent(
                home: emptyHome,
                allAttention: .constant(false),
                allRunning: .constant(false),
                filterText: .constant("")
            )
            .environment(context)
            .environment(model)
            .environment(nav)
        )

        let manage = ScreenGoldens.manageProjects("project_manage_detail")
        #expect(!manage.listing.pinned.isEmpty || !manage.listing.others.isEmpty)
        render(
            ManageProjectsContent(manage: manage)
                .environment(context)
                .environment(model)
                .environment(nav)
        )

        let detail = ScreenGoldens.projectDetail("project_manage_detail")
        #expect(!detail.key.isEmpty)
        let ns = ScreenGoldens.newSession("new_session_lifecycle")
        #expect(!ns.projectKey.isEmpty)
        var showEnded = false
        var folded = false
        var text = ns.prompt
        render(
            ProjectDetailContent(
                detail: detail,
                newSession: ns,
                showEnded: Binding(get: { showEnded }, set: { showEnded = $0 }),
                scrolledAway: Binding(get: { folded }, set: { folded = $0 })
            ) {
                NewSessionContent(vm: ns, text: Binding(get: { text }, set: { text = $0 }))
            }
            .environment(context)
            .environment(model)
            .environment(nav)
        )

        render(
            NewTaskModelPanel(
                vm: ns,
                settled: ns.settled,
                blocked: nil,
                onRetryModels: {},
                onSelectModel: { _, _, _ in },
                onClose: {}
            )
            .environment(context)
            .environment(model)
            .environment(nav)
        )
    }

    @Test func concurrentDirPickersStayIsolatedByPickerKey() async throws {
        let settings = SettingsStore(defaults: UserDefaults(suiteName: "ra-dirpicker-\(UUID())")!)
        let model = AppModel(settings: settings)
        let remote = Demo.remote()
        let initial = Demo.world(.everyday)
        model.setForeground(true)
        model.showDemo(account: Demo.account, remote: remote, world: initial)
        defer { model.closeSyncForTest() }
        let host = try #require(model.context?.coreHost)
        let core = try #require(model.context?.core)
        for _ in 0..<200 where core.home.headline.title.rendered().isEmpty {
            try? await Task.sleep(for: .milliseconds(20))
        }
        let t1 = await host.acquireDirPicker(key: "manage")
        let t2 = await host.acquireDirPicker(key: "schedule-form")
        await host.dispatch(PBCoreEvent.with {
            $0.projectOpenDirPicker = .with { $0.machineID = "mac"; $0.path = "/Users/me/Code"; $0.pickerKey = "manage" }
        })
        await host.dispatch(PBCoreEvent.with {
            $0.projectOpenDirPicker = .with { $0.machineID = "mac"; $0.path = "/Users/me/Docs"; $0.pickerKey = "schedule-form" }
        })
        for _ in 0..<200 where (core.dirPickers["manage"]?.currentPath ?? "").isEmpty || (core.dirPickers["schedule-form"]?.currentPath ?? "").isEmpty {
            try? await Task.sleep(for: .milliseconds(20))
        }
        #expect(core.dirPickers["manage"]?.pickerKey == "manage")
        #expect(core.dirPickers["schedule-form"]?.pickerKey == "schedule-form")
        #expect(core.dirPickers["manage"]?.currentPath != core.dirPickers["schedule-form"]?.currentPath)
        await host.dispatch(PBCoreEvent.with { $0.projectCloseDirPicker = .with { $0.pickerKey = "manage" } })
        await host.releaseDirPicker(t1)
        #expect(core.dirPickers["manage"] == nil)
        #expect(core.dirPickers["schedule-form"]?.currentPath == "/Users/me/Docs")
        await host.dispatch(PBCoreEvent.with { $0.projectCloseDirPicker = .with { $0.pickerKey = "schedule-form" } })
        await host.releaseDirPicker(t2)
    }

    private func render<V: View>(_ view: V) {
        let host = UIHostingController(rootView: view)
        let size = host.sizeThatFits(in: CGSize(width: 390, height: 1200))
        #expect(size.width <= 390.5)
        host.view.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
    }
}
#endif
