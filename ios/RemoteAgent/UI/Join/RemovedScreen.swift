import RACore
import SwiftUI

/// Token rejection alone cannot prove revocation or a completed handoff.
struct RemovedScreen: View {
    @Environment(AppModel.self) private var model
    @State private var leaving = false
    @State private var pending = true
    @State private var verified = false
    @State private var advice = ""
    @State private var action = ActionRunner()

    var body: some View {
        Page {
            ScrollView {
                VStack(spacing: 16) {
                    EmptyState(
                        model.authorityHandoffComplete ? "已交接管理权" : "服务器拒绝了这台设备的登录",
                        text: model.authorityHandoffComplete ? "本机已移除，接任设备成为唯一管理端。要再次加入，请重新配对。" : "可能是设备被移除、服务重置或连接出了问题。请先再试一次；仍无法登录时，请在管理端查看这台设备是否还在账户里。电脑上的会话不受影响。",
                        action: "重新加入账户", onAction: {
                            do {
                                if let warning = try model.rejoinAdvice() { advice = warning; leaving = true }
                                else { action.run { await model.logout(pairing: true) } }
                            } catch { action.run { throw error } }
                        },
                        note: "会先删除这台设备上的账户密钥和缓存，再用配对码加入。",
                        link: "再试一次", onLink: { model.retryLogin() })
                    if pending {
                        Banner("还有未完成的安全操作。请先重试或在管理端核实结果；退出后，这台手机将无法继续原操作。", tone: .warn)
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
            text: advice,
            confirm: "退出", danger: true) {
                action.run {
                    guard try model.rejoinAdvice() == advice else { throw UserError("安全操作状态已变化，请重新选择加入方式。") }
                    await model.logout(pairing: true)
                }
            }
        .debugSignal("removed.retry") { model.retryLogin() }
    }
}

/// A definitive login rejection before the retained-identity setup form is shown.
struct RetainedIdentityDeniedScreen: View {
    @Environment(AppModel.self) private var model
    @State private var advice: String?
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
                EmptyState("服务器不认识这台设备",
                    text: "可能已被移除，或服务已重建。电脑上的会话不受影响。",
                    action: "用配对码重新加入", onAction: rejoin,
                    note: "会先删除这台手机上的账户密钥和缓存，再用配对码加入。")
                if !action.error.isEmpty { Banner(action.error, tone: .error) }
                Spacer()
            }.padding(16)
        }
        .confirm(isPresented: $asking, title: "删除本机密钥并重新加入？",
            text: advice ?? "", confirm: "重新加入", danger: true) {
                action.run {
                    guard try model.rejoinAdvice() == advice else { throw UserError("安全操作状态已变化，请重新选择加入方式。") }
                    await model.logout(pairing: true)
                }
            }
        .debugSignal("join.rejoin", rejoin)
    }
}
