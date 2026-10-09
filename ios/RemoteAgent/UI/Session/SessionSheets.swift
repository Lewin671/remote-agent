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

    private func unavailable(_ m: Mode) -> Bool {
        (m == .full && session.info?.fullAccessUnavailable == true) || (m == .auto && noAutoMode(session))
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
                    ? (m == .full ? "本会话启动时未启用；需要时新建会话选择" : "当前 Haiku 模型不支持")
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
            Text("读取中…").type(T.sm, Theme.muted).padding(.vertical, 12)
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

/// Presents `overlay` over the whole window on a scrim, without the
/// system's slide from the bottom: the look of the kit's dialogs, for the
/// one dialog here that must stay open while it saves.
private struct DialogCover<Overlay: View>: ViewModifier {
    @Binding var isPresented: Bool
    @ViewBuilder let overlay: () -> Overlay
    /// The cover itself, put up and taken down without the system's animation.
    @State private var covered = false

    func body(content: Content) -> some View {
        content
            .fullScreenCover(isPresented: $covered) {
                DialogStage(overlay: overlay).presentationBackground(.clear)
            }
            .onChange(of: isPresented, initial: true) { _, shown in
                guard shown != covered else { return }
                var now = Transaction()
                now.disablesAnimations = true
                withTransaction(now) { covered = shown }
            }
    }
}

private struct DialogStage<Overlay: View>: View {
    @ViewBuilder let overlay: () -> Overlay
    @State private var shown = false

    var body: some View {
        ZStack {
            Theme.scrim.ignoresSafeArea().accessibilityHidden(true)
            overlay()
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .opacity(shown ? 1 : 0)
        .accessibilityAddTraits(.isModal)
        .tint(Theme.blue)
        .onAppear { withAnimation(.easeOut(duration: 0.18)) { shown = true } }
    }
}

/// 重命名会话: the title on one line with the keyboard up. Saving it empty
/// goes back to the automatic title. The new title comes back in the
/// session's metadata; a failure keeps the dialog and what was typed.
struct SessionRenameDialog: View {
    let session: SessionView
    let onClose: () -> Void
    @Environment(AccountContext.self) private var context
    @State private var act = ActionRunner()
    @State private var value: String
    private let initial: String
    @FocusState private var focused: Bool

    init(session: SessionView, onClose: @escaping () -> Void) {
        self.session = session
        self.onClose = onClose
        initial = renameInitial(session)
        _value = State(initialValue: initial)
    }

    private func save() {
        if act.running { return }
        // The same title again is a cancel.
        guard let title = renameTitle(typed: value, initial: initial) else { return onClose() }
        let (remote, sid) = (context.remote, session.id)
        act.run({
            var cmd = PBCommand()
            cmd.renameSession = .with {
                $0.sessionID = sid
                $0.title = title
            }
            try await remote.command(sid, cmd)
        }, onDone: {
            Haptics.confirm()
            onClose()
        })
    }

    var body: some View {
        VStack(spacing: 0) {
            Text("重命名会话").type(T.h17, Theme.fg).multilineTextAlignment(.center).padding(.bottom, 12)
                .accessibilityAddTraits(.isHeader)
            FieldBox {
                TextField("", text: $value)
                    .type(T.body, Theme.fg)
                    .tint(Theme.blue)
                    .submitLabel(.done)
                    .onSubmit(save)
                    .focused($focused)
                    .onChange(of: value) { _, typed in
                        let kept = titleInput(typed)
                        if kept != typed { value = kept }
                    }
                    .accessibilityLabel("标题")
            }
            .padding(.bottom, 8)
            Text(act.error.isEmpty ? "留空保存会恢复自动标题" : "没能保存：\(act.error)")
                .type(T.sm, act.error.isEmpty ? Theme.muted : Theme.red).multilineTextAlignment(.center).padding(.bottom, 14)
            Btns {
                Btn("取消", enabled: !act.running, action: onClose)
                Btn(act.running ? "保存中…" : "保存", kind: .primary, enabled: !act.running, action: save)
            }
        }
        .padding(.init(top: 18, leading: 16, bottom: 12, trailing: 16))
        .frame(maxWidth: 320)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.Radius.lg, style: .continuous))
        .compositingGroup()
        .shadow(color: .black.opacity(0.2), radius: 15, y: 10)
        .padding(.horizontal, 30)
        // The keyboard comes up with the dialog: there is nothing else to do in it.
        .task { focused = true }
        .debugSignal("session.rename.save") {
            value = titleInput(SessionDebug.text)
            save()
        }
    }
}

extension View {
    /// 重命名会话 over this view.
    func sessionRename(isPresented: Binding<Bool>, session: SessionView) -> some View {
        modifier(DialogCover(isPresented: isPresented) {
            SessionRenameDialog(session: session) { isPresented.wrappedValue = false }
        })
    }
}
