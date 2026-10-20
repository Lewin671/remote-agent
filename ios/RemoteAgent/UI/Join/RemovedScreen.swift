import RACore
import SwiftUI

/// Token rejection alone cannot prove revocation or a completed handoff.
struct RemovedScreen: View {
    @Environment(AppModel.self) private var model
    @State private var leaving = false
    @State private var pending = false
    @State private var verified = false
    @State private var action = ActionRunner()

    var body: some View {
        Page {
            ScrollView {
                VStack(spacing: 16) {
                    EmptyState(
                        model.authorityHandoffComplete ? "已交接管理权" : "服务器拒绝了这台设备的登录",
                        text: model.authorityHandoffComplete ? "本机已移除，接任设备成为唯一管理端。要再次加入，请重新配对。" : "可能是设备被移除、服务重置或连接异常。这条消息不能证明安全撤销已经完成，请先重试或在当前管理端核实。",
                        action: "重新加入账户", onAction: { leaving = true },
                        note: "会先删除这台设备上的账户密钥和缓存，再用配对码加入。",
                        link: "再试一次", onLink: { model.retryLogin() })
                    if pending {
                        Banner("安全操作结果尚未核实，请先继续原操作；退出会删除唯一完成材料。", tone: .warn)
                        Btn("核实原安全操作", kind: .primary, enabled: !action.running) {
                            action.run {
                                guard let remote = model.remote else { return }
                                let c = try await remote.authorityCoordinator()
                                try await c.resumeSecurityOperation()
                                pending = try await c.pendingOperation() != nil
                                verified = !pending
                            }
                        }
                    }
                    if verified { Banner("原安全操作已核实。本机已不再拥有管理权。") }
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
        .confirm(isPresented: $leaving, title: "退出并删除本机密钥？",
            text: AuthorityAccess.exitAdvice(authority: !model.authorityHandoffComplete && model.state.authorityDeviceId == model.account?.deviceId, unresolved: pending),
            confirm: "退出", danger: true) { Task { await model.logout() } }
        .debugSignal("removed.retry") { model.retryLogin() }
    }
}
