// The sheets and the dialog of a session's ⋯ menu: 审批模式, 会话设置 (context,
// model and reasoning effort) and 重命名会话. Android's ui/Sheets.kt.
//
// What they show is what the computer reports (architecture §7 「以 CLI
// 为准」): a choice is a command, and the sheet closes when the computer has
// taken it, never before. A failure keeps the sheet open and says why.
import RACore
import RAProto
import SwiftUI

/// A sheet's title names the CLI only when it is not Codex, as the mockup does.
private func sheetTitle(_ s: SessionView, _ what: String) -> String {
    s.agent == .codex ? what : "\(agentName(s.agent)) · \(what)"
}

/// 审批模式: the four modes; full access is confirmed first; only this session changes.
struct SessionModeSheet: View {
    let session: SessionView
    var startConfirm = false
    let onClose: () -> Void
    @Environment(AccountContext.self) private var context
    @State private var act = ActionRunner()
    @State private var confirmFull = false
    /// The auto mode goes with the model, which the CLI's own list says.
    @State private var modelList: [PBModelInfo]?

    private func unavailable(_ m: Mode) -> Bool {
        (m == .full && session.info?.fullAccessUnavailable == true) || (m == .auto && autoUnavailable(modelList, model: session.info?.model ?? ""))
    }

    private func set(_ m: Mode) {
        let (remote, sid) = (context.remote, session.id)
        act.run({
            var cmd = PBCommand()
            cmd.setApprovalMode = .with {
                $0.sessionID = sid
                $0.mode = m.proto
            }
            try await remote.command(sid, cmd)
        }, onDone: { onClose() })
    }

    var body: some View {
        let current = modeOf(session)
        // A mode set in the terminal outside the four is shown as it is.
        let pm = session.info?.permissionMode ?? ""
        let outside = !pm.isEmpty && permissionMode(pm) == nil ? (pm == "acceptEdits" ? "accept edits" : pm) : nil
        Group {
            SheetHead(sheetTitle(session, "审批模式"))
            if let outside { Text("当前：\(outside)（在终端设置）").type(T.meta, Theme.muted).padding(.bottom, 4) }
            ForEach(Array(Mode.allCases.enumerated()), id: \.offset) { i, m in
                let off = unavailable(m)
                let help = off
                    ? (m == .full ? "本会话启动时未启用；需要时新建会话选择" : "当前模型不支持")
                    : modeHelp(m).replacingOccurrences(of: "CLI", with: agentName(session.agent))
                ListItem(
                    m.text, desc: help, checked: m == current, enabled: !off, danger: m == .full && !off,
                    last: i == Mode.allCases.count - 1,
                    onTap: act.running || off
                        ? nil
                        : {
                            // Full access mid-session is a second, explicit yes (architecture §7).
                            if m == .full && current != .full { confirmFull = true } else { set(m) }
                        })
            }
            Hint(outside.map { "照实显示终端中的模式；不提供切到 \($0) 的按钮。" } ?? "只改当前会话")
            if act.running { Hint("正在切换…") }
            if !act.error.isEmpty { Hint("没能切换：\(act.error)", color: Theme.red) }
        }
        .confirm(
            isPresented: $confirmFull, title: "切到完全放行？", text: "之后所有操作都不会再问你，手机上也收不到审批。只对这个会话生效。",
            confirm: "切换", danger: true
        ) { set(.full) }
        .task { if startConfirm { confirmFull = true } }
        .task {
            let (remote, machine) = (context.remote, session.machineId)
            modelList = try? await remote.listModels(machine, agent: session.agent).models
        }
        .debugSignal("session.mode.pick") {
            let named: [String: Mode] = ["ask": .ask, "auto": .auto, "plan": .plan, "full": .full]
            if let m = named[SessionDebug.text] { set(m) }
        }
    }
}

/// 会话设置: context and 压缩, the model and its reasoning effort. A choice
/// is sent at once while idle; the sheet shows the computer's value after.
struct SessionModelSheet: View {
    let session: SessionView
    /// A turn is running: nothing can be switched until it ends.
    let running: Bool
    let onClose: () -> Void
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var models: PBListModelsResponse?
    @State private var loadError = ""
    @State private var attempt = 0
    @State private var all = false
    @State private var act = ActionRunner()
    @State private var compact = ActionRunner()
    @State private var picked = ""

    private func setModel(_ slug: String, _ effort: String) {
        let (remote, sid) = (context.remote, session.id)
        act.run({
            var cmd = PBCommand()
            cmd.setModel = .with {
                $0.sessionID = sid
                $0.model = slug
                if !effort.isEmpty { $0.effort = effort }
            }
            try await remote.command(sid, cmd)
        }, onDone: { onClose() })
    }

    var body: some View {
        let (remote, sid) = (context.remote, session.id)
        SheetHead(sheetTitle(session, "会话设置"))
        if let percent = contextPercent(session), let info = session.info {
            ContextBox(
                percent: percent,
                detail: info.contextWindow > 0 ? "上下文 \(info.contextTokens / 1000)k / \(info.contextWindow / 1000)k" : "上下文占用",
                action: compact.running ? "正在压缩…" : "压缩", enabled: !running && !compact.running
            ) {
                compact.run({
                    var cmd = PBCommand()
                    cmd.compact = .with { $0.sessionID = sid }
                    try await remote.command(sid, cmd)
                }, onDone: { onClose() })
            }
            if !compact.error.isEmpty { Hint("没能压缩：\(compact.error)", color: Theme.red) }
        }
        FieldLabel("模型")
        if !loadError.isEmpty {
            Banner("没能读取模型列表：\(loadError)", tone: .error, action: "重试") { attempt += 1 }
        } else if let models {
            list(models.models)
        } else {
            LoadingRow(inset: 0)
        }
        Hint(running ? "只在空闲时能切换；这一轮结束后再选" : "仅显示当前模型支持的档位；空闲时点选提交，等电脑确认后更新实际值。")
        if act.running { Hint("正在切换…") }
        if !act.error.isEmpty { Hint("没能切换：\(act.error)", color: Theme.red) }
        // Read again each time the sheet opens: the computer's list changes with the CLI.
        Color.clear.frame(height: 0).task(id: attempt) {
            do {
                // Sample data has no computer to ask again.
                let list = try await remote.listModels(session.machineId, agent: session.agent, fresh: !model.isDemo)
                models = list
                loadError = ""
                if picked.isEmpty { picked = findModel(list.models, id: session.info?.model ?? "") { $0.slug }?.slug ?? "" }
            } catch {
                loadError = errorText(error)
            }
        }
    }

    @ViewBuilder private func list(_ offered: [PBModelInfo]) -> some View {
        let current = findModel(offered, id: session.info?.model ?? "") { $0.slug }
        let few = all || offered.count <= 5
        let shown = few ? offered : Array(offered.prefix(3))
        ForEach(Array(shown.enumerated()), id: \.element.slug) { i, m in
            let top = m.efforts.last
            ListItem(
                m.displayName.isEmpty ? m.slug : m.displayName, checked: m.slug == picked, enabled: !running && !act.running,
                small: m.efforts.isEmpty ? "无推理强度" : top.flatMap { $0 == "high" ? nil : "最高 \($0)" },
                last: i == shown.count - 1 && few
            ) {
                picked = m.slug
                // A model without efforts has nothing more to choose.
                if m.efforts.isEmpty && m.slug != current?.slug { setModel(m.slug, "") }
            }
        }
        if !few { TextAction("全部模型", style: T.body, arrow: true) { all = true } }
        if let chosen = offered.first(where: { $0.slug == picked }), !chosen.efforts.isEmpty {
            FieldLabel("推理强度")
            ChoiceGrid(
                chosen.efforts, selected: chosen.slug == current?.slug ? session.info?.effort : nil, enabled: !running && !act.running
            ) { setModel(chosen.slug, $0) }
        }
    }
}

// MARK: Renaming

/// 重命名会话: the title on one line with the keyboard up, in the kit's
/// input dialog. Saving it empty goes back to the automatic title. The new
/// title comes back in the session's metadata; a failure keeps the dialog
/// and what was typed.
private struct SessionRename: ViewModifier {
    @Binding var isPresented: Bool
    let session: SessionView
    @Environment(AccountContext.self) private var context

    func body(content: Content) -> some View {
        let initial = renameInitial(session)
        let (remote, sid) = (context.remote, session.id)
        content.inputAlert(
            isPresented: $isPresented, title: "重命名会话", initial: initial, placeholder: "标题", text: "留空保存会恢复自动标题",
            limit: .max, lines: 1...1, filter: titleInput, debugSignal: "session.rename.save", debugText: { SessionDebug.text }
        ) { typed in
            // The same title again is a cancel.
            guard let title = renameTitle(typed: typed, initial: initial) else { return }
            var cmd = PBCommand()
            cmd.renameSession = .with {
                $0.sessionID = sid
                $0.title = title
            }
            try await remote.command(sid, cmd)
        }
    }
}

extension View {
    /// 重命名会话 over this view.
    func sessionRename(isPresented: Binding<Bool>, session: SessionView) -> some View {
        modifier(SessionRename(isPresented: isPresented, session: session))
    }
}
