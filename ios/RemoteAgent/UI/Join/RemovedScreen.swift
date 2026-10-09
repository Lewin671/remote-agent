import SwiftUI

/// The server turned this device away (architecture §13 「移除设备」): nothing
/// of the account is shown until it joins again. The keys are not deleted
/// for the user: a server whose data was reset says the same, and then the
/// root key is what gets the device back in. Android's `RemovedScreen`.
struct RemovedScreen: View {
    @Environment(AppModel.self) private var model
    @State private var leaving = false

    var body: some View {
        Page {
            EmptyState(
                "这台设备已被移出账户",
                text: "服务器不再接受它的登录：它在另一台设备上被移除了，或者服务器的数据被重置了。电脑上的会话不受影响。",
                action: "重新加入账户",
                onAction: {
                    guard !leaving else { return }
                    leaving = true
                    Task { await model.logout() }
                },
                note: "会先删除这台设备上的账户密钥和缓存，再用配对码加入。",
                link: "再试一次",
                onLink: { if !leaving { model.retryLogin() } }
            )
            .frame(maxHeight: .infinity)
        }
        .debugSignal("removed.retry") { model.retryLogin() }
    }
}
