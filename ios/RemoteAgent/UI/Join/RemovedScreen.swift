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
                "服务器拒绝了这台设备的登录",
                text: "可能是设备被移除、服务重置或连接异常。这条消息不能证明安全撤销已经完成，请先重试或在管理电脑核实。",
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
