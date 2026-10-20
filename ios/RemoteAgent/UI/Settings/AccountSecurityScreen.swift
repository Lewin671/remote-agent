import Observation
import RACore
import RAProto
import SwiftUI

let iosAuthorityNotice = "iOS 版没有后台安全提醒，建议每天打开一次，以免错过恢复申请。"

struct RecoveryPendingEntry: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    var body: some View {
        if AuthorityAccess(state: app.state, me: context.me).isAuthority {
            if app.state.recoveryPending {
                GroupBanner("有人正在用恢复码申请接管管理权，" + securityTime(app.state.recoveryReadyMs) + " 后生效。不是你本人就现在否决。",
                    tone: .warn, action: "处理") { nav.open(.accountSecurity) }
            } else if app.state.recoveryStatusStale {
                GroupBanner("恢复申请状态可能过期，请连接服务器后重新核实。", tone: .warn)
            }
        }
    }
}

struct AccountSecurityGroup: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    var body: some View {
        let access = AuthorityAccess(state: app.state, me: context.me)
        GroupLabel("账户安全")
        RecoveryPendingEntry()
        GroupCard { GRow("账户安全", value: access.isAuthority ? "本机是管理端" : access.name, chevron: true) { nav.open(.accountSecurity) } }
    }
}

struct AccountSecurityDetails: View {
    var pending: Bool
    var onPending: () -> Void
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var action = ActionRunner()
    @State private var renewed = false
    var body: some View {
        let access = AuthorityAccess(state: app.state, me: context.me)
        if app.state.recoveryPending {
            GroupBanner("有人正在用恢复码申请接管管理权，" + securityTime(app.state.recoveryReadyMs) + " 后生效。不是你本人就现在否决。", tone: .warn, action: access.isAuthority ? "否决" : nil) { nav.open(.recoveryCode) }
        }
        if app.recoveryRotationRequired {
            GroupBanner("恢复码尚未确认备份，请立即更换并离线保存。", tone: .warn, action: "换码") { nav.open(.recoveryCode) }
        }
        if access.isAuthority && app.state.leaseEndMs == 0 && app.state.devices[context.me]?.meta?.handoffRecoveryEd.count == 32 {
            GroupBanner("正在完成接管，等待远程控制续期。")
        }
        if pending { GroupCard { GRow("未完成的安全操作", chevron: true) { onPending() } } }
        GroupCard {
            GRow("当前管理端", value: access.isAuthority ? "本机" : access.name)
            GRow("远程控制有效期", value: securityTime(app.state.leaseEndMs))
            if access.isAuthority {
                GRow(action.running ? "续期中…" : "现在续期", action: true, enabled: !action.running) {
                    renewed = false
                    action.run {
                        try await context.remote.renewLeaseNow()
                        let c = try await context.remote.authorityCoordinator()
                        guard try await c.pendingOperation() == nil else { throw UserError("续期结果尚未核实，请继续原操作。") }
                    } onDone: { renewed = true }
                }
            }
        }
        GroupNote("回到管理端时自动续期；过期后远程控制暂停，聊天和历史仍可查看。")
        GroupNote(iosAuthorityNotice)
        if !access.isAuthority { GroupNote("请打开\(access.name)续期。") }
        GroupCard {
            if access.isAuthority {
                GRow("恢复码", chevron: true) { nav.open(.recoveryCode) }
                GRow("交接管理权", chevron: true) { nav.open(.handoff) }
            } else if access.isClient {
                GRow("接管管理权", chevron: true) { nav.open(.handoff) }
            }
        }
        if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
        if renewed { GroupBanner("远程控制已续期。") }
    }
}

struct AccountSecurityScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @State private var pending: SecurityOperation?
    @State private var action = ActionRunner()
    @State private var readError = ""
    @State private var showPending = false
    @State private var abandon = false
    var body: some View {
        SettingsPage {
            if !showPending { AccountSecurityDetails(pending: pending != nil) { showPending = true } }
            if showPending, let pending {
                GroupNote("安全操作结果尚未核实，请先继续原操作；退出会删除唯一完成材料。")
                Btn("继续原操作", kind: .primary, enabled: !action.running) {
                    action.run {
                        let c = try await context.remote.authorityCoordinator()
                        if pending.kind == .handoffOffer { try await c.resumeHandoffOffer() }
                        else {
                            try await c.resumeSecurityOperation()
                            if !pending.delivery.isEmpty {
                                let request = try PBApprovePairingRequest(serializedBytes: pending.delivery)
                                try await c.finishPairingOperation(pairingID: request.pairingID)
                            }
                        }
                        await read()
                    }
                }.padding(16)
                if pending.kind == .handoffOffer {
                    TextAction("放弃此接任准备", color: Theme.red) { abandon = true }.disabled(action.running).padding(.horizontal, 16)
                } else if !pending.delivery.isEmpty {
                    TextAction("移除未收件候选…", color: Theme.red) { abandon = true }.padding(.horizontal, 16)
                }
            }
            if !readError.isEmpty { GroupBanner(readError, tone: .error, action: "重试") { Task { await read() } } }
            if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
        }
        .navBar(showPending ? "未完成的安全操作" : "账户安全")
        .navigationBarBackButtonHidden(showPending)
        .toolbar {
            if showPending {
                ToolbarItem(placement: .topBarLeading) {
                    Button { showPending = false } label: { Ic(Lucide.chevronLeft, size: 22, color: Theme.blue) }.accessibilityLabel("返回")
                }
            }
        }
        .task { await read() }
        .confirm(isPresented: $abandon, title: "放弃此安全操作？",
            text: pending?.kind == .handoffOffer
                ? "接任公钥可能已经发布；放弃只清除本机准备，不会撤回管理端已看到的公钥。若已交接，本机仍会核实管理权并续期，请保留已保存的恢复码。"
                : "已批准但未收件的候选会被明确移除，需要重新配对。", confirm: "放弃", danger: true) {
                action.run {
                    guard let pending else { return }
                    let c = try await context.remote.authorityCoordinator()
                    if pending.kind == .handoffOffer { try await c.discardHandoffOffer(acknowledgePublishedKey: true) }
                    else {
                        let delivery = try PBApprovePairingRequest(serializedBytes: pending.delivery)
                        let approval = try PBPairApproval(serializedBytes: delivery.approval)
                        try await context.remote.removeDevice(deviceId(publicKey: approval.candidateDevicePublicKey), abandonPairDelivery: true)
                        context.memory.value { AuthorityPairingSessions() }.discard(pairingID: delivery.pairingID)
                    }
                    await read()
                }
            }
    }
    private func read() async {
        guard !app.isDemo else { return }
        do {
            let c = try await context.remote.authorityCoordinator()
            pending = try await c.pendingOperation(); readError = ""
            if pending == nil { showPending = false }
        } catch { readError = errorText(error) }
    }
}

/// Displayed only in memory. A failed write keeps the same material and the confirmation.
@MainActor @Observable
final class RecoveryCodeDraft {
    var prepared: PreparedRecovery?
    var saved = false
    var success = false
    var offered = false
    var canPublish: Bool { prepared != nil && saved }
}

struct RecoveryCodeFigure: View {
    let code: String
    @Binding var saved: Bool
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(code).type(T.mono13, Theme.fg).textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading).padding(16)
                .background(Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.sm))
            Hint("请离线保存恢复码。任何拿到它的人都能申请接管管理权；不要发送给别人。")
            Toggle("我已离线保存新的恢复码", isOn: $saved).type(T.body, Theme.fg).tint(Theme.blue)
        }.padding(16)
    }
}

struct RecoveryCodeScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @State private var draft = RecoveryCodeDraft()
    @State private var action = ActionRunner()
    @State private var confirm = false
    var body: some View {
        let access = AuthorityAccess(state: app.state, me: context.me)
        SettingsPage {
            if access.isAuthority {
                RecoveryPendingEntry()
                GroupLabel("恢复码")
                GroupNote("更换恢复码后，旧码立即作废；正在等待的恢复申请会被否决。")
                #if DEBUG
                if app.isDemo { AuthorityDemoCode(handoff: false) }
                #endif
                if let prepared = draft.prepared {
                    RecoveryCodeFigure(code: prepared.code, saved: $draft.saved)
                    Btn("我已保存，发布换码", kind: .primary, enabled: draft.canPublish && !action.running) { confirm = true }
                        .padding(.horizontal, 16)
                } else if !app.isDemo {
                    Btn("生成新的恢复码", kind: .primary, enabled: !action.running) {
                        action.run {
                            let c = try await context.remote.authorityCoordinator()
                            draft.prepared = try await c.prepareRecovery(); draft.saved = false; draft.success = false
                        }
                    }.padding(16)
                }
                if draft.success { GroupBanner("新恢复码已生效，旧码已作废。") }
                if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
            } else { GroupNote(access.addHint) }
        }
        .navBar("恢复码")
        .task { draft = context.memory.value { RecoveryCodeDraft() } }
        .confirm(isPresented: $confirm, title: "发布新恢复码？", text: "旧恢复码立即作废，正在等待的恢复申请会被否决。", confirm: "发布") {
            action.run {
                guard let prepared = draft.prepared, draft.saved else { throw UserError("请先离线保存恢复码。") }
                let c = try await context.remote.authorityCoordinator()
                try await c.publishRecovery(prepared, codeSaved: draft.saved)
                guard try await c.pendingOperation() == nil else { throw UserError("发布结果尚未核实，请继续原操作。") }
                await context.remote.refreshDevices()
                app.recoveryRotationRequired = false
                draft.success = true; draft.prepared = nil; draft.saved = false
            }
        }
    }
}
