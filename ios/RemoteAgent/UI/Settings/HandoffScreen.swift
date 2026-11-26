// Management handoff screen, driven strictly by Go `clientcore`'s `HandoffViewModel` (Screen 54).
// Branches on `PBHandoffRole` enum; confirm dialog ONLY dispatches `confirmTransfer` (never cancel first).
import RACore
import RAProto
import SwiftUI

struct HandoffScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var publicCode = ""

    var body: some View {
        let vm = context.core.handoff
        let role: PBHandoffRole = app.isDemo
            ? (context.core.settings.security.isManager ? .outgoingManager : (context.core.settings.hasAccount_p && !context.core.settings.security.unlinked ? .incomingCandidate : .unavailable))
            : vm.role
        let addHint = context.core.settings.addDeviceHint.rendered()
            .or(L10n.securityAddOnAuthority(name: context.core.settings.security.authorityName))
        let base = SettingsPage {
            switch role {
            case .outgoingManager:
                outgoing(vm)
            case .incomingCandidate:
                incoming(vm)
            case .unavailable, .unspecified, .UNRECOGNIZED:
                GroupNote(addHint)
            }
            let message = vm.messageBanner.rendered()
            if !message.isEmpty { GroupBanner(message) }
            SecurityBanner(banner: vm.errorBanner)
                .padding(.horizontal, 16).padding(.top, 8)
        }
        .navBar(role == .outgoingManager ? L10n.securityTransferManagement() : L10n.securityTakeOverManagement())
        .task {
            await context.coreHost.openSettings()
            guard !app.isDemo else { return }
            await context.coreHost.handoffAction(.open)
        }
        Self.applyTransferConfirm(to: base, vm: vm, coreHost: context.coreHost)
    }

    static func applyTransferConfirm<V: View>(to content: V, vm: PBHandoffViewModel, coreHost: CoreHost) -> some View {
        content.confirm(
            isPresented: !vm.confirmingTargetID.isEmpty,
            title: L10n.securityIosTransferManagement(),
            text: vm.confirmDialogBody.rendered(),
            confirm: L10n.securityConfirmTransfer(),
            danger: true,
            busy: vm.busy,
            onCancel: {
                Task { await coreHost.handoffAction(.cancelTransfer) }
            },
            onConfirm: {
                let targetID = vm.confirmingTargetID
                Task { await coreHost.handoffAction(.confirmTransfer, targetDeviceID: targetID) }
            }
        )
    }

    private func outgoing(_ vm: PBHandoffViewModel) -> some View {
        Group {
            GroupLabel(L10n.securityTransferManagement())
            GroupNote(L10n.securityOnceTheSuccessorDeviceJoinsAndConfirmsSavingIts())
            GroupCard {
                ForEach(vm.candidates, id: \.deviceID) { candidate in
                    let label = candidate.displayName.rendered().or(L10n.securityClient())
                    GRow(
                        L10n.securityIosTransferTo(value: String(describing: label)),
                        chevron: true,
                        enabled: vm.canTransfer && !vm.busy
                    ) {
                        Task {
                            await context.coreHost.handoffAction(
                                .requestTransfer,
                                targetDeviceID: candidate.deviceID
                            )
                        }
                    }
                }
                GRow(L10n.securityRefreshSuccessorDevices(), action: true, enabled: vm.canRefresh && !vm.busy) {
                    guard !app.isDemo else { return }
                    Task { await context.coreHost.handoffAction(.refreshCandidates) }
                }
            }
            if vm.candidates.isEmpty {
                GroupNote(L10n.securityWaitingForTheSuccessorDeviceToGenerateAndSave())
            }
        }
    }

    private func incoming(_ vm: PBHandoffViewModel) -> some View {
        Group {
            GroupLabel(L10n.securityTakeOverManagement())
            GroupNote(L10n.securitySaveTheNewRecoveryCodeFirstThenAskThe())
            GroupNote(iosAuthorityNotice)
            #if DEBUG
            if app.isDemo { AuthorityDemoCode(handoff: true) }
            #endif
            if !app.isDemo {
                if !vm.preparedCode.isEmpty {
                    RecoveryCodeFigure(
                        code: vm.preparedCode,
                        saved: Binding(
                            get: { vm.codeSavedChecked },
                            set: { checked in
                                Task { await context.coreHost.handoffAction(.setCodeSaved, flag: checked) }
                            }
                        )
                    )
                    Btn(
                        L10n.securitySavedNotifyManagingClient(),
                        kind: .primary,
                        enabled: vm.canPublishOffer && !vm.busy
                    ) {
                        Task { await context.coreHost.handoffAction(.publishOffer) }
                    }
                    .padding(.horizontal, 16)
                } else if vm.hasPublishedOffer_p {
                    GroupBanner(L10n.securityRecoveryCodeSavedAndConfirmedWaitingForTheCurrent())
                } else {
                    Btn(
                        L10n.securityGenerateNewRecoveryCode(),
                        kind: .primary,
                        enabled: !vm.busy && vm.canPrepareCode
                    ) {
                        Task { await context.coreHost.handoffAction(.prepareCode) }
                    }
                    .padding(16)
                }
                if vm.needsPublicProfileRestore {
                    GroupLabel(L10n.securityIosRestoreAccountPublicKeyInformation())
                    GroupNote(L10n.securityIosIfSuccessorPreparationReportsAMissingAccountPublicKey())
                    GroupCard {
                        GRow(L10n.securityIosPairAgainAndPreserveIdentity(), chevron: true) { nav.open(.rePair) }
                    }
                    InputField(
                        L10n.securityIosCurrentRecoveryCode(),
                        text: $publicCode,
                        placeholder: L10n.securityRecoveryCode(),
                        look: .mono,
                        lines: 2...6
                    )
                    .padding(.horizontal, 16).privacySensitive()
                    TextAction(L10n.securityIosRestoreInformation(), enabled: !publicCode.isEmpty && !vm.busy) {
                        let code = publicCode
                        publicCode = ""
                        Task {
                            await context.coreHost.handoffAction(.restorePublicProfile, recoveryCode: code)
                        }
                    }
                    .padding(.horizontal, 16)
                }
            }
        }
    }
}
