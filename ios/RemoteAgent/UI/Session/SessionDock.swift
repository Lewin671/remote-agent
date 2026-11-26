// What waits for the user, docked above the message box: an approval, a
// question, a plan, or a terminal popup, rendered directly from Core's
// `PBSessionDockView`. Commands submit via Core semantic actions and
// command status comes from `PBSessionDockView.command`.
import RACore
import RAProto
import SwiftUI
import UIKit

/// The taps of the session screens (docs: iOS-native polish). A decision
/// ticks when it is made; the success tick waits for the computer's answer.
@MainActor
enum SessionHaptics {
    static func tap() { UIImpactFeedbackGenerator(style: .light).impactOccurred() }
}

/// What an open dock item says after its type, in place of the summary.
struct DockNote: Equatable {
    var text = ""
    var urgent = false
}

extension EnvironmentValues {
    /// How tall the chat pane is that the dock sits in: an open item's body takes up to about 60% of it.
    @Entry var chatHeight: CGFloat = 0
}

func dockFoldKey(sessionID: String, _ dock: PBSessionDockView) -> String {
    "\(sessionID):\(dock.kind.rawValue):\(dock.itemID)"
}

/// Open from the start, until the user folds it: one line then, 「● 类型 ·
/// 摘要 · 处理」.
struct PendingDock<Content: View, Actions: View>: View {
    private let summaryLabel: String
    private let summaryBrief: String
    private let openTitle: String?
    private let more: Int
    private let note: DockNote
    @Binding private var open: Bool
    private let onOpen: () -> Void
    private let extra: AnyView?
    private let content: Content
    private let actions: Actions
    @State private var bodyHeight: CGFloat = 0
    @ScaledMetric(relativeTo: .footnote) private var least: CGFloat = 240
    @Environment(\.chatHeight) private var chatHeight
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.verticalSizeClass) private var vertical
    @Environment(\.dynamicTypeSize) private var typeSize

    init(
        label: String, brief: String, openTitle: String? = nil, more: Int, note: DockNote = DockNote(),
        open: Binding<Bool>, onOpen: @escaping () -> Void, extra: AnyView? = nil,
        @ViewBuilder content: () -> Content, @ViewBuilder actions: () -> Actions
    ) {
        self.summaryLabel = label
        self.summaryBrief = brief
        self.openTitle = openTitle
        self.more = more
        self.note = note
        _open = open
        self.onOpen = onOpen
        self.extra = extra
        self.content = content()
        self.actions = actions()
    }

    @ViewBuilder private var rest: some View {
        Group {
            if open {
                let count = more > 0 ? L10n.dockMore(count: Int(more)) : ""
                let gap = !count.isEmpty && !note.text.isEmpty ? " · " : ""
                Text("\(Text(count + gap).foregroundStyle(DS.ink2))\(Text(note.text).foregroundStyle(note.urgent ? DS.orange : DS.ink2))")
                    .font(.footnote).fixedSize(horizontal: false, vertical: true)
            } else {
                HStack(spacing: 0) {
                    Text(summaryBrief).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1).frame(idealWidth: 0)
                    if more > 0 {
                        Text((summaryBrief.isEmpty ? "" : " · ") + L10n.dockMore(count: Int(more))).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1).fixedSize()
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func head(withToggle: Bool = true) -> some View {
        let dot = StatusMark(.waiting)
        let label = Text(open ? (openTitle ?? summaryLabel) : summaryLabel).font(.footnote.weight(.semibold)).foregroundStyle(DS.orange)
        let toggle = Group { if withToggle { toggleText } }
        return ViewThatFits(in: .horizontal) {
            HStack(spacing: 6) {
                dot
                label.lineLimit(1).fixedSize()
                rest.fixedSize(horizontal: open, vertical: false)
                if open { Spacer(minLength: 0) }
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

    private var room: CGFloat {
        if vertical == .compact { return 96 }
        if typeSize.isAccessibilitySize { return max(160, chatHeight * 0.35) }
        return max(least, chatHeight * 0.6)
    }

    private var toggleText: some View {
        HStack(spacing: 2) {
            Text(open ? L10n.markdownCollapse() : L10n.dockHandle())
            Image(systemName: open ? "chevron.up" : "chevron.down").imageScale(.small)
        }
        .font(.footnote)
        .foregroundStyle(DS.blue)
        .fixedSize()
    }

    private func toggle() {
        if !open { onOpen() }
        withAnimation(reduceMotion ? nil : Motion.enter()) { open.toggle() }
    }

    var body: some View {
        Group {
            if open {
                DarkCard(floating: true) { card }
                    .transition(Motion.roomy())
            } else {
                card
                    .padding(.horizontal, 16)
                    .background(DS.tc, in: Capsule())
                    .overlay { Capsule().strokeBorder(DS.tcLine, lineWidth: 1) }
                    .environment(\.colorScheme, .dark)
                    .floatShadow()
                    .transition(Motion.roomy())
            }
        }
        .padding(.horizontal, DS.Space.m)
        .padding(.bottom, 8)
    }

    private var card: some View {
        VStack(spacing: 0) {
            if open, let extra {
                HStack(spacing: 12) {
                    Button(action: toggle) {
                        head(withToggle: false)
                            .frame(minHeight: DS.minHit)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityElement(children: .combine)
                    .accessibilityHint(L10n.markdownCollapse())
                    extra
                    Button(action: toggle) {
                        toggleText.frame(minHeight: DS.minHit).contentShape(Rectangle())
                    }
                    .buttonStyle(PressStyle())
                    .accessibilityHidden(true)
                }
            } else {
                Button(action: toggle) {
                    head()
                        .frame(minHeight: DS.minHit)
                        .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .accessibilityElement(children: .combine)
                .accessibilityHint(open ? L10n.markdownCollapse() : L10n.dockHandle())
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

/// The grey line under a dock's buttons.
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
        ViewThatFits(in: .horizontal) {
            if !typeSize.isAccessibilitySize {
                HStack(spacing: 8) {
                    if !left.isEmpty {
                        Text(left).font(.footnote).ink(DS.ink2).fixedSize()
                        Spacer(minLength: 0)
                    }
                    TextAction(link, style: .footnote.weight(.semibold), arrow: arrow, action: onLink)
                    if left.isEmpty { Spacer(minLength: 0) }
                }
            }
            VStack(alignment: .trailing, spacing: 0) {
                if !left.isEmpty { Text(left).font(.footnote).ink(DS.ink2).frame(maxWidth: .infinity, alignment: .leading) }
                Button(action: onLink) {
                    Group {
                        if arrow { ArrowText(link, style: .footnote.weight(.semibold)) } else { Text(link).font(.footnote.weight(.semibold)).ink(DS.blue) }
                    }
                    .multilineTextAlignment(.trailing).fixedSize(horizontal: false, vertical: true)
                    .hitTarget(alignment: .trailing)
                }
                .buttonStyle(PressStyle())
            }
        }
        .padding(.top, 7)
    }
}

// MARK: External Resolution Notice

/// Brief confirmation line when a docked item was handled in the terminal or on another device.
struct ExternalResolutionLine: View {
    let text: String

    var body: some View {
        DarkCard(floating: true) {
            HStack(spacing: 8) {
                StatusMark(.waiting)
                Text(text)
                    .font(.footnote)
                    .foregroundStyle(DS.ink2)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .accessibilityElement(children: .combine)
        }
        .padding(.horizontal, DS.Space.m)
        .padding(.bottom, 8)
    }
}

// MARK: Approvals

let actionsWidth: CGFloat = 480

/// An approval, docked: rendered from Core's `PBSessionDockView`.
struct ApprovalDock: View {
    let sessionID: String
    let dock: PBSessionDockView
    @Binding var open: Bool
    var startReason = false
    let onOpen: () -> Void
    @Environment(AccountContext.self) private var context

    var body: some View {
        let approval = dock.approval
        let target = ApprovalTarget(sessionId: sessionID, dock: dock)
        let compact = target.showCompactSubmitted
        Group {
            if compact {
                ApprovalSubmitted(target: target)
                    .padding(.horizontal, DS.Space.m).padding(.bottom, 8)
                    .transition(Motion.roomy())
            } else {
                Ticking(every: 15) { now in
                    let urgent = dock.deadlineMs > 0 && dock.deadlineMs - now < 60_000
                    let timeText = dock.hasTimeLeftPlain ? dock.timeLeftPlain.rendered() : ""
                    let openTitle = approval.commandPreview.isEmpty ? approval.toolName : approval.commandPreview
                    PendingDock(
                        label: dock.title.rendered(),
                        brief: dock.summary.rendered(),
                        openTitle: openTitle.isEmpty ? nil : openTitle,
                        more: Int(dock.moreCount),
                        note: DockNote(text: timeText, urgent: urgent),
                        open: $open,
                        onOpen: onOpen
                    ) {
                        ApprovalContent(approval: approval, isSkeleton: dock.isSkeleton, denying: target.denyingOpen)
                    } actions: {
                        if !dock.isSkeleton {
                            ApprovalDecision(target: target)
                        }
                    }
                }
                .transition(Motion.roomy())
            }
        }
        .animation(Motion.enter(), value: compact)
        .onAppear {
            if startReason && !approval.denyingOpen {
                Task {
                    await context.coreHost.editApprovalDeny(sessionID: sessionID, approvalID: approval.approvalID, denyingOpen: true)
                }
            }
        }
    }
}

// MARK: Questions

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

struct QuestionBody: View {
    enum Part { case content, actions }

    let sessionID: String
    let dock: PBSessionDockView
    let part: Part
    let full: Bool
    var onTerminal: () -> Void = {}
    var onLong: () -> Void = {}
    var onAnswered: () -> Void = {}
    @Environment(AccountContext.self) private var context
    @State private var initialSucceededSeq: UInt64?

    private var qDock: PBSessionQuestionDockView { dock.question }
    private var qid: String { qDock.questionID }
    private var index: Int { Int(qDock.activeIndex) }
    private var cur: PBSessionQuestionStepView { qDock.question }
    private var cmd: PBCoreSessionCommandState { dock.command }
    private var isRunning: Bool { cmd.isRunning }

    private func toggleOption(_ optionNum: Int32) {
        Haptics.selection()
        Task {
            await context.coreHost.toggleQuestionOption(sessionID: sessionID, questionID: qid, optionNum: optionNum)
        }
    }

    private func submitSingle(_ optionNum: Int32) {
        if initialSucceededSeq == nil { initialSucceededSeq = cmd.succeededSeq }
        SessionHaptics.tap()
        Task {
            await context.coreHost.answerQuestion(sessionID: sessionID, questionID: qid, optionNum: optionNum)
        }
    }

    private func submitMulti(_ optionNums: [Int32]) {
        guard shouldSubmitMultiSelect(optionNums) else { return }
        if initialSucceededSeq == nil { initialSucceededSeq = cmd.succeededSeq }
        SessionHaptics.tap()
        Task {
            await context.coreHost.answerQuestion(sessionID: sessionID, questionID: qid, optionNums: optionNums)
        }
    }

    var body: some View {
        if dock.isSkeleton {
            if part == .content { Text(L10n.sessionSkeleton()).font(.subheadline).ink(DS.ink2) }
        } else {
            Group {
                switch part {
                case .content: content
                case .actions: actions
                }
            }
            .onAppear {
                if initialSucceededSeq == nil { initialSucceededSeq = cmd.succeededSeq }
            }
            .onChange(of: cmd.succeededSeq) { _, newSeq in
                if shouldDismissQuestionScreen(baseline: initialSucceededSeq, current: newSeq, isLastStep: qDock.isLastStep) {
                    initialSucceededSeq = newSeq
                    onAnswered()
                }
            }
        }
    }

    @ViewBuilder private var content: some View {
        let count = Int(qDock.totalQuestions)
        let progress = qDock.hasProgressText ? qDock.progressText.rendered() : ""
        let selectedNums = Set(qDock.selectedOptionNums)
        let answered = qDock.answered
        if full {
            if !progress.isEmpty { Text(progress).font(.footnote).ink(DS.ink2).padding(.top, 12) }
        } else if count > 1 {
            QuestionSteps(count: count, at: index).padding(.top, 4)
        }
        Text(cur.questionText).font(full ? .headline : .body.weight(.semibold)).ink(DS.ink).lineLimit(full ? nil : 4)
            .padding(.top, full ? 8 : 4).padding(.bottom, full ? 12 : 6)
            .accessibilityAddTraits(.isHeader)
        if !full && isLongStep(cur) {
            Button(action: onLong) {
                ArrowText(L10n.markdownExpand(), style: .subheadline.weight(.semibold)).padding(.bottom, 4)
                    .padding(.vertical, 12).contentShape(Rectangle()).padding(.vertical, -12)
            }
            .buttonStyle(PressStyle())
        }
        ForEach(Array(cur.options.enumerated()), id: \.offset) { i, option in
            let num = option.optionNum > 0 ? option.optionNum : Int32(i + 1)
            let selected = selectedNums.contains(num)
            let submitting = selected && isRunning && !cur.multiSelect
            QuestionOptionRow(
                option: option, multi: cur.multiSelect, selected: selected, submitting: submitting,
                dark: !full
            ) {
                if cur.multiSelect {
                    toggleOption(num)
                } else {
                    submitSingle(num)
                }
            }
            .disabled(answered || isRunning || cmd.frozen)
        }
        if answered && qDock.hasAnsweredText {
            let text = qDock.answeredText.rendered()
            if !text.isEmpty {
                Text(text).font(.footnote).ink(DS.ink2).padding(.top, 6)
            }
        } else if !isRunning && cmd.frozen {
            Banner(
                cmd.hasStatusText ? cmd.statusText.rendered() : cmd.renderedErrorText,
                tone: .error,
                action: cmd.canAbandon ? L10n.newSessionIosAbandon() : nil,
                onAction: {
                    Task { await context.coreHost.abandonSessionCommand(sessionID: sessionID, slotKey: cmd.slotKey) }
                }
            )
            .padding(.top, 6)
        } else if !isRunning && cmd.status == .failed {
            let reason = cmd.renderedErrorText
            Banner(L10n.dockIosQuestionFailed(reason: reason), tone: .error).padding(.top, 6)
        }
    }

    @ViewBuilder private func primaryActionButton(_ label: String, enabled: Bool = true, action: @escaping () -> Void) -> some View {
        if full {
            Btn(label, kind: .primary, enabled: enabled, action: action)
        } else {
            Button(label, action: action)
                .buttonStyle(.darkCard(primary: true))
                .disabled(!enabled)
        }
    }

    @ViewBuilder private var actions: some View {
        let picked = qDock.draftedOptionNums
        let showError = cmd.isFailed
        let canRetry = showError && !cmd.expired && cmd.canRetry && !cmd.slotKey.isEmpty
        if cur.multiSelect && !(showError && cmd.frozen) {
            let label: String = {
                if qDock.hasSubmitButtonLabel {
                    let s = qDock.submitButtonLabel.rendered()
                    if !s.isEmpty { return s }
                }
                return isRunning ? L10n.dockSubmitting() : picked.isEmpty ? L10n.dockSelectThenSubmit() : L10n.dockSubmitSelected(count: picked.count)
            }()
            primaryActionButton(label, enabled: qDock.canSubmitMulti) { submitMulti(picked) }
                .padding(.top, 6)
        }
        if canRetry {
            primaryActionButton(L10n.commonRetry()) {
                Task { await context.coreHost.retrySessionCommand(sessionID: sessionID, slotKey: cmd.slotKey) }
            }
            .padding(.top, 6)
        }
        DockFoot(
            cur.multiSelect ? (picked.isEmpty ? "" : L10n.dockSelected(count: picked.count)) : L10n.dockNoneFit(),
            link: L10n.dockOtherTerminal(),
            arrow: true,
            onLink: onTerminal
        )
    }

    private func isLongStep(_ step: PBSessionQuestionStepView) -> Bool {
        step.options.count > 4 || step.questionText.count > 120
    }
}

private struct QuestionOptionRow: View {
    let option: PBSessionQuestionOptionView
    let multi: Bool
    let selected: Bool
    let submitting: Bool
    var dark = false
    let onTap: () -> Void
    @ScaledMetric(relativeTo: .footnote) private var box: CGFloat = 18

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: dark ? 14 : DS.Radius.small, style: .continuous)
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
                    Text(option.label).font(dark ? .body : .footnote).foregroundStyle(ink)
                    if !option.description_p.isEmpty { Text(option.description_p).font(dark ? .footnote : .caption).foregroundStyle(second) }
                    if submitting { Text(L10n.dockSubmitting()).font(.caption).foregroundStyle(second) }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                if option.recommended { Text(L10n.dockRecommended()).font(.caption.weight(.semibold)).foregroundStyle(dark ? (selected ? Color.black : DS.blue) : DS.blue) }
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
    let sessionID: String
    let dock: PBSessionDockView
    @Binding var open: Bool
    let onOpen: () -> Void
    let onTerminal: () -> Void
    let onLong: () -> Void

    var body: some View {
        let progress = dock.question.hasProgressText ? dock.question.progressText.rendered() : ""
        PendingDock(
            label: dock.title.rendered(),
            brief: dock.summary.rendered(),
            more: Int(dock.moreCount),
            note: DockNote(text: progress),
            open: $open,
            onOpen: onOpen
        ) {
            QuestionBody(sessionID: sessionID, dock: dock, part: .content, full: false, onLong: onLong)
        } actions: {
            QuestionBody(sessionID: sessionID, dock: dock, part: .actions, full: false, onTerminal: onTerminal)
        }
    }
}

// MARK: Plans

struct PlanDock: View {
    let sessionID: String
    let dock: PBSessionDockView
    @Binding var open: Bool
    let onOpen: () -> Void
    let onFull: () -> Void

    var body: some View {
        let plan = dock.plan
        if dock.isSkeleton {
            Text(L10n.sessionSkeleton()).font(.subheadline).ink(DS.ink2)
        } else {
            PendingDock(
                label: dock.title.rendered(),
                brief: dock.summary.rendered(),
                more: Int(dock.moreCount),
                open: $open,
                onOpen: onOpen,
                extra: AnyView(TextAction(L10n.markdownExpand(), style: .subheadline, alignment: .trailing, arrow: true, action: onFull))
            ) {
                Text(planPreview(plan.markdown, lines: .max)).font(.subheadline).lineSpacing(3).foregroundStyle(DS.ink)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.top, 4).padding(.bottom, 4)
            } actions: {
                PlanButtons(sessionID: sessionID, dock: dock, full: false)
            }
        }
    }
}

struct PlanButtons: View {
    let sessionID: String
    let dock: PBSessionDockView
    let full: Bool
    var onDone: () -> Void = {}
    @Environment(AccountContext.self) private var context
    @State private var initialSucceededSeq: UInt64?

    private var plan: PBSessionPlanDockView { dock.plan }
    private var cmd: PBCoreSessionCommandState { dock.command }
    private var isRunning: Bool { cmd.isRunning }

    private func labelFor(_ choice: PBPlanChoice) -> String {
        if isRunning && plan.submittedChoice == choice {
            return L10n.dockSubmitting()
        }
        if let hit = plan.choices.first(where: { $0.choice == choice && $0.hasLabel }) {
            let s = hit.label.rendered()
            if !s.isEmpty { return s }
        }
        switch choice {
        case .keepPlanning: return L10n.dockPlanKeep()
        case .implement: return L10n.approvalApprove()
        case .clearAndImplement: return L10n.dockPlanClear()
        default: return ""
        }
    }

    private func answer(_ choice: PBPlanChoice) {
        guard !plan.markdown.isEmpty else { return }
        if initialSucceededSeq == nil { initialSucceededSeq = cmd.succeededSeq }
        SessionHaptics.tap()
        Task {
            await context.coreHost.answerPlan(sessionID: sessionID, choice: choice)
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            choices
        }
        .frame(maxWidth: actionsWidth, alignment: .leading)
        .frame(maxWidth: .infinity, alignment: .leading)
        .onAppear {
            if initialSucceededSeq == nil { initialSucceededSeq = cmd.succeededSeq }
        }
        .onChange(of: cmd.succeededSeq) { _, newSeq in
            if shouldCloseAfterCommandSuccess(baseline: initialSucceededSeq, current: newSeq) {
                initialSucceededSeq = newSeq
                onDone()
            }
        }
    }

    @ViewBuilder private var choices: some View {
        let locked = isRunning || cmd.frozen
        let keepLabel = labelFor(.keepPlanning)
        let approveLabel = labelFor(.implement)
        if full {
            Btns {
                Btn(keepLabel, enabled: !locked) { answer(.keepPlanning) }
                Btn(approveLabel, kind: .primary, enabled: !locked) { answer(.implement) }
            }
        } else {
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 8) { darkChoices(keepLabel: keepLabel, approveLabel: approveLabel, locked: locked) }
                VStack(spacing: 8) { darkChoices(keepLabel: keepLabel, approveLabel: approveLabel, locked: locked) }
            }
        }
        if plan.hasNote {
            let note = plan.note.rendered()
            if !note.isEmpty { Text(note).font(.footnote).ink(DS.ink2).padding(.top, 7) }
        }
        if full && plan.clearContextAvailable_p {
            DockFoot("", link: labelFor(.clearAndImplement)) { answer(.clearAndImplement) }
                .disabled(locked)
        }
        let err = cmd.renderedErrorText
        if !isRunning && (!err.isEmpty || cmd.frozen) {
            let actionTitle: String? = cmd.canRetry && !cmd.expired && !cmd.slotKey.isEmpty
                ? L10n.commonRetry()
                : (cmd.canAbandon ? L10n.newSessionIosAbandon() : nil)
            Banner(
                L10n.dockSubmitFailed(reason: err.isEmpty ? L10n.approvalIosUnknownResult() : err),
                tone: .error,
                action: actionTitle,
                onAction: {
                    if cmd.canRetry && !cmd.expired && !cmd.slotKey.isEmpty {
                        Task { await context.coreHost.retrySessionCommand(sessionID: sessionID, slotKey: cmd.slotKey) }
                    } else if cmd.canAbandon {
                        Task { await context.coreHost.abandonSessionCommand(sessionID: sessionID, slotKey: cmd.slotKey) }
                    }
                }
            )
            .padding(.top, 8)
        }
    }

    @ViewBuilder private func darkChoices(keepLabel: String, approveLabel: String, locked: Bool) -> some View {
        Button(keepLabel) { answer(.keepPlanning) }.buttonStyle(.darkCard()).disabled(locked)
        Button(approveLabel) { answer(.implement) }
            .buttonStyle(.darkCard(primary: true)).disabled(locked)
    }
}

// MARK: Popups in the terminal

struct PopupDock: View {
    let dock: PBSessionDockView
    @Binding var open: Bool
    let onOpen: () -> Void
    let onTerminal: () -> Void

    var body: some View {
        let popup = dock.terminalPopup
        PendingDock(
            label: dock.title.rendered(),
            brief: dock.summary.rendered(),
            more: Int(dock.moreCount),
            note: DockNote(text: dock.hasMeta ? dock.meta.rendered() : ""),
            open: $open,
            onOpen: onOpen
        ) {
            if popup.hasPopupTitle {
                let pt = popup.popupTitle.rendered()
                if !pt.isEmpty { Text(pt).font(.body.weight(.medium)).foregroundStyle(DS.ink).padding(.bottom, 6) }
            }
            if popup.hasHint {
                let ht = popup.hint.rendered()
                if !ht.isEmpty { Text(ht).font(.footnote).foregroundStyle(DS.orangeInk) }
            }
        } actions: {
            Button(L10n.dockHandleTerminal(), action: onTerminal)
                .buttonStyle(.darkCard(primary: true))
                .disabled(!popup.canGoTerminal)
        }
    }
}
