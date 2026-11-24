import Observation
import RACore
import RAProto
import SwiftUI

@MainActor @Observable
final class HandoffDraft {
    var code: String?
    var saved = false
    var offered = false
    var canPublish: Bool { code != nil && saved }
}

struct HandoffScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var draft = HandoffDraft()
    @State private var action = ActionRunner()
    @State private var target: PBHandoffCandidateView?
    @State private var confirming = false
    @State private var publicCode = ""
    @State private var completedProfile = false
    @State private var handedOff = false

    static func candidateLabel(_ candidate: PBHandoffCandidateView) -> String {
        if candidate.hasDisplayNameText {
            let rendered = candidate.displayNameText.rendered()
            if !rendered.isEmpty { return rendered }
        }
        return candidate.displayName.or(L10n.securityClient())
    }

    var body: some View {
        let settings = context.core.settings
        let sec = settings.security
        let candidates = sec.handoffCandidates
        let isClient = settings.hasAccount_p && !sec.unlinked
        let addHint = settings.addDeviceHint.rendered().or(L10n.securityAddOnAuthority(name: sec.authorityName))
        SettingsPage {
            if sec.isManager { outgoing(candidates: candidates) }
            else if isClient { incoming(hasHandoffOffer: sec.hasHandoffOffer_p) }
            else { GroupNote(addHint) }
            if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
        }
        .navBar(sec.isManager ? L10n.securityTransferManagement() : L10n.securityTakeOverManagement())
        .task {
            await context.coreHost.openSettings()
            draft = context.memory.value { HandoffDraft() }
            if !app.isDemo && context.core.settings.security.isManager {
                refreshCandidates()
            }
        }
        .confirm(isPresented: $confirming, title: L10n.securityIosTransferManagement(),
            text: L10n.securityConfirmThatTheSuccessorDeviceSavedTheNewRecovery(
                value: String(describing: target.map(Self.candidateLabel) ?? L10n.securityIosSuccessorDevice())),
            confirm: L10n.securityConfirmTransfer(), danger: true) {
                action.run {
                    guard let target else { return }
                    try await context.remote.handoffAuthority(targetDeviceId: target.deviceID)
                    let st = try await context.remote.getSecurityStatus()
                    guard !st.hasPendingOp else {
                        throw UserError(L10nMessage.securityIosTransferResultUnverifiedContinueTheOriginalOperation())
                    }
                    handedOff = true
                    app.authorityHandoffComplete = true
                }
            }
    }
    private func outgoing(candidates: [PBHandoffCandidateView]) -> some View {
        Group {
            GroupLabel(L10n.securityTransferManagement())
            GroupNote(L10n.securityOnceTheSuccessorDeviceJoinsAndConfirmsSavingIts())
            GroupCard {
                ForEach(candidates, id: \.deviceID) { candidate in
                    let label = Self.candidateLabel(candidate)
                    GRow(L10n.securityIosTransferTo(value: String(describing: label)),
                        chevron: true, enabled: !action.running) { target = candidate; confirming = true }
                }
                GRow(L10n.securityRefreshSuccessorDevices(), action: true, enabled: !action.running) { refreshCandidates() }
            }
            if candidates.isEmpty { GroupNote(L10n.securityWaitingForTheSuccessorDeviceToGenerateAndSave()) }
            if handedOff { GroupBanner(L10n.securityIosManagementTransferredThisDeviceWasRemoved()) }
        }
    }
    private func incoming(hasHandoffOffer: Bool) -> some View {
        Group {
            GroupLabel(L10n.securityTakeOverManagement())
            GroupNote(L10n.securitySaveTheNewRecoveryCodeFirstThenAskThe())
            GroupNote(iosAuthorityNotice)
            #if DEBUG
            if app.isDemo { AuthorityDemoCode(handoff: true) }
            #endif
            if let code = draft.code {
                RecoveryCodeFigure(code: code, saved: $draft.saved)
                Btn(L10n.securitySavedNotifyManagingClient(), kind: .primary, enabled: draft.canPublish && !action.running) {
                    action.run {
                        guard draft.saved else { throw UserError(L10nMessage.securityIosSaveTheRecoveryCodeOfflineFirst()) }
                        try await context.remote.publishHandoffOffer()
                        let st = try await context.remote.getSecurityStatus()
                        guard !st.hasPendingOp else {
                            throw UserError(L10nMessage.securityIosSuccessorPreparationResultUnverifiedContinueTheOriginalOperation())
                        }
                        draft.offered = true; draft.code = nil; draft.saved = false
                        await context.remote.refreshDevices()
                    }
                }.padding(.horizontal, 16)
            } else if !app.isDemo {
                if draft.offered || hasHandoffOffer {
                    GroupBanner(L10n.securityRecoveryCodeSavedAndConfirmedWaitingForTheCurrent())
                }
                Btn(draft.offered || hasHandoffOffer ? L10n.securityRegenerateRecoveryCode() : L10n.securityGenerateNewRecoveryCode(), kind: .primary, enabled: !action.running) {
                    action.run {
                        draft.code = try await context.remote.prepareHandoffCode()
                        draft.saved = false
                    }
                }.padding(16)
            }
            GroupLabel(L10n.securityIosRestoreAccountPublicKeyInformation())
            GroupNote(L10n.securityIosIfSuccessorPreparationReportsAMissingAccountPublicKey())
            GroupCard { GRow(L10n.securityIosPairAgainAndPreserveIdentity(), chevron: true) { nav.open(.rePair) } }
            InputField(L10n.securityIosCurrentRecoveryCode(), text: $publicCode, placeholder: L10n.securityRecoveryCode(), look: .mono, lines: 2...6)
                .padding(.horizontal, 16).privacySensitive()
            TextAction(L10n.securityIosRestoreInformation(), enabled: !publicCode.isEmpty && !action.running) {
                action.run {
                    try await context.remote.completeAccountPublicProfile(server: context.account.server, code: publicCode)
                    publicCode = ""; completedProfile = true
                }
            }.padding(.horizontal, 16)
            if completedProfile { GroupBanner(L10n.securityIosAccountPublicKeyInformationRestoredYouCanPrepareTo()) }
        }
    }
    private func refreshCandidates() {
        guard !app.isDemo else { return }
        action.run {
            await context.remote.refreshDevices()
        }
    }
}
