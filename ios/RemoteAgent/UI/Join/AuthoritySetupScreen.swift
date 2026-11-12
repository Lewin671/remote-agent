import Observation
import RACore
import RAProto
import SwiftUI

@MainActor @Observable
final class AuthoritySetupModel {
    enum Mode { case create, recover }
    let mode: Mode
    let bootstrap: AuthorityBootstrap
    var status: AuthorityBootstrapStatus?
    var server: String
    var setupToken = ""
    var oldCode = ""
    var code = ""
    var saved = false
    var acknowledgeMissing = false
    var recovery: RecoveryContext?
    var busy = false
    var error = ""
    var hasDisplay: Bool { !code.isEmpty }
    var canContinue: Bool { status?.codeSaved == true || (hasDisplay ? saved : acknowledgeMissing) }
    init(mode: Mode, bootstrap: AuthorityBootstrap, server: String, status: AuthorityBootstrapStatus? = nil) {
        self.mode = mode; self.bootstrap = bootstrap; self.server = server; self.status = status
    }
    func run(_ work: @escaping @MainActor () async throws -> Void) async {
        guard !busy else { return }
        busy = true; error = ""
        defer { busy = false }
        do { try await work() } catch { self.error = errorText(error) }
    }
    func prepare() async {
        await run {
            if self.mode == .create { self.code = try await self.bootstrap.prepareCreation(server: self.server) }
            else {
                self.code = try await self.bootstrap.prepareEmergencyRecovery(server: self.server, code: self.oldCode)
                self.oldCode = ""
            }
            self.saved = false; self.status = try await self.bootstrap.status()
        }
    }
    func confirmBackupIfNeeded() async throws {
        guard canContinue else { throw UserError(L10nMessage.securityIosSaveTheRecoveryCodeOfflineFirst()) }
        if hasDisplay && saved && status?.codeSaved != true {
            try await bootstrap.confirmRecoveryCodeSaved()
            status = try await bootstrap.status()
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
                    GroupNote(iosAuthorityNotice)
                    if flow.status == nil { form }
                    else { draft }
                    if !flow.error.isEmpty {
                        GroupBanner(flow.error + (flow.status == nil ? "" : L10n.securityIosTheOriginalRequestIsRetainedContinueTheOriginalOperation()), tone: .error)
                    }
                    if flow.status != nil { SetupDiscardButton { await app.logout() }.padding(16) }
                    if flow.status == nil, let back { TextAction(L10n.commonBack(), action: back).padding(.horizontal, 16) }
                }.frame(maxWidth: 480).frame(maxWidth: .infinity)
            }.scrollDismissesKeyboard(.interactively)
        }
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
        } else if flow.status?.codeSaved == true {
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
        if let recovery = flow.recovery {
            GroupLabel(L10n.securityIosRecoveryRequest())
            GroupNote(L10n.securityIosCanTakeEffectAt(value: String(describing: securityTime(recovery.readyMs))))
            if recovery.phase == .rejected {
                GroupBanner(L10n.securityIosRecoveryRequestVetoedTheOldRecoveryCodeIsInvalid(), tone: .warn)
                Btn(L10n.securityIosVerifyVetoAndAbandonOriginalRequest(), enabled: !flow.busy) {
                    Task { await discardRejected() }
                }.padding(16)
            } else if recovery.phase == .ready || recovery.phase == .published {
                Btn(L10n.securityCompleteRecovery(), kind: .primary, enabled: flow.canContinue && !flow.busy) {
                    Task { await complete() }
                }.padding(16)
            } else { GroupNote(L10n.securityIosWaitingForThe48HourRecoveryWindowTheCurrent()) }
        }
        Btn(flow.recovery == nil ? L10n.securityIosSubmitAndVerifyOriginalRequest() : L10n.securityIosRefreshRecoveryStatus(),
            kind: flow.recovery == nil ? .primary : .secondary, enabled: flow.canContinue && !flow.busy) {
                Task {
                    await flow.run {
                        try await flow.confirmBackupIfNeeded()
                        flow.recovery = try await flow.bootstrap.resumeRecovery()
                        flow.status = try await flow.bootstrap.status()
                    }
                }
            }.padding(16)
        if !flow.error.isEmpty {
            TextAction(L10n.securityIosVerifyAndAbandon(), enabled: !flow.busy) { Task { await discardRejected() } }
                .padding(.horizontal, 16)
        }
    }
    private func create() async {
        await flow.run {
            try await flow.confirmBackupIfNeeded()
            _ = try await flow.bootstrap.continueCreation(setupToken: flow.setupToken)
            let missing = try await flow.bootstrap.status()?.codeSaved != true
            if missing { try await flow.bootstrap.finishCreationWithoutCodeBackup(acknowledgeRotationRequired: flow.acknowledgeMissing) }
            else { try await flow.bootstrap.finishCreation() }
            flow.code = ""
            app.reloadAfterAuthorityBootstrap(rotationRequired: missing)
        }
    }
    private func complete() async {
        await flow.run {
            try await flow.confirmBackupIfNeeded()
            _ = try await flow.bootstrap.completeRecovery()
            let missing = try await flow.bootstrap.status()?.codeSaved != true
            try await flow.bootstrap.finishRecovery(acknowledgeMissingCodeBackup: flow.acknowledgeMissing)
            flow.code = ""
            app.reloadAfterAuthorityBootstrap(rotationRequired: missing)
        }
    }
    private func discardRejected() async {
        await flow.run {
            try await flow.bootstrap.discardRejectedRecovery(confirmedAfterDefinitiveRejection: true)
            flow.status = nil; flow.recovery = nil; flow.code = ""; flow.saved = false
        }
    }
}
