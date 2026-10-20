import Observation
import RACore
import SwiftUI

@MainActor @Observable
final class HandoffDraft {
    var prepared: PreparedRecovery?
    var saved = false
    var offered = false
    var canPublish: Bool { prepared != nil && saved }
}

struct HandoffScreen: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var draft = HandoffDraft()
    @State private var action = ActionRunner()
    @State private var candidates: [DeviceView] = []
    @State private var target: DeviceView?
    @State private var confirming = false
    @State private var publicCode = ""
    @State private var completedProfile = false
    @State private var handedOff = false

    var body: some View {
        let access = AuthorityAccess(state: app.state, me: context.me)
        SettingsPage {
            if access.isAuthority { outgoing }
            else if access.isClient { incoming }
            else { GroupNote(access.addHint) }
            if !action.error.isEmpty { GroupBanner(action.error, tone: .error) }
        }
        .navBar(access.isAuthority ? "交接管理权" : "接管管理权")
        .task {
            draft = context.memory.value { HandoffDraft() }
            if app.isDemo {
                #if DEBUG
                candidates = app.state.devices.values.filter { $0.id != context.me && $0.kind == .client && $0.meta?.handoffRecoveryEd.count == 32 }
                #endif
            } else if access.isAuthority { loadCandidates() }
        }
        .confirm(isPresented: $confirming, title: "交接管理权？",
            text: "确认接任设备（\(target.map { clientName($0, id: $0.id, among: app.state.devices) } ?? "接任设备")）已经保存新的恢复码。交接后本机会被移除，接任设备成为唯一管理端。",
            confirm: "确认交接", danger: true) {
                action.run {
                    guard let target else { return }
                    try await context.remote.handoffAuthority(incoming: target.id)
                    let c = try await context.remote.authorityCoordinator()
                    guard try await c.pendingOperation() == nil else { throw UserError("交接结果尚未核实，请继续原操作。") }
                    handedOff = true
                    app.authorityHandoffComplete = true
                }
            }
    }
    private var outgoing: some View {
        Group {
            GroupLabel("交接管理权")
            GroupNote("在接任设备加入账户并确认保存新恢复码后，在这里批准。交接保留可达历史，同时移除本机。")
            GroupCard {
                ForEach(candidates) { device in
                    GRow("交接给 " + clientName(device, id: device.id, among: app.state.devices),
                        chevron: true, enabled: !action.running) { target = device; confirming = true }
                }
                GRow("刷新接任设备", action: true, enabled: !action.running) { loadCandidates() }
            }
            if candidates.isEmpty { GroupNote("等待接任设备生成并保存恢复码") }
            if handedOff { GroupBanner("已交接管理权，本机已移除。") }
        }
    }
    private var incoming: some View {
        Group {
            GroupLabel("接管管理权")
            GroupNote("先保存新的恢复码，再请当前管理端批准交接。当前管理端会被移除，不需要等待 48 小时。")
            GroupNote(iosAuthorityNotice)
            #if DEBUG
            if app.isDemo { AuthorityDemoCode(handoff: true) }
            #endif
            if let prepared = draft.prepared {
                RecoveryCodeFigure(code: prepared.code, saved: $draft.saved)
                Btn("我已保存，通知管理端", kind: .primary, enabled: draft.canPublish && !action.running) {
                    action.run {
                        guard draft.saved else { throw UserError("请先离线保存恢复码。") }
                        let c = try await context.remote.authorityCoordinator()
                        try await c.offerHandoff(prepared, codeSaved: draft.saved)
                        guard try await c.pendingOperation() == nil else { throw UserError("接任准备结果尚未核实，请继续原操作。") }
                        draft.offered = true; draft.prepared = nil; draft.saved = false
                        await context.remote.refreshDevices()
                    }
                }.padding(.horizontal, 16)
            } else if !app.isDemo {
                if draft.offered || app.state.devices[context.me]?.meta?.handoffRecoveryEd.count == 32 {
                    GroupBanner("恢复码已确认保存，等待当前管理端批准；可以重新打开此页查看。")
                }
                Btn(draft.offered || app.state.devices[context.me]?.meta?.handoffRecoveryEd.count == 32 ? "重新生成恢复码" : "生成新的恢复码", kind: .primary, enabled: !action.running) {
                    action.run {
                        let c = try await context.remote.authorityCoordinator()
                        draft.prepared = try await c.prepareHandoffCode(); draft.saved = false
                    }
                }.padding(16)
            }
            GroupLabel("补齐账户公钥资料")
            GroupNote("如果接任准备提示缺少账户公钥，请重新配对一次；也可输入当前恢复码，只补齐公钥资料，不发起恢复申请。")
            GroupCard { GRow("重新配对并保留身份", chevron: true) { nav.open(.rePair) } }
            InputField("当前恢复码", text: $publicCode, placeholder: "恢复码", look: .mono, lines: 2...6)
                .padding(.horizontal, 16).privacySensitive()
            TextAction("补齐资料", enabled: !publicCode.isEmpty && !action.running) {
                action.run {
                    try await app.authorityBootstrap.completeAccountPublicProfile(server: context.account.server, code: publicCode)
                    publicCode = ""; completedProfile = true
                }
            }.padding(.horizontal, 16)
            if completedProfile { GroupBanner("账户公钥资料已补齐，可以准备接任。") }
        }
    }
    private func loadCandidates() {
        guard !app.isDemo else { return }
        action.run { candidates = try await context.remote.handoffCandidates() }
    }
}
