import Observation
import RACore
import RAProto
import SwiftUI

@MainActor @Observable
final class AuthoritySetupModel {
    enum Mode { case create, recover }
    let mode: Mode
    let bootstrap: AuthorityBootstrap
    var status: AuthorityBootstrapStatus?
    var server: String
    var setupToken = ""
    var oldCode = ""
    var code = ""
    var saved = false
    var acknowledgeMissing = false
    var recovery: RecoveryContext?
    var busy = false
    var error = ""
    var hasDisplay: Bool { !code.isEmpty }
    var canContinue: Bool { status?.codeSaved == true || (hasDisplay ? saved : acknowledgeMissing) }
    init(mode: Mode, bootstrap: AuthorityBootstrap, server: String, status: AuthorityBootstrapStatus? = nil) {
        self.mode = mode; self.bootstrap = bootstrap; self.server = server; self.status = status
    }
    func run(_ work: @escaping @MainActor () async throws -> Void) async {
        guard !busy else { return }
        busy = true; error = ""
        defer { busy = false }
        do { try await work() } catch { self.error = errorText(error) }
    }
    func prepare() async {
        await run {
            if self.mode == .create { self.code = try await self.bootstrap.prepareCreation(server: self.server) }
            else {
                self.code = try await self.bootstrap.prepareEmergencyRecovery(server: self.server, code: self.oldCode)
                self.oldCode = ""
            }
            self.saved = false; self.status = try await self.bootstrap.status()
        }
    }
    func confirmBackupIfNeeded() async throws {
        guard canContinue else { throw UserError("请先离线保存恢复码。") }
        if hasDisplay && saved && status?.codeSaved != true {
            try await bootstrap.confirmRecoveryCodeSaved()
            status = try await bootstrap.status()
        }
    }
}

/// Available before ordinary pairing; a durable creation/recovery draft always wins.
struct AuthoritySetupScreen: View {
    @Bindable var flow: AuthoritySetupModel
    @Environment(AppModel.self) private var app
    var back: (() -> Void)?

    var body: some View {
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    LargeTitle(flow.mode == .create ? "新建账户" : "用恢复码找回管理权")
                    GroupNote(iosAuthorityNotice)
                    if flow.status == nil { form }
                    else { draft }
                    if !flow.error.isEmpty {
                        GroupBanner(flow.error + (flow.status == nil ? "" : " 原请求已保留，请继续原操作。"), tone: .error)
                    }
                    if flow.status != nil { SetupDiscardButton { await app.logout() }.padding(16) }
                    if flow.status == nil, let back { TextAction("返回", action: back).padding(.horizontal, 16) }
                }.frame(maxWidth: 480).frame(maxWidth: .infinity)
            }.scrollDismissesKeyboard(.interactively)
        }
    }
    private var form: some View {
        VStack(alignment: .leading, spacing: 12) {
            InputField("服务器", text: $flow.server, placeholder: "https://", keyboard: .URL)
            if flow.mode == .create {
                InputField("设置令牌（如需要）", text: $flow.setupToken, placeholder: "设置令牌").privacySensitive()
            } else {
                Hint("用离线保存的恢复码申请接管。等待 48 小时，期间当前管理端可以否决。恢复后会产生历史边界，部分旧内容可能无法读取。")
                InputField("当前恢复码", text: $flow.oldCode, placeholder: "恢复码", look: .mono, lines: 2...6).privacySensitive()
            }
            Btn(flow.mode == .create ? "准备账户" : "准备恢复", kind: .primary,
                enabled: !flow.busy && !flow.server.isEmpty && (flow.mode == .create || !flow.oldCode.isEmpty)) {
                Task { await flow.prepare() }
            }
        }.padding(16)
    }
    @ViewBuilder private var draft: some View {
        if flow.hasDisplay {
            GroupLabel("保存新的恢复码")
            RecoveryCodeFigure(code: flow.code, saved: $flow.saved).privacySensitive()
        } else if flow.status?.codeSaved == true {
            GroupNote("恢复码已确认离线保存。继续原请求，不会生成另一份恢复码。")
        } else {
            GroupBanner("上次显示的恢复码无法再次显示。先核实并完成原请求，再立即更换恢复码。", tone: .warn)
            Toggle("我知道尚未备份，完成后立即换码", isOn: $flow.acknowledgeMissing)
                .type(T.body, Theme.fg).padding(16)
        }
        if flow.mode == .create {
            InputField("设置令牌（如需要）", text: $flow.setupToken, placeholder: "设置令牌")
                .padding(.horizontal, 16).privacySensitive()
            Btn("继续创建账户", kind: .primary, enabled: flow.canContinue && !flow.busy) {
                Task { await create() }
            }.padding(16)
        } else {
            recoveryProgress
        }
        if flow.busy { LoadingRow() }
    }
    @ViewBuilder private var recoveryProgress: some View {
        if let recovery = flow.recovery {
            GroupLabel("恢复申请")
            GroupNote("可生效时间：" + securityTime(recovery.readyMs))
            if recovery.phase == .rejected {
                GroupBanner("恢复申请已被否决。旧恢复码已经作废。", tone: .warn)
                Btn("核实否决并放弃原申请", enabled: !flow.busy) {
                    Task { await discardRejected() }
                }.padding(16)
            } else if recovery.phase == .ready || recovery.phase == .published {
                Btn("完成恢复", kind: .primary, enabled: flow.canContinue && !flow.busy) {
                    Task { await complete() }
                }.padding(16)
            } else { GroupNote("正在等待 48 小时恢复窗口，当前管理端仍可否决。") }
        }
        Btn(flow.recovery == nil ? "提交并核实原申请" : "刷新恢复状态",
            kind: flow.recovery == nil ? .primary : .secondary, enabled: flow.canContinue && !flow.busy) {
                Task {
                    await flow.run {
                        try await flow.confirmBackupIfNeeded()
                        flow.recovery = try await flow.bootstrap.resumeRecovery()
                        flow.status = try await flow.bootstrap.status()
                    }
                }
            }.padding(16)
        if !flow.error.isEmpty {
            TextAction("核实并放弃", enabled: !flow.busy) { Task { await discardRejected() } }
                .padding(.horizontal, 16)
        }
    }
    private func create() async {
        await flow.run {
            try await flow.confirmBackupIfNeeded()
            _ = try await flow.bootstrap.continueCreation(setupToken: flow.setupToken)
            let missing = try await flow.bootstrap.status()?.codeSaved != true
            if missing { try await flow.bootstrap.finishCreationWithoutCodeBackup(acknowledgeRotationRequired: flow.acknowledgeMissing) }
            else { try await flow.bootstrap.finishCreation() }
            flow.code = ""
            app.reloadAfterAuthorityBootstrap(rotationRequired: missing)
        }
    }
    private func complete() async {
        await flow.run {
            try await flow.confirmBackupIfNeeded()
            _ = try await flow.bootstrap.completeRecovery()
            let missing = try await flow.bootstrap.status()?.codeSaved != true
            try await flow.bootstrap.finishRecovery(acknowledgeMissingCodeBackup: flow.acknowledgeMissing)
            flow.code = ""
            app.reloadAfterAuthorityBootstrap(rotationRequired: missing)
        }
    }
    private func discardRejected() async {
        await flow.run {
            try await flow.bootstrap.discardRejectedRecovery(confirmedAfterDefinitiveRejection: true)
            flow.status = nil; flow.recovery = nil; flow.code = ""; flow.saved = false
        }
    }
}
