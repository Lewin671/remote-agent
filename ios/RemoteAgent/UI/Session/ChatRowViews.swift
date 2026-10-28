// The chat's rows, drawn: what Android's ui/Chat.kt and ui/Tools.kt draw
// from the same `ChatItem`s (docs/design-system.md §6 「工具调用」, §7). What a
// row says is the package's (`buildChat`, `toolTitle`, `toolState`); this
// file only lays it out.
//
// A row is a value and its view is `Equatable`: when the list changes, the
// rows that stayed the same are not drawn again. What a row can open in
// place (a folded group, a plan, 思考过程) is remembered outside the row,
// because a lazy list lets go of rows that scroll away.
import Observation
import RACore
import RAMarkdown
import RAMarkdownUI
import RAProto
import SwiftUI
import UIKit

/// What every row needs besides itself. Small and `Equatable`, so that a
/// row is drawn again only when something it shows changed.
struct ChatEnv: Equatable, Sendable {
    var agent = PBAgent.codex
    var cwd = ""
    var home = ""
    /// When the connection dropped, 0 while online: timers stop there.
    var frozenAt: Int64 = 0
    /// What waits for an answer: a running tool that asked for one says so instead of counting.
    var approvals = OrderedMap<String, PBApprovalRequested>()
    /// Tools whose turn completed without their result.
    var noResult = Set<UInt64>()
}

/// What a row can do. Handed down in the environment: closures are not part of what a row shows.
struct ChatActions: Sendable {
    /// Opens the viewer at an event's image.
    var image: @MainActor (_ seq: UInt64, _ index: Int) -> Void = { _, _ in }
    /// Opens another session, as from the line where a fork left it.
    var session: @MainActor (String) -> Void = { _ in }
    /// Puts a message the session ended before writing back into the box.
    var putBack: @MainActor (_ requestId: String, _ text: String, _ files: [PBSentFile]) -> Void = { _, _, _ in }
    /// Shows a message's text for selecting parts of it.
    var select: @MainActor (String) -> Void = { _ in }
}

extension EnvironmentValues {
    @Entry var chatActions = ChatActions()
}

/// Which rows are opened in place, by row key.
@MainActor
@Observable
final class ChatOpen {
    private var keys = Set<String>()

    func isOpen(_ key: String) -> Bool { keys.contains(key) }

    func toggle(_ key: String) {
        if keys.remove(key) == nil { keys.insert(key) }
    }
}

/// The side margin of the chat (design-system §4).
let chatMargin: CGFloat = 14

// MARK: The row

struct ChatRowView: View, Equatable {
    let row: ChatRow
    let env: ChatEnv

    @Environment(\.chatActions) private var actions
    @Environment(ChatOpen.self) private var open

    nonisolated static func == (a: ChatRowView, b: ChatRowView) -> Bool { a.row == b.row && a.env == b.env }

    var body: some View {
        switch row.item {
        case .user(let m):
            ChatBubble(
                m.text.jvmIsBlank && (!m.images.isEmpty || !m.files.isEmpty) ? nil : m.text,
                caption: m.from
            ) {
                FileChips(files: m.files)
                if !m.images.isEmpty {
                    ChatImageRow(m.images) { actions.image(m.seq, $0) }.padding(.bottom, 4)
                }
            }
            .messageMenu(m.text, actions: actions)
            .accessibilityElement(children: .combine)
            .accessibilityLabel(m.text.isEmpty ? (m.files.isEmpty ? "图片" : "文件") : m.text)
            .accessibilityValue(m.from ?? "你发的消息")
        case .agent(let m):
            // The row's own menu answers the long press (复制, and 选择文字 for a part of it), not the text's.
            MarkdownView(blocks: row.blocks, bleed: chatMargin, selectable: false)
                .padding(.horizontal, chatMargin).padding(.vertical, 8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .messageMenu(m.text, actions: actions)
                .accessibilityElement(children: .contain)
                .accessibilityLabel("\(agentName(env.agent)) 的回复")
        case .asyncQuestion(let m):
            VStack(alignment: .leading, spacing: 8) {
                ForEach(Array(m.questions.enumerated()), id: \.offset) { _, q in
                    if !q.header.isEmpty {
                        Text(verbatim: q.header).type(T.chat.with(weight: .semibold), Theme.fg)
                    }
                    MarkdownView(q.text, bleed: chatMargin)
                    ForEach(Array(q.options.enumerated()), id: \.offset) { _, o in
                        HStack(alignment: .top, spacing: 8) {
                            Text("•").type(T.chat, Theme.muted)
                            (Text(verbatim: o.label).bold() + Text(verbatim: o.description_p.isEmpty ? "" : " · \(o.description_p)"))
                                .type(T.chat, Theme.fg)
                        }
                    }
                }
            }
            .padding(.horizontal, chatMargin).padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .contain)
            .accessibilityLabel("\(agentName(env.agent)) 的回复")
        case .thought(let t):
            ChatExpandable("思考过程", key: t.key) {
                Text(t.text).type(T.sm, Theme.muted).padding(.bottom, 6)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.horizontal, chatMargin)
        case .tool(let t):
            VStack(alignment: .leading, spacing: 0) {
                if t.background {
                    BackgroundToolRow(call: t.call, env: env)
                } else {
                    DoneToolRow(call: t.call, decision: t.decision, decisionOk: t.decisionOk, env: env)
                }
                // A tool's images sit right under it, e.g. the file Claude Code read.
                if !t.call.images.isEmpty {
                    ChatImageRow(t.call.images) { actions.image(t.seq, $0) }.padding(.leading, 21).padding(.bottom, 6)
                }
            }
            .padding(.horizontal, chatMargin)
        case .running(let r):
            RunningToolRow(item: r, env: env).padding(.horizontal, chatMargin)
        case .tools(let g):
            ToolGroupRow(group: g, env: env).padding(.horizontal, chatMargin)
        case .generated(let g):
            GeneratedCard(item: g) { actions.image(g.seq, $0) }
        case .note(let n):
            if let icon = n.icon {
                ChatToolLine(
                    icon == .check ? Lucide.check : Lucide.x, color: Theme.muted, title: n.text, meta: n.meta,
                    metaColor: n.tone == 2 ? Theme.red : Theme.muted
                )
                .padding(.horizontal, chatMargin)
            } else {
                Text(n.text).type(T.meta, Theme.muted).multilineTextAlignment(.center).lineLimit(2)
                    .frame(maxWidth: .infinity)
                    .padding(.horizontal, chatMargin).padding(.vertical, 10)
            }
        case .plan(let p):
            PlanCard(key: p.key, text: p.text)
        case .unsent(let u):
            ChatBubble(
                u.text?.isEmpty == false ? u.text : (u.files.isEmpty ? "一条排队的消息" : nil), caption: notSentText("会话已结束"), captionColor: Theme.red,
                links: u.text.map { text in [ChatLink("放回输入框") { actions.putBack(u.requestId, text, u.files) }] } ?? []) { FileChips(files: u.files) }
        case .turnEnd(let t):
            ChatSepLine(t.text)
        case .fork(let f):
            ForkRow(item: f)
        }
    }
}

extension View {
    /// What a long press on a message offers: its text as it was written
    /// (a reply's is its Markdown), or a page to select part of it from.
    /// VoiceOver gets the same as actions of the message.
    @ViewBuilder fileprivate func messageMenu(_ text: String, actions: ChatActions) -> some View {
        if text.isEmpty {
            self
        } else {
            contextMenu {
                Button("复制") { UIPasteboard.general.string = text }
                Button("选择文字") { actions.select(text) }
            } preview: {
                // A full reply's snapshot can fill the screen and looks like
                // selectable text, but is only the menu's inert preview.
                MessageMenuPreview(text: text)
            }
            .accessibilityAction(named: "复制") { UIPasteboard.general.string = text }
            .accessibilityAction(named: "选择文字") { actions.select(text) }
        }
    }
}

// MARK: Messages

/// A text action after a bubble's caption.
struct ChatLink: Identifiable {
    let label: String
    let action: @MainActor () -> Void

    var id: String { label }

    init(_ label: String, action: @escaping @MainActor () -> Void) {
        self.label = label
        self.action = action
    }
}

/// Lays its one child out at most `fraction` of the width offered, at the trailing edge.
private struct TrailingFraction: Layout {
    let fraction: CGFloat
    /// Text set very large would be a column of single characters at 80%: it then takes the whole width.
    let whole: Bool

    private func width(_ proposal: ProposedViewSize) -> CGFloat? {
        proposal.width.map { whole ? $0 : $0 * fraction }
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let size = subviews.first?.sizeThatFits(ProposedViewSize(width: width(proposal), height: proposal.height)) ?? .zero
        return CGSize(width: proposal.width ?? size.width, height: size.height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        subviews.first?.place(
            at: CGPoint(x: bounds.maxX, y: bounds.minY), anchor: .topTrailing,
            proposal: ProposedViewSize(width: width(ProposedViewSize(width: bounds.width, height: nil)), height: bounds.height))
    }
}

/// A message of the user's, at the right: soft, 18 pt corners with a small
/// one at the bottom right, at most 80% wide. `caption` under it says where
/// it came from or how sending went, `captionIcon` before it and `links`
/// after it; `top` is what stands over the bubble, its images.
struct ChatBubble<Top: View>: View {
    private let text: String?
    private let caption: String?
    private let captionColor: Color
    private let captionIcon: LucideIcon?
    private let failed: Bool
    private let pending: Bool
    private let lines: Int?
    private let links: [ChatLink]
    private let padded: Bool
    private let top: Top
    @Environment(\.dynamicTypeSize) private var typeSize

    /// `pending`: not written yet, drawn as an outline; `lines` cuts a long one short.
    init(
        _ text: String?, caption: String? = nil, captionColor: Color = Theme.muted, captionIcon: LucideIcon? = nil,
        failed: Bool = false, pending: Bool = false, lines: Int? = nil, links: [ChatLink] = [], padded: Bool = true,
        @ViewBuilder top: () -> Top
    ) {
        self.text = text
        self.caption = caption
        self.captionColor = captionColor
        self.captionIcon = captionIcon
        self.failed = failed
        self.pending = pending
        self.lines = lines
        self.links = links
        self.padded = padded
        self.top = top()
    }

    var body: some View {
        let shape = UnevenRoundedRectangle(
            topLeadingRadius: 18, bottomLeadingRadius: 18, bottomTrailingRadius: 5, topTrailingRadius: 18, style: .continuous)
        VStack(alignment: .trailing, spacing: 0) {
            top
            if let text {
                TrailingFraction(fraction: 0.8, whole: typeSize.isAccessibilitySize) {
                    Text(text).type(T.body, pending ? Theme.fg2 : Theme.fg)
                        .lineLimit(lines)
                        .padding(.horizontal, 12).padding(.vertical, 8)
                        .background(pending ? Theme.card : failed ? Theme.chip : Theme.soft, in: shape)
                        .overlay {
                            if failed {
                                shape.strokeBorder(Theme.red, lineWidth: 1.5)
                            } else if pending {
                                shape.strokeBorder(Theme.line, lineWidth: 1)
                            }
                        }
                }
            }
            if caption != nil || !links.isEmpty {
                // The note and its actions on one line when they fit, else the actions under it:
                // a long note at a large text size is not squeezed into a column beside them.
                ViewThatFits(in: .horizontal) {
                    HStack(alignment: .firstTextBaseline, spacing: 0) {
                        note
                        actions
                    }
                    VStack(alignment: .trailing, spacing: 4) {
                        note
                        HStack(spacing: 0) { actions }
                    }
                }
                .padding(.top, 2)
            }
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
        .padding(.horizontal, padded ? chatMargin : 0).padding(.vertical, 8)
    }
}

extension ChatBubble {
    @ViewBuilder fileprivate var note: some View {
        HStack(alignment: .firstTextBaseline, spacing: 0) {
            if let captionIcon { Mark(captionIcon, color: captionColor).padding(.trailing, 4) }
            if let caption {
                Text(caption).type(T.meta, captionColor).multilineTextAlignment(.trailing)
            }
        }
    }

    @ViewBuilder fileprivate var actions: some View {
        ForEach(links) { link in
            Button(action: link.action) {
                Text(link.label).type(T.metaB, Theme.blue).lineLimit(1).fixedSize()
                    // 44 pt to the finger without pushing the rows apart.
                    .padding(.leading, 6).padding(.vertical, 14)
                    .contentShape(Rectangle())
            }
            .buttonStyle(PressStyle())
            .padding(.vertical, -14)
        }
    }
}

extension ChatBubble where Top == EmptyView {
    init(
        _ text: String?, caption: String? = nil, captionColor: Color = Theme.muted, captionIcon: LucideIcon? = nil,
        failed: Bool = false, pending: Bool = false, lines: Int? = nil, links: [ChatLink] = [], padded: Bool = true
    ) {
        self.init(
            text, caption: caption, captionColor: captionColor, captionIcon: captionIcon, failed: failed, pending: pending,
            lines: lines, links: links, padded: padded
        ) { EmptyView() }
    }
}

// MARK: Lines between turns

private struct Rule: View {
    var body: some View {
        Rectangle().fill(Theme.line).frame(height: 1).accessibilityHidden(true)
    }
}

/// A thin line with a note in the middle: a turn's time and tokens, where a session ended.
struct ChatSepLine: View {
    private let text: String

    init(_ text: String) { self.text = text }

    var body: some View {
        HStack(spacing: 8) {
            Rule().frame(minWidth: 16)
            // The note keeps its line; the rules take what is left, down to a stub at each side.
            Text(text).type(T.meta, Theme.muted).multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true).layoutPriority(1)
            Rule().frame(minWidth: 16)
        }
        .padding(.horizontal, chatMargin).padding(.vertical, 12)
        .accessibilityElement(children: .combine)
    }
}

/// The line where a forked session leaves its source: the time, 分叉自, and
/// the source's title on one line, cut short when long. The title opens the
/// source; one not held here is fetched by its ID when opened.
private struct ForkRow: View {
    let item: ChatItem.Fork
    @Environment(AppModel.self) private var model
    @Environment(\.chatActions) private var actions

    var body: some View {
        let title = forkSource(item, sessions: model.state.sessions)
        HStack(spacing: 8) {
            Rule().frame(minWidth: 16)
            HStack(spacing: 4) {
                Text("\(item.time) 分叉自").type(T.meta, Theme.muted).lineLimit(1).fixedSize()
                if item.sourceId.isEmpty {
                    Text(title).type(T.meta, Theme.muted).lineLimit(1)
                } else {
                    Button {
                        actions.session(item.sourceId)
                    } label: {
                        HStack(spacing: 4) {
                            Text(title).type(T.meta, Theme.blue).lineLimit(1)
                            Arrow(Theme.blue, style: T.meta)
                        }
                        .padding(.vertical, 12).frame(minHeight: Theme.minHit).contentShape(Rectangle())
                    }
                    .buttonStyle(PressStyle())
                }
            }
            // The rules give way to a long title, down to a stub at each side.
            .layoutPriority(1)
            Rule().frame(minWidth: 16)
        }
        .padding(.horizontal, chatMargin)
    }
}

// MARK: Tools

/// A tool's line: a quiet mark, what it did in mono, and a note at the right
/// (design-system §6 「工具调用」). `bold` is the folded group's summary.
struct ChatToolLine<Trailing: View>: View {
    private let mark: LucideIcon
    private let color: Color
    private let title: String
    private let meta: String
    private let metaColor: Color
    private let bold: Bool
    private let trailing: Trailing
    @Environment(\.dynamicTypeSize) private var typeSize

    init(
        _ mark: LucideIcon, color: Color, title: String, meta: String = "", metaColor: Color = Theme.muted, bold: Bool = false,
        @ViewBuilder trailing: () -> Trailing
    ) {
        self.mark = mark
        self.color = color
        self.title = title
        self.meta = meta
        self.metaColor = metaColor
        self.bold = bold
        self.trailing = trailing()
    }

    private var name: some View {
        Text(title).type(bold ? T.sm.with(weight: .medium) : T.mono, bold ? Theme.muted : Theme.fg2)
            .lineLimit(1).truncationMode(.tail)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    var body: some View {
        Group {
            // A large text size leaves no room for the note beside the command: it goes underneath, at the right.
            if typeSize.isAccessibilitySize && !meta.isEmpty {
                VStack(alignment: .trailing, spacing: 0) {
                    HStack(spacing: 8) {
                        Mark(mark, color: color)
                        name
                        trailing
                    }
                    Text(meta).type(T.meta, metaColor).lineLimit(1)
                }
            } else {
                HStack(spacing: 8) {
                    Mark(mark, color: color)
                    name
                    if !meta.isEmpty { Text(meta).type(T.meta, metaColor).lineLimit(1).fixedSize() }
                    trailing
                }
            }
        }
        .padding(.vertical, 4)
        .frame(minHeight: 20)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(markWord + title)
        .accessibilityValue(meta)
    }

    /// The mark in a word, for VoiceOver: a row is not told apart by colour or shape alone.
    private var markWord: String {
        if mark == Lucide.check { return "已完成，" }
        if mark == Lucide.x { return "失败，" }
        if mark == Lucide.loaderCircle { return "进行中，" }
        return ""
    }
}

extension ChatToolLine where Trailing == EmptyView {
    init(_ mark: LucideIcon, color: Color, title: String, meta: String = "", metaColor: Color = Theme.muted, bold: Bool = false) {
        self.init(mark, color: color, title: title, meta: meta, metaColor: metaColor, bold: bold) { EmptyView() }
    }
}

private struct DoneToolRow: View {
    let call: PBToolCall
    let decision: String
    let decisionOk: Bool
    let env: ChatEnv

    var body: some View {
        let ok = toolOk(call)
        // A failed call shows its exit code and, when it needed one, its approval: 退出码 7 · 已允许.
        let parts = [!ok && call.hasExitCode ? "退出码 \(call.exitCode)" : "", decision].filter { !$0.isEmpty }
        let meta = parts.isEmpty ? (call.durationMs >= 1000 ? "\(call.durationMs / 1000)s" : "") : parts.joined(separator: " · ")
        ChatToolLine(
            ok ? Lucide.check : Lucide.x, color: ok ? Theme.muted : Theme.red,
            title: firstLine(toolTitle(call, cwd: env.cwd, home: env.home)), meta: meta,
            metaColor: !ok || !decisionOk ? Theme.red : Theme.muted)
    }
}

/// A call that went on in the background: its event is its start, and
/// nothing reliable says when it ends, so it is never shown as done.
private struct BackgroundToolRow: View {
    let call: PBToolCall
    let env: ChatEnv

    private func raw(_ s: String) -> Bool { s == "ToolCall" || s == "CommandExecution" }

    var body: some View {
        let generic = (call.title.isEmpty || raw(call.title)) && raw(call.itemType)
        ChatToolLine(
            Lucide.loaderCircle, color: Theme.blue,
            title: generic ? "后台运行" : firstLine(toolTitle(call, cwd: env.cwd, home: env.home)),
            meta: generic ? "" : "后台运行")
    }
}

/// A tool that started and has no result yet, where it started. Its timer
/// counts from the daemon's event time, the same on every client, and stops
/// where the connection dropped (architecture §13 「工具执行反馈」).
private struct RunningToolRow: View {
    let item: ChatItem.Running
    let env: ChatEnv

    var body: some View {
        let tool = item.tool
        let title = firstLine(toolTitle(tool, cwd: env.cwd, home: env.home))
        if item.unfinished && env.noResult.contains(tool.seq) {
            // The turn completed without this tool's result: say only that.
            ChatToolLine(Lucide.loaderCircle, color: Theme.faint, title: title, meta: "等待同步")
        } else {
            let state = state
            if state == .running {
                Ticking(every: 1) { now in
                    ChatToolLine(
                        Lucide.loaderCircle, color: Theme.blue, title: title,
                        meta: toolTimer(tool, state: state, now: now, frozenAt: env.frozenAt))
                }
            } else {
                ChatToolLine(
                    Lucide.loaderCircle, color: Theme.blue, title: title,
                    meta: state == .sync ? "等待同步" : item.unfinished ? "后台运行" : state.text)
            }
        }
    }

    /// How the tool reads now, by the package's rule; only the pending approvals matter to it.
    private var state: ToolState {
        var pending = Derived()
        pending.approvals = env.approvals
        return toolState(item.tool, unfinished: item.unfinished, d: pending, frozenAt: env.frozenAt)
    }
}

/// Two or more completed calls in a row, folded into one line; open, it lists the commands only.
private struct ToolGroupRow: View {
    let group: ChatItem.Tools
    let env: ChatEnv
    @Environment(ChatOpen.self) private var open
    @Environment(\.chatActions) private var actions

    private func seq(_ call: Int) -> UInt64 { call < group.callSeqs.count ? group.callSeqs[call] : 0 }

    var body: some View {
        let isOpen = open.isOpen(group.key)
        VStack(alignment: .leading, spacing: 0) {
            Button {
                open.toggle(group.key)
            } label: {
                ChatToolLine(Lucide.check, color: Theme.muted, title: toolsSummary(group.calls), bold: true) {
                    Arrow(Theme.muted, style: T.sm, direction: isOpen ? .up : .down)
                }
                .frame(minHeight: 28)
                // The line is 28 pt; to the finger it is 44.
                .padding(.vertical, 8).contentShape(Rectangle()).padding(.vertical, -8)
            }
            .buttonStyle(PressStyle())
            .accessibilityHint(isOpen ? "收起" : "展开")
            if isOpen {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(Array(group.calls.enumerated()), id: \.offset) { i, call in
                        Text(firstLine(toolTitle(call, cwd: env.cwd, home: env.home)))
                            .type(T.mono, Theme.fg2).lineLimit(1)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        // Each call's images stay under it when the group is open.
                        if !call.images.isEmpty {
                            ChatImageRow(call.images) { actions.image(seq(i), $0) }.padding(.leading, 21).padding(.bottom, 6)
                        }
                    }
                }
                .padding(.leading, 12)
            } else {
                // Screenshots stay in sight with the group closed.
                let all = group.calls.enumerated().flatMap { i, call in call.images.indices.map { (call: i, index: $0) } }
                if !all.isEmpty {
                    ChatImageRow(group.calls.flatMap(\.images)) { n in
                        if n < all.count { actions.image(seq(all[n].call), all[n].index) }
                    }
                    .padding(.leading, 21).padding(.bottom, 6)
                }
            }
        }
    }
}

// MARK: Things that open in place

/// A line that opens what is under it in place: 思考过程 and an arrow down.
struct ChatExpandable<Content: View>: View {
    private let title: String
    private let key: String
    private let content: Content
    @Environment(ChatOpen.self) private var open
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    init(_ title: String, key: String, @ViewBuilder content: () -> Content) {
        self.title = title
        self.key = key
        self.content = content()
    }

    var body: some View {
        let isOpen = open.isOpen(key)
        VStack(alignment: .leading, spacing: 0) {
            Button {
                withAnimation(reduceMotion ? nil : .easeOut(duration: 0.18)) { open.toggle(key) }
            } label: {
                ArrowText(isOpen ? "收起\(title)" : title, color: Theme.muted, style: T.sm, direction: isOpen ? .up : .down)
                    .padding(.vertical, 6)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.vertical, 6).contentShape(Rectangle()).padding(.vertical, -6)
            }
            .buttonStyle(PressStyle())
            if isOpen { content }
        }
    }
}

/// A proposed plan where it was proposed: its first lines, and the whole of it in place.
private struct PlanCard: View {
    let key: String
    let text: String
    @Environment(ChatOpen.self) private var open
    /// The preview does not show all of its six lines: they wrap past the six it is drawn in.
    @State private var cut = false

    var body: some View {
        let isOpen = open.isOpen(key)
        VStack(alignment: .leading, spacing: 0) {
            Text("计划").type(T.smB, Theme.fg2).accessibilityAddTraits(.isHeader)
            Group {
                if isOpen {
                    MarkdownView(text)
                } else {
                    CutText(planPreview(text, lines: 6), style: T.sm.with(lineHeight: 1.6), color: Theme.fg, lines: 6, cut: $cut)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.top, 4)
            // Whatever the preview leaves out can be opened, and what was opened can be closed again.
            if isOpen || cut || planFolds(text, lines: 6) {
                Button {
                    open.toggle(key)
                } label: {
                    ArrowText(isOpen ? "收起" : "展开全文", style: T.smB, direction: isOpen ? .up : .down)
                        .padding(.top, 4).padding(.vertical, 10).contentShape(Rectangle()).padding(.vertical, -10)
                }
                .buttonStyle(PressStyle())
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous))
        .padding(.horizontal, chatMargin).padding(.vertical, 8)
    }
}

/// The first line of a tool's title: a command of several lines is a row of one.
private func firstLine(_ s: String) -> String { s.jvmLines()[0] }

/// Whether a plan has more lines than its preview takes. A plan of fewer
/// lines may still be cut where they wrap, which only its layout knows.
func planFolds(_ text: String, lines: Int) -> Bool {
    text.jvmLines().count > lines
}

/// A plan's first lines as plain text, for where it is shown cut short.
func planPreview(_ text: String, lines: Int) -> String {
    text.jvmLines().map { plain($0) }.filter { !$0.jvmIsBlank }.prefix(lines).joined(separator: "\n")
}
