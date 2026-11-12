import RACore
import SwiftUI

struct AccountDeletionScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(\.dismiss) private var dismiss
    @State private var confirmation = ""
    @State private var working = false
    @State private var failure = ""
    @State private var asking = false

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Text(L10n.securityIosPermanentlyDeleteAccount()).font(.title2.bold())
                    Text(L10n.securityIosThisDeletesTheEntireAccountOnTheCurrentRelay())
                    Text(L10n.securityIosOriginalCliSessionsAndProjectFilesOnComputersAnd())
                        .foregroundStyle(.secondary)
                    if !model.isDemo && AuthorityAccess(state: model.state, me: context.me).isAuthority {
                        TextField(L10n.securityIosEnterDeleteAccountToConfirm(), text: $confirmation)
                            .textFieldStyle(.roundedBorder)
                            .accessibilityLabel(L10n.securityIosEnterDeleteAccountToConfirm136())
                            .disabled(working)
                        if !failure.isEmpty {
                            Text(failure).foregroundStyle(.red)
                            Text(L10n.securityIosIfTheNetworkDropsAfterTheRequestIsSent())
                                .font(.footnote).foregroundStyle(.secondary)
                        }
                        Button(working ? L10n.securityIosDeleting() : L10n.securityIosPermanentlyDeleteAccount(), role: .destructive) { asking = true }
                            .buttonStyle(.borderedProminent)
                            .disabled(confirmation != L10n.securityIosDeleteAccount() || working)
                    } else {
                        Text(model.isDemo ? L10n.securityIosDemoDataWillNotSendADeletionRequest() : L10n.securityIosDeleteFromTheCurrentAccountSManagingClientIf())
                    }
                }.padding(20)
            }
            .navigationTitle(L10n.securityIosDeleteAccount())
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button(L10n.commonClose()) { dismiss() }.disabled(working) } }
            .interactiveDismissDisabled(working)
            .confirmationDialog(L10n.securityIosPermanentlyDeleteTheEntireAccount(), isPresented: $asking, titleVisibility: .visible) {
                Button(L10n.securityIosConfirmPermanentDeletion(), role: .destructive) {
                    working = true
                    failure = ""
                    Task {
                        do {
                            try await context.remote.deleteAccount()
                            dismiss()
                            await model.logout()
                        } catch {
                            failure = errorText(error)
                            working = false
                        }
                    }
                }
                Button(L10n.commonCancel(), role: .cancel) {}
            } message: { Text(L10n.securityIosAllDevicesWillDisconnectDataOnTheRelayCannot()) }
        }
    }
}
