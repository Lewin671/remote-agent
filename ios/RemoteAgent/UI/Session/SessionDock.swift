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

/// One line until opened; open, its body scrolls within a limit while the
/// actions stay put, and the chat's keyboard goes away. `more` others wait behind it.
struct PendingDock<Content: View, Actions: View>: View {
    private let item: DockItem
    private let agent: String
    private let more: Int
    private let onOpen: () -> Void
    private let content: Content
    private let actions: Actions
    @State private var open: Bool
    @State private var bodyHeight: CGFloat = 0
    /// The room for an open item's body and actions; it grows with the text size.
    @ScaledMetric(relativeTo: .footnote) private var limit: CGFloat = 240
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.verticalSizeClass) private var vertical

    init(
        _ item: DockItem, agent: String, more: Int, startOpen: Bool = false, onOpen: @escaping () -> Void,
        @ViewBuilder content: () -> Content, @ViewBuilder actions: () -> Actions
    ) {
        self.item = item
        self.agent = agent
        self.more = more
        self.onOpen = onOpen
        self.content = content()
        self.actions = actions()
        _open = State(initialValue: startOpen)
    }

    /// 「● 类型 · 摘要 · 处理」 on one line. Where the type, the count of
    /// those waiting behind it and 处理 do not fit side by side, as with
    /// large text on a narrow screen, the type takes a line of its own
    /// (two if it must) and the rest goes under it, 处理 at the right
    /// (design-system §6 「长内容、大量内容与大字号」).
    private func head(_ summary: (label: String, brief: String)) -> some View {
        let dot = Dot(Theme.orange, size: 7)
        let label = Text(summary.label).type(T.sm, Theme.fg2)
        // The summary gives way, down to nothing: it asks for no width of its own. That more wait behind it is always said.
        let rest = HStack(spacing: 0) {
            Text(summary.brief).type(T.sm, Theme.muted).lineLimit(1).frame(idealWidth: 0)
            if more > 0 {
                Text((summary.brief.isEmpty ? "" : " · ") + "还有 \(more) 项").type(T.sm, Theme.muted).lineLimit(1).fixedSize()
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        let toggle = ArrowText(open ? "收起" : "处理", style: T.sm, direction: open ? .up : .down).fixedSize()
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

    var body: some View {
        let summary = dockSummary(item, agent: agent)
        VStack(spacing: 0) {
            Button {
                if !open { onOpen() }
                withAnimation(reduceMotion ? nil : .easeOut(duration: 0.2)) { open.toggle() }
            } label: {
                head(summary)
                    .frame(minHeight: Theme.minHit)
                    .contentShape(Rectangle())
            }
            .buttonStyle(PressStyle())
            .accessibilityElement(children: .combine)
            .accessibilityHint(open ? "收起" : "处理")
            if open {
                VStack(spacing: 0) {
                    ScrollView {
                        VStack(alignment: .leading, spacing: 0) { content }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.top, 4).padding(.bottom, 6)
                            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { bodyHeight = $0 }
                    }
                    .scrollBounceBehavior(.basedOnSize)
                    // As tall as what it holds, up to the limit: past it the body scrolls and the actions stay.
                    // At the largest text sizes the limit stops growing: the chat keeps some of the screen.
                    // On a phone on its side there is little height: the body keeps a few lines and scrolls, so
                    // that the actions and the message box under them stay on the screen.
                    .frame(height: min(max(bodyHeight, 1), vertical == .compact ? 96 : min(limit, 380)))
                    VStack(alignment: .leading, spacing: 0) { actions }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.top, 4).padding(.bottom, 10)
                }
                .transition(.opacity)
            }
        }
        .padding(.horizontal, chatMargin)
        .topLine()
    }
}

/// The first line of an open dock item: its type and, at the right, a note or a link.
struct DockHead<Extra: View>: View {
    private let title: String
    private let right: String
    private let rightColor: Color
    private let onRight: (() -> Void)?
    private let extra: Extra
    @Environment(\.dynamicTypeSize) private var typeSize

    init(_ title: String, right: String = "", rightColor: Color = Theme.muted, onRight: (() -> Void)? = nil, @ViewBuilder extra: () -> Extra) {
        self.title = title
        self.right = right
        self.rightColor = rightColor
        self.onRight = onRight
        self.extra = extra()
    }

    @ViewBuilder private var note: some View {
        if !right.isEmpty {
            if let onRight {
                Button(action: onRight) {
                    ArrowText(right, style: T.smB).padding(.vertical, 12).contentShape(Rectangle()).padding(.vertical, -12)
                }
                .buttonStyle(PressStyle())
            } else {
                Text(right).type(T.sm, rightColor).multilineTextAlignment(.trailing)
            }
        }
    }

    var body: some View {
        let head = Text(title).type(T.smB, Theme.fg2).accessibilityAddTraits(.isHeader)
        // Side by side; at a large text size the note goes under the title, at the right (design-system §6).
        if typeSize.isAccessibilitySize {
            VStack(alignment: .trailing, spacing: 2) {
                HStack(spacing: 8) {
                    head.frame(maxWidth: .infinity, alignment: .leading)
                    extra
                }
                note
            }
        } else {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                head.frame(maxWidth: .infinity, alignment: .leading)
                extra
                note
            }
        }
    }
}

extension DockHead where Extra == EmptyView {
    init(_ title: String, right: String = "", rightColor: Color = Theme.muted, onRight: (() -> Void)? = nil) {
        self.init(title, right: right, rightColor: rightColor, onRight: onRight) { EmptyView() }
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
                    if !left.isEmpty { Text(left).type(T.meta, Theme.muted).frame(maxWidth: .infinity, alignment: .leading) }
                    Button(action: onLink) {
                        Group {
                            if arrow { ArrowText(link, style: T.metaB) } else { Text(link).type(T.metaB, Theme.blue) }
                        }
                        .multilineTextAlignment(.trailing).hitTarget(alignment: .trailing)
                    }
                    .buttonStyle(PressStyle())
                }
            } else {
                HStack(spacing: 8) {
                    // With nothing at the left the link starts the line.
                    if !left.isEmpty { Text(left).type(T.meta, Theme.muted).frame(maxWidth: .infinity, alignment: .leading) }
                    TextAction(link, style: T.metaB, arrow: arrow, action: onLink)
                    if left.isEmpty { Spacer(minLength: 0) }
                }
            }
        }
        .padding(.top, 7)
    }
}

// MARK: Approvals

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

struct ApprovalDock: View {
    let session: SessionView
    let approval: PBApprovalRequested
    let more: Int
    var startOpen = false
    var startReason = false
    let onOpen: () -> Void
    let onDetail: () -> Void
    @Environment(\.onSoft) private var onSoft

    var body: some View {
        if isSkeleton(approval) { Text(skeletonMessage).type(T.sm, Theme.muted) } else { dock(approval) }
    }

    private func dock(_ a: PBApprovalRequested) -> some View {
        PendingDock(.approval(a), agent: agentName(session.agent), more: more, startOpen: startOpen, onOpen: onOpen) {
            Ticking(every: 15) { now in
                let left = ApprovalDeadline(a, now: now)
                DockHead(
                    approvalHead(a), right: left.map { $0.text + "，超时拒绝" } ?? "",
                    rightColor: left?.last == true ? Theme.orangeInk : Theme.muted)
            }
            if !a.description_p.isEmpty {
                Text(approvalDisplay(a.description_p)).type(T.sm, Theme.fg2).padding(.top, 4)
            }
            let text = approvalBody(a)
            if !text.isEmpty {
                let cut = text.jvmLines().count > 4 || text.count > 200
                Code(text, maxLines: 4)
                    .overlay(alignment: .bottomTrailing) {
                        if cut {
                            Button(action: onDetail) {
                                Text("展开全文").type(T.metaB, Theme.blue)
                                    .padding(.leading, 12).padding(.trailing, 6).padding(.bottom, 4)
                                    .background(onSoft ? Theme.card : Theme.soft)
                                    // 44 pt to the finger, drawn over the code's corner.
                                    .padding(.top, 20).padding(.leading, 20).contentShape(Rectangle())
                            }
                            .buttonStyle(PressStyle())
                        }
                    }
                    .padding(.vertical, 8)
            }
        } actions: {
            ApprovalButtons(session: session, approval: a, startReason: startReason)
        }
    }
}

/// 不允许 / 允许, and a reason for refusing, wherever an approval is drawn:
/// the session's dock, the approval's page and the home page's card
/// (Android's one `ApprovalButtons`). A tap locks both until the computer
/// confirms; an unknown outcome only offers to send the same decision
/// again. What was decided, and how far it got, is the account's
/// (`SessionStore.Resolve`), not this view's: a decision made on one screen
/// is locked on the others. `onCard`: on a grey card, where the buttons and
/// the reason's field are white.
struct ApprovalButtons: View {
    let session: SessionView
    let approval: PBApprovalRequested
    var startReason = false
    var onCard = false
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var reasonOpen = false
    @State private var started = false

    private func resolve(_ allow: Bool, message: String = "", always: Bool = false) {
        guard !isSkeleton(approval), !allow || !approvalTruncated(approval) else { return }
        if allow { SessionHaptics.decide() } else { Haptics.destructive() }
        let sent = context.sessionStore.decide(
            context.remote, machineId: session.machineId, sid: session.id, approvalId: approval.approvalID, allow: allow,
            message: message, always: always)
        // The computer has it: now it may be said to have gone through.
        Task { if await sent.value == true { Haptics.confirm() } }
    }

    private func retry() {
        guard !isSkeleton(approval), !approvalTruncated(approval) || !context.sessionStore.resolve(approval.approvalID).allow else { return }
        context.sessionStore.retryDecision(
            context.remote, machineId: session.machineId, sid: session.id, approvalId: approval.approvalID)
    }

    var body: some View {
        let store = context.sessionStore
        let id = approval.approvalID
        let r = store.resolve(id)
        let always = capabilities(model.state.machines[session.machineId])?.approvalAlways == true
        let surface: Color? = onCard ? Theme.card : nil
        let truncated = approvalTruncated(approval)
        VStack(alignment: .leading, spacing: 0) {
            if isSkeleton(approval) {
                Text(skeletonMessage).type(T.sm, Theme.muted)
            } else {
                if truncated { Text(approvalTruncatedMessage).type(T.sm, Theme.red) }
                if r.running || r.frozen || r.decided {
                    if r.running || (r.error.isEmpty && !r.frozen) {
                        Text(r.running ? "正在送到电脑…" : "已提交，等待电脑确认").type(T.meta, Theme.muted)
                            .frame(minHeight: 28)
                            .accessibilityAddTraits(.updatesFrequently)
                    }
                } else if reasonOpen || (startReason && !started) {
                    InputField(
                        text: Binding(get: { store.reason(id) }, set: { store.setReason(id, $0) }), placeholder: "拒绝理由（可选）",
                        lines: 1...4, background: surface, focusOnAppear: !startReason
                    )
                    .padding(.bottom, 8)
                    Btns {
                        Btn("取消", background: surface) {
                            reasonOpen = false
                            started = true
                        }
                        Btn("不允许", kind: .negative, background: surface) {
                            resolve(false, message: store.reason(id).jvmTrim())
                        }
                    }
                } else {
                    Btns {
                        Btn("不允许", kind: .negative, background: surface) { reasonOpen = true }
                        Btn("允许", kind: .primary, enabled: !truncated) { resolve(true) }
                    }
                    if always && !truncated {
                        TextAction("允许，这类请求以后不再问", alignment: .center) { resolve(true, always: true) }
                            .frame(maxWidth: .infinity)
                            // On a card its 44 pt to the finger reach into the card's own padding.
                            .padding(.bottom, onCard ? -8 : 0)
                    }
                }
                if !r.running && r.frozen {
                    Banner(
                        r.expired
                            ? "不确定「\(r.allow ? "允许" : "拒绝")」有没有送到，它已过期，不会再重发。审批超时后会自动拒绝"
                            : "没送到电脑：\(r.error.isEmpty ? "结果未知" : r.error)",
                        tone: .error, action: r.expired ? nil : "重试", onAction: retry
                    )
                    .padding(.top, 8)
                } else if !r.running && !r.error.isEmpty {
                    Banner("没送到电脑：\(r.error)", tone: .error, action: "重试", onAction: retry).padding(.top, 8)
                }
            }
        }
        .debugSignal("session.allow") { resolve(true) }
        .debugSignal("session.deny") { resolve(false, message: SessionDebug.text.jvmTrim()) }
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
                RoundedRectangle(cornerRadius: 2).fill(i <= at ? Theme.blue : Theme.track).frame(width: 16, height: 4)
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
            if part == .content { Text(skeletonMessage).type(T.sm, Theme.muted) }
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
        let progress = [count > 1 ? "第 \(index + 1) 题，共 \(count) 题" : "", cur.multiSelect ? "可多选" : ""]
            .filter { !$0.isEmpty }.joined(separator: " · ")
        if full {
            if !progress.isEmpty { Text(progress).type(T.meta, Theme.muted).padding(.top, 12) }
        } else {
            DockHead("\(agentName(session.agent)) 在问你", right: progress) {
                if count > 1 { QuestionSteps(count: count, at: index) }
            }
        }
        Text(cur.text).type(full ? T.h17 : T.bodyB, Theme.fg).lineLimit(full ? nil : 4)
            .padding(.top, full ? 8 : 6).padding(.bottom, full ? 12 : 6)
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
                option: option, multi: cur.multiSelect, selected: selected, submitting: selected && act.running && !cur.multiSelect
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
            Btn(
                act.running ? "提交中…" : picked.isEmpty ? "选好后提交" : "提交 \(picked.count) 项", kind: .primary,
                enabled: !act.running && !picked.isEmpty
            ) { submit(picked.sorted(), cur) }
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
    let onTap: () -> Void
    @ScaledMetric(relativeTo: .footnote) private var box: CGFloat = 18

    var body: some View {
        let label = optionLabel(option.label)
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous)
        Button(action: onTap) {
            HStack(alignment: .top, spacing: 8) {
                if multi {
                    RoundedRectangle(cornerRadius: 4, style: .continuous)
                        .fill(selected ? Theme.blueBg : Color.clear)
                        .overlay {
                            RoundedRectangle(cornerRadius: 4, style: .continuous)
                                .strokeBorder(selected ? Theme.blueBg : Theme.muted, lineWidth: 1)
                        }
                        .overlay { if selected { Ic(Lucide.check, size: 12, color: Theme.onSolid, relativeTo: .footnote) } }
                        .frame(width: box, height: box).padding(.top, 1)
                } else if selected {
                    Ic(Lucide.check, size: 13, color: Theme.blue, relativeTo: .footnote).padding(.top, 3)
                }
                VStack(alignment: .leading, spacing: 0) {
                    Text(label.label).type(T.sm, Theme.fg)
                    if !option.description_p.isEmpty { Text(option.description_p).type(T.meta, Theme.muted) }
                    if submitting { Text("提交中…").type(T.meta, Theme.muted) }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                if label.recommended { Text("推荐").type(T.metaB, Theme.blue) }
            }
            .multilineTextAlignment(.leading)
            .padding(.horizontal, 11).padding(.vertical, 8)
            .frame(maxWidth: .infinity, minHeight: Theme.minHit, alignment: .leading)
            .background(selected ? Theme.blueSoft : Color.clear, in: shape)
            .overlay { shape.strokeBorder(selected ? Theme.blue : Theme.line, lineWidth: 1) }
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
    var startOpen = false
    let onOpen: () -> Void
    let onTerminal: () -> Void
    let onLong: () -> Void
    @State private var act = ActionRunner()

    var body: some View {
        PendingDock(.question(question), agent: agentName(session.agent), more: more, startOpen: startOpen, onOpen: onOpen) {
            QuestionBody(session: session, question: question, part: .content, full: false, act: act, onLong: onLong)
        } actions: {
            QuestionBody(session: session, question: question, part: .actions, full: false, act: act, onTerminal: onTerminal)
        }
    }
}

// MARK: Plans

struct PlanDock: View {
    let session: SessionView
    let text: String
    let more: Int
    var startOpen = false
    let onOpen: () -> Void
    let onFull: () -> Void

    var body: some View {
        if isSkeleton(text) { Text(skeletonMessage).type(T.sm, Theme.muted) } else { dock }
    }

    private var dock: some View {
        PendingDock(.plan(text: text), agent: agentName(session.agent), more: more, startOpen: startOpen, onOpen: onOpen) {
            DockHead("计划待批准", right: "展开全文", onRight: onFull)
            Text(planPreview(text, lines: 4)).type(T.sm.with(lineHeight: 1.6), Theme.fg).lineLimit(4)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 10).padding(.vertical, 7)
                .background(Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.xs, style: .continuous))
                .padding(.vertical, 8)
            if session.agent == .claude { Text("批准后使用询问模式").type(T.meta, Theme.muted) }
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
        if terminalOnly { Text(skeletonMessage).type(T.sm, Theme.muted) } else { choices }
    }

    @ViewBuilder private var choices: some View {
        Btns {
            Btn("继续规划…", enabled: !act.running) { answer(.keepPlanning) }
            Btn(act.running ? "提交中…" : "批准执行", kind: .primary, enabled: !act.running) { answer(.implement) }
        }
        // Claude Code has no such choice.
        if full && session.agent != .claude {
            DockFoot("", link: "清空上下文后执行") { answer(.clearAndImplement) }
                .disabled(act.running)
        }
        if !act.error.isEmpty { Banner("没能提交：\(act.error)", tone: .error).padding(.top, 8) }
    }
}

// MARK: Popups in the terminal

struct PopupDock: View {
    let session: SessionView
    let title: String
    let more: Int
    var startOpen = false
    let onOpen: () -> Void
    let onTerminal: () -> Void

    var body: some View {
        let agent = agentName(session.agent)
        PendingDock(.popup(title: title), agent: agent, more: more, startOpen: startOpen, onOpen: onOpen) {
            DockHead("终端里有弹窗", right: "\(agent) 在等你")
            if !title.isEmpty { Text(title).type(T.body.with(weight: .medium), Theme.fg).padding(.vertical, 6) }
            let hint = popupHint(agent: session.agent, detail: title)
            if !hint.isEmpty { Text(hint).type(T.sm, Theme.orangeInk) }
        } actions: {
            Btn("去终端处理", kind: .primary, action: onTerminal)
        }
    }
}
