// The chat of a session: its rows, what is still on its way under them, and
// at the bottom what waits for the user and the message box. Android's
// `ChatPane` and `Bottom` in ui/Session.kt.
//
// Scrolling. The list is lazy, opens at its end, and stays there as
// content arrives only for someone who is at the end (`ChatScroll`, the
// package's `Follow`): a reply that grows, a new row, the dock opening or
// the keyboard coming up keep the end in sight; someone reading back is
// moved by none of them, and is told what arrived by a pill that goes to
// the end. Where the end is, is measured, not inferred from a scroll the
// app asked for, which ends before a long list has laid itself out.
//
// The keyboard. The bottom bar is the scroll view's safe-area inset, so
// the system moves it with the keyboard and the rows end above it. A drag
// on the chat takes the keyboard down with the finger
// (`scrollDismissesKeyboard(.interactively)`); SwiftUI only learns of the
// keyboard's place when that drag ends, so meanwhile the bar is kept on
// the keyboard by `KeyboardTrack`, which reads UIKit's keyboard layout
// guide.
import RACore
import RAProto
import SwiftUI
import UIKit

// MARK: Where the end of the list is

/// The list's measurements and the decision they lead to. Not observed as a
/// whole: it changes on every frame of a scroll, and only what the screen
/// draws from it (`fresh`, `away`) is published.
@MainActor
@Observable
final class ChatScrollTracker {
    /// Rows that came in while reading back.
    private(set) var fresh = 0
    /// The end is more than a little out of sight.
    private(set) var away = false

    @ObservationIgnored private(set) var scroll = ChatScroll()
    @ObservationIgnored var dragging = false
    @ObservationIgnored private var contentHeight: CGFloat = 0
    @ObservationIgnored private var endY: CGFloat = 0
    @ObservationIgnored private var floorY: CGFloat = 0
    @ObservationIgnored private var rows = 0
    @ObservationIgnored private var measured = false

    /// The end counts as in sight within this of the bar: a list at rest is rarely there to the point.
    static let slack: CGFloat = 28

    private var atBottom: Bool { !measured || endY <= floorY + Self.slack }

    /// The list follows the end and is not at it.
    var shortOfEnd: Bool { scroll.following && !atBottom }

    private func publish() {
        let f = scroll.fresh(rows: rows)
        if f != fresh { fresh = f }
        let a = measured && endY > floorY + 240
        if a != away { away = a }
    }

    /// The content was laid out or scrolled: its height, and where its end is on the screen.
    /// True when the list should go to its end.
    func content(height: CGFloat, endY: CGFloat) -> Bool {
        let grew = abs(height - contentHeight) > 0.5
        contentHeight = height
        self.endY = endY
        return settle(changed: grew)
    }

    /// The top of the bar under the list moved: the dock opened, the keyboard came or went.
    func floor(_ y: CGFloat) -> Bool {
        let moved = abs(y - floorY) > 0.5
        floorY = y
        measured = true
        return settle(changed: moved)
    }

    /// The number of rows changed.
    func count(_ n: Int) -> Bool {
        let changed = n != rows
        rows = n
        return settle(changed: changed)
    }

    private func settle(changed: Bool) -> Bool {
        // A change of size alone is not the user leaving the end: the list is asked whether it follows
        // before the new place is judged. Under a finger the place is the user's doing, and is judged first.
        if dragging || !changed {
            scroll.observe(userDragging: dragging, atBottom: atBottom, rows: rows)
        }
        let go = changed && scroll.contentChanged(rows: rows)
        publish()
        return go
    }

    /// Reads back as a finger would: away from the end, and no longer following. For driving the page from a script.
    func leave() {
        scroll.observe(userDragging: true, atBottom: false, rows: rows)
        publish()
    }

    /// The user asked for the end.
    func jump() {
        scroll.jumpToEnd(rows: rows)
        publish()
    }
}

/// Tells when the user's finger is moving the list. The system says so from
/// iOS 18; before that a drag on the list is taken for it.
private struct DragWatch: ViewModifier {
    let onChange: (Bool) -> Void

    func body(content: Content) -> some View {
        if #available(iOS 18.0, *) {
            content.onScrollPhaseChange { _, phase in
                onChange(phase == .interacting || phase == .decelerating || phase == .tracking)
            }
        } else {
            content.simultaneousGesture(
                DragGesture(minimumDistance: 4).onChanged { _ in onChange(true) }.onEnded { _ in
                    onChange(false)
                })
        }
    }
}

/// Opens the list at its end. Where the system can be told apart what the
/// anchor is for (iOS 18), it is only that: a chat shorter than the screen
/// starts at the top, and a change of size moves nothing by itself, since a
/// list held to its end by the system would also drag back someone who is
/// reading earlier rows. Staying at the end is `ChatScrollTracker`'s.
private struct EndAnchored: ViewModifier {
    func body(content: Content) -> some View {
        if #available(iOS 18.0, *) {
            content
                .defaultScrollAnchor(.bottom, for: .initialOffset)
                .defaultScrollAnchor(.top, for: .sizeChanges)
                .defaultScrollAnchor(.top, for: .alignment)
        } else {
            content.defaultScrollAnchor(.bottom)
        }
    }
}

// MARK: The keyboard under the bar

/// How much of the window the keyboard covers now, read from UIKit's
/// keyboard layout guide, which follows the keyboard while a drag takes it
/// down; SwiftUI's safe area only changes when the drag ends.
@MainActor
@Observable
final class KeyboardTrack {
    /// The keyboard's height over the window's bottom edge; 0 without one.
    fileprivate(set) var overlap: CGFloat = 0
    /// The window's own bottom inset (the home indicator).
    fileprivate(set) var base: CGFloat = 0
    /// The bottom inset SwiftUI lays the page out with.
    var inset: CGFloat = 0

    /// How far the bar must be moved down to sit on the keyboard: nothing
    /// while SwiftUI and the keyboard agree, the difference while a drag is
    /// taking the keyboard away.
    var lag: CGFloat {
        guard inset > base + 1 else { return 0 }
        return max(0, inset - max(overlap, base))
    }
}

private struct KeyboardProbe: UIViewRepresentable {
    let track: KeyboardTrack

    func makeUIView(context: Context) -> ProbeView { ProbeView(track: track) }
    func updateUIView(_ view: ProbeView, context: Context) {}

    final class ProbeView: UIView {
        private let track: KeyboardTrack
        private var canvas: Canvas?

        init(track: KeyboardTrack) {
            self.track = track
            super.init(frame: .zero)
            isUserInteractionEnabled = false
            isHidden = true
        }

        @available(*, unavailable)
        required init?(coder: NSCoder) { fatalError("not from a nib") }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            canvas?.removeFromSuperview()
            canvas = nil
            guard let window else { return }
            let canvas = Canvas(track: track)
            window.insertSubview(canvas, at: 0)
            canvas.pin(to: window)
            self.canvas = canvas
        }
    }

    /// Covers the window, unseen and untouched, with a line tied to the top of the keyboard.
    final class Canvas: UIView {
        private let track: KeyboardTrack
        private let line = UIView()

        init(track: KeyboardTrack) {
            self.track = track
            super.init(frame: .zero)
            isUserInteractionEnabled = false
            isHidden = true
            translatesAutoresizingMaskIntoConstraints = false
            line.translatesAutoresizingMaskIntoConstraints = false
            addSubview(line)
            // The keyboard alone, not the home indicator under it.
            keyboardLayoutGuide.usesBottomSafeArea = false
            NSLayoutConstraint.activate([
                line.topAnchor.constraint(equalTo: keyboardLayoutGuide.topAnchor),
                line.leadingAnchor.constraint(equalTo: leadingAnchor),
                line.widthAnchor.constraint(equalToConstant: 1),
                line.heightAnchor.constraint(equalToConstant: 1),
            ])
        }

        @available(*, unavailable)
        required init?(coder: NSCoder) { fatalError("not from a nib") }

        func pin(to window: UIWindow) {
            NSLayoutConstraint.activate([
                topAnchor.constraint(equalTo: window.topAnchor),
                bottomAnchor.constraint(equalTo: window.bottomAnchor),
                leadingAnchor.constraint(equalTo: window.leadingAnchor),
                trailingAnchor.constraint(equalTo: window.trailingAnchor),
            ])
        }

        override func layoutSubviews() {
            super.layoutSubviews()
            let overlap = max(0, bounds.maxY - line.frame.minY)
            let base = window?.safeAreaInsets.bottom ?? 0
            guard abs(overlap - track.overlap) > 0.5 || abs(base - track.base) > 0.5 else { return }
            // Inside the system's own keyboard animation the bar follows with one of its own; under a finger it follows at once.
            let animated = UIView.inheritedAnimationDuration > 0
            let track = track
            if animated {
                withAnimation(.easeOut(duration: 0.25)) {
                    track.overlap = overlap
                    track.base = base
                }
            } else {
                track.overlap = overlap
                track.base = base
            }
        }
    }
}

// MARK: The pane

/// A message's text on a page of its own, where part of it can be selected.
private struct SelectedText: Identifiable {
    let text: String
    var id: String { text }
}

struct ChatPane: View {
    let session: SessionView
    let status: Status
    let chat: SessionModel
    let onTerminal: () -> Void

    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var open = ChatOpen()
    @State private var tracker = ChatScrollTracker()
    @State private var keyboard = KeyboardTrack()
    @State private var allPending = false
    @State private var selecting: SelectedText?
    @State private var announcer = LiveAnnouncer()
    @State private var appliedScheduledJump: String?
    @State private var announced = false
    @State private var paneHeight: CGFloat = 0
    @FocusState private var typing: Bool

    private static let end = "chat-end"

    private func selectMessage(_ text: String) {
        typing = false
        selecting = SelectedText(text: text)
    }

    private func toEnd(_ proxy: ScrollViewProxy, animated: Bool = false) {
        if animated && !reduceMotion {
            withAnimation(.easeOut(duration: 0.25)) { proxy.scrollTo(Self.end, anchor: .bottom) }
            return
        }
        proxy.scrollTo(Self.end, anchor: .bottom)
        // A lazy list places rows it has not measured by guess: asked once it may stop short of the end.
        Task {
            for _ in 0..<3 {
                await Task.yield()
                guard tracker.shortOfEnd else { return }
                proxy.scrollTo(Self.end, anchor: .bottom)
            }
        }
    }

    private func dockItems(_ d: Derived) -> [DockItem] {
        var items =
            d.approvals.values.map(DockItem.approval) + d.questions.values.map(DockItem.question)
        if session.info?.planOpen == true, let plan = d.plan {
            items.append(.plan(text: plan.text))
        }
        if status == .popup { items.append(.popup(title: session.info?.statusDetail ?? "")) }
        return items
    }

    var body: some View {
        let state = model.state
        let store = context.sessionStore
        let d = chat.derived
        let jump = context.sessionStore.scheduledJump(session.id)
        let rows = chat.rows.flatMap { row in
            if let jump, row.folded.contains(where: { if case .user(let u) = $0.item { return u.scheduledRunID == jump.runID }; return false }) { return row.folded }
            return [row]
        }
        let sid = session.id
        let online = state.conn == .online
        let env = ChatEnv(
            agent: session.agent, cwd: session.info?.cwd ?? "",
            home: state.machines[session.machineId]?.meta?.home ?? "",
            frozenAt: online ? 0 : state.offlineSinceMs, approvals: d.approvals,
            noResult: d.noResult)
        let pending = pendingInputs(d)
        let outgoing = store.outgoing(sid).filter { outgoingShown(input: d.inputs[$0.requestId]) }
        let dock = dockItems(d)
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 0) {
                    Color.clear.frame(height: 6)
                    if !chat.ready || (!chat.loaded && rows.isEmpty && chat.startPrompt == nil) {
                        Skeleton(inset: chatMargin)
                    }
                    if chat.unavailableCount > 0 {
                        Text("恢复前有 \(chat.unavailableCount) 条记录不可读；后续可读记录照常显示。")
                            .type(T.sm, Theme.muted).padding(.horizontal,16)
                    }
                    if chat.unverifiedLegacy {
                        Banner("旧格式记录的内容和来源未验证，仅供阅读。当前操作以电脑的已验证快照为准。", tone: .error).padding(
                            .horizontal, chatMargin)
                    }
                    if let message = chat.liveError {
                        Banner(message, tone: .error, action: "重试") {
                            context.remote.retryEvents(sid)
                        }.padding(.horizontal, chatMargin)
                    }
                    if let start = chat.startPrompt {
                        ChatBubble(
                            start.text, caption: start.caption,
                            captionColor: start.failed ? Theme.red : start.warning ? Theme.orangeInk : Theme.muted)
                    }
                    ForEach(rows) { row in
                        ScheduledChatRow(row: row, env: env).id(row.id)
                    }
                    // Offline, the connection bar says the content may be stale and the next
                    // connection fetches again: a failed fetch adds nothing to that.
                    if let failure = chat.error ?? (online ? chat.fetchError : nil) {
                        Banner("后面的记录没读到：\(failure)", tone: .error, action: "重试") {
                            context.remote.retryEvents(sid)
                        }
                        .padding(.horizontal, chatMargin).padding(.vertical, 8)
                    }
                    let visible = shownPending(pending, expanded: allPending)
                    // A waiting message and the same message on its way share a request ID: each kind of
                    // bubble is told apart from the other, or the list keeps the one it had.
                    ForEach(Array(visible.enumerated()), id: \.element.id) { i, p in
                        PendingBubble(
                            session: session, pending: p, index: i + 1, count: pending.count,
                            starting: status == .starting ? agentName(session.agent) : nil
                        )
                        .id("pending-" + p.id)
                    }
                    if visible.count < pending.count {
                        TextAction(
                            "还有 \(pending.count - 2) 条排队 展开", style: T.metaB, alignment: .trailing
                        ) { allPending = true }
                        .frame(maxWidth: .infinity, alignment: .trailing)
                        .padding(.horizontal, chatMargin)
                    }
                    // Messages from this client, until the chat shows them.
                    ForEach(outgoing) { o in
                        OutgoingBubble(
                            session: session, message: o, input: d.inputs[o.requestId],
                            failure: d.failures[o.requestId], onTerminal: onTerminal
                        )
                        .id("out-" + o.requestId)
                    }
                    Color.clear.frame(height: 8).id(Self.end)
                }
                // A reply is a column of text: on a wide screen it keeps the width it reads well at (design-system §4).
                .frame(maxWidth: 760)
                .frame(maxWidth: .infinity)
                .onGeometryChange(for: CGRect.self) {
                    $0.frame(in: .global)
                } action: { frame in
                    if tracker.content(height: frame.height, endY: frame.maxY) { toEnd(proxy) }
                }
            }
            .modifier(EndAnchored())
            .scrollDismissesKeyboard(.interactively)
            .modifier(DragWatch { tracker.dragging = $0 })
            .safeAreaInset(edge: .bottom, spacing: 0) {
                bottom(d, dock: dock)
                    .frame(maxWidth: 760)
                    .frame(maxWidth: .infinity)
                    .background(Theme.card.ignoresSafeArea(edges: .bottom))
                    .onGeometryChange(for: CGFloat.self) {
                        $0.frame(in: .global).minY
                    } action: { y in
                        if tracker.floor(y) { toEnd(proxy) }
                    }
                    .overlay(alignment: .top) {
                        pill {
                            tracker.jump()
                            toEnd(proxy, animated: true)
                        }
                        .alignmentGuide(.top) { $0[.bottom] + 8 }
                    }
                    .offset(y: keyboard.lag)
            }
            .onChange(of: rows.count + pending.count + outgoing.count, initial: true) { _, n in
                if tracker.count(n) { toEnd(proxy) }
            }
            .modifier(ScheduledJumpObserver(rowIDs: rows.map(\.id), loaded: chat.loaded, jump: jump) {
                if let jump, appliedScheduledJump != jump.runID, chat.loaded, !rows.isEmpty {
                    tracker.leave()
                    let exact = rows.first { if case .user(let u) = $0.item { return u.scheduledRunID == jump.runID }; return false }
                    let fallback = rows.first { $0.id.hasPrefix("e") && (UInt64($0.id.dropFirst()) ?? 0) >= jump.seq }
                    if let target = exact ?? fallback {
                        proxy.scrollTo(target.id, anchor: .top)
                        appliedScheduledJump = jump.runID
                    }
                }
            })
            .debugSignal("session.end") {
                tracker.jump()
                toEnd(proxy, animated: true)
            }
            .debugSignal("session.top") {
                // The rows as they are when the signal comes, not as they were when it was set up.
                tracker.leave()
                if let first = chat.rows.first { proxy.scrollTo(first.id, anchor: .top) }
            }
            .debugSignal("session.back") {
                tracker.leave()
                if let row = chat.rows.dropFirst(chat.rows.count * 3 / 4).first {
                    proxy.scrollTo(row.id, anchor: .top)
                }
            }
        }
        .onDisappear { context.sessionStore.clearScheduledJump(sid) }
        .background { KeyboardProbe(track: keyboard).frame(width: 0, height: 0) }
        .onGeometryChange(for: CGFloat.self) {
            $0.safeAreaInsets.bottom
        } action: {
            keyboard.inset = $0
        }
        // What the chat shows of itself, above the keyboard: an open dock item takes up to about 60% of it.
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { paneHeight = $0 }
        .environment(\.chatHeight, paneHeight)
        .environment(open)
        .environment(
            \.chatActions,
            ChatActions(
                image: { seq, index in nav.open(.image(session: sid, seq: seq, index: index)) },
                session: { nav.open(.session(id: $0)) },
                putBack: { requestId, text in
                    // A message the session ended before writing: its text goes back into the box.
                    if let o = store.find(sid, requestId: requestId) {
                        SessionImages.putBack(
                            o, session: sid, state: model.state, remote: context.remote,
                            store: store)
                    } else {
                        store.appendToDraft(sid, text)
                    }
                },
                select: selectMessage)
        )
        .sheet(item: $selecting) { picked in
            MessageTextSelection(text: picked.text) { selecting = nil }
        }
        // Also when the pane comes back from the terminal with the events it already had.
        .onChange(of: d.inputDetails, initial: true) { _, inputs in
            store.settle(sid, inputs: inputs)
            store.settleCancels(inputs: d.inputs)
        }
        .onChange(of: rows.last?.id, initial: true) { _, _ in announce(rows.last?.item, dock) }
        .onChange(of: dock.map(\.id)) { _, _ in announce(rows.last?.item, dock) }
        .debugSignal("session.focus") { typing = true }
        .debugSignal("session.blur") { typing = false }
        .debugSignal("session.select") {
            for row in chat.rows.reversed() {
                if case .agent(let reply) = row.item {
                    selectMessage(reply.text)
                    break
                }
            }
        }
    }

    private func announce(_ last: ChatItem?, _ dock: [DockItem]) {
        // What was there when the page opened is read, not announced.
        let initial = !announced || !chat.ready
        if chat.ready { announced = true }
        let word = announcer.announce(
            last: last, dock: dock, agent: agentName(session.agent), now: nowMs(), initial: initial)
        if let word, UIAccessibility.isVoiceOverRunning {
            AccessibilityNotification.Announcement(word).post()
        }
    }

    /// What came in while reading back, or only the way to the end.
    @ViewBuilder private func pill(_ action: @escaping () -> Void) -> some View {
        if tracker.fresh > 0 {
            Button(action: action) {
                IconText(Lucide.arrowDown, "\(tracker.fresh) 条新消息", color: Theme.blue, style: T.smB)
                    .padding(.horizontal, 12)
                    .frame(minHeight: Theme.minHit)
                    .background(
                        Theme.card, in: RoundedRectangle(cornerRadius: 16, style: .continuous)
                    )
                    // In the dark a shadow shows nothing: the line tells the pill from the chat under it.
                    .overlay {
                        RoundedRectangle(cornerRadius: 16, style: .continuous).strokeBorder(
                            Theme.line, lineWidth: 1)
                    }
                    .compositingGroup()
                    .shadow(color: .black.opacity(0.15), radius: 10, y: 3)
            }
            .buttonStyle(PressStyle())
            .transition(.opacity)
        } else if tracker.away {
            Button(action: action) {
                Ic(Lucide.arrowDown, size: 18, color: Theme.blue)
                    .frame(width: 36, height: 36)
                    .background(Theme.card, in: Circle())
                    .overlay { Circle().strokeBorder(Theme.line, lineWidth: 1) }
                    .compositingGroup()
                    .shadow(color: .black.opacity(0.15), radius: 10, y: 3)
                    .hitTarget()
            }
            .buttonStyle(PressStyle())
            .accessibilityLabel("回到最新消息")
            .transition(.opacity)
        }
    }

    /// The dock and the message box, or a card when nothing can be sent.
    @ViewBuilder private func bottom(_ d: Derived, dock: [DockItem]) -> some View {
        let state = model.state
        let machine = state.machines[session.machineId]
        let name = machineLabel(state, id: session.machineId)
        if status == .exited {
            VStack(spacing: 0) {
                if session.info?.taskOwned == true { Text("回复后，这个对话会移到会话列表").type(T.meta, Theme.muted).padding(8) }
                EndedCard(scheduledClosed: d.scheduledClosed, session: session, machine: machine, name: name)
            }
        } else if status == .offline {
            Ticking { now in
                let seen = machine?.lastSeenMs ?? 0
                SessionEndCard(
                    "\(name) 不在线",
                    text: offlineText(lastSeen: seen > 0 ? whenText(seen, now: now) : "")
                ) { EmptyView() }
            }
        } else {
            VStack(spacing: 0) {
                if let first = dock.first {
                    let more = dock.count - 1
                    let hide = { typing = false }
                    let store = context.sessionStore
                    let key = dockFoldKey(session: session.id, first)
                    // Open until the user folds it, or folded when it came with a draft in the box (`SessionStore.dockOpen`).
                    let sid = session.id
                    let remote = context.remote
                    let open = Binding(
                        get: {
                            store.dockOpen(key) {
                                !store.draft(sid).jvmTrim().isEmpty
                                    || !ImageCenter.of(remote).draft(ImageCenter.sessionKey(sid)).images.isEmpty
                            } && !SessionDebug.shows("fold")
                        },
                        set: { store.setDockOpen(key, $0) })
                    Group {
                        switch first {
                        case .approval(let a):
                            ApprovalDock(
                                session: session, approval: a, more: more, open: open,
                                startReason: SessionDebug.shows("reason"), onOpen: hide)
                        case .question(let q):
                            QuestionDock(
                                session: session, question: q, more: more, open: open, onOpen: hide,
                                onTerminal: onTerminal
                            ) { nav.open(.question(session: session.id, question: q.questionID)) }
                        case .plan(let text):
                            PlanDock(session: session, text: text, more: more, open: open, onOpen: hide) {
                                nav.open(.plan(session: session.id))
                            }
                        case .popup(let title):
                            PopupDock(
                                session: session, title: title, more: more, open: open, onOpen: hide,
                                onTerminal: onTerminal)
                        }
                    }
                    // Another item is another card: what was opened or typed in the one before does not carry over.
                    .id(first.id)
                }
                if session.info?.taskOwned == true { Text("回复后，这个对话会移到会话列表").type(T.meta, Theme.muted).padding(8) }
                SessionComposer(session: session, status: status, derived: d, focus: $typing)
            }
        }
    }
}

// MARK: Under the rows

/// A message the model has not received yet, at the end of the chat: what
/// it waits for, and a way to take it back (architecture §6).
private struct PendingBubble: View {
    let session: SessionView
    let pending: PendingInput
    let index: Int
    /// How many messages wait, this one among them.
    let count: Int
    let starting: String?
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var expanded = false
    @State private var resume = ActionRunner()

    var body: some View {
        let store = context.sessionStore
        let (remote, sid, id) = (context.remote, session.id, pending.id)
        let cancel = store.cancels[id]
        let online = model.state.conn == .online
        let canceling = cancel == .canceling
        let cancelFailed = cancel != nil && !canceling
        let label = pendingLabel(
            pending, cancel: cancel, resuming: resume.running, resumeFailed: !resume.error.isEmpty,
            starting: starting)
        let origin = originLabel(pending.origin, state: model.state, me: context.me, machine: "")
        if isSkeleton(pending.id) || isSkeleton(pending.text) {
            Text(skeletonMessage).type(T.sm, Theme.muted)
        } else {
            VStack(alignment: .trailing, spacing: 0) {
                Button {
                    expanded.toggle()
                } label: {
                    ChatBubble(
                        pending.text.isEmpty ? "（图片）" : pending.text, pending: true,
                        lines: expanded ? nil : 4, padded: false
                    )
                    .padding(.vertical, -8)
                    .opacity(canceling ? 0.5 : 1)
                }
                .buttonStyle(PressStyle())
                .accessibilityHint(expanded ? "收起" : "展开全文")
                HStack(spacing: 6) {
                    // The note gives way; what can be done stays.
                    Text(label + pendingSuffix(pending, origin: origin, index: index, of: count, cancel: cancel))
                        .type(T.meta, cancelFailed ? Theme.red : Theme.muted).lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .trailing)
                    if pending.paused && !canceling {
                        TextAction(
                            resume.error.isEmpty ? "发送" : "重试", style: T.metaB,
                            enabled: online && !resume.running, alignment: .center
                        ) {
                            SessionHaptics.tap()
                            resume.run {
                                var cmd = PBCommand()
                                cmd.resumeInput = .with {
                                    $0.sessionID = sid
                                    $0.requestID = id
                                }
                                try await remote.command(sid, cmd)
                            }
                        }
                    }
                    if !canceling {
                        TextAction(
                            cancelFailed ? "重试" : "取消", style: T.metaB, enabled: online,
                            alignment: .trailing
                        ) {
                            store.cancel(remote, sid: sid, requestId: id)
                        }
                    }
                }
                .frame(minHeight: 36)
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
            .padding(.horizontal, chatMargin).padding(.vertical, 6)
            .debugSignal("session.cancel") { store.cancel(remote, sid: sid, requestId: id) }
            .debugSignal("session.resume") {
                resume.run {
                    var cmd = PBCommand()
                    cmd.resumeInput = .with {
                        $0.sessionID = sid
                        $0.requestID = id
                    }
                    try await remote.command(sid, cmd)
                }
            }
        }
    }
}

/// A message sent from this client, until the daemon's events show it: how sending stands, and what to do when it did not go.
private struct OutgoingBubble: View {
    let session: SessionView
    let message: Outgoing
    let input: PBInputStatus?
    let failure: String?
    let onTerminal: () -> Void
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context

    private func act(_ action: OutgoingAction) {
        let store = context.sessionStore
        switch action {
        case .retry: store.deliver(context.remote, sid: session.id, message)
        case .remove:
            store.remove(session.id, message)
            message.images.forEach { $0.release() }
        case .putBack:
            SessionImages.putBack(
                message, session: session.id, state: model.state, remote: context.remote,
                store: store)
        case .terminal: onTerminal()
        }
    }

    var body: some View {
        let note = outgoingNote(
            state: message.state, error: message.error, input: input, failure: failure,
            agent: session.agent, hasImages: !message.images.isEmpty,
            missingImages: message.missingImages)
        let ink: Color =
            switch note.tone {
            case .plain: Theme.muted
            case .warn: Theme.orangeInk
            case .error: Theme.red
            }
        ChatBubble(
            message.text.isEmpty ? nil : message.text, caption: note.caption, captionColor: ink,
            captionIcon: note.sending ? Lucide.loaderCircle : nil, failed: note.failed,
            links: note.actions.map { action in ChatLink(action.label) { act(action) } }
        ) {
            SessionImages.OutgoingThumbs(message: message)
        }
        .accessibilityElement(children: .contain)
    }
}

/// In place of the message box when nothing can be sent: the session ended, or its computer is away.
struct SessionEndCard<Actions: View>: View {
    private let title: String
    private let text: String
    private let dot: Color
    private let actions: Actions

    init(
        _ title: String, text: String, dot: Color = Theme.gray, @ViewBuilder actions: () -> Actions
    ) {
        self.title = title
        self.text = text
        self.dot = dot
        self.actions = actions()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Dot(dot)
                Text(title).type(T.bodyB, Theme.fg)
            }
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isHeader)
            Text(text).type(T.sm, Theme.muted).padding(.top, 3)
                .frame(maxWidth: .infinity, alignment: .leading)
            // Whatever can be done stands 10 pt under the text; with nothing to do the card ends with it.
            if Actions.self != EmptyView.self {
                VStack(alignment: .leading, spacing: 0) { actions }.padding(.top, 10)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 12)
        .background(
            Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous)
        )
        .padding(.init(top: 6, leading: 12, bottom: 8, trailing: 12))
    }
}

/// 会话已结束, with 恢复会话 while its computer can be reached.
private struct EndedCard: View {
    let scheduledClosed: Bool
    let session: SessionView
    let machine: MachineView?
    let name: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var act = ActionRunner()

    var body: some View {
        let state = model.state
        let reachable = machine?.online == true && !state.unlinked
        SessionEndCard(
            act.running ? "正在恢复…" : "会话已结束",
            text: endedText(
                detail: session.info?.statusDetail ?? "", scheduledClosed: scheduledClosed, unlinked: state.unlinked,
                machineOnline: machine?.online == true, machine: name),
            dot: act.running ? Theme.blueBg : Theme.gray
        ) {
            if reachable && !act.running {
                Btn(
                    act.error.isEmpty ? "恢复会话" : "再试一次",
                    kind: act.error.isEmpty ? .primary : .secondary,
                    enabled: state.conn == .online, background: act.error.isEmpty ? nil : Theme.card
                ) {
                    SessionHaptics.tap()
                    restore()
                }
            }
            if !act.error.isEmpty {
                Text("恢复失败：\(act.error)").type(T.meta, Theme.red).padding(.top, 6)
            }
        }
        .debugSignal("session.restore") { if reachable { restore() } }
    }
    private func restore() {
        guard model.state.conn == .online && machine?.online == true else { return }
        act.run {
            var cmd = PBCommand(); cmd.resumeSession.sessionID = session.id
            try await context.remote.command(session.id, cmd)
        }
    }
}

/// Loading can finish without changing row IDs; that transition must retry a pending jump.
struct ScheduledJumpObserver: ViewModifier {
    let rowIDs: [String]
    let loaded: Bool
    let jump: SessionStore.ScheduledJump?
    let apply: () -> Void
    private struct Input: Equatable {
        let rowIDs: [String]
        let loaded: Bool
        let jump: SessionStore.ScheduledJump?
    }
    func body(content: Content) -> some View {
        content.onChange(of: Input(rowIDs: rowIDs, loaded: loaded, jump: jump), initial: true) { _, _ in apply() }
    }
}
