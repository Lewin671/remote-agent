import SwiftUI

/// Current protocol members can supplement their account profile by pairing again,
/// keeping Ed identity and the existing pin; the core rotates only their X key.
struct RePairScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @State private var flow: JoinModel?
    var body: some View {
        Group {
            if let flow { JoinFlow(model: flow) }
            else { Page { Color.clear } }
        }
        .navBar("重新配对并保留身份")
        .task {
            guard flow == nil, !app.isDemo else { return }
            flow = JoinModel(lastServer: context.account.server,
                calls: app.rePairingCalls(for: context.account), retainedIdentity: true) { account in
                    try await app.joined(account)
                }
        }
        .onDisappear { flow?.cancel() }
    }
}
