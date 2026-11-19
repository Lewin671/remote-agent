// The chat's rows, rendered directly from Core's `PBSessionTimelineItem`s.
// Markdown parsing is cached per item ID + Markdown source; UIKit
// `ChatCollection` owns virtualization and scroll preservation.
import Observation
import RACore
import RAMarkdown
import RAMarkdownUI
import RAProto
import SwiftUI
import UIKit

/// Timeline item paired with cached parsed Markdown blocks so unchanged rows remain `Equatable`.
struct ChatTimelineRow: Identifiable, Equatable, Sendable {
    var id: String { item.id }
    let item: PBSessionTimelineItem
    let blocks: [MarkdownBlock]
    /// Notify receipts in a row sit 4 apart (design-system §7), so a receipt knows its neighbours.
    var receiptBefore = false
    var receiptAfter = false

    static func == (lhs: ChatTimelineRow, rhs: ChatTimelineRow) -> Bool {
        lhs.item == rhs.item && lhs.blocks.count == rhs.blocks.count
            && lhs.receiptBefore == rhs.receiptBefore && lhs.receiptAfter == rhs.receiptAfter
    }
}

/// Shell environment needed by timeline rows.
struct ChatEnv: Equatable, Sendable {
    var agent = defaultAgent
    /// When the connection dropped, 0 while online: timers stop there.
    var frozenAt: Int64 = 0
    /// Shared files transfer context.
    var session = ""
    var machine = ""
    var machineOnline = true
    var sessionExited = false
}

/// Row actions routed through `ChatPane` into `Navigator` or `CoreHost`.
struct ChatActions: Sendable {
    var image: @MainActor (_ seq: UInt64, _ index: Int) -> Void = { _, _ in }
    var session: @MainActor (String) -> Void = { _ in }
    var putBack: @MainActor (_ requestId: String, _ text: String, _ files: [PBSessionFileRefView]) -> Void = { _, _, _ in }
    var toggleGroup: @MainActor (String) -> Void = { _ in }
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
        ChatDiagnosticRing.shared.record(.collapseToggle, Double(keys.count))
    }
}

let chatMargin: CGFloat = 14

// MARK: The row

struct ChatRowView: View, Equatable {
    let row: ChatTimelineRow
    let env: ChatEnv

    @Environment(\.chatActions) private var actions
    @Environment(ChatOpen.self) private var open

    nonisolated static func == (a: ChatRowView, b: ChatRowView) -> Bool { a.row == b.row && a.env == b.env }

    var body: some View {
        rowBody(row.item, blocks: row.blocks)
    }

    @ViewBuilder private func rowBody(_ tlItem: PBSessionTimelineItem, blocks: [MarkdownBlock]) -> some View {
        switch tlItem.kind {
        case .userMessage(let m):
            let caption: String? = m.hasOriginCaption ? m.originCaption.rendered().nilIfEmpty : nil
            if !m.scheduledRunID.isEmpty {
                ScheduledUserMessage(text: m.text, caption: caption)
            } else {
                ChatBubble(
                    m.text.jvmIsBlank && (!m.images.isEmpty || !m.files.isEmpty) ? nil : m.text,
                    caption: caption
                ) {
                    FileChips(refs: m.files)
                    if !m.images.isEmpty {
                        ChatImageRow(m.images) { actions.image(tlItem.seq, $0) }.padding(.bottom, 4)
                    }
                }
                .messageMenu(m.text, actions: actions)
                .accessibilityElement(children: .combine)
                .accessibilityLabel(m.text.isEmpty ? (m.files.isEmpty ? L10n.imageTitle() : L10n.attachmentFile()) : m.text)
                .accessibilityValue(caption ?? L10n.sessionIosYourMessage())
            }

        case .agentMessage(let m):
            MarkdownView(blocks: blocks, bleed: chatMargin, selectable: false)
                .padding(.horizontal, chatMargin).padding(.vertical, 8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .messageMenu(m.markdown, actions: actions)
                .accessibilityElement(children: .contain)
                .accessibilityLabel(L10n.sessionIosAgentReply(agent: agentName(env.agent)))

        case .asyncQuestion(let m):
            VStack(alignment: .leading, spacing: 8) {
                ForEach(Array(m.questions.enumerated()), id: \.offset) { _, q in
                    if !q.header.isEmpty {
                        Text(verbatim: q.header).font(.body.weight(.semibold)).ink(DS.ink)
                    }
                    MarkdownView(q.questionText, bleed: chatMargin)
                    ForEach(Array(q.options.enumerated()), id: \.offset) { _, o in
                        HStack(alignment: .top, spacing: 8) {
                            Text("•").font(.body).ink(DS.ink2)
                            (Text(verbatim: o.label).bold() + Text(verbatim: o.description_p.isEmpty ? "" : " · \(o.description_p)"))
                                .font(.body).ink(DS.ink)
                        }
                    }
                }
            }
            .padding(.horizontal, chatMargin).padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .contain)
            .accessibilityLabel(L10n.sessionIosAgentReply(agent: agentName(env.agent)))

        case .reasoning(let r):
            ChatExpandable(L10n.sessionIosReasoning(), key: tlItem.id) {
                Text(r.markdown).font(.subheadline).ink(DS.ink2).padding(.bottom, 6)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.horizontal, chatMargin)

        case .toolCall(let t):
            let callKey = t.callID.isEmpty ? tlItem.id : t.callID
            VStack(alignment: .leading, spacing: 0) {
                ExpandableToolRow(key: callKey, text: commandOf(kind: t.toolKind, title: t.title, input: t.rawInput)) {
                    TimelineToolRow(call: t, env: env)
                }
                if !t.images.isEmpty {
                    ChatImageRow(t.images) { actions.image(tlItem.seq, $0) }.padding(.leading, 21).padding(.bottom, 6)
                }
            }
            .padding(.horizontal, chatMargin)

        case .sharedFiles(let s):
            SharedFilesRow(item: s, env: env)

        case .toolGroup(let g):
            ToolGroupRow(key: tlItem.id, seq: tlItem.seq, group: g).padding(.horizontal, chatMargin)

        case .generatedImages(let g):
            GeneratedCard(key: tlItem.id, item: g) { actions.image(tlItem.seq, $0) }

        case .note(let n):
            let text = n.text.rendered()
            let meta = n.hasMeta ? n.meta.rendered() : ""
            if n.kind == .approval || n.kind == .question || n.kind == .interrupted {
                let isFail = n.tone == .negative || n.tone == .warning
                ChatToolLine(
                    isFail ? "xmark" : "checkmark", color: DS.ink3, title: text, meta: meta,
                    metaColor: isFail ? DS.red : DS.ink2
                )
                .padding(.horizontal, chatMargin)
            } else {
                Text(text).font(.footnote).ink(DS.ink2).multilineTextAlignment(.center).lineLimit(2)
                    .frame(maxWidth: .infinity)
                    .padding(.horizontal, chatMargin).padding(.vertical, 10)
            }

        case .plan(let p):
            PlanCard(key: tlItem.id, text: p.markdown)

        case .inputState(let u):
            let caption = u.statusText.rendered()
            let bodyText = u.preview.isEmpty ? L10n.queueIosMessage() : u.preview
            let links: [ChatLink] = u.kind == .unsentOnExit && !u.preview.isEmpty
                ? [ChatLink(L10n.composerRestore()) { actions.putBack(u.requestID, u.preview, []) }]
                : []
            ChatBubble(
                bodyText,
                caption: caption.isEmpty ? (u.hasReasonText ? u.reasonText.rendered() : nil) : caption,
                captionColor: u.kind == .failed || u.kind == .unsentOnExit ? DS.red : DS.orangeInk,
                links: links
            )

        case .notifyReceipt(let r):
            NotifyReceiptRow(key: tlItem.id, item: r, before: row.receiptBefore, after: row.receiptAfter)

        case .turnEnd(let t):
            ChatSepLine(t.label.rendered())

        case .errorLine(let e):
            Text(e.text.rendered()).font(.footnote).ink(DS.red).multilineTextAlignment(.center)
                .frame(maxWidth: .infinity)
                .padding(.horizontal, chatMargin).padding(.vertical, 10)

        case .forkMarker(let f):
            ForkRow(item: f)

        case .quietScheduledGroup(let g):
            VStack(spacing: 0) {
                Button { actions.toggleGroup(g.groupID) } label: {
                    HStack(spacing: 6) {
                        Text(g.title.rendered()).font(.footnote).ink(DS.ink2).multilineTextAlignment(.leading)
                        Arrow(DS.ink2, direction: g.expanded ? .up : .down)
                    }
                    .frame(maxWidth: .infinity, minHeight: 44, alignment: .leading).padding(.horizontal, 16)
                }
                .buttonStyle(PressStyle())
                .accessibilityValue(g.expanded ? L10n.projectsIosExpanded() : L10n.sessionIosCollapsed())
                if g.expanded {
                    ForEach(g.items, id: \.id) { child in
                        AnyView(erasedBody(child))
                    }
                }
            }

        case .skeleton(let s):
            Text(s.text.rendered()).font(.subheadline).ink(DS.ink2)
                .padding(.horizontal, chatMargin).padding(.vertical, 6)

        case nil:
            EmptyView()
        }
    }

    private func erasedBody(_ item: PBSessionTimelineItem) -> AnyView {
        if case .agentMessage(let a)? = item.kind {
            return AnyView(rowBody(item, blocks: Markdown.parse(a.markdown)))
        }
        return AnyView(rowBody(item, blocks: []))
    }
}

private extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}

extension View {
    @ViewBuilder fileprivate func messageMenu(_ text: String, actions: ChatActions) -> some View {
        if text.isEmpty {
            self
        } else {
            contextMenu {
                Button(L10n.commonCopy()) { UIPasteboard.general.string = text }
                Button(L10n.sessionIosSelectText()) { actions.select(text) }
            } preview: {
                MessageMenuPreview(text: text)
            }
            .accessibilityAction(named: L10n.commonCopy()) { UIPasteboard.general.string = text }
            .accessibilityAction(named: L10n.sessionIosSelectText()) { actions.select(text) }
        }
    }
}

// MARK: Messages

struct ChatLink: Identifiable {
    let label: String
    let action: @MainActor () -> Void

    var id: String { label }

    init(_ label: String, action: @escaping @MainActor () -> Void) {
        self.label = label
        self.action = action
    }
}

private struct TrailingFraction: Layout {
    let fraction: CGFloat
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

struct ChatBubble<Top: View>: View {
    private let text: String?
    private let caption: String?
    private let captionColor: Color
    private let captionIcon: String?
    private let failed: Bool
    private let pending: Bool
    private let lines: Int?
    private let links: [ChatLink]
    private let padded: Bool
    private let top: Top
    @Environment(\.dynamicTypeSize) private var typeSize

    init(
        _ text: String?, caption: String? = nil, captionColor: Color = DS.ink2, captionIcon: String? = nil,
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
        let shape = RoundedRectangle(cornerRadius: 20, style: .continuous)
        VStack(alignment: .trailing, spacing: 0) {
            top
            if let text {
                TrailingFraction(fraction: 0.8, whole: typeSize.isAccessibilitySize) {
                    Text(text).font(.body).foregroundStyle(pending ? DS.ink2 : DS.ink)
                        .lineLimit(lines)
                        .padding(.horizontal, 14).padding(.vertical, 9)
                        .background(pending ? Color.clear : DS.bubble, in: shape)
                        .overlay {
                            if failed {
                                shape.strokeBorder(DS.red, lineWidth: 1.5)
                            } else if pending {
                                shape.strokeBorder(DS.faint, lineWidth: 1)
                            }
                        }
                }
            }
            if caption != nil || !links.isEmpty {
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
                Text(caption).font(.footnote).foregroundStyle(captionColor).multilineTextAlignment(.trailing)
                    .contentTransition(.opacity)
            }
        }
        .animation(Motion.enter(Motion.fast), value: caption)
    }

    @ViewBuilder fileprivate var actions: some View {
        ForEach(links) { link in
            Button(action: link.action) {
                Text(link.label).font(.footnote.weight(.semibold)).foregroundStyle(DS.blue).lineLimit(1).fixedSize()
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
        _ text: String?, caption: String? = nil, captionColor: Color = DS.ink2, captionIcon: String? = nil,
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
        RowSeparator()
    }
}

struct ChatSepLine: View {
    private let text: String

    init(_ text: String) { self.text = text }

    var body: some View {
        HStack(spacing: 8) {
            Rule().frame(minWidth: 16)
            Text(text).font(.footnote).foregroundStyle(DS.ink2).multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true).layoutPriority(1)
            Rule().frame(minWidth: 16)
        }
        .padding(.horizontal, chatMargin).padding(.vertical, 12)
        .accessibilityElement(children: .combine)
    }
}

private struct ForkRow: View {
    let item: PBSessionForkItem
    @Environment(\.chatActions) private var actions

    var body: some View {
        let title = item.hasSourceTitle ? item.sourceTitle.rendered() : ""
        HStack(spacing: 8) {
            Rule().frame(minWidth: 16)
            HStack(spacing: 4) {
                Text(item.text.rendered()).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1).fixedSize()
                if !title.isEmpty {
                    if item.sourceSessionID.isEmpty {
                        Text(title).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1)
                    } else {
                        Button {
                            actions.session(item.sourceSessionID)
                        } label: {
                            HStack(spacing: 4) {
                                Text(title).font(.footnote).foregroundStyle(DS.blue).lineLimit(1)
                                Image(systemName: "chevron.right").font(.caption2.weight(.semibold)).foregroundStyle(DS.blue)
                            }
                            .padding(.vertical, 12).frame(minHeight: DS.minHit).contentShape(Rectangle())
                        }
                        .buttonStyle(PressStyle())
                    }
                }
            }
            .layoutPriority(1)
            Rule().frame(minWidth: 16)
        }
        .padding(.horizontal, chatMargin)
    }
}

// MARK: Tools

struct ChatToolLine<Trailing: View>: View {
    private let mark: String
    private let color: Color
    private let title: String
    private let meta: String
    private let metaColor: Color
    private let bold: Bool
    private let trailing: Trailing
    @Environment(\.dynamicTypeSize) private var typeSize

    init(
        _ mark: String, color: Color, title: String, meta: String = "", metaColor: Color = DS.ink2, bold: Bool = false,
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
        Text(title).font(bold ? .footnote : .footnote.monospaced()).foregroundStyle(DS.ink2)
            .lineLimit(1).truncationMode(.tail)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    var body: some View {
        Group {
            if typeSize.isAccessibilitySize && !meta.isEmpty {
                VStack(alignment: .trailing, spacing: 0) {
                    HStack(spacing: 8) {
                        Mark(mark, color: color)
                        name
                        trailing
                    }
                    Text(meta).font(.footnote).foregroundStyle(metaColor).lineLimit(1)
                }
            } else {
                HStack(spacing: 8) {
                    Mark(mark, color: color)
                    name
                    if !meta.isEmpty { Text(meta).font(.footnote).foregroundStyle(metaColor).lineLimit(1).fixedSize() }
                    trailing
                }
            }
        }
        .padding(.vertical, 4)
        .frame(minHeight: 20)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(markedTitle)
        .accessibilityValue(meta)
    }

    private var markedTitle: String {
        if mark == "checkmark" { return L10n.toolsIosCompletedLabel(title: title) }
        if mark == "xmark" { return L10n.toolsIosFailedLabel(title: title) }
        if mark == Mark.progress { return L10n.toolsIosRunningLabel(title: title) }
        return title
    }
}

extension ChatToolLine where Trailing == EmptyView {
    init(_ mark: String, color: Color, title: String, meta: String = "", metaColor: Color = DS.ink2, bold: Bool = false) {
        self.init(mark, color: color, title: title, meta: meta, metaColor: metaColor, bold: bold) { EmptyView() }
    }
}

/// Resolve original command input identically to web and Android. File rows keep the full path.
func commandOf(kind: PBToolKind, title: String, input: String) -> String {
    if kind == .fileChange || input.isEmpty || input == title { return title }
    if let data = input.data(using: .utf8),
       let value = try? JSONSerialization.jsonObject(with: data, options: .fragmentsAllowed) {
        if let object = value as? [String: Any] {
            for key in ["command", "CommandLine", "cmd"] {
                if let command = object[key] as? String { return command }
            }
            return title
        }
    }
    return input
}

private struct ExpandableToolRow<Preview: View>: View {
    let key: String
    let text: String
    @ViewBuilder let preview: () -> Preview
    @Environment(ChatOpen.self) private var open
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private func toggle() {
        withAnimation(reduceMotion ? nil : Motion.enter()) { open.toggle("command/" + key) }
    }

    var body: some View {
        let expanded = open.isOpen("command/" + key)
        VStack(alignment: .leading, spacing: 0) {
            Button(action: toggle) {
                preview().frame(maxWidth: .infinity, minHeight: 28, alignment: .leading)
                    .contentShape(Rectangle())
            }
            .buttonStyle(PressStyle())
            .accessibilityValue(expanded ? L10n.projectsIosExpanded() : L10n.sessionIosCollapsed())
            if expanded {
                ToolSelectableCommand(text: text, onTap: toggle)
                    .background(DS.gbg, in: RoundedRectangle(cornerRadius: 16))
                    .overlay(alignment: .topTrailing) { CodeCopyButton(text: text, theme: DS.markdown) }
            }
        }
    }
}

/// UIKit owns selection and scrolling; a selection gesture never toggles the row.
private struct ToolSelectableCommand: UIViewRepresentable {
    let text: String
    let onTap: () -> Void

    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        var onTap: () -> Void
        init(onTap: @escaping () -> Void) { self.onTap = onTap }
        @objc func tap(_ gesture: UITapGestureRecognizer) {
            guard let view = gesture.view as? UITextView, view.selectedRange.length == 0 else { return }
            onTap()
        }
        func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer, shouldReceive touch: UITouch) -> Bool {
            (gestureRecognizer.view as? UITextView)?.selectedRange.length == 0
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator(onTap: onTap) }

    func makeUIView(context: Context) -> UITextView {
        let view = UITextView()
        view.isEditable = false
        view.isSelectable = true
        view.backgroundColor = .clear
        view.textContainerInset = UIEdgeInsets(top: 12, left: 14, bottom: 12, right: 44)
        view.textContainer.lineFragmentPadding = 0
        view.textContainer.lineBreakMode = .byCharWrapping
        view.adjustsFontForContentSizeCategory = true
        view.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        let tap = UITapGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.tap))
        tap.delegate = context.coordinator
        tap.cancelsTouchesInView = false
        for recognizer in view.gestureRecognizers ?? [] {
            if let other = recognizer as? UITapGestureRecognizer, other.numberOfTapsRequired > 1 {
                tap.require(toFail: other)
            }
        }
        view.addGestureRecognizer(tap)
        return view
    }

    func updateUIView(_ view: UITextView, context: Context) {
        context.coordinator.onTap = onTap
        let font = UIFontMetrics(forTextStyle: .footnote).scaledFont(for: .monospacedSystemFont(ofSize: 13, weight: .regular))
        if view.text != text || view.font != font || view.textColor != UIColor(DS.ink2) {
            let paragraph = NSMutableParagraphStyle()
            paragraph.lineBreakMode = .byCharWrapping
            paragraph.minimumLineHeight = font.pointSize * 1.5
            paragraph.maximumLineHeight = font.pointSize * 1.5
            view.attributedText = NSAttributedString(string: text, attributes: [.font: font, .foregroundColor: UIColor(DS.ink2), .paragraphStyle: paragraph])
        }
        view.tintColor = UIColor(DS.blue)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UITextView, context: Context) -> CGSize? {
        guard let width = proposal.width else { return nil }
        let height = uiView.sizeThatFits(CGSize(width: width, height: .greatestFiniteMagnitude)).height
        let line = (uiView.font?.pointSize ?? 13) * 1.5
        return CGSize(width: width, height: min(height, line * 12 + 24))
    }
}

private struct TimelineToolRow: View {
    let call: PBSessionToolCallItem
    let env: ChatEnv

    var body: some View {
        let title = firstLine(call.title)
        switch call.status {
        case .running:
            if call.noResult {
                ChatToolLine(Mark.progress, color: DS.faint, title: title, meta: L10n.toolsWaitingSync())
            } else if call.unconfirmed {
                ChatToolLine(Mark.progress, color: DS.ink, title: title, meta: L10n.toolsWaitingSync())
            } else {
                let freeze = call.frozenAtMs > 0 ? call.frozenAtMs : env.frozenAt
                if freeze > 0 {
                    ChatToolLine(Mark.progress, color: DS.ink, title: title, meta: runningTimer(sinceMs: call.sinceMs, now: freeze, frozenAt: freeze))
                } else {
                    Ticking(every: 1) { now in
                        ChatToolLine(Mark.progress, color: DS.ink, title: title, meta: runningTimer(sinceMs: call.sinceMs, now: now, frozenAt: 0))
                    }
                    .padding(.horizontal, 12).padding(.vertical, 6)
                    .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.notice, style: .continuous))
                }
            }
        case .background:
            let label = call.hasBackgroundLabel ? call.backgroundLabel.rendered() : ""
            ChatToolLine(Mark.progress, color: DS.ink, title: title, meta: label.isEmpty ? L10n.toolsBackground() : label)
        case .unconfirmed:
            ChatToolLine(Mark.progress, color: DS.ink, title: title, meta: L10n.toolsWaitingSync())
        default:
            let ok = call.status == .ok
            let exitPart = !ok && call.hasExitCode_p ? L10n.toolsExitCode(code: String(call.exitCode)) : ""
            let decisionPart = call.hasDecisionLabel ? call.decisionLabel.rendered() : ""
            let parts = [exitPart, decisionPart].filter { !$0.isEmpty }
            let meta = parts.isEmpty ? (call.elapsedMs >= 1000 ? "\(call.elapsedMs / 1000)s" : "") : parts.joined(separator: " · ")
            ChatToolLine(
                ok ? "checkmark" : "xmark", color: ok ? DS.ink3 : DS.red,
                title: title, meta: meta,
                metaColor: !ok || (call.hasDecisionLabel && !call.decisionAllowed) ? DS.red : DS.ink2
            )
        }
    }

    private func runningTimer(sinceMs: Int64, now: Int64, frozenAt: Int64) -> String {
        guard sinceMs > 0 else { return "" }
        let end = frozenAt > 0 ? min(frozenAt, now) : now
        let ms = max(0, end - sinceMs)
        if ms < 3_000 { return "" }
        return clock(ms)
    }
}

private struct ToolGroupRow: View {
    let key: String
    let seq: UInt64
    let group: PBSessionToolGroupItem
    @Environment(ChatOpen.self) private var open
    @Environment(\.chatActions) private var actions

    var body: some View {
        let isOpen = open.isOpen(key)
        VStack(alignment: .leading, spacing: 0) {
            Button {
                open.toggle(key)
            } label: {
                ChatToolLine("checkmark", color: DS.ink3, title: group.summary.rendered(), bold: true) {
                    Arrow(DS.ink2, style: .subheadline, direction: isOpen ? .up : .down)
                }
                .frame(minHeight: 28)
                .padding(.vertical, 8).contentShape(Rectangle()).padding(.vertical, -8)
            }
            .buttonStyle(PressStyle())
            .accessibilityHint(isOpen ? L10n.markdownCollapse() : L10n.sessionIosExpand())
            if isOpen {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(Array(group.calls.enumerated()), id: \.offset) { i, call in
                        let callKey = call.callID.isEmpty ? "\(key)/\(i)" : "\(key)/\(call.callID)"
                        ExpandableToolRow(key: callKey, text: commandOf(kind: call.toolKind, title: call.title, input: call.rawInput)) {
                            Text(firstLine(call.title))
                                .font(.footnote.monospaced()).ink(DS.ink2).lineLimit(1)
                                .frame(maxWidth: .infinity, minHeight: 28, alignment: .leading)
                        }
                        if !call.images.isEmpty {
                            ChatImageRow(call.images) { actions.image(seq, $0) }.padding(.leading, 21).padding(.bottom, 6)
                        }
                    }
                }
                .padding(.leading, 12)
            } else {
                let all = group.calls.flatMap(\.images)
                if !all.isEmpty {
                    ChatImageRow(all) { actions.image(seq, $0) }
                        .padding(.leading, 21).padding(.bottom, 6)
                }
            }
        }
    }
}

// MARK: Notify receipts

/// A scheduled run's push, left as one grey line (design-system §7): Core says what it shows;
/// the agent's longer texts open in place under a faint rule.
private struct NotifyReceiptRow: View {
    let key: String
    let item: PBSessionNotifyReceiptItem
    let before: Bool
    let after: Bool
    @Environment(ChatOpen.self) private var open

    var body: some View {
        let isOpen = item.expandable && open.isOpen("receipt/" + key)
        let detail = isOpen || item.preview.isEmpty ? item.time.rendered() : item.preview
        VStack(alignment: .leading, spacing: 0) {
            if item.expandable {
                Button { open.toggle("receipt/" + key) } label: {
                    line(detail, chevron: isOpen ? 180 : 0)
                        .padding(.vertical, 12).contentShape(Rectangle()).padding(.vertical, -12)
                }
                .buttonStyle(PressStyle())
                .accessibilityLabel(isOpen ? L10n.markdownCollapse() : L10n.sessionNotifyReceiptExpand())
                if isOpen {
                    HStack(spacing: 0) {
                        Rectangle().fill(DS.faint).frame(width: 2)
                        Text(verbatim: item.body).font(.subheadline).lineSpacing(2.5).ink(DS.ink2)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.leading, 14)
                    }
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.leading, 5).padding(.top, 8).padding(.bottom, 4)
                }
            } else {
                line(detail, chevron: nil).accessibilityElement(children: .combine)
            }
        }
        .padding(.horizontal, chatMargin).padding(.top, before ? 0 : 6).padding(.bottom, after ? 4 : 6)
    }

    private func line(_ detail: String, chevron: Double?) -> some View {
        HStack(spacing: 8) {
            Mark("bell", color: DS.ink3)
            HStack(spacing: 4) {
                Text(verbatim: item.label.rendered() + " · " + detail).font(.footnote).ink(DS.ink2)
                    .lineLimit(1).truncationMode(.tail)
                if let chevron {
                    Arrow(DS.ink3, style: .footnote, direction: .down)
                        .rotationEffect(.degrees(chevron))
                        .animation(.easeOut(duration: 0.16), value: chevron)
                }
            }
            Spacer(minLength: 0)
        }
        .frame(minHeight: 20)
    }
}

// MARK: Things that open in place

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
                withAnimation(reduceMotion ? nil : Motion.enter()) { open.toggle(key) }
            } label: {
                ArrowText(isOpen ? L10n.sessionIosCollapseTitle(title: title) : title, color: DS.ink2, style: .subheadline, direction: isOpen ? .up : .down)
                    .padding(.vertical, 6)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.vertical, 6).contentShape(Rectangle()).padding(.vertical, -6)
            }
            .buttonStyle(PressStyle())
            if isOpen { content }
        }
    }
}

private struct PlanCard: View {
    let key: String
    let text: String
    @Environment(ChatOpen.self) private var open
    @State private var cut = false

    var body: some View {
        let isOpen = open.isOpen(key)
        VStack(alignment: .leading, spacing: 0) {
            Text(L10n.modesPlan()).font(.subheadline.weight(.semibold)).ink(DS.ink2).accessibilityAddTraits(.isHeader)
            Group {
                if isOpen {
                    MarkdownView(text)
                } else {
                    CutText(planPreview(text, lines: 6), style: .subheadline, color: DS.ink, lines: 6, cut: $cut)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.top, 4)
            if isOpen || cut || planFolds(text, lines: 6) {
                Button {
                    open.toggle(key)
                } label: {
                    ArrowText(isOpen ? L10n.markdownCollapse() : L10n.markdownExpand(), style: .subheadline.weight(.semibold), direction: isOpen ? .up : .down)
                        .padding(.top, 4).padding(.vertical, 10).contentShape(Rectangle()).padding(.vertical, -10)
                }
                .buttonStyle(PressStyle())
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.code, style: .continuous))
        .padding(.horizontal, chatMargin).padding(.vertical, 8)
    }
}

private func firstLine(_ s: String) -> String { s.jvmLines()[0] }

func planFolds(_ text: String, lines: Int) -> Bool {
    text.jvmLines().count > lines
}

func planPreview(_ text: String, lines: Int) -> String {
    text.jvmLines().map { plain($0) }.filter { !$0.jvmIsBlank }.prefix(lines).joined(separator: "\n")
}

struct ScheduledUserMessage: View {
    let text: String
    var caption: String? = nil
    @Environment(\.chatActions) private var actions
    @Environment(\.dynamicTypeSize) private var typeSize
    var body: some View {
        VStack(alignment: .trailing, spacing: 0) {
            TrailingFraction(fraction: 0.8, whole: typeSize.isAccessibilitySize) {
                ScheduleMessageText(text: text, caption: caption, bubble: true)
            }
        }.frame(maxWidth: .infinity, alignment: .trailing).padding(.horizontal, chatMargin).padding(.vertical, 8)
            .messageMenu(text, actions: actions)
    }
}
