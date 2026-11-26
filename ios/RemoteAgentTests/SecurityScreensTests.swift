#if DEBUG
import Foundation
import RACore
import RAProto
import Security
import SwiftUI
import Testing
import UIKit
import os

@testable import RemoteAgent

private func waitFor(
    _ label: String,
    timeoutSeconds: Double = 5,
    condition: @MainActor () -> Bool
) async throws {
    let deadline = ContinuousClock.now + .seconds(timeoutSeconds)
    while await !condition() {
        try Task.checkCancellation()
        if ContinuousClock.now >= deadline {
            Issue.record("Timed out after \(timeoutSeconds)s waiting for: \(label)")
            throw CancellationError()
        }
        try await Task.sleep(for: .milliseconds(15))
    }
}

private actor SecurityMockRelay {
    var states: [PBSecurityState] = []
    var lease = Data()
    var devicePubs: [String: Data] = [:]
    var approverPub = Data()
    let nowMs: Int64 = 1_791_000_000_000

    func handle(_ eff: PBRelayRpcEffect) -> PBRelayRpcResultEvent {
        var res = PBRelayRpcResultEvent()
        res.opID = eff.opID
        do {
            switch eff.method {
            case .registerDevice:
                let req = try PBRegisterDeviceRequest(serializedBytes: eff.requestBytes)
                let reg = try PBDeviceRegistration(serializedBytes: req.registration)
                let devId = deviceId(publicKey: reg.devicePublicKey)
                devicePubs[devId] = reg.devicePublicKey
                if approverPub.isEmpty { approverPub = reg.devicePublicKey }
                res.responseBytes = try PBRegisterDeviceResponse.with { $0.deviceID = devId }.serializedData()
            case .getChallenge:
                res.responseBytes = try PBGetChallengeResponse.with {
                    $0.challenge = Data(repeating: 0x33, count: 32)
                }.serializedData()
            case .login:
                let req = try PBLoginRequest(serializedBytes: eff.requestBytes)
                res.responseBytes = try PBLoginResponse.with {
                    $0.token = "tok-\(req.deviceID)"
                    $0.expiresMs = nowMs + 86_400_000
                    $0.devicePublicKey = devicePubs[req.deviceID] ?? Data()
                }.serializedData()
            case .pinSecurityDomain:
                let req = try PBPinSecurityDomainRequest(serializedBytes: eff.requestBytes)
                states = [.with { $0.state = req.genesisState }]
                res.responseBytes = try PBPinSecurityDomainResponse().serializedData()
            case .putSecurityState:
                let req = try PBPutSecurityStateRequest(serializedBytes: eff.requestBytes)
                states.append(contentsOf: req.states)
                if !req.lease.isEmpty { lease = req.lease }
                res.responseBytes = try PBPutSecurityStateResponse.with {
                    $0.headEpoch = UInt64(states.count)
                    $0.head = states.last?.state ?? Data()
                }.serializedData()
            case .getSecurityStates:
                let req = try PBGetSecurityStatesRequest(serializedBytes: eff.requestBytes)
                let start = Int(req.afterEpoch)
                let slice = start < states.count ? Array(states[start...]) : []
                res.responseBytes = try PBGetSecurityStatesResponse.with {
                    $0.states = slice
                    $0.headEpoch = UInt64(states.count)
                    $0.lease = lease
                }.serializedData()
            case .putDeviceMeta:
                res.responseBytes = try PBPutDeviceMetaResponse.with { $0.applied = true }.serializedData()
            default:
                res.connectCode = 12
                res.diagnosticMessage = "unhandled rpc \(eff.method)"
            }
        } catch {
            res.connectCode = 13
            res.diagnosticMessage = error.localizedDescription
        }
        return res
    }
}

/// A toggleable SecurityStoreV4 that can fail `clearAll` on demand to test failed exit recovery.
private final class FailingClearSecurityStore: SecurityStoreV4Proxy, @unchecked Sendable {
    let inner = MemorySecurityStoreV4()
    var securityCore: SecurityStoreV4Core { inner.securityCore }
    private let lock = NSLock()
    private var _failClearAll = false

    var failClearAll: Bool {
        get { lock.withLock { _failClearAll } }
        set { lock.withLock { _failClearAll = newValue } }
    }

    func executeStorage(_ effect: PBSecurityStorageEffect) -> PBSecurityStorageResultEvent {
        if effect.op == .clearAll && failClearAll {
            return .with {
                $0.opID = effect.opID
                $0.error = "keychain locked during clearAll"
            }
        }
        return securityCore.executeStorage(effect)
    }
}

private final class HostBox: @unchecked Sendable {
    weak var host: CoreHost?
}

@MainActor
private func makeTestSingleCoreHost(
    store: any SecurityStoreV4 = MemorySecurityStoreV4(),
    relayRpcOverride: SecurityExecutor.RelayRpcOverride? = nil,
    onClearCaches: (@Sendable () async -> String)? = nil
) async throws -> (CoreHost, CoreScreens) {
    let screens = CoreScreens()
    let box = HostBox()
    let callbacks = CoreHost.SecurityHostCallbacks(
        onUnregisterPushOnExit: { eff in
            await box.host?.pushUnregistered(opID: eff.opID)
        },
        onClearAccountCaches: { eff in
            let err = await onClearCaches?() ?? ""
            await box.host?.accountCachesCleared(opID: eff.opID, error: err)
        }
    )
    let host = try CoreHost.singleCore(
        store: store,
        http: HTTP(),
        allowPlainHttp: true,
        screens: screens,
        clock: { 1_791_000_000_000 },
        relayRpcOverride: relayRpcOverride,
        callbacks: callbacks
    )
    box.host = host
    await host.clientInit(
        platform: "ios",
        deviceDescription: "iOS 26.0 · iPhone",
        foreground: true,
        networkAvailable: true,
        networkSignature: "wifi",
        defaultServer: "https://relay.example.com"
    )
    return (host, screens)
}

@MainActor
private func createReadyAccount(host: CoreHost, screens: CoreScreens) async throws {
    try await waitFor("onboarding entry") { screens.appRoot.stage == .onboarding }
    await host.onboardingSetMode(.create)
    await host.onboardingCreateEdit(serverInput: "https://relay.example.com")
    await host.onboardingCreateSubmit(serverInput: "https://relay.example.com")
    try await waitFor("recovery code generated") { !screens.onboarding.create.recoveryCode.isEmpty }
    await host.onboardingCreateEdit(codeSavedChecked: true)
    await host.onboardingCreateSubmit(serverInput: "https://relay.example.com", confirmCodeSaved: true)
    try await waitFor("ready stage after create") { screens.appRoot.stage == .ready }
}

@MainActor
private protocol OverlayCoverInspectable {
    var inspectedPresented: Bool { get }
    var inspectedCanDismiss: Bool { get }
    func buildInspectedOverlay() -> Any
}

extension OverlayCover: OverlayCoverInspectable {
    var inspectedPresented: Bool { isPresented }
    var inspectedCanDismiss: Bool { canDismiss() }
    func buildInspectedOverlay() -> Any { overlay() }
}

/// Extracts concrete SwiftUI child values and `Btn` closures from a real view tree (including
/// `OverlayCover`'s `overlay` closure), enabling direct invocation of production button actions.
@MainActor
private enum SwiftUIViewInspector {
    struct FoundButton {
        let text: String
        let enabled: Bool
        let action: () -> Void
    }

    static func collectTypeNames(_ root: Any, maxDepth: Int = 20) -> [String] {
        var out: [String] = []
        func walk(_ value: Any, depth: Int) {
            guard depth < maxDepth else { return }
            out.append(String(describing: type(of: value)))
            for child in Mirror(reflecting: value).children {
                walk(child.value, depth: depth + 1)
            }
        }
        walk(root, depth: 0)
        return out
    }

    static func collectStrings(_ root: Any, maxDepth: Int = 20) -> [String] {
        var out: [String] = []
        func walk(_ value: Any, depth: Int) {
            guard depth < maxDepth else { return }
            if let s = value as? String {
                out.append(s)
                return
            }
            for child in Mirror(reflecting: value).children {
                walk(child.value, depth: depth + 1)
            }
        }
        walk(root, depth: 0)
        return out
    }

    static func findConfirmButtons(in modifiedView: Any) -> (isPresented: Bool, buttons: [FoundButton]) {
        var overlay: (any OverlayCoverInspectable)?
        func findOverlay(_ value: Any, depth: Int) {
            guard depth < 12, overlay == nil else { return }
            if let cov = value as? any OverlayCoverInspectable {
                overlay = cov
                return
            }
            for child in Mirror(reflecting: value).children {
                findOverlay(child.value, depth: depth + 1)
            }
        }
        findOverlay(modifiedView, depth: 0)
        guard let overlay else { return (false, []) }
        let overlayContent = overlay.buildInspectedOverlay()
        var buttons: [FoundButton] = []
        func walkButtons(_ value: Any, depth: Int) {
            guard depth < 20 else { return }
            if value is Btn {
                var text = ""
                var enabled = true
                var action: (() -> Void)?
                for child in Mirror(reflecting: value).children {
                    if child.label == "text", let t = child.value as? String { text = t }
                    if child.label == "enabled", let e = child.value as? Bool { enabled = e }
                    if child.label == "action", let a = child.value as? () -> Void { action = a }
                }
                if let action { buttons.append(FoundButton(text: text, enabled: enabled, action: action)) }
                return
            }
            for child in Mirror(reflecting: value).children {
                walkButtons(child.value, depth: depth + 1)
            }
        }
        walkButtons(overlayContent, depth: 0)
        return (overlay.inspectedPresented, buttons)
    }
}

@MainActor
@Suite(.serialized) struct SecurityScreensTests {
    @Test func pairingCodeUITextFieldDigitByDigitPreservesCursorAndSettledSeqGuard() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        let container = UIViewController()
        window.rootViewController = container
        let previousKey = scene.windows.first { $0.isKeyWindow }
        window.makeKeyAndVisible()
        defer {
            window.isHidden = true
            window.rootViewController = nil
            if let previousKey, !previousKey.isHidden { previousKey.makeKey() }
        }

        let (host, screens) = try await makeTestSingleCoreHost()
        defer { host.close() }
        try await waitFor("onboarding entry") { screens.appRoot.stage == .onboarding }
        await host.onboardingSetMode(.join)
        try await waitFor("join mode") { screens.onboarding.mode == .join }

        var pendingEdits: [(raw: String, seq: UInt64)] = []
        let onEdit: (String) -> UInt64 = { raw in
            let seq = host.nextOnboardingEditSeq()
            pendingEdits.append((raw, seq))
            return seq
        }
        let representable = PairingCodeTextFieldRepresentable(
            formattedCode: "",
            settledEditSeq: 0,
            placeholder: "code",
            onEdit: onEdit,
            onSubmit: {}
        )
        let coordinator = representable.makeCoordinator()
        let tf = UITextField(frame: CGRect(x: 0, y: 0, width: 300, height: 44))
        tf.delegate = coordinator
        tf.addTarget(coordinator, action: #selector(PairingCodeTextFieldRepresentable.Coordinator.editingChanged(_:)), for: .editingChanged)
        container.view.addSubview(tf)
        #expect(tf.becomeFirstResponder())
        #expect(tf.isFirstResponder)

        // Type 8 digits one by one into the real UITextField, verifying the settledEditSeq guard
        // blocks stale Core updates while an edit is in flight and maps the cursor after settling.
        let digits = ["1", "2", "3", "4", "5", "6", "7", "8"]
        for (idx, digit) in digits.enumerated() {
            tf.insertText(digit)
            let edit = try #require(pendingEdits.last)
            #expect(coordinator.lastDispatchedSeq == edit.seq)

            // Before Core settles `edit.seq`, a stale render update (settledEditSeq < lastDispatchedSeq)
            // MUST NOT clobber the user's in-progress text in UITextField.
            let textBeforeStale = tf.text ?? ""
            PairingCodeTextFieldRepresentable(
                formattedCode: "9999",
                settledEditSeq: edit.seq - 1,
                placeholder: "code",
                onEdit: onEdit,
                onSubmit: {}
            ).applyCoreUpdate(tf, coordinator: coordinator)
            #expect(tf.text == textBeforeStale)

            // Now dispatch through real Core and apply the settled formattedCode.
            await host.onboardingJoinEdit(codeInput: edit.raw, seq: edit.seq)
            try await waitFor("settled edit \(idx + 1)") {
                screens.settledOnboardingEditSeq >= edit.seq
            }
            PairingCodeTextFieldRepresentable(
                formattedCode: screens.onboarding.join.formattedCode,
                settledEditSeq: screens.settledOnboardingEditSeq,
                placeholder: "code",
                onEdit: onEdit,
                onSubmit: {}
            ).applyCoreUpdate(tf, coordinator: coordinator)

            let expectedText = screens.onboarding.join.formattedCode
            #expect(tf.text == expectedText)
            let sel = try #require(tf.selectedTextRange)
            let cursorOffset = tf.offset(from: tf.beginningOfDocument, to: sel.start)
            #expect(cursorOffset == expectedText.utf16.count)
        }
        #expect(tf.text == "1234 5678")
        #expect(screens.onboarding.join.canSubmit)

        // Verify mid-string insertion at digit 2 preserves digit count before the cursor across formatting.
        let pos2 = try #require(tf.position(from: tf.beginningOfDocument, offset: 2))
        tf.selectedTextRange = tf.textRange(from: pos2, to: pos2)
        tf.insertText("0")
        let midEdit = try #require(pendingEdits.last)
        await host.onboardingJoinEdit(codeInput: midEdit.raw, seq: midEdit.seq)
        try await waitFor("settled mid-edit") { screens.settledOnboardingEditSeq >= midEdit.seq }
        PairingCodeTextFieldRepresentable(
            formattedCode: screens.onboarding.join.formattedCode,
            settledEditSeq: screens.settledOnboardingEditSeq,
            placeholder: "code",
            onEdit: onEdit,
            onSubmit: {}
        ).applyCoreUpdate(tf, coordinator: coordinator)
        let midSel = try #require(tf.selectedTextRange)
        #expect(tf.offset(from: tf.beginningOfDocument, to: midSel.start) == 3)
        #expect(tf.text == "1203 4567")
    }

    @Test func coreDrivenConfirmDialogsOnlyFireConfirmAndNeverCancel() async throws {
        // 1. Render real ExitConfirmOverlay inside UIHostingController AND invoke the production
        //    confirm Btn closure built by Dialogs.swift; observe CoreHost events to verify ONLY
        //    exitConfirm(true) fires and ZERO exitConfirm(false) events are emitted.
        let h = Harness(); defer { h.cleanUp() }
        h.model.setForeground(true)
        try await h.model.waitForLoadForTest()
        let host = h.model.coreHost
        let recorded = OSAllocatedUnfairLock<[PBCoreEvent]>(initialState: [])
        await host.setDebugEventObserver { ev in recorded.withLock { $0.append(ev) } }
        defer { Task { await host.setDebugEventObserver(nil) } }

        let exitVM = PBExitConfirmView.with {
            $0.step = .awaitingConfirm
            $0.intent = .leaveAccount
            $0.warningBody = .with { $0.literal = "Sole manager warning" }
        }
        h.model.screens.exitConfirm = exitVM
        let exitModified = ExitConfirmOverlay.apply(to: Color.clear, vm: exitVM, coreHost: host)
        let exitHostVC = UIHostingController(rootView: exitModified)
        exitHostVC.view.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        exitHostVC.view.layoutIfNeeded()

        let exitDialog = SwiftUIViewInspector.findConfirmButtons(in: exitModified)
        #expect(exitDialog.isPresented)
        let exitConfirmBtn = try #require(exitDialog.buttons.first(where: { $0.text == L10n.securityExit() }))
        #expect(exitConfirmBtn.enabled)
        exitConfirmBtn.action()

        try await waitFor("exitConfirm(true) dispatched") {
            recorded.withLock { evs in
                evs.contains { if case .exitConfirm(let c)? = $0.event { return c.confirm } else { return false } }
            }
        }
        await Task.yield()
        let exitEvents = recorded.withLock { $0 }.compactMap { ev -> PBExitConfirmAction? in
            if case .exitConfirm(let c)? = ev.event { return c } else { return nil }
        }
        #expect(exitEvents.count == 1)
        #expect(exitEvents.first?.confirm == true)

        // 2. Render real HandoffScreen confirm modifier inside UIHostingController AND invoke its
        //    production confirm Btn closure; verify ONLY confirmTransfer fires and ZERO cancelTransfer.
        let handoffVM = PBHandoffViewModel.with {
            $0.role = .outgoingManager
            $0.confirmingTargetID = "successor-device-id"
            $0.confirmDialogBody = .with { $0.literal = "Confirm transfer to successor" }
        }
        recorded.withLock { $0.removeAll() }
        let handoffModified = HandoffScreen.applyTransferConfirm(to: EmptyView(), vm: handoffVM, coreHost: host)
        let handoffHostVC = UIHostingController(rootView: handoffModified)
        handoffHostVC.view.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        handoffHostVC.view.layoutIfNeeded()

        let handoffDialog = SwiftUIViewInspector.findConfirmButtons(in: handoffModified)
        #expect(handoffDialog.isPresented)
        let handoffConfirmBtn = try #require(handoffDialog.buttons.first(where: { $0.text == L10n.securityConfirmTransfer() }))
        #expect(handoffConfirmBtn.enabled)
        handoffConfirmBtn.action()

        try await waitFor("handoff confirmTransfer dispatched") {
            recorded.withLock { evs in
                evs.contains { if case .handoffAction(let a)? = $0.event { return a.kind == .confirmTransfer } else { return false } }
            }
        }
        await Task.yield()
        let handoffEvents = recorded.withLock { $0 }.compactMap { ev -> PBHandoffAction? in
            if case .handoffAction(let a)? = ev.event { return a } else { return nil }
        }
        #expect(handoffEvents.contains { $0.kind == .confirmTransfer && $0.targetDeviceID == "successor-device-id" })
        #expect(!handoffEvents.contains { $0.kind == .cancelTransfer })
    }

    @Test func failedExitKeepsOriginalAccountAndLateTokenDoesNotPersistAfterSuccess() async throws {
        let relay = SecurityMockRelay()
        let store = FailingClearSecurityStore()
        let (host, screens) = try await makeTestSingleCoreHost(
            store: store,
            relayRpcOverride: { eff in await relay.handle(eff) }
        )
        defer { host.close() }

        try await createReadyAccount(host: host, screens: screens)
        let originalDeviceId = screens.appRoot.deviceID
        #expect(!originalDeviceId.isEmpty)

        // Lock store so clearAll fails during confirmed exit.
        store.failClearAll = true
        await host.exitRequest(intent: .leaveAccount)
        try await waitFor("awaitingConfirm") { screens.exitConfirm.step == .awaitingConfirm }
        await host.exitConfirm(true)
        try await waitFor("failed exit surfaces error") {
            screens.exitConfirm.step == .failed && !renderCoreText(screens.exitConfirm.errorText).isEmpty
        }
        #expect(screens.appRoot.stage == .ready)
        #expect(screens.appRoot.deviceID == originalDeviceId)

        // Dismiss error dialog, unlock store, and confirm exit successfully.
        await host.exitConfirm(false)
        try await waitFor("closed after failed exit cancel") { screens.exitConfirm.step == .closed }
        #expect(screens.appRoot.stage == .ready)

        store.failClearAll = false
        await host.exitRequest(intent: .leaveAccount)
        try await waitFor("awaitingConfirm retry") { screens.exitConfirm.step == .awaitingConfirm }
        await host.exitConfirm(true)
        try await waitFor("reset to onboarding") { screens.appRoot.stage == .onboarding }
        #expect(screens.appRoot.deviceID.isEmpty)
        #expect(store.inner.readLoginTokenRaw() == nil)

        // A late SecurityTokenUpdatedEffect for the exited account must NEVER write a token back to storage.
        await host.processEffectBatchForTesting([
            PBCoreEffect.with {
                $0.securityTokenUpdated = .with {
                    $0.token = "late-stale-token"
                    $0.expiresMs = 1_791_086_400_000
                    $0.cleared = false
                }
            }
        ])
        #expect(store.inner.readLoginTokenRaw() == nil)
        #expect(store.inner.loadLoginToken(server: "https://relay.example.com", deviceId: originalDeviceId) == nil)
    }

    @Test func recoveryRotationRequiresSavedCheckAndClearsDraftOnReturnToOverview() async throws {
        let relay = SecurityMockRelay()
        let (host, screens) = try await makeTestSingleCoreHost(relayRpcOverride: { eff in await relay.handle(eff) })
        defer { host.close() }

        try await createReadyAccount(host: host, screens: screens)

        await host.accountSecurityOpen(subpage: .overview)
        await host.accountSecurityAction(.setSubpage, subpage: .rotateRecovery)
        try await waitFor("rotate subpage") { screens.accountSecurity.subpage == .rotateRecovery }
        await host.accountSecurityAction(.prepareRotation)
        try await waitFor("rotation code prepared") {
            !screens.accountSecurity.preparedCode.isEmpty
        }
        #expect(!screens.accountSecurity.codeSavedChecked)
        #expect(!screens.accountSecurity.canPublishRotation)

        await host.accountSecurityAction(.setRotationSaved, flag: true)
        try await waitFor("rotation submittable") {
            screens.accountSecurity.codeSavedChecked && screens.accountSecurity.canPublishRotation
        }

        // Switching back to overview clears the unpublished draft while keeping AccountSecurity open.
        await host.accountSecurityAction(.setSubpage, subpage: .overview)
        try await waitFor("rotation draft cleared") {
            screens.accountSecurity.subpage == .overview
                && screens.accountSecurity.preparedCode.isEmpty
                && !screens.accountSecurity.codeSavedChecked
        }

        // Publishing a replacement and returning to overview preserves Core's successNotice.
        await host.accountSecurityAction(.setSubpage, subpage: .rotateRecovery)
        await host.accountSecurityAction(.prepareRotation)
        try await waitFor("second rotation code prepared") { !screens.accountSecurity.preparedCode.isEmpty }
        await host.accountSecurityAction(.setRotationSaved, flag: true)
        await host.accountSecurityAction(.publishRotation)
        try await waitFor("rotation published with notice") {
            !renderCoreText(screens.accountSecurity.successNotice).isEmpty && screens.accountSecurity.preparedCode.isEmpty
        }
        await host.accountSecurityAction(.setSubpage, subpage: .overview)
        try await waitFor("back on overview") { screens.accountSecurity.subpage == .overview }
        #expect(!renderCoreText(screens.accountSecurity.successNotice).isEmpty)
        await host.accountSecurityClose()
    }

    @Test func startupRoutingRendersActualScreenForEachAppRootStage() async throws {
        let h = Harness(); defer { h.cleanUp() }
        h.model.setForeground(true)
        try await h.model.waitForLoadForTest()
        let host = h.model.coreHost

        func inspectStage(_ root: PBAppRootViewModel, context: AccountContext? = nil) -> (types: [String], strings: [String]) {
            h.model.screens.appRoot = root
            let view = RootView.stageView(for: root, context: context, coreHost: host)
            let vc = UIHostingController(rootView: RootView().environment(h.model))
            vc.view.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
            vc.view.layoutIfNeeded()
            return (
                SwiftUIViewInspector.collectTypeNames(view),
                SwiftUIViewInspector.collectStrings(view)
            )
        }

        // 1. .loading renders StartupShell (and none of the other stage views).
        let loading = inspectStage(.with { $0.stage = .loading })
        #expect(loading.types.contains("StartupShell"))
        #expect(!loading.types.contains("JoinScreen"))
        #expect(!loading.types.contains("RemovedScreen"))
        #expect(!loading.types.contains("MainView"))

        // 2. .bootstrapError renders ErrorRow with the Core error message and reset button.
        let bootErr = inspectStage(.with {
            $0.stage = .bootstrapError
            $0.bootstrapError = .with {
                $0.message = .with { $0.literal = "Keychain locked sentinel" }
                $0.canRetry = true
                $0.canReset = true
            }
        })
        #expect(bootErr.types.contains("ErrorRow"))
        #expect(bootErr.strings.contains("Keychain locked sentinel"))
        #expect(!bootErr.types.contains("StartupShell"))

        // 3. .onboarding renders JoinScreen.
        let onboarding = inspectStage(.with { $0.stage = .onboarding })
        #expect(onboarding.types.contains("JoinScreen"))
        #expect(!onboarding.types.contains("StartupShell"))

        // 4. .retainedDenied renders RetainedIdentityDeniedScreen.
        let retained = inspectStage(.with {
            $0.stage = .retainedDenied
            $0.retainedDenied = .with { $0.server = "https://relay.example.com" }
        })
        #expect(retained.types.contains("RetainedIdentityDeniedScreen"))
        #expect(!retained.types.contains("RemovedScreen"))

        // 5. .removed renders RemovedScreen and branches title & body on removalVerified.
        let removedVerified = PBRemovedDeviceView.with { $0.removalVerified = true; $0.server = "https://relay.example.com" }
        let removed = inspectStage(.with { $0.stage = .removed; $0.removed = removedVerified })
        #expect(removed.types.contains("RemovedScreen"))
        let verifiedCopy = RemovedScreen.copy(for: removedVerified)
        #expect(verifiedCopy.title == L10n.settingsThisDeviceWasRemovedFromTheAccount())
        #expect(verifiedCopy.text == L10n.settingsSignedAccountStateConfirmsThisDeviceWasRemovedSessions())

        let unverifiedCopy = RemovedScreen.copy(for: .with { $0.removalVerified = false; $0.server = "https://relay.example.com" })
        #expect(unverifiedCopy.title == L10n.settingsCannotSignInToTheRelayRightNow())
        #expect(unverifiedCopy.text == L10n.settingsTheDeviceMayHaveBeenRemovedTheServiceReset())

        // 6. .ready without context falls back to StartupShell; with context renders MainView.
        let readyPending = inspectStage(.with { $0.stage = .ready }, context: nil)
        #expect(readyPending.types.contains("StartupShell"))
        #expect(!readyPending.types.contains("MainView"))

        h.model.screens.appRoot = .with { $0.stage = .onboarding }
        try await h.activateSyntheticAccount()
        let ctx = try #require(h.model.context)
        let readyAttached = inspectStage(.with { $0.stage = .ready }, context: ctx)
        #expect(readyAttached.types.contains("MainView"))
        #expect(!readyAttached.types.contains("StartupShell"))
    }
}
#endif
