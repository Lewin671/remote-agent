// The chat of a session: its rows, what is still on its way under them, and
// at the bottom what waits for the user and the message box. Android's
// `ChatPane` and `Bottom` in ui/Session.kt.
//
// Scrolling. UIKit caches self-sizing SwiftUI cells and owns the position.
// The tracker publishes only the new-row count and jump-to-end pill; the
// collection controller handles follow, history anchors and user gestures.
//
// The keyboard. The bottom bar is the scroll view's safe-area inset, so
// the system moves it with the keyboard and the rows end above it. A drag
// on the chat takes the keyboard down with the finger
// (`UIScrollView.keyboardDismissMode = .interactive`); SwiftUI only learns of the
// keyboard's place when that drag ends, so meanwhile the bar is kept on
// the keyboard by `KeyboardTrack`, which reads UIKit's keyboard layout
// guide.
import RACore
import RAMarkdownUI
import RAProto
import SwiftUI
import UIKit

// MARK: Where the end of the list is

/// Coalesce UIKit layout/scroll observations outside layout. A gesture cancels
/// pending follow work before it can issue a programmatic move.
@MainActor
final class ChatScrollWork {
    private let diagnosticEntity = ChatDiagnosticRing.shared.identity()
    private var task: Task<Void, Never>?
    private var action: (() -> Void)?

    func schedule(_ action: @escaping () -> Void) {
        ChatDiagnosticRing.shared.record(.workSchedule, entity: diagnosticEntity)
        self.action = action
        guard task == nil else { return }
        task = Task { [weak self] in
            do { try await Task.sleep(for: .milliseconds(16)) }
            catch { return }
            guard let self else { return }
            let action = self.action
            self.action = nil
            self.task = nil
            ChatDiagnosticRing.shared.record(.workExecute, entity: diagnosticEntity)
            action?()
        }
    }

    func cancel() {
        ChatDiagnosticRing.shared.record(.workCancel, entity: diagnosticEntity)
        task?.cancel()
        task = nil
        action = nil
    }
}

/// The list's measurements and the decision they lead to. Not observed as a
/// whole: it changes on every frame of a scroll, and only what the screen
/// draws from it (`fresh`, `away`) is published.
@MainActor
@Observable
final class ChatScrollTracker {
    @ObservationIgnored let diagnosticEntity = ChatDiagnosticRing.shared.identity()
    /// Rows that came in while reading back.
    private(set) var fresh = 0
    /// The end is more than a little out of sight.
    private(set) var away = false

    @ObservationIgnored private(set) var scroll = ChatScroll()
    @ObservationIgnored private(set) var dragging = false
    @ObservationIgnored private var requestedRow: String?
    @ObservationIgnored weak var controller: ChatCollectionController? {
        didSet {
            if let controller, let requestedRow {
                self.requestedRow = nil
                controller.toRow(requestedRow)
            }
        }
    }

    @ObservationIgnored private var contentHeight: CGFloat = 0
    @ObservationIgnored private var endY: CGFloat = 0
    @ObservationIgnored private var floorY: CGFloat = 0
    @ObservationIgnored private var rows = 0
    @ObservationIgnored private var measured = false
    @ObservationIgnored private let followWork = ChatScrollWork()

    /// The end counts as in sight within this of the bar: a list at rest is rarely there to the point.
    static let slack: CGFloat = 28

    private var atBottom: Bool { !measured || endY <= floorY + Self.slack }
    // Follow slack decides intent, not whether another layout is needed.
    private var needsScroll: Bool { measured && endY > floorY + 0.5 }

    /// An initial scheduled jump can arrive before the representable mounts.
    func toRow(_ id: String) {
        if let controller { controller.toRow(id) }
        else { requestedRow = id }
    }

    func follow(_ action: @escaping () -> Void) {
        ChatDiagnosticRing.shared.record(.followRequest, entity: diagnosticEntity, Double(endY), Double(floorY), scroll.following ? 1 : 0, dragging ? 1 : 0)
        followWork.schedule { [weak self] in
            guard let self, !self.dragging, !self.keeping, self.scroll.following, self.needsScroll else { return }
            ChatDiagnosticRing.shared.record(.followExecute, entity: self.diagnosticEntity)
            action()
        }
    }

    func cancelFollow() { followWork.cancel() }

    private func publish() {
        let f = scroll.fresh(rows: rows)
        if f != fresh { ChatDiagnosticRing.shared.record(.fresh, entity: diagnosticEntity, Double(f)); fresh = f }
        let a = measured && endY > floorY + 240
        if a != away { ChatDiagnosticRing.shared.record(.away, entity: diagnosticEntity, a ? 1 : 0); away = a }
    }

    /// Rows joined in front, and the list is being put back where the reader was (UIKit snapshot update).
    @ObservationIgnored private(set) var keeping = false

    /// The list is put back where the reader was: it is neither judged nor sent to the end meanwhile.
    func keep() {
        cancelFollow()
        ChatDiagnosticRing.shared.record(.keep, entity: diagnosticEntity)
        keeping = true
    }

    /// The list is back, or the reader took it over: judged again from the next change.
    func kept() {
        guard keeping else { return }
        ChatDiagnosticRing.shared.record(.kept, entity: diagnosticEntity)
        keeping = false
        // The collection publishes its coherent post-update viewport later.
    }

    /// A new gesture owns the position, including any correction still pending
    /// from an earlier history join. Follow again after release if still at the end.
    func interaction(_ active: Bool) -> Bool {
        let ended = dragging && !active
        ChatDiagnosticRing.shared.record(.dragging, entity: diagnosticEntity, active ? 1 : 0)
        dragging = active
        if active {
            requestedRow = nil
            cancelFollow()
            kept()
        }
        return ended && !keeping && scroll.following && needsScroll
    }

    /// UIKit delivers one coherent viewport sample. Update all coordinates
    /// before judging intent; mixing an old end with a new floor could revive
    /// following just before a requested jump to an earlier row.
    func viewport(height: CGFloat, offset: CGFloat, floor: CGFloat, count: Int) -> Bool {
        ChatDiagnosticRing.shared.record(.content, entity: diagnosticEntity, Double(height), Double(height - offset))
        ChatDiagnosticRing.shared.record(.floor, entity: diagnosticEntity, Double(floor))
        ChatDiagnosticRing.shared.record(.count, entity: diagnosticEntity, Double(count))
        let changed = abs(height - contentHeight) > 0.5 || abs(floor - floorY) > 0.5 || count != rows
        contentHeight = height
        endY = height - offset
        floorY = floor
        rows = count
        measured = true
        return settle(changed: changed)
    }

    private func settle(changed: Bool) -> Bool {
        // The list is being put back where the reader was: neither judged nor sent to the end meanwhile.
        if keeping { return false }
        // A change of size alone is not the user leaving the end: the list is asked whether it follows
        // before the new place is judged. Under a finger the place is the user's doing, and is judged first.
        if dragging || !changed {
            scroll.observe(userDragging: dragging, atBottom: atBottom, rows: rows)
        }
        let go = changed && scroll.contentChanged(rows: rows)
        publish()
        // Even at the end, a programmatic scroll competes with the finger
        // (or its glide). Keep observing it, and follow after release instead.
        return go && !dragging && needsScroll
    }

    /// Reads back as a finger would: away from the end, and no longer following. For driving the page from a script.
    func leave() {
        cancelFollow()
        ChatDiagnosticRing.shared.record(.leave, entity: diagnosticEntity)
        keeping = false
        scroll.observe(userDragging: true, atBottom: false, rows: rows)
        publish()
    }

    /// Rows came in front of those there were.
    func prepended(_ n: Int) {
        ChatDiagnosticRing.shared.record(.prepend, entity: diagnosticEntity, Double(n))
        scroll.prepended(n)
        // Do not publish against the old row count while a snapshot is being
        // applied: the next viewport includes the prepend and its new count.
    }

    /// The user asked for the end.
    func jump() {
        cancelFollow()
        ChatDiagnosticRing.shared.record(.jump, entity: diagnosticEntity)
        keeping = false
        scroll.jumpToEnd(rows: rows)
        publish()
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
        guard inset > base + 1 else {
            ChatDiagnosticRing.shared.record(.keyboardLag, 0)
            return 0
        }
        let value = max(0, inset - max(overlap, base))
        ChatDiagnosticRing.shared.record(.keyboardLag, Double(value))
        return value
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
            let pass = ChatDiagnosticRing.shared.beginLayout(.keyboardLayout)
            defer { ChatDiagnosticRing.shared.endLayout(pass) }
            super.layoutSubviews()
            let overlap = max(0, bounds.maxY - line.frame.minY)
            let base = window?.safeAreaInsets.bottom ?? 0
            guard abs(overlap - track.overlap) > 0.5 || abs(base - track.base) > 0.5 else { return }
            ChatDiagnosticRing.shared.record(.keyboardGuide, Double(overlap), Double(base))
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

    private func selectMessage(_ text: String) {
        typing = false
        selecting = SelectedText(text: text)
    }

    private func toEnd() { tracker.controller?.toEnd() }

    /// Back to the end of the chat and following again, from wherever the reader was: the user asked for it
    /// (the 回到最新 pill) or sent a message and wants to see the answer. What others send does not do this.
    private func followEnd() {
        tracker.jump()
        toEnd()
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
        let diagnosticEntity = tracker.diagnosticEntity
        let _ = ChatDiagnosticRing.shared.record(.chatBody, entity: diagnosticEntity, Double(chat.diagnosticEntity))
        let state = model.state
        let store = context.sessionStore
        let d = chat.derived
        let jump = context.sessionStore.scheduledJump(session.id)
        // The rows as shown: a group holding the scheduled run jumped to is unfolded.
        let shown = { (row: ChatRow) -> [ChatRow] in
            if let jump, row.folded.contains(where: { if case .user(let u) = $0.item { return u.scheduledRunID == jump.runID }; return false }) { return row.folded }
            return [row]
        }
        let rows = chat.rows.flatMap(shown)
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
        ChatCollection(
            items: listItems(rows: rows, env: env, pending: pending, outgoing: outgoing, derived: d, online: online),
            env: env, tracker: tracker, ready: chat.ready, joins: chat.joins,
            prepended: chat.rows.prefix(chat.joined).reduce(0) { $0 + shown($1).count },
            count: rows.count + pending.count + outgoing.count,
            interaction: { chat.interaction($0) })
        .safeAreaInset(edge: .bottom, spacing: 0) {
            bottom(d, dock: dock)
                .frame(maxWidth: 760)
                .frame(maxWidth: .infinity)
                // The message box floats on glass: the chat fades out under it, it is not cut by a bar.
                .background { EdgeFade(edge: .bottom) }
                .overlay(alignment: .top) {
                    pill { followEnd() }
                    .alignmentGuide(.top) { $0[.bottom] + 8 }
                    // It rises in and fades out (§4 动效).
                    .animation(Motion.enter(), value: tracker.fresh > 0)
                    .animation(Motion.enter(), value: tracker.away)
                }
                .offset(y: keyboard.lag)
        }
        .modifier(ScheduledJumpObserver(rowIDs: rows.map(\.id), loaded: chat.loaded, jump: jump) {
            if let jump, appliedScheduledJump != jump.runID, chat.loaded, !rows.isEmpty {
                tracker.leave()
                let exact = rows.first { if case .user(let u) = $0.item { return u.scheduledRunID == jump.runID }; return false }
                let fallback = rows.first { $0.id.hasPrefix("e") && (UInt64($0.id.dropFirst()) ?? 0) >= jump.seq }
                if let target = exact ?? fallback {
                    ChatDiagnosticRing.shared.record(.proxyScheduled, entity: tracker.diagnosticEntity)
                    tracker.toRow(target.id)
                    appliedScheduledJump = jump.runID
                }
            }
        })
        .debugSignal("session.end") { followEnd() }
        .debugSignal("session.top") {
            // The rows as they are when the signal comes, not as they were when it was set up.
            tracker.leave()
            if let first = chat.rows.first { ChatDiagnosticRing.shared.record(.proxyTop, entity: tracker.diagnosticEntity); tracker.toRow(first.id) }
        }
        .debugSignal("session.back") {
            tracker.leave()
            if let row = chat.rows.dropFirst(chat.rows.count * 3 / 4).first {
                ChatDiagnosticRing.shared.record(.proxyBack, entity: tracker.diagnosticEntity)
                tracker.toRow(row.id)
            }
        }
        .onAppear { ChatDiagnosticRing.shared.record(.chatEnter) }
        .onDisappear {
            ChatDiagnosticRing.shared.record(.chatLeave)
            tracker.cancelFollow()
            chat.interaction(false)
            context.sessionStore.clearScheduledJump(sid)
        }
        .background { KeyboardProbe(track: keyboard).frame(width: 0, height: 0) }
        .onGeometryChange(for: CGFloat.self) { g in
            ChatDiagnosticRing.shared.measure(.keyboardInset) { g.safeAreaInsets.bottom }
        } action: {
            ChatDiagnosticRing.shared.record(.keyboardInset, Double($0))
            keyboard.inset = $0
        }
        // What the chat shows of itself, above the keyboard: an open dock item takes up to about 60% of it.
        .onGeometryChange(for: CGFloat.self) { g in ChatDiagnosticRing.shared.measure(.paneHeight) { g.size.height } } action: {
            ChatDiagnosticRing.shared.record(.paneHeight, Double($0)); paneHeight = $0
        }
        .environment(\.chatHeight, paneHeight)
        .environment(open)
        .environment(
            \.chatActions,
            ChatActions(
                image: { seq, index in nav.open(.image(session: sid, seq: seq, index: index)) },
                session: { nav.open(.session(id: $0)) },
                putBack: { requestId, text, files in
                    // A message the session ended before writing: its text goes back into the box.
                    if let o = store.find(sid, requestId: requestId) {
                        SessionImages.putBack(
                            o, session: sid, state: model.state, remote: context.remote,
                            store: store)
                    } else {
                        let center = ImageCenter.of(context.remote)
                        center.putBack(sid: sid, text: text, files: files, store: store)
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

    /// Each auxiliary row participates in the same diffable snapshot as messages.
    private func listItems(rows: [ChatRow], env: ChatEnv, pending: [PendingInput], outgoing: [Outgoing],
                           derived d: Derived, online: Bool) -> [ChatCollectionItem] {
        var items = [ChatCollectionItem("chat-top") { Color.clear.frame(height: 6) }]
        let sid = session.id
        if !chat.ready || (!chat.loaded && rows.isEmpty && chat.startPrompt == nil) {
            items.append(ChatCollectionItem("chat-loading") { Skeleton(inset: chatMargin) })
        }
        if chat.historyPartial && chat.error == nil && chat.fetchError == nil {
            items.append(ChatCollectionItem("chat-history") {
                Text("历史记录尚未读全。").font(.subheadline).ink(DS.ink2).padding(.horizontal, chatMargin)
            })
        }
        if chat.unavailableCount > 0 {
            items.append(ChatCollectionItem("chat-unavailable") {
                Text("恢复前有 \(chat.unavailableCount) 条记录不可读；后续可读记录照常显示。")
                    .font(.subheadline).ink(DS.ink2).padding(.horizontal, 16)
            })
        }
        if let message = ImageCenter.of(context.remote).outgoingRestoreErrors[sid] {
            items.append(ChatCollectionItem("chat-outgoing-restore-error") {
                Banner(message, tone: .error, action: "重试") {
                    ImageCenter.of(context.remote).restoreOutgoing(sid, store: context.sessionStore)
                }.padding(.horizontal, chatMargin)
            })
        }
        if let message = chat.liveError {
            items.append(ChatCollectionItem("chat-live-error") {
                Banner(message, tone: .error, action: "重试") { context.remote.retryEvents(sid) }
                    .padding(.horizontal, chatMargin)
            })
        }
        if let start = chat.startPrompt {
            items.append(ChatCollectionItem("chat-start") {
                ChatBubble(start.text, caption: start.caption,
                           captionColor: start.failed ? DS.red : start.warning ? DS.orangeInk : DS.ink2)
            })
        }
        items += rows.map { row in
            ChatCollectionItem(row.id, row: row) { ScheduledChatRow(row: row, env: env) }
        }
        if let failure = chat.error ?? (online ? chat.fetchError : nil) {
            items.append(ChatCollectionItem("chat-fetch-error") {
                Banner("后面的记录没读到：\(failure)", tone: .error, action: "重试") { context.remote.retryEvents(sid) }
                    .padding(.horizontal, chatMargin).padding(.vertical, 8)
            })
        }
        let visible = shownPending(pending, expanded: allPending)
        for (i, p) in visible.enumerated() {
            items.append(ChatCollectionItem("pending-" + p.id) {
                PendingBubble(session: session, pending: p, index: i + 1, count: pending.count,
                              starting: status == .starting ? agentName(session.agent) : nil)
            })
        }
        if visible.count < pending.count {
            items.append(ChatCollectionItem("chat-more-pending") {
                TextAction("还有 \(pending.count - 2) 条排队 展开", style: .footnote.weight(.semibold), alignment: .trailing) { allPending = true }
                    .frame(maxWidth: .infinity, alignment: .trailing).padding(.horizontal, chatMargin)
            })
        }
        for o in outgoing {
            items.append(ChatCollectionItem("out-" + o.requestId) {
                OutgoingBubble(session: session, message: o, input: d.inputs[o.requestId],
                               failure: d.failures[o.requestId], onTerminal: onTerminal)
            })
        }
        items.append(ChatCollectionItem("chat-end") { Color.clear.frame(height: 8) })
        return items
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
                Label("\(tracker.fresh) 条新消息", systemImage: "arrow.down")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(DS.ink)
                    .padding(.horizontal, 14)
                    .frame(minHeight: 36)
                    .glass(Capsule())
                    .frame(minHeight: DS.minHit)
            }
            .buttonStyle(.plain)
            .transition(Motion.appear(rise: Motion.bubbleRise))
        } else if tracker.away {
            Button(action: action) {
                Image(systemName: "arrow.down")
                    .font(.body.weight(.semibold))
                    .foregroundStyle(DS.ink)
                    .frame(width: 36, height: 36)
                    .glass(Circle())
                    .frame(minWidth: DS.minHit, minHeight: DS.minHit)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("回到最新消息")
            .transition(Motion.appear(rise: Motion.bubbleRise))
        }
    }

    /// The dock and the message box, or a card when nothing can be sent.
    @ViewBuilder private func bottom(_ d: Derived, dock: [DockItem]) -> some View {
        let state = model.state
        let machine = state.machines[session.machineId]
        let name = machineLabel(state, id: session.machineId)
        if status == .exited {
            VStack(spacing: 0) {
                if session.info?.taskOwned == true { Text("回复后，这个对话会移到会话列表").font(.footnote).foregroundStyle(DS.ink2).padding(8) }
                EndedCard(scheduledClosed: d.scheduledClosed, idleClosed: d.idleClosed, session: session, machine: machine, name: name)
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
                    // It comes up from the message box, 12 points and fading, while the chat gives up its room;
                    // answered, it fades and closes (§4 动效, 停靠卡).
                    .transition(Motion.roomy(rise: Motion.dockRise))
                }
                if session.info?.taskOwned == true { Text("回复后，这个对话会移到会话列表").font(.footnote).foregroundStyle(DS.ink2).padding(8) }
                SessionComposer(session: session, status: status, derived: d, focus: $typing, onSent: followEnd)
            }
            .animation(dock.first == nil ? Motion.exit() : Motion.enter(), value: dock.first?.id)
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
            Text(skeletonMessage).font(.subheadline).ink(DS.ink2)
        } else {
            VStack(alignment: .trailing, spacing: 0) {
                Button {
                    expanded.toggle()
                } label: {
                    ChatBubble(
                        pending.text.isEmpty ? (pending.files.isEmpty ? "（图片）" : nil) : pending.text, pending: true,
                        lines: expanded ? nil : 4, padded: false
                    ) {
                        SessionImages.PendingThumbs(session: sid, requestId: id)
                        FileChips(files: pending.files)
                    }
                    .padding(.vertical, -8)
                    .opacity(canceling ? 0.5 : 1)
                }
                .buttonStyle(PressStyle())
                .accessibilityHint(expanded ? "收起" : "展开全文")
                HStack(spacing: 6) {
                    // The note gives way; what can be done stays.
                    Text(scheduledPendingLabel(pending, label: label) ?? (label + pendingSuffix(pending, origin: origin, index: index, of: count, cancel: cancel)))
                        .font(.footnote).ink(cancelFailed ? DS.red : DS.ink2).lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .trailing)
                    if pending.paused && !canceling {
                        TextAction(
                            resume.error.isEmpty ? "发送" : "重试", style: .footnote.weight(.semibold),
                            enabled: online && !resume.running, alignment: .center
                        ) {
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
                            cancelFailed ? "重试" : "取消", style: .footnote.weight(.semibold), enabled: online,
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
            agent: session.agent, hasImages: message.canPutBack,
            missingImages: message.missingImages)
        let ink: Color =
            switch note.tone {
            case .plain: DS.ink2
            case .warn: DS.orangeInk
            case .error: DS.red
            }
        ChatBubble(
            message.text.isEmpty ? nil : message.text, caption: note.caption, captionColor: ink,
            captionIcon: note.sending ? Mark.progress : nil, failed: note.failed,
            links: note.actions.filter { $0 != .putBack || (message.imageIds.isEmpty && message.fileIds.isEmpty) || message.hasLocalAttachments }
                .map { action in ChatLink(action.label) { act(action) } }
        ) {
            SessionImages.OutgoingThumbs(message: message)
        }
        .accessibilityElement(children: .contain)
    }
}

/// In place of the message box when nothing can be sent: the session
/// ended, or its computer is away. A card of glass where the capsule
/// would float, its state's mark, one line of why and what can be done.
struct SessionEndCard<Actions: View>: View {
    private let title: String
    private let text: String
    private let mark: StatusKind
    private let actions: Actions

    init(_ title: String, text: String, mark: StatusKind = .off, @ViewBuilder actions: () -> Actions) {
        self.title = title
        self.text = text
        self.mark = mark
        self.actions = actions()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                StatusMark(mark)
                Text(title).font(.headline).foregroundStyle(DS.ink)
            }
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isHeader)
            Text(text).font(.subheadline).foregroundStyle(DS.ink2).padding(.top, 4)
                .frame(maxWidth: .infinity, alignment: .leading)
            // Whatever can be done stands under the text; with nothing to do the card ends with it.
            if Actions.self != EmptyView.self {
                VStack(alignment: .leading, spacing: 0) { actions }.padding(.top, 12)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(.horizontal, 18).padding(.vertical, 16)
        .glass(RoundedRectangle(cornerRadius: DS.Radius.card, style: .continuous), interactive: false)
        .padding(.horizontal, DS.Space.m).padding(.top, 4).padding(.bottom, 8)
    }
}

/// 会话已结束, with 恢复会话 while its computer can be reached.
private struct EndedCard: View {
    let scheduledClosed: Bool
    let idleClosed: Bool
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
                detail: session.info?.statusDetail ?? "", scheduledClosed: scheduledClosed, idleClosed: idleClosed, unlinked: state.unlinked,
                machineOnline: machine?.online == true, machine: name),
            mark: act.running ? .running : .idle
        ) {
            if reachable && !act.running {
                InkButton(act.error.isEmpty ? "恢复会话" : "再试一次") {
                    restore()
                }
                .disabled(state.conn != .online)
            }
            if !act.error.isEmpty {
                Text("恢复失败：\(act.error)").font(.footnote).foregroundStyle(DS.red).padding(.top, 8)
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
