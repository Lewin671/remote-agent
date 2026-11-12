import Observation
import RACore
import RAProto
import SwiftUI

var iosAuthorityNotice: String { L10n.securityIosTheIosAppHasNoBackgroundSecurityAlertsOpen() }

struct RecoveryPendingEntry: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    var body: some View {
        if AuthorityAccess(state: app.state, me: context.me).isAuthority {
            if app.state.recoveryPending {
                GroupBanner(L10n.securitySomeoneIsRequestingManagementTakeoverWithARecoveryCode(value: String(describing: securityTime(app.state.recoveryReadyMs))),
                    tone: .warn, action: L10n.dockHandle()) { nav.open(.accountSecurity) }
            } else if app.state.recoveryStatusStale {
                GroupBanner(L10n.errorsIosRecoveryRequestStateMayBeOutOfDateConnect(), tone: .warn)
            }
        }
    }
}

struct AccountSecurityGroup: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    var body: some View {
        let access = AuthorityAccess(state: app.state, me: context.me)
        GroupLabel(L10n.securityAccountSecurity())
        RecoveryPendingEntry()
        GroupCard { GRow(L10n.securityAccountSecurity(), value: access.isAuthority ? L10n.securityThisDeviceManagesTheAccount() : access.name, chevron: true) { nav.open(.accountSecurity) } }
    }
}

struct AccountSecurityDetails: View {
    var pending: Bool
    var onPending: () -> Void
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var action = ActionRunner()
    @State private var renewed = false
    var body: some View {
        let access = AuthorityAccess(state: app.state, me: context.me)
        if app.state.recoveryPending {
            GroupBanner(L10n.securitySomeoneIsRequestingManagementTakeoverWithARecoveryCode(value: String(describing: securityTime(app.state.recoveryReadyMs))), tone: .warn, action: access.isAuthority ? L10n.securityReject() : nil) { nav.open(.recoveryCode) }
        }
        if app.recoveryRotationRequired {
            GroupBanner(L10n.securityIosRecoveryCodeBackupIsUnconfirmedReplaceTheCodeAnd(), tone: .warn, action: L10n.securityIosReplaceCode()) { nav.open(.recoveryCode) }
        }
        if access.isAuthority && app.state.leaseEndMs == 0 && app.state.devices[context.me]?.meta?.handoffRecoveryEd.count == 32 {
            GroupBanner(L10n.securityIosCompletingTakeoverWaitingForRemoteControlRenewal())
        }
        if pending { GroupCard { GRow(L10n.securityUnfinishedSecurityOperation(), chevron: true) { onPending() } } }
        GroupCard {
            GRow(L10n.securityCurrentManagingClient(), value: access.isAuthority ? L10n.securityThisDevice() : access.name)
            GRow(L10n.securityRemoteControlExpiry(), value: securityTime(app.state.leaseEndMs))
            if access.isAuthority {
                GRow(action.running ? L10n.securityIosRenewing() : L10n.securityRenewNow(), action: true, enabled: !action.running) {
                    renewed = false
                    action.run {
                        try await context.remote.renewLeaseNow()
                        let c = try await context.remote.authorityCoordinator()
                        guard try await c.pendingOperation() == nil else { throw UserError(L10nMessage.securityIosRenewalResultUnverifiedContinueTheOriginalOperation()) }
                    } onDone: { renewed = true }
                }
            }
        }
        GroupNote(L10n.securityIosAutomaticallyRenewsWhenYouReturnToTheManagingClient())
        GroupNote(iosAuthorityNotice)
        if !access.isAuthority { GroupNote(L10n.securityOpenToRenew(value: String(describing: access.name))) }
        GroupCard {
            if access.isAuthority {
                GRow(L10n.securityRecoveryCode(), chevron: true) { nav.open(.recoveryCode) }
                GRow(L10n.securityTransferManagement(), chevron: true) { nav.open(.handoff) }
            } else if access.isClient {
                GRow(L10n.securityTakeOverManagement(), chevron: true) { nav.open(.handoff) }
            }
        }
        if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
        if renewed { GroupBanner(L10n.securityIosRemoteControlRenewed()) }
    }
}

struct AccountSecurityScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @State private var pending: SecurityOperation?
    @State private var action = ActionRunner()
    @State private var readError = ""
    @State private var showPending = false
    @State private var abandon = false
    var body: some View {
        SettingsPage {
            if !showPending { AccountSecurityDetails(pending: pending != nil) { showPending = true } }
            if showPending, let pending {
                GroupNote(L10n.securityTheSecurityOperationSOutcomeIsUnverifiedContinueThe())
                Btn(L10n.securityIosContinueOriginalOperation(), kind: .primary, enabled: !action.running) {
                    action.run {
                        let c = try await context.remote.authorityCoordinator()
                        if pending.kind == .handoffOffer { try await c.resumeHandoffOffer() }
                        else {
                            try await c.resumeSecurityOperation()
                            if !pending.delivery.isEmpty {
                                let request = try PBApprovePairingRequest(serializedBytes: pending.delivery)
                                try await c.finishPairingOperation(pairingID: request.pairingID)
                            }
                        }
                        await read()
                    }
                }.padding(16)
                if pending.kind == .handoffOffer {
                    TextAction(L10n.securityAbandonThisSuccessionPreparationSecurity(), color: DS.red) { abandon = true }.disabled(action.running).padding(.horizontal, 16)
                } else if !pending.delivery.isEmpty {
                    TextAction(L10n.securityIosRemoveUndeliveredCandidate(), color: DS.red) { abandon = true }.padding(.horizontal, 16)
                }
            }
            if !readError.isEmpty { GroupBanner(readError, tone: .error, action: L10n.commonRetry()) { Task { await read() } } }
            if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
        }
        .navBar(showPending ? L10n.securityUnfinishedSecurityOperation() : L10n.securityAccountSecurity())
        .navigationBarBackButtonHidden(showPending)
        .toolbar {
            if showPending {
                ToolbarItem(placement: .topBarLeading) {
                    Button { showPending = false } label: { SymbolImage("chevron.left", size: 22, color: DS.blue) }.accessibilityLabel(L10n.commonBack())
                }
            }
        }
        .task { await read() }
        .confirm(isPresented: $abandon, title: L10n.securityIosAbandonThisSecurityOperation(),
            text: pending?.kind == .handoffOffer
                ? L10n.securityIosTheSuccessorSPublicKeyMayAlreadyBePublished()
                : L10n.securityIosTheApprovedCandidateWithoutConfirmedDeliveryWillBeExplicitly(), confirm: L10n.newSessionIosAbandon(), danger: true) {
                action.run {
                    guard let pending else { return }
                    let c = try await context.remote.authorityCoordinator()
                    if pending.kind == .handoffOffer { try await c.discardHandoffOffer(acknowledgePublishedKey: true) }
                    else {
                        let delivery = try PBApprovePairingRequest(serializedBytes: pending.delivery)
                        let approval = try PBPairApproval(serializedBytes: delivery.approval)
                        try await context.remote.removeDevice(deviceId(publicKey: approval.candidateDevicePublicKey), abandonPairDelivery: true)
                        context.memory.value { AuthorityPairingSessions() }.discard(pairingID: delivery.pairingID)
                    }
                    await read()
                }
            }
    }
    private func read() async {
        guard !app.isDemo else { return }
        do {
            let c = try await context.remote.authorityCoordinator()
            pending = try await c.pendingOperation(); readError = ""
            if pending == nil { showPending = false }
        } catch { readError = errorText(error) }
    }
}

/// Displayed only in memory. A failed write keeps the same material and the confirmation.
@MainActor @Observable
final class RecoveryCodeDraft {
    var prepared: PreparedRecovery?
    var saved = false
    var success = false
    var offered = false
    var canPublish: Bool { prepared != nil && saved }
}

struct RecoveryCodeFigure: View {
    let code: String
    @Binding var saved: Bool
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(code).font(.footnote.monospaced()).ink(DS.ink).textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading).padding(16)
                .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.small))
            Hint(L10n.securitySaveTheRecoveryCodeOfflineAnyoneWithItCan())
            Toggle(L10n.securityISavedTheNewRecoveryCodeOffline(), isOn: $saved).font(.body).ink(DS.ink).tint(DS.blue)
        }.padding(16)
    }
}

struct RecoveryCodeScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @State private var draft = RecoveryCodeDraft()
    @State private var action = ActionRunner()
    @State private var confirm = false
    var body: some View {
        let access = AuthorityAccess(state: app.state, me: context.me)
        SettingsPage {
            if access.isAuthority {
                RecoveryPendingEntry()
                GroupLabel(L10n.securityRecoveryCode())
                GroupNote(L10n.securityReplacingTheRecoveryCodeImmediatelyInvalidatesTheOldCode())
                #if DEBUG
                if app.isDemo { AuthorityDemoCode(handoff: false) }
                #endif
                if let prepared = draft.prepared {
                    RecoveryCodeFigure(code: prepared.code, saved: $draft.saved)
                    Btn(L10n.securitySavedPublishReplacement(), kind: .primary, enabled: draft.canPublish && !action.running) { confirm = true }
                        .padding(.horizontal, 16)
                } else if !app.isDemo {
                    Btn(L10n.securityGenerateNewRecoveryCode(), kind: .primary, enabled: !action.running) {
                        action.run {
                            let c = try await context.remote.authorityCoordinator()
                            draft.prepared = try await c.prepareRecovery(); draft.saved = false; draft.success = false
                        }
                    }.padding(16)
                }
                if draft.success { GroupBanner(L10n.securityIosTheNewRecoveryCodeIsActiveTheOldCode()) }
                if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
            } else { GroupNote(access.addHint) }
        }
        .navBar(L10n.securityRecoveryCode())
        .task { draft = context.memory.value { RecoveryCodeDraft() } }
        .confirm(isPresented: $confirm, title: L10n.securityIosPublishNewRecoveryCode(), text: L10n.securityIosTheOldRecoveryCodeIsImmediatelyInvalidatedAndPending(), confirm: L10n.securityIosPublish()) {
            action.run {
                guard let prepared = draft.prepared, draft.saved else { throw UserError(L10nMessage.securityIosSaveTheRecoveryCodeOfflineFirst()) }
                let c = try await context.remote.authorityCoordinator()
                try await c.publishRecovery(prepared, codeSaved: draft.saved)
                guard try await c.pendingOperation() == nil else { throw UserError(L10nMessage.securityIosPublicationResultUnverifiedContinueTheOriginalOperation()) }
                await context.remote.refreshDevices()
                app.recoveryRotationRequired = false
                draft.success = true; draft.prepared = nil; draft.saved = false
            }
        }
    }
}
