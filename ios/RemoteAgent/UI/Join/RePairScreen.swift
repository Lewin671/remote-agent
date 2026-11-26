import RACore
import SwiftUI

/// Current protocol members can supplement their account profile by pairing again,
/// keeping Ed identity and the existing pin; Go `clientcore` rotates only their X key.
struct RePairScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context

    var body: some View {
        let vm = app.screens.onboarding
        CoreJoinFlow(vm: vm.join, showLegacyRepairBanner: false, canBack: false)
            .navBar(L10n.securityIosPairAgainAndPreserveIdentity())
            .task {
                guard !app.isDemo else { return }
                await context.coreHost.onboardingSetMode(
                    .join,
                    existingRepair: true,
                    existingServer: context.account.server
                )
            }
            .onDisappear {
                guard !app.isDemo else { return }
                let host = context.coreHost
                Task { await host.onboardingJoinCancel() }
            }
    }
}
