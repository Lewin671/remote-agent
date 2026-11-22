import Observation
import RACore
import RAProto
import SwiftUI

@MainActor @Observable
final class AuthoritySetupModel {
    enum Mode { case create, recover }
    let mode: Mode
    private let makeHost: @MainActor () throws -> StandaloneSecurityHost
    private var host: StandaloneSecurityHost?
    var status: PBSecurityWorkflowStatus?
    var server: String
    var setupToken = ""
    var oldCode = ""
    var code = ""
    var saved = false
    var acknowledgeMissing = false
    var recovery: PBSecurityRecoveryInfo?
    var signatureMissing = false
    var busy = false
    var error = ""

    var hasPendingDraft: Bool {
        guard let boot = status?.bootstrap else { return false }
        return mode == .create ? boot == .pendingCreate : boot == .pendingRecovery
    }
    var hasDisplay: Bool { !code.isEmpty }
    var codeSavedInStore: Bool { status?.pendingOp.codeSaved == true }
    var canContinue: Bool { codeSavedInStore || (hasDisplay ? saved : acknowledgeMissing) }

    init(
        mode: Mode,
        server: String,
        status: PBSecurityWorkflowStatus? = nil,
        makeHost: @escaping @MainActor () throws -> StandaloneSecurityHost
    ) {
        self.mode = mode
        self.server = server
        self.status = status
        self.makeHost = makeHost
        if let op = status?.pendingOp, status?.bootstrap == .pendingRecovery {
            if !op.signed {
                signatureMissing = true
            }
            if !op.recoveryID.isEmpty {
                self.recovery = .with {
                    $0.recoveryID = op.recoveryID
                    $0.readyMs = op.recoveryReadyMs
                }
            }
        }
    }

    func ensureClient() async throws -> SecurityClient {
        if let host { return host.client }
        let created = try makeHost()
        host = created
        let st = try await created.client.bootstrap()
        status = st
        if st.bootstrap == .pendingRecovery, st.hasPendingOp {
            if !st.pendingOp.signed && code.isEmpty { signatureMissing = true }
            if recovery == nil && !st.pendingOp.recoveryID.isEmpty {
                recovery = .with {
                    $0.recoveryID = st.pendingOp.recoveryID
                    $0.readyMs = st.pendingOp.recoveryReadyMs
                }
            }
        }
        return created.client
    }

    func closeHost() {
        host?.close()
        host = nil
    }

    func run(_ work: @escaping @MainActor (SecurityClient) async throws -> Void) async {
        guard !busy else { return }
        busy = true; error = ""
        defer { busy = false }
        do {
            let client = try await ensureClient()
            try await work(client)
        } catch {
            self.error = errorText(error)
        }
    }

    func prepare() async {
        await run { client in
            if self.mode == .create {
                let res = try await client.prepareCreation(server: self.server)
                self.code = res.recoveryCode
                self.saved = res.codeSaved
            } else {
                let res = try await client.prepareEmergencyRecovery(server: self.server, code: self.oldCode)
                self.code = res.recoveryCode
                self.recovery = res.recovery
                self.oldCode = ""
                self.signatureMissing = false
                self.saved = false
            }
            self.status = try await client.getStatus()
        }
    }

    func confirmBackupIfNeeded(_ client: SecurityClient) async throws {
        guard canContinue else { throw UserError(L10nMessage.securityIosSaveTheRecoveryCodeOfflineFirst()) }
        if hasDisplay && saved && !codeSavedInStore {
            if mode == .create {
                try await client.confirmCreationCodeSaved()
            } else {
                try await client.confirmRecoveryCodeSaved()
            }
            status = try await client.getStatus()
        }
    }

    func continueSigning() async {
        await run { client in
            self.recovery = try await client.continueRecoverySigning(oldCode: self.oldCode)
            self.oldCode = ""
            self.signatureMissing = false
            self.status = try await client.getStatus()
        }
    }

    func dismissUpgradeNotice() async {
        await run { client in
            self.status = try await client.dismissUpgradeNotice()
        }
    }
}

/// Available before ordinary pairing; a durable creation/recovery draft always wins.
struct AuthoritySetupScreen: View {
    @Bindable var flow: AuthoritySetupModel
    @Environment(AppModel.self) private var app
    var back: (() -> Void)?

    var body: some View {
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    LargeTitle(flow.mode == .create ? L10n.securityCreateAccount() : L10n.securityRecoverManagementWithARecoveryCode())
                    LegacyRepairBanner(show: app.legacyRepairNotice || flow.status?.legacyRepairNotice == true) {
                        app.dismissUpgradeNotice()
                        Task { await flow.dismissUpgradeNotice() }
                    }
                    GroupNote(iosAuthorityNotice)
                    if !flow.hasPendingDraft { form }
                    else { draft }
                    if !flow.error.isEmpty {
                        GroupBanner(flow.error + (!flow.hasPendingDraft ? "" : L10n.securityIosTheOriginalRequestIsRetainedContinueTheOriginalOperation()), tone: .error)
                    }
                    if flow.hasPendingDraft {
                        SetupDiscardButton {
                            flow.closeHost()
                            await app.logout()
                        }.padding(16)
                    }
                    if !flow.hasPendingDraft, let back {
                        TextAction(L10n.commonBack(), action: {
                            flow.closeHost()
                            back()
                        }).padding(.horizontal, 16)
                    }
                }.frame(maxWidth: 480).frame(maxWidth: .infinity)
            }.scrollDismissesKeyboard(.interactively)
        }
        .onDisappear { flow.closeHost() }
    }

    private var form: some View {
        VStack(alignment: .leading, spacing: 12) {
            InputField(L10n.securityServer(), text: $flow.server, placeholder: "https://", keyboard: .URL)
            if flow.mode == .create {
                InputField(L10n.securityIosSetupTokenIfRequired(), text: $flow.setupToken, placeholder: L10n.securityIosSetupToken()).privacySensitive()
            } else {
                Hint(L10n.securityIosRequestManagementTakeoverWithYourOfflineRecoveryCodeWait())
                InputField(L10n.securityIosCurrentRecoveryCode(), text: $flow.oldCode, placeholder: L10n.securityRecoveryCode(), look: .mono, lines: 2...6).privacySensitive()
            }
            Btn(flow.mode == .create ? L10n.securityIosPrepareAccount() : L10n.securityPrepareRecovery(), kind: .primary,
                enabled: !flow.busy && !flow.server.isEmpty && (flow.mode == .create || !flow.oldCode.isEmpty)) {
                Task { await flow.prepare() }
            }
        }.padding(16)
    }

    @ViewBuilder private var draft: some View {
        if flow.hasDisplay {
            GroupLabel(L10n.securityIosSaveTheNewRecoveryCode())
            RecoveryCodeFigure(code: flow.code, saved: $flow.saved).privacySensitive()
        } else if flow.codeSavedInStore {
            GroupNote(L10n.securityIosRecoveryCodeConfirmedSavedOfflineContinueTheOriginalRequest())
        } else {
            GroupBanner(L10n.securityIosThePreviouslyDisplayedRecoveryCodeCannotBeShownAgain(), tone: .warn)
            Toggle(L10n.securityIosIUnderstandThereIsNoBackupReplaceTheCode(), isOn: $flow.acknowledgeMissing)
                .font(.body).ink(DS.ink).padding(16)
        }
        if flow.mode == .create {
            InputField(L10n.securityIosSetupTokenIfRequired(), text: $flow.setupToken, placeholder: L10n.securityIosSetupToken())
                .padding(.horizontal, 16).privacySensitive()
            Btn(L10n.securityIosContinueCreatingAccount(), kind: .primary, enabled: flow.canContinue && !flow.busy) {
                Task { await create() }
            }.padding(16)
        } else {
            recoveryProgress
        }
        if flow.busy { LoadingRow() }
    }

    @ViewBuilder private var recoveryProgress: some View {
        if flow.signatureMissing && flow.status?.pendingOp.signed != true {
            InputField(L10n.securityIosCurrentRecoveryCode(), text: $flow.oldCode, placeholder: L10n.securityRecoveryCode(), look: .mono, lines: 2...6)
                .padding(.horizontal, 16).privacySensitive()
            Btn(L10n.securityIosSubmitAndVerifyOriginalRequest(), kind: .secondary, enabled: !flow.busy && !flow.oldCode.isEmpty) {
                Task { await flow.continueSigning() }
            }.padding(.horizontal, 16)
        }
        if let recovery = flow.recovery {
            GroupLabel(L10n.securityIosRecoveryRequest())
            GroupNote(L10n.securityIosCanTakeEffectAt(value: String(describing: securityTime(UInt64(max(0, recovery.readyMs))))))
            if recovery.phase == .rejected {
                GroupBanner(L10n.securityIosRecoveryRequestVetoedTheOldRecoveryCodeIsInvalid(), tone: .warn)
                Btn(L10n.securityIosVerifyVetoAndAbandonOriginalRequest(), enabled: !flow.busy) {
                    Task { await discardRejected() }
                }.padding(16)
            } else if recovery.phase == .ready || recovery.phase == .published {
                Btn(L10n.securityCompleteRecovery(), kind: .primary, enabled: flow.canContinue && !flow.busy) {
                    Task { await complete() }
                }.padding(16)
            } else {
                GroupNote(L10n.securityIosWaitingForThe48HourRecoveryWindowTheCurrent())
            }
        }
        Btn(flow.recovery == nil ? L10n.securityIosSubmitAndVerifyOriginalRequest() : L10n.securityIosRefreshRecoveryStatus(),
            kind: flow.recovery == nil ? .primary : .secondary, enabled: flow.canContinue && !flow.busy) {
                Task {
                    await flow.run { client in
                        try await flow.confirmBackupIfNeeded(client)
                        flow.recovery = try await client.resumeRecovery()
                        flow.status = try await client.getStatus()
                    }
                }
            }.padding(16)
        if !flow.error.isEmpty {
            TextAction(L10n.securityIosVerifyAndAbandon(), enabled: !flow.busy) { Task { await discardRejected() } }
                .padding(.horizontal, 16)
        }
    }

    private func create() async {
        await flow.run { client in
            try await flow.confirmBackupIfNeeded(client)
            let res: PBSecurityWorkflowResultEffect
            if flow.codeSavedInStore {
                res = try await client.continueCreation(setupToken: flow.setupToken)
            } else {
                res = try await client.adoptCreationWithoutCode()
            }
            let st = res.status
            let acct = Account(
                server: !res.accountAdopted.server.isEmpty ? res.accountAdopted.server : st.server,
                deviceId: !res.accountAdopted.deviceID.isEmpty ? res.accountAdopted.deviceID : st.deviceID,
                accountId: !res.accountAdopted.accountID.isEmpty ? res.accountAdopted.accountID : st.accountID
            )
            let rot = res.needsRecoveryRotation || res.accountAdopted.needsRecoveryRotation || st.needsRecoveryCodeRotation
            flow.code = ""
            flow.closeHost()
            await app.reloadAfterSecuritySetup(account: acct.deviceId.isEmpty ? nil : acct, rotationRequired: rot)
        }
    }

    private func complete() async {
        await flow.run { client in
            try await flow.confirmBackupIfNeeded(client)
            let res = try await client.completeRecovery()
            let st = res.status
            let acct = Account(
                server: !res.accountAdopted.server.isEmpty ? res.accountAdopted.server : st.server,
                deviceId: !res.accountAdopted.deviceID.isEmpty ? res.accountAdopted.deviceID : st.deviceID,
                accountId: !res.accountAdopted.accountID.isEmpty ? res.accountAdopted.accountID : st.accountID
            )
            let rot = res.needsRecoveryRotation || res.accountAdopted.needsRecoveryRotation || st.needsRecoveryCodeRotation
            flow.code = ""
            flow.closeHost()
            await app.reloadAfterSecuritySetup(account: acct.deviceId.isEmpty ? nil : acct, rotationRequired: rot)
        }
    }

    private func discardRejected() async {
        await flow.run { client in
            if flow.status?.pendingOp.signed == false {
                try await client.discardUnsignedRecovery()
            } else {
                try await client.discardRejectedRecovery()
            }
            flow.status = try await client.getStatus()
            flow.recovery = nil
            flow.code = ""
            flow.saved = false
            flow.signatureMissing = false
        }
    }
}
