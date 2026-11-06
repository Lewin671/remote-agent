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
                    Text("永久删除账户").font(.title2.bold())
                    Text("这会删除当前中继上的整个账户、设备登记、加密会话记录、图片、文件块和账户设置，并使所有已配对设备断开。这不是退出登录，删除后无法用恢复码找回。")
                    Text("电脑上的原始 CLI 会话和项目文件，以及其他设备已下载的本地副本，不会被远程删除。为防止旧设备身份重新注册，中继仍保留最小的设备撤销标记。")
                        .foregroundStyle(.secondary)
                    if !model.isDemo && AuthorityAccess(state: model.state, me: context.me).isAuthority {
                        TextField("输入“删除账户”以确认", text: $confirmation)
                            .textFieldStyle(.roundedBorder)
                            .accessibilityLabel("输入删除账户以确认")
                            .disabled(working)
                        if !failure.isEmpty {
                            Text(failure).foregroundStyle(.red)
                            Text("如果请求发出后网络中断，账户可能已经删除。本机密钥暂时保留；重新连接核对状态，确认后可退出并清除本机密钥。")
                                .font(.footnote).foregroundStyle(.secondary)
                        }
                        Button(working ? "正在删除…" : "永久删除账户", role: .destructive) { asking = true }
                            .buttonStyle(.borderedProminent)
                            .disabled(confirmation != "删除账户" || working)
                    } else {
                        Text(model.isDemo ? "示例数据不会发出删除请求。" : "请在当前账户的管理端执行删除。若管理端已丢失，请先在首次使用页面通过恢复码找回管理权。")
                    }
                }.padding(20)
            }
            .navigationTitle("删除账户")
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("关闭") { dismiss() }.disabled(working) } }
            .interactiveDismissDisabled(working)
            .confirmationDialog("永久删除整个账户？", isPresented: $asking, titleVisibility: .visible) {
                Button("确认永久删除", role: .destructive) {
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
                Button("取消", role: .cancel) {}
            } message: { Text("所有设备将断开，中继上的数据无法恢复。") }
        }
    }
}
