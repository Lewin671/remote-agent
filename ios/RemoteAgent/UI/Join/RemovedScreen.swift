import RACore
import SwiftUI

/// Token rejection alone cannot prove revocation or a completed handoff.
struct RemovedScreen: View {
    @Environment(AppModel.self) private var model
    @State private var leaving = false
    @State private var pending = false
    @State private var verified = false
    @State private var advice: AppModel.RejoinAdvice?
    @State private var action = ActionRunner()

    var body: some View {
        Page {
            ScrollView {
                VStack(spacing: 16) {
                    EmptyState(
                        model.authorityHandoffComplete ? L10n.securityIosManagementTransferred() : L10n.securityIosTheServerRejectedThisDeviceSLogin(),
                        text: model.authorityHandoffComplete ? L10n.securityIosThisDeviceWasRemovedTheSuccessorIsNowThe() : L10n.settingsTheDeviceMayHaveBeenRemovedTheServiceReset(),
                        action: L10n.settingsRejoinAccount(), onAction: {
                            do {
                                if let warning = try model.rejoinAdvice() { advice = warning; leaving = true }
                                else { action.run { await model.logout(pairing: true) } }
                            } catch { action.run { throw error } }
                        },
                        note: L10n.securityIosThisDeviceSAccountKeysAndCacheWillBe(),
                        link: L10n.sessionTryAgain(), onLink: { model.retryLogin() })
                    if pending {
                        Banner(L10n.securityIosASecurityOperationIsUnfinishedRetryOrVerifyIts(), tone: .warn)
                        Btn(L10n.securityIosVerifyOriginalSecurityOperation(), kind: .primary, enabled: !action.running) {
                            action.run {
                                guard let remote = model.remote else { return }
                                let c = try await remote.authorityCoordinator()
                                try await c.resumeSecurityOperation()
                                pending = try await c.pendingOperation() != nil
                                verified = !pending
                            }
                        }
                    }
                    if verified { Banner(L10n.securityIosOriginalSecurityOperationVerifiedThisDeviceNoLongerHas()) }
                    if !action.error.isEmpty { Banner(action.error, tone: .error) }
                }.padding(16)
            }
        }
        .task {
            guard !model.isDemo, let remote = model.remote else { return }
            do {
                let c = try await remote.authorityCoordinator()
                pending = try await c.pendingOperation() != nil
            } catch { pending = true }
        }
        .confirm(isPresented: $leaving, title: L10n.securityExitAndDeleteLocalKeys(),
            text: advice?.text ?? "",
            confirm: L10n.securityExit(), danger: true) {
                action.run {
                    guard try model.rejoinAdvice() == advice else { throw UserError(L10nMessage.securityIosSecurityOperationStateChangedChooseHowToJoinAgain()) }
                    await model.logout(pairing: true)
                }
            }
        .debugSignal("removed.retry") { model.retryLogin() }
    }
}

/// A definitive login rejection before the retained-identity setup form is shown.
struct RetainedIdentityDeniedScreen: View {
    @Environment(AppModel.self) private var model
    @State private var advice: AppModel.RejoinAdvice?
    @State private var asking = false
    @State private var action = ActionRunner()

    private func rejoin() {
        action.run {
            if let warning = try model.rejoinAdvice() { advice = warning; asking = true }
            else { await model.logout(pairing: true) }
        }
    }
    var body: some View {
        Page {
            VStack(spacing: 16) {
                Spacer()
                EmptyState(L10n.securityTheServerDoesNotRecognizeThisDevice(),
                    text: L10n.securityItMayHaveBeenRemovedOrTheServiceMay(),
                    action: L10n.securityRejoinWithAPairingCode(), onAction: rejoin,
                    note: L10n.securityIosThisPhoneSAccountKeysAndCacheWillBe())
                if !action.error.isEmpty { Banner(action.error, tone: .error) }
                Spacer()
            }.padding(16)
        }
        .confirm(isPresented: $asking, title: L10n.securityDeleteLocalKeysAndRejoin(),
            text: advice?.text ?? "", confirm: L10n.securityRejoin(), danger: true) {
                action.run {
                    guard try model.rejoinAdvice() == advice else { throw UserError(L10nMessage.securityIosSecurityOperationStateChangedChooseHowToJoinAgain()) }
                    await model.logout(pairing: true)
                }
            }
        .debugSignal("join.rejoin", rejoin)
    }
}
