// What waits for the user, docked above the message box: an approval, a
// question, a plan, a popup in the terminal. Android's ui/Dock.kt; which
// item is docked and what its one line says are the package's (`DockItem`,
// `dockSummary`). docs/design-system.md §6 「等待事项（停靠区）」「审批」.
//
// Nothing here says a decision went through before the computer says so:
// a tap locks the card on a line of progress, the card goes away when the
// daemon's event withdraws it (whoever answered it, from whichever client),
// and a decision whose outcome is unknown is only ever sent again as it
// was (architecture §10 「指令」).
import RACore
import RAProto
import SwiftUI
import UIKit

/// The taps of the session screens (docs: iOS-native polish). A decision
/// ticks when it is made; the success tick waits for the computer's answer.
@MainActor
enum SessionHaptics {
    static func tap() { UIImpactFeedbackGenerator(style: .light).impactOccurred() }
    static func decide() { UIImpactFeedbackGenerator(style: .medium).impactOccurred() }
}

/// What an open dock item says after its type, in place of the summary: how long it still waits, how
/// far through several questions. `urgent` turns it orange (an approval's last minute, design-system §1).
struct DockNote: Equatable {
    var text = ""
    var urgent = false
}

extension EnvironmentValues {
    /// How tall the chat pane is that the dock sits in: an open item's body takes up to about 60% of it.
    @Entry var chatHeight: CGFloat = 0
}

/// Open from the start, until the user folds it: one line then, 「● 类型 ·
/// 摘要 · 处理」. Open, the line keeps only the type and what is said of it
/// (`note`, and how many more wait), and the body under it scrolls within
/// about 60% of the chat pane, at least 240, while the actions stay put.
/// Whether it is open is the caller's, kept by the item's ID
/// (`SessionStore.dockOpen`), so a folded item stays folded and the next
/// one opens, unless it comes while the message box holds a draft.
struct PendingDock<Content: View, Actions: View>: View {
    private let item: DockItem
    private let agent: String
    private let more: Int
    private let title: String?
    private let note: DockNote
    @Binding private var open: Bool
    private let onOpen: () -> Void
    private let extra: AnyView?
    private let content: Content
    private let actions: Actions
    @State private var bodyHeight: CGFloat = 0
    /// The least room for an open item's body; it grows with the text size.
    @ScaledMetric(relativeTo: .footnote) private var least: CGFloat = 240
    @Environment(\.chatHeight) private var chatHeight
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.verticalSizeClass) private var vertical
    @Environment(\.dynamicTypeSize) private var typeSize

    /// `extra`, a link of the open item's own (the plan's 展开全文), goes in its line before 收起.
    init(
        _ item: DockItem, agent: String, more: Int, title: String? = nil, note: DockNote = DockNote(), open: Binding<Bool>,
        onOpen: @escaping () -> Void, extra: AnyView? = nil, @ViewBuilder content: () -> Content,
        @ViewBuilder actions: () -> Actions
    ) {
        self.item = item
        self.agent = agent
        self.more = more
        self.title = title
        self.note = note
        _open = open
        self.onOpen = onOpen
        self.extra = extra
        self.content = content()
        self.actions = actions()
    }

    /// What follows the type: folded, the summary and how many more wait; open, how many more wait and
    /// the note. It gives way, down to nothing: it asks for no width of its own. That more wait behind
    /// it is always said.
    @ViewBuilder private func rest(_ brief: String) -> some View {
        Group {
            if open {
                let count = more > 0 ? "还有 \(more) 项" : ""
                let gap = !count.isEmpty && !note.text.isEmpty ? " · " : ""
                Text("\(Text(count + gap).foregroundStyle(DS.ink2))\(Text(note.text).foregroundStyle(note.urgent ? DS.orange : DS.ink2))")
                    .font(.footnote).lineLimit(1).frame(idealWidth: 0)
            } else {
                HStack(spacing: 0) {
                    Text(brief).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1).frame(idealWidth: 0)
                    if more > 0 {
                        Text((brief.isEmpty ? "" : " · ") + "还有 \(more) 项").font(.footnote).foregroundStyle(DS.ink2).lineLimit(1).fixedSize()
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// One line: 「● 类型 · 摘要 · 处理」 folded, 「● 类型　还有 N 项 · 剩 X 分钟　收起」 open. Where the
    /// type, what follows it and the toggle do not fit side by side, as with large text on a narrow
    /// screen, the type takes a line of its own (two if it must) and the rest goes under it, the
    /// toggle at the right (design-system §6 「长内容、大量内容与大字号」).
    private func head(_ summary: (label: String, brief: String), withToggle: Bool = true) -> some View {
        let dot = StatusMark(.waiting)
        // Open, the summary is not there to say what the type leaves out: `title` says it whole.
        // Only the dot and the type word are orange (design-system §6 「等待事项」).
        let label = Text(open ? title ?? summary.label : summary.label).font(.footnote.weight(.semibold)).foregroundStyle(DS.orange)
        let rest = rest(summary.brief)
        let toggle = Group { if withToggle { toggleText } }
        return ViewThatFits(in: .horizontal) {
            HStack(spacing: 6) {
                dot
                label.lineLimit(1).fixedSize()
                rest
                toggle
            }
            VStack(alignment: .leading, spacing: 0) {
                HStack(spacing: 6) {
                    dot
                    label.lineLimit(2).fixedSize(horizontal: false, vertical: true)
                }
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 6) {
                        rest
                        toggle
                    }
                    VStack(alignment: .trailing, spacing: 0) {
                        rest
                        toggle
                    }
                }
            }
            .padding(.vertical, 8)
        }
    }

    /// The most an open item's body takes. On a phone on its side there is little height: the body
    /// keeps a few lines and scrolls, so that the actions and the message box under them stay on the
    /// screen. At the accessibility text sizes the actions stack and grow, and the line above the body
    /// is several: the body keeps about a third, or the line and the actions would leave the screen.
    private var room: CGFloat {
        if vertical == .compact { return 96 }
        if typeSize.isAccessibilitySize { return max(160, chatHeight * 0.35) }
        return max(least, chatHeight * 0.6)
    }

    private var toggleText: some View {
        HStack(spacing: 2) {
            Text(open ? "收起" : "处理")
            Image(systemName: open ? "chevron.up" : "chevron.down").imageScale(.small)
        }
        .font(.footnote)
        .foregroundStyle(DS.blue)
        .fixedSize()
    }

    private func toggle() {
        if !open { onOpen() }
        withAnimation(reduceMotion ? nil : .easeOut(duration: 0.2)) { open.toggle() }
    }

    var body: some View {
        // The dark card's shell, docked over the chat: open, the whole card; folded, one dark capsule.
        Group {
            if open {
                DarkCard(floating: true) { card }
            } else {
                card
                    .padding(.horizontal, 16)
                    .background(DS.tc, in: Capsule())
                    .overlay { Capsule().strokeBorder(DS.tcLine, lineWidth: 1) }
                    .environment(\.colorScheme, .dark)
                    .floatShadow()
            }
        }
        .padding(.horizontal, DS.Space.m)
        .padding(.bottom, 8)
    }

    private var card: some View {
        let summary = dockSummary(item, agent: agent)
        return VStack(spacing: 0) {
            if open, let extra {
                // A link of its own sits between the line and 收起, so the line is two buttons around it.
                HStack(spacing: 12) {
                    Button(action: toggle) {
                        head(summary, withToggle: false)
                            .frame(minHeight: DS.minHit)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityElement(children: .combine)
                    .accessibilityHint("收起")
                    extra
                    Button(action: toggle) {
                        toggleText.frame(minHeight: DS.minHit).contentShape(Rectangle())
                    }
                    .buttonStyle(PressStyle())
                    .accessibilityHidden(true)
                }
            } else {
                Button(action: toggle) {
                    head(summary)
                        .frame(minHeight: DS.minHit)
                        .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .accessibilityElement(children: .combine)
                .accessibilityHint(open ? "收起" : "处理")
            }
            if open {
                VStack(spacing: 0) {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 0) { content }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.bottom, 6)
                            .onGeometryChange(for: CGFloat.self) { g in ChatDiagnosticRing.shared.measure(.dockBody) { g.size.height } } action: {
                                ChatDiagnosticRing.shared.record(.dockBody, Double($0)); bodyHeight = $0
                            }
                    }
                    .scrollBounceBehavior(.basedOnSize)
                    // As tall as what it holds, up to the room: past it the body scrolls and the actions stay.
                    .frame(height: min(max(bodyHeight, 1), room))
                    VStack(alignment: .leading, spacing: 0) { actions }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.top, 4).padding(.bottom, 10)
                }
                .transition(.opacity)
            }
        }
    }
}

/// The grey line under a dock's buttons: a question at the left, one link at the right, with an arrow when it goes somewhere else.
struct DockFoot: View {
    private let left: String
    private let link: String
    private let arrow: Bool
    private let onLink: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    init(_ left: String, link: String, arrow: Bool = false, onLink: @escaping () -> Void) {
        self.left = left
        self.link = link
        self.arrow = arrow
        self.onLink = onLink
    }

    var body: some View {
        Group {
            if typeSize.isAccessibilitySize {
                // No room for both on a line: the link goes under the question, at the right.
                VStack(alignment: .trailing, spacing: 0) {
                    if !left.isEmpty { Text(left).type(T.meta, DS.ink2).frame(maxWidth: .infinity, alignment: .leading) }
                    Button(action: onLink) {
                        Group {
                            if arrow { ArrowText(link, style: T.metaB) } else { Text(link).type(T.metaB, DS.blue) }
                        }
                        .multilineTextAlignment(.trailing).hitTarget(alignment: .trailing)
                    }
                    .buttonStyle(PressStyle())
                }
            } else {
                HStack(spacing: 8) {
                    // With nothing at the left the link starts the line.
                    if !left.isEmpty { Text(left).type(T.meta, DS.ink2).frame(maxWidth: .infinity, alignment: .leading) }
                    TextAction(link, style: T.metaB, arrow: arrow, action: onLink)
                    if left.isEmpty { Spacer(minLength: 0) }
                }
            }
        }
        .padding(.top, 7)
    }
}

// MARK: Approvals

/// The widest an approval's or a plan's actions get: on a tablet they keep to the left, not the whole width.
let actionsWidth: CGFloat = 480

/// How long an approval still waits: muted, and orange only in its last minute (design-system §1).
struct ApprovalDeadline: Equatable {
    var text: String
    var last: Bool

    init?(_ a: PBApprovalRequested, now: Int64) {
        guard a.hasDeadline else { return nil }
        let deadline = a.deadline.seconds * 1000
        text = "剩 \(minutesLeft(deadlineMs: deadline, now: now)) 分钟"
        last = deadline - now < 60_000
    }
}

/// An approval, docked: the whole command scrolls above the decision,
/// which stays put; decided, the card is one line of where that stands.
struct ApprovalDock: View {
    let session: SessionView
    let approval: PBApprovalRequested
    let more: Int
    @Binding var open: Bool
    var startReason = false
    let onOpen: () -> Void
    @Environment(AccountContext.self) private var context
    @State private var denying = false
    @State private var chosen: Bool?

    var body: some View {
        Ticking(every: 15) { now in
            let left = ApprovalDeadline(approval, now: now)
            if context.sessionStore.resolve(approval.approvalID).submitted {
                ApprovalSubmitted(session: session, approval: approval, chosen: chosen)
                    .padding(.horizontal, DS.Space.m).padding(.bottom, 8)
            } else {
                PendingDock(
                    .approval(approval), agent: agentName(session.agent), more: more, title: approvalHead(approval),
                    note: DockNote(text: left?.text ?? "", urgent: left?.last == true), open: $open, onOpen: onOpen
                ) {
                    ApprovalContent(approval: approval, denying: denying)
                } actions: {
                    if !isSkeleton(approval) {
                        ApprovalDecision(session: session, approval: approval, now: now, denying: $denying, chosen: $chosen)
                    }
                }
            }
        }
        .onAppear { if startReason { denying = true } }
    }
}

// MARK: Questions

/// 第 1 题 of several, as short bars.
private struct QuestionSteps: View {
    let count: Int
    let at: Int

    var body: some View {
        HStack(spacing: 4) {
            ForEach(0..<count, id: \.self) { i in
                RoundedRectangle(cornerRadius: 2).fill(i <= at ? DS.blue : DS.fill2).frame(width: 16, height: 4)
            }
        }
        .accessibilityHidden(true)
    }
}

/// Answers the current question by option; several questions go one by
/// one. One choice answers at a tap; a multiple choice ticks, then 提交 N 项.
/// The dock shows its two halves apart, the scrolling body and the fixed
/// actions; the page (`full`) lays them out the same way, larger.
struct QuestionBody: View {
    enum Part { case content, actions }

    let session: SessionView
    let question: PBQuestionAsked
    let part: Part
    let full: Bool
    /// One submission for both halves.
    let act: ActionRunner
    var onTerminal: () -> Void = {}
    var onLong: () -> Void = {}
    var onAnswered: () -> Void = {}
    @Environment(AccountContext.self) private var context

    private var store: SessionStore { context.sessionStore }
    private var qid: String { question.questionID }
    private var index: Int { store.questionIndex[qid] ?? 0 }
    private var current: PBQuestion? {
        let qs = question.questions
        return index < qs.count ? qs[index] : qs.last
    }

    private func submit(_ options: [Int], _ cur: PBQuestion) {
        guard !isSkeleton(question) else { return }
        let (qi, multi, count) = (index, cur.multiSelect, question.questions.count)
        let (store, remote, s, qid) = (store, context.remote, session, qid)
        let answer = store.answer(qid, qi)
        let done = onAnswered
        SessionHaptics.decide()
        act.run({
            let sent = try await store.sendAnswer(remote, answer, options: options) { picked in
                var cmd = PBCommand()
                cmd.answerQuestion = .with {
                    $0.sessionID = s.id
                    $0.questionID = qid
                    if multi { $0.options = picked.map { Int32($0 + 1) } } else { $0.option = Int32((picked.first ?? 0) + 1) }
                    if count > 1 { $0.questionIndex = Int32(qi) }
                }
                return try remote.prepare(machineId: s.machineId, sessionId: s.id, cmd)
            }
            // Show what was answered, which after an unknown outcome is the earlier answer.
            if multi { store.multiPick["\(qid)#\(qi)"] = Set(sent) } else { store.singlePick["\(qid)#\(qi)"] = sent.first }
        }, onDone: {
            if qi + 1 < count { store.questionIndex[qid] = qi + 1 } else { done() }
        })
    }

    var body: some View {
        if isSkeleton(question) {
            if part == .content { Text(skeletonMessage).type(T.sm, DS.ink2) }
        } else if let cur = current {
            switch part {
            case .content: content(cur)
            case .actions: actions(cur)
            }
        }
    }

    @ViewBuilder private func content(_ cur: PBQuestion) -> some View {
        let count = question.questions.count
        let key = "\(qid)#\(index)"
        let answer = store.answer(qid, index)
        let progress = questionProgress(count: count, at: index, multi: cur.multiSelect)
        if full {
            if !progress.isEmpty { Text(progress).type(T.meta, DS.ink2).padding(.top, 12) }
        } else if count > 1 {
            // The dock's line says which of them it is; the steps show it.
            QuestionSteps(count: count, at: index).padding(.top, 4)
        }
        Text(cur.text).type(full ? T.h17 : T.bodyB, DS.ink).lineLimit(full ? nil : 4)
            .padding(.top, full ? 8 : 4).padding(.bottom, full ? 12 : 6)
            .accessibilityAddTraits(.isHeader)
        if !full && longQuestion(cur) {
            Button(action: onLong) {
                ArrowText("展开全文", style: T.smB).padding(.bottom, 4)
                    .padding(.vertical, 12).contentShape(Rectangle()).padding(.vertical, -12)
            }
            .buttonStyle(PressStyle())
        }
        ForEach(Array(cur.options.enumerated()), id: \.offset) { i, option in
            let selected = cur.multiSelect ? store.multiPick[key]?.contains(i) == true : store.singlePick[key] == i
            QuestionOptionRow(
                option: option, multi: cur.multiSelect, selected: selected, submitting: selected && act.running && !cur.multiSelect,
                dark: !full
            ) {
                if cur.multiSelect {
                    var set = store.multiPick[key] ?? []
                    if set.remove(i) == nil { set.insert(i) }
                    store.multiPick[key] = set
                    Haptics.selection()
                } else {
                    store.singlePick[key] = i
                    submit([i], cur)
                }
            }
            .disabled(act.running)
        }
        if !act.running && answer.attempt.frozen {
            let shown = answer.options.map { i in i < cur.options.count ? optionLabel(cur.options[i].label).label : "\(i + 1)" }
                .joined(separator: "、")
            Banner(
                answer.attempt.expired
                    ? "不确定「\(shown)」有没有提交，它已过期，不会再重发。请去终端看看"
                    : "上次的回答「\(shown)」没能确认，终端里的问题可能已经变了。再点任一选项会重发同一个回答",
                tone: .error, action: answer.attempt.expired ? "放弃" : nil,
                onAction: {
                    store.abandon(answer)
                    act.error = ""
                }
            )
            .padding(.top, 6)
        } else if !act.error.isEmpty {
            Banner("没能提交，终端里的问题可能已经变了：\(act.error)", tone: .error).padding(.top, 6)
        }
    }

    @ViewBuilder private func actions(_ cur: PBQuestion) -> some View {
        if cur.multiSelect {
            let picked = store.multiPick["\(qid)#\(index)"] ?? []
            let label = act.running ? "提交中…" : picked.isEmpty ? "选好后提交" : "提交 \(picked.count) 项"
            Group {
                if full {
                    Btn(label, kind: .primary, enabled: !act.running && !picked.isEmpty) { submit(picked.sorted(), cur) }
                } else {
                    // On the dark card the main button is white (design-system §6 「深色卡里的按钮」).
                    Button(label) { submit(picked.sorted(), cur) }
                        .buttonStyle(.darkCard(primary: true))
                        .disabled(act.running || picked.isEmpty)
                }
            }
            .padding(.top, 6)
            DockFoot(picked.isEmpty ? "" : "已选 \(picked.count) 项", link: "其他回答：去终端", arrow: true, onLink: onTerminal)
        } else {
            DockFoot("都不合适？", link: "其他回答：去终端", arrow: true, onLink: onTerminal)
        }
    }
}

private struct QuestionOptionRow: View {
    let option: PBQuestionOption
    let multi: Bool
    let selected: Bool
    let submitting: Bool
    /// On the dark card: translucent rows, the chosen one white with black text (design-system §6 「等待事项」).
    var dark = false
    let onTap: () -> Void
    @ScaledMetric(relativeTo: .footnote) private var box: CGFloat = 18

    var body: some View {
        let label = optionLabel(option.label)
        let shape = RoundedRectangle(cornerRadius: dark ? 14 : Theme.Radius.sm, style: .continuous)
        let ink: Color = dark ? (selected ? .black : .white) : DS.ink
        let second: Color = dark ? (selected ? Color.black.opacity(0.6) : DS.ink2) : DS.ink2
        Button(action: onTap) {
            HStack(alignment: .top, spacing: 8) {
                if multi {
                    Image(systemName: selected ? "checkmark.square.fill" : "square")
                        .foregroundStyle(dark ? ink : (selected ? DS.blue : DS.ink2))
                        .frame(width: box, height: box).padding(.top, 1)
                } else if selected {
                    Image(systemName: "checkmark").font(.footnote.weight(.semibold)).foregroundStyle(dark ? ink : DS.blue).padding(.top, 3)
                }
                VStack(alignment: .leading, spacing: 1) {
                    Text(label.label).font(dark ? .body : .footnote).foregroundStyle(ink)
                    if !option.description_p.isEmpty { Text(option.description_p).font(dark ? .footnote : .caption).foregroundStyle(second) }
                    if submitting { Text("提交中…").font(.caption).foregroundStyle(second) }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                if label.recommended { Text("推荐").font(.caption.weight(.semibold)).foregroundStyle(dark ? (selected ? Color.black : DS.blue) : DS.blue) }
            }
            .multilineTextAlignment(.leading)
            .padding(.horizontal, dark ? 14 : 11).padding(.vertical, dark ? 11 : 8)
            .frame(maxWidth: .infinity, minHeight: DS.minHit, alignment: .leading)
            .background(dark ? (selected ? Color.white : Color.white.opacity(0.1)) : (selected ? DS.fill : Color.clear), in: shape)
            .overlay { if !dark { shape.strokeBorder(selected ? DS.blue : DS.sep, lineWidth: 1) } }
            .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .padding(.vertical, 3)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

struct QuestionDock: View {
    let session: SessionView
    let question: PBQuestionAsked
    let more: Int
    @Binding var open: Bool
    let onOpen: () -> Void
    let onTerminal: () -> Void
    let onLong: () -> Void
    @Environment(AccountContext.self) private var context
    @State private var act = ActionRunner()

    var body: some View {
        let qs = question.questions
        let index = min(context.sessionStore.questionIndex[question.questionID] ?? 0, max(qs.count - 1, 0))
        let progress = questionProgress(count: qs.count, at: index, multi: qs.isEmpty ? false : qs[index].multiSelect)
        PendingDock(
            .question(question), agent: agentName(session.agent), more: more, note: DockNote(text: progress), open: $open,
            onOpen: onOpen
        ) {
            QuestionBody(session: session, question: question, part: .content, full: false, act: act, onLong: onLong)
        } actions: {
            QuestionBody(session: session, question: question, part: .actions, full: false, act: act, onTerminal: onTerminal)
        }
    }
}

/// 第 1 题，共 3 题 · 可多选: where the user is among several questions, and whether this one takes several answers.
func questionProgress(count: Int, at index: Int, multi: Bool) -> String {
    [count > 1 ? "第 \(index + 1) 题，共 \(count) 题" : "", multi ? "可多选" : ""].filter { !$0.isEmpty }.joined(separator: " · ")
}

// MARK: Plans

struct PlanDock: View {
    let session: SessionView
    let text: String
    let more: Int
    @Binding var open: Bool
    let onOpen: () -> Void
    let onFull: () -> Void

    var body: some View {
        if isSkeleton(text) { Text(skeletonMessage).type(T.sm, DS.ink2) } else { dock }
    }

    private var dock: some View {
        PendingDock(
            .plan(text: text), agent: agentName(session.agent), more: more, open: $open, onOpen: onOpen,
            // In the open line before 收起, as on the web (design-system §6).
            extra: AnyView(TextAction("展开全文", style: T.sm, alignment: .trailing, arrow: true, action: onFull))
        ) {
            // The whole plan, scrolling with the dock's body; 展开全文 is the page that draws it as written.
            Text(planPreview(text, lines: .max)).font(.subheadline).lineSpacing(3).foregroundStyle(DS.ink)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.top, 4).padding(.bottom, 4)
        } actions: {
            PlanButtons(session: session, full: false, terminalOnly: isSkeleton(text))
        }
    }
}

/// 继续规划… / 批准执行; the page (`full`) also offers Codex's 清空上下文后执行.
struct PlanButtons: View {
    let session: SessionView
    let full: Bool
    var terminalOnly = false
    var onDone: () -> Void = {}
    @Environment(AccountContext.self) private var context
    @State private var act = ActionRunner()

    private func answer(_ choice: PBPlanChoice) {
        guard !terminalOnly else { return }
        let (remote, sid) = (context.remote, session.id)
        SessionHaptics.decide()
        act.run({
            var cmd = PBCommand()
            cmd.answerPlan = .with {
                $0.sessionID = sid
                $0.choice = choice
            }
            try await remote.command(sid, cmd)
        }, onDone: { onDone() })
    }

    var body: some View {
        if terminalOnly {
            Text(skeletonMessage).type(T.sm, DS.ink2)
        } else {
            VStack(alignment: .leading, spacing: 0) { choices }
                .frame(maxWidth: actionsWidth, alignment: .leading)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    @ViewBuilder private var choices: some View {
        if full {
            Btns {
                Btn(agentInfo(session.agent).planKeepLabel, enabled: !act.running) { answer(.keepPlanning) }
                Btn(act.running ? "提交中…" : agentInfo(session.agent).planApproveLabel, kind: .primary, enabled: !act.running) { answer(.implement) }
            }
        } else {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 8) { darkChoices }
                VStack(spacing: 8) { darkChoices }
            }
        }
        if let note = agentInfo(session.agent).planNote {
            Text(note).type(T.meta, DS.ink2).padding(.top, 7)
        }
        if full && agentInfo(session.agent).planClearContext {
            DockFoot("", link: "清空上下文后执行") { answer(.clearAndImplement) }
                .disabled(act.running)
        }
        if !act.error.isEmpty { Banner("没能提交：\(act.error)", tone: .error).padding(.top, 8) }
    }

    /// On the dark card: 批准 is the white one.
    @ViewBuilder private var darkChoices: some View {
        Button(agentInfo(session.agent).planKeepLabel) { answer(.keepPlanning) }.buttonStyle(.darkCard()).disabled(act.running)
        Button(act.running ? "提交中…" : agentInfo(session.agent).planApproveLabel) { answer(.implement) }
            .buttonStyle(.darkCard(primary: true)).disabled(act.running)
    }
}

// MARK: Popups in the terminal

struct PopupDock: View {
    let session: SessionView
    let title: String
    let more: Int
    @Binding var open: Bool
    let onOpen: () -> Void
    let onTerminal: () -> Void

    var body: some View {
        let agent = agentName(session.agent)
        PendingDock(
            .popup(title: title), agent: agent, more: more, note: DockNote(text: "\(agent) 在等你"), open: $open, onOpen: onOpen
        ) {
            if !title.isEmpty { Text(title).font(.body.weight(.medium)).foregroundStyle(DS.ink).padding(.bottom, 6) }
            let hint = popupHint(agent: session.agent, detail: title)
            if !hint.isEmpty { Text(hint).font(.footnote).foregroundStyle(DS.orangeInk) }
        } actions: {
            Button("去终端处理", action: onTerminal).buttonStyle(.darkCard(primary: true))
        }
    }
}
