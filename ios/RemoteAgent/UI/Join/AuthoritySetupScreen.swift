// Account creation and emergency recovery screens driven strictly by Go `clientcore`'s
// `OnboardingViewModel.create` and `OnboardingViewModel.recovery`.
import RACore
import RAProto
import SwiftUI

struct CreateAccountScreen: View {
    @Environment(AppModel.self) private var app
    let vm: PBOnboardingViewModel
    @State private var localServer = ""
    @State private var lastServerEditSeq: UInt64 = 0
    @State private var setupToken = ""
    @State private var acknowledgeMissing = false

    var body: some View {
        let create = vm.create
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    LargeTitle(L10n.securityCreateAccount())
                    LegacyRepairBanner(show: vm.showLegacyRepairBanner) {
                        Task { await app.coreHost.appRootDismissUpgradeNotice() }
                    }
                    GroupNote(iosAuthorityNotice)
                    switch create.step {
                    case .form, .unspecified, .UNRECOGNIZED:
                        createForm(create)
                    case .backupCode:
                        backupCodeStep(create)
                    case .missingCodeAck:
                        missingCodeAckStep(create)
                    }
                    if create.busy { LoadingRow() }
                    SecurityBanner(banner: create.banner)
                        .padding(.horizontal, 16).padding(.top, 8)
                    if create.showSetupReset {
                        Button(L10n.securityExitAndStartOver(), role: .destructive) {
                            Task { await app.coreHost.exitRequest(intent: .setupReset) }
                        }
                        .padding(16)
                    }
                    if vm.canBack {
                        TextAction(L10n.commonBack()) {
                            Task { await app.coreHost.onboardingSetMode(.entry) }
                        }
                        .padding(.horizontal, 16)
                    }
                }
                .frame(maxWidth: 480)
                .frame(maxWidth: .infinity)
            }
            .scrollDismissesKeyboard(.interactively)
        }
        .onChange(of: create.serverInput, initial: true) { _, next in
            if app.screens.settledOnboardingEditSeq >= lastServerEditSeq && localServer != next {
                localServer = next
            }
        }
    }

    private func createForm(_ create: PBCreateAccountView) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            if !create.hasPendingCreate_p {
                InputField(
                    L10n.securityServer(),
                    text: Binding(
                        get: { localServer },
                        set: { next in
                            let trimmed = next.trimmingCharacters(in: .whitespacesAndNewlines)
                            localServer = trimmed
                            let seq = app.coreHost.nextOnboardingEditSeq()
                            lastServerEditSeq = seq
                            Task { await app.coreHost.onboardingCreateEdit(serverInput: trimmed, seq: seq) }
                        }
                    ),
                    placeholder: "https://",
                    keyboard: .URL
                )
            } else {
                GroupNote(L10n.securityIosRecoveryCodeConfirmedSavedOfflineContinueTheOriginalRequest())
            }
            InputField(
                L10n.securityIosSetupTokenIfRequired(),
                text: $setupToken,
                placeholder: L10n.securityIosSetupToken()
            )
            .privacySensitive()
            Btn(
                create.hasPendingCreate_p ? L10n.securityIosContinueCreatingAccount() : L10n.securityIosPrepareAccount(),
                kind: .primary,
                enabled: create.canSubmit && !create.busy
            ) {
                let srv = localServer
                let tok = setupToken
                Task { await app.coreHost.onboardingCreateSubmit(serverInput: srv, setupToken: tok) }
            }
        }
        .padding(16)
    }

    private func backupCodeStep(_ create: PBCreateAccountView) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            GroupLabel(L10n.securityIosSaveTheNewRecoveryCode())
            RecoveryCodeFigure(
                code: create.recoveryCode,
                saved: Binding(
                    get: { create.codeSavedChecked },
                    set: { checked in
                        Task { await app.coreHost.onboardingCreateEdit(codeSavedChecked: checked) }
                    }
                )
            )
            .privacySensitive()
            InputField(
                L10n.securityIosSetupTokenIfRequired(),
                text: $setupToken,
                placeholder: L10n.securityIosSetupToken()
            )
            .padding(.horizontal, 16)
            .privacySensitive()
            Btn(
                L10n.securityIosContinueCreatingAccount(),
                kind: .primary,
                enabled: create.canSubmit && !create.busy
            ) {
                let srv = create.serverInput
                let tok = setupToken
                let confirmed = create.codeSavedChecked
                Task {
                    await app.coreHost.onboardingCreateSubmit(
                        serverInput: srv,
                        setupToken: tok,
                        confirmCodeSaved: confirmed
                    )
                }
            }
            .padding(16)
        }
    }

    private func missingCodeAckStep(_ create: PBCreateAccountView) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            GroupBanner(L10n.securityIosThePreviouslyDisplayedRecoveryCodeCannotBeShownAgain(), tone: .warn)
            Toggle(L10n.securityIosIUnderstandThereIsNoBackupReplaceTheCode(), isOn: $acknowledgeMissing)
                .font(.body).ink(DS.ink).padding(16)
            Btn(
                L10n.securityIosContinueCreatingAccount(),
                kind: .primary,
                enabled: acknowledgeMissing && create.canSubmit && !create.busy
            ) {
                Task { await app.coreHost.onboardingCreateSubmit() }
            }
            .padding(16)
        }
    }
}

struct EmergencyRecoveryScreen: View {
    @Environment(AppModel.self) private var app
    let vm: PBOnboardingViewModel
    @State private var localServer = ""
    @State private var lastServerEditSeq: UInt64 = 0
    @State private var oldCode = ""

    var body: some View {
        let rec = vm.recovery
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    LargeTitle(L10n.securityRecoverManagementWithARecoveryCode())
                    LegacyRepairBanner(show: vm.showLegacyRepairBanner) {
                        Task { await app.coreHost.appRootDismissUpgradeNotice() }
                    }
                    GroupNote(iosAuthorityNotice)
                    if !rec.newRecoveryCode.isEmpty {
                        GroupLabel(L10n.securityIosSaveTheNewRecoveryCode())
                        RecoveryCodeFigure(
                            code: rec.newRecoveryCode,
                            saved: Binding(
                                get: { rec.codeSavedChecked },
                                set: { checked in
                                    Task { await app.coreHost.onboardingRecoveryEdit(codeSavedChecked: checked) }
                                }
                            )
                        )
                        .privacySensitive()
                        if rec.canConfirmCodeSaved {
                            Btn(L10n.securityIosRecoveryCodeConfirmedSavedOfflineContinueTheOriginalRequest(), kind: .primary, enabled: !rec.busy) {
                                Task { await app.coreHost.onboardingRecoveryAction(.confirmCodeSaved) }
                            }
                            .padding(.horizontal, 16).padding(.bottom, 12)
                        }
                    }
                    switch rec.phase {
                    case .form, .unspecified, .UNRECOGNIZED:
                        recoveryForm(rec)
                    case .unsignedDraft:
                        unsignedDraftStep(rec)
                    case .submittedWaiting:
                        waitingStep(rec)
                    case .readyToComplete:
                        readyStep(rec)
                    case .rejected:
                        rejectedStep(rec)
                    }
                    if rec.busy { LoadingRow() }
                    SecurityBanner(banner: rec.banner)
                        .padding(.horizontal, 16).padding(.top, 8)
                    if rec.showSetupReset {
                        Button(L10n.securityExitAndStartOver(), role: .destructive) {
                            Task { await app.coreHost.exitRequest(intent: .setupReset) }
                        }
                        .padding(16)
                    }
                    if vm.canBack {
                        TextAction(L10n.commonBack()) {
                            Task { await app.coreHost.onboardingSetMode(.entry) }
                        }
                        .padding(.horizontal, 16)
                    }
                }
                .frame(maxWidth: 480)
                .frame(maxWidth: .infinity)
            }
            .scrollDismissesKeyboard(.interactively)
        }
        .onChange(of: rec.serverInput, initial: true) { _, next in
            if app.screens.settledOnboardingEditSeq >= lastServerEditSeq && localServer != next {
                localServer = next
            }
        }
    }

    private func recoveryForm(_ rec: PBEmergencyRecoveryView) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            InputField(
                L10n.securityServer(),
                text: Binding(
                    get: { localServer },
                    set: { next in
                        let trimmed = next.trimmingCharacters(in: .whitespacesAndNewlines)
                        localServer = trimmed
                        let seq = app.coreHost.nextOnboardingEditSeq()
                        lastServerEditSeq = seq
                        Task { await app.coreHost.onboardingRecoveryEdit(serverInput: trimmed, seq: seq) }
                    }
                ),
                placeholder: "https://",
                keyboard: .URL
            )
            let hint = rec.statusHintText.rendered()
            Hint(hint.isEmpty ? L10n.securityIosRequestManagementTakeoverWithYourOfflineRecoveryCodeWait() : hint)
            InputField(
                L10n.securityIosCurrentRecoveryCode(),
                text: $oldCode,
                placeholder: L10n.securityRecoveryCode(),
                look: .mono,
                lines: 2...6
            )
            .privacySensitive()
            Btn(
                L10n.securityPrepareRecovery(),
                kind: .primary,
                enabled: rec.canPrepare && !oldCode.isEmpty && !rec.busy
            ) {
                let srv = localServer
                let code = oldCode
                oldCode = ""
                Task { await app.coreHost.onboardingRecoveryAction(.prepare, serverInput: srv, oldCode: code) }
            }
        }
        .padding(16)
    }

    private func unsignedDraftStep(_ rec: PBEmergencyRecoveryView) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            let hint = rec.statusHintText.rendered()
            if !hint.isEmpty { GroupNote(hint) }
            InputField(
                L10n.securityIosCurrentRecoveryCode(),
                text: $oldCode,
                placeholder: L10n.securityRecoveryCode(),
                look: .mono,
                lines: 2...6
            )
            .padding(.horizontal, 16)
            .privacySensitive()
            Btn(
                L10n.securityIosSubmitAndVerifyOriginalRequest(),
                kind: .primary,
                enabled: rec.canContinueSigning && !oldCode.isEmpty && !rec.busy
            ) {
                let code = oldCode
                oldCode = ""
                Task { await app.coreHost.onboardingRecoveryAction(.continueSigning, oldCode: code) }
            }
            .padding(.horizontal, 16)
            if rec.canDiscard {
                TextAction(L10n.securityIosVerifyAndAbandon(), enabled: !rec.busy) {
                    Task { await app.coreHost.onboardingRecoveryAction(.discardUnsigned) }
                }
                .padding(.horizontal, 16)
            }
        }
    }

    private func waitingStep(_ rec: PBEmergencyRecoveryView) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            GroupLabel(L10n.securityIosRecoveryRequest())
            let hint = rec.statusHintText.rendered()
            if !hint.isEmpty {
                GroupNote(hint)
            } else if rec.readyMs > 0 {
                GroupNote(L10n.securityIosCanTakeEffectAt(value: String(describing: securityTime(UInt64(max(0, rec.readyMs))))))
            }
            GroupNote(L10n.securityIosWaitingForThe48HourRecoveryWindowTheCurrent())
            if rec.canSubmitOrRefresh {
                Btn(L10n.securityIosRefreshRecoveryStatus(), kind: .secondary, enabled: !rec.busy) {
                    Task { await app.coreHost.onboardingRecoveryAction(.refreshOrSubmit) }
                }
                .padding(16)
            }
        }
    }

    private func readyStep(_ rec: PBEmergencyRecoveryView) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            GroupLabel(L10n.securityIosRecoveryRequest())
            let hint = rec.statusHintText.rendered()
            if !hint.isEmpty { GroupNote(hint) }
            Btn(L10n.securityCompleteRecovery(), kind: .primary, enabled: rec.canComplete && !rec.busy) {
                Task { await app.coreHost.onboardingRecoveryAction(.complete) }
            }
            .padding(16)
            if rec.canSubmitOrRefresh {
                Btn(L10n.securityIosRefreshRecoveryStatus(), kind: .secondary, enabled: !rec.busy) {
                    Task { await app.coreHost.onboardingRecoveryAction(.refreshOrSubmit) }
                }
                .padding(.horizontal, 16)
            }
        }
    }

    private func rejectedStep(_ rec: PBEmergencyRecoveryView) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            let hint = rec.statusHintText.rendered()
            GroupBanner(hint.isEmpty ? L10n.securityIosRecoveryRequestVetoedTheOldRecoveryCodeIsInvalid() : hint, tone: .warn)
            if rec.canDiscard {
                Btn(L10n.securityIosVerifyVetoAndAbandonOriginalRequest(), enabled: !rec.busy) {
                    Task { await app.coreHost.onboardingRecoveryAction(.discardRejected) }
                }
                .padding(16)
            }
        }
    }
}
