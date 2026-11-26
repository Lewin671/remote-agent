import RACore
import RAProto
import SwiftUI

/// Shown when `AppRootViewModel.stage == .removed`, driven strictly by Core's `RemovedDeviceView`.
struct RemovedScreen: View {
    @Environment(AppModel.self) private var model

    static func copy(for removed: PBRemovedDeviceView) -> (title: String, text: String) {
        (
            title: removed.removalVerified
                ? L10n.settingsThisDeviceWasRemovedFromTheAccount()
                : L10n.settingsCannotSignInToTheRelayRightNow(),
            text: removed.removalVerified
                ? L10n.settingsSignedAccountStateConfirmsThisDeviceWasRemovedSessions()
                : L10n.settingsTheDeviceMayHaveBeenRemovedTheServiceReset()
        )
    }

    var body: some View {
        let removed = model.screens.appRoot.removed
        let (title, text) = Self.copy(for: removed)
        Page {
            ScrollView {
                VStack(spacing: 16) {
                    EmptyState(
                        title,
                        text: text,
                        action: L10n.settingsRejoinAccount(),
                        onAction: {
                            if model.isDemo {
                                Task { await model.logout(pairing: true) }
                            } else {
                                Task {
                                    await model.coreHost.exitRequest(
                                        intent: .rejoinPairing,
                                        server: removed.server
                                    )
                                }
                            }
                        },
                        note: L10n.securityIosThisDeviceSAccountKeysAndCacheWillBe(),
                        link: removed.canRetryLogin ? L10n.sessionTryAgain() : nil,
                        onLink: removed.canRetryLogin ? { model.retryLogin() } : nil
                    )
                    if removed.hasPendingOp_p {
                        Banner(L10n.securityIosASecurityOperationIsUnfinishedRetryOrVerifyIts(), tone: .warn)
                        Btn(
                            L10n.securityIosVerifyOriginalSecurityOperation(),
                            kind: .primary,
                            enabled: removed.canVerifyPending && !removed.busy
                        ) {
                            Task { await model.coreHost.appRootVerifyPendingOp() }
                        }
                    }
                    let verifyText = removed.hasVerifyResultText ? removed.verifyResultText.rendered() : ""
                    if !verifyText.isEmpty {
                        Banner(verifyText)
                    }
                }
                .padding(16)
            }
        }
        .debugSignal("removed.retry") { model.retryLogin() }
    }
}

/// Shown when `AppRootViewModel.stage == .retainedDenied`, driven strictly by Core's `RetainedDeniedView`.
struct RetainedIdentityDeniedScreen: View {
    @Environment(AppModel.self) private var model

    private func rejoin() {
        let server = model.screens.appRoot.retainedDenied.server
        Task { await model.coreHost.exitRequest(intent: .rejoinPairing, server: server) }
    }

    var body: some View {
        let denied = model.screens.appRoot.retainedDenied
        Page {
            VStack(spacing: 16) {
                Spacer()
                LegacyRepairBanner(show: denied.showLegacyRepairBanner) {
                    Task { await model.coreHost.appRootDismissUpgradeNotice() }
                }
                EmptyState(
                    L10n.securityTheServerDoesNotRecognizeThisDevice(),
                    text: L10n.securityItMayHaveBeenRemovedOrTheServiceMay(),
                    action: L10n.securityRejoinWithAPairingCode(),
                    onAction: rejoin,
                    note: L10n.securityIosThisPhoneSAccountKeysAndCacheWillBe()
                )
                Spacer()
            }
            .padding(16)
        }
        .debugSignal("join.rejoin", rejoin)
    }
}
