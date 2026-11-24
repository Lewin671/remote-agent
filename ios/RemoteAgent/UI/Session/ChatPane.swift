// The chat of a session: renders Core's `PBSessionViewModel.{timeline, dock, composer, actions}`
// into UIKit `ChatCollection` with scroll tracking, incremental Markdown parsing cache,
// queued/outgoing overlays, and `SessionComposer` / `SessionEndCard`.
import RACore
import RAMarkdown
import RAMarkdownUI
import RAProto
import SwiftUI
import UIKit

// MARK: Where the end of the list is

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

@MainActor
@Observable
final class ChatScrollTracker {
    @ObservationIgnored let diagnosticEntity = ChatDiagnosticRing.shared.identity()
    private(set) var fresh = 0
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

    static let slack: CGFloat = 28

    private var atBottom: Bool { !measured || endY <= floorY + Self.slack }
    private var needsScroll: Bool { measured && endY > floorY + 0.5 }

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

    @ObservationIgnored private(set) var keeping = false

    func keep() {
        cancelFollow()
        ChatDiagnosticRing.shared.record(.keep, entity: diagnosticEntity)
        keeping = true
    }

    func kept() {
        guard keeping else { return }
        ChatDiagnosticRing.shared.record(.kept, entity: diagnosticEntity)
        keeping = false
    }

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
        if keeping { return false }
        if dragging || !changed {
            scroll.observe(userDragging: dragging, atBottom: atBottom, rows: rows)
        }
        let go = changed && scroll.contentChanged(rows: rows)
        publish()
        return go && !dragging && needsScroll
    }

    func leave() {
        cancelFollow()
        ChatDiagnosticRing.shared.record(.leave, entity: diagnosticEntity)
        keeping = false
        scroll.observe(userDragging: true, atBottom: false, rows: rows)
        publish()
    }

    func prepended(_ n: Int) {
        ChatDiagnosticRing.shared.record(.prepend, entity: diagnosticEntity, Double(n))
        scroll.prepended(n)
    }

    func jump() {
        cancelFollow()
        ChatDiagnosticRing.shared.record(.jump, entity: diagnosticEntity)
        keeping = false
        scroll.jumpToEnd(rows: rows)
        publish()
    }
}

// MARK: The keyboard under the bar

@MainActor
@Observable
final class KeyboardTrack {
    fileprivate(set) var overlap: CGFloat = 0
    fileprivate(set) var base: CGFloat = 0
    var inset: CGFloat = 0

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

// MARK: Markdown Cache

@MainActor
private final class MarkdownBlockCache {
    private var entries: [String: (markdown: String, blocks: [MarkdownBlock])] = [:]

    func syncBuild(items: [PBSessionTimelineItem]) -> [ChatTimelineRow] {
        var nextEntries: [String: (String, [MarkdownBlock])] = [:]
        var rows: [ChatTimelineRow] = []
        rows.reserveCapacity(items.count)
        for item in items {
            if case .agentMessage(let a)? = item.kind {
                let blocks: [MarkdownBlock]
                if let cached = entries[item.id], cached.markdown == a.markdown {
                    blocks = cached.blocks
                } else {
                    blocks = Markdown.parse(a.markdown)
                }
                nextEntries[item.id] = (a.markdown, blocks)
                rows.append(ChatTimelineRow(item: item, blocks: blocks))
            } else {
                rows.append(ChatTimelineRow(item: item, blocks: []))
            }
        }
        entries = nextEntries
        for i in rows.indices {
            guard case .notifyReceipt? = rows[i].item.kind else { continue }
            if i > 0, case .notifyReceipt? = rows[i - 1].item.kind { rows[i].receiptBefore = true }
            if i + 1 < rows.count, case .notifyReceipt? = rows[i + 1].item.kind { rows[i].receiptAfter = true }
        }
        return rows
    }
}

// MARK: The pane

private struct SelectedText: Identifiable {
    let text: String
    var id: String { text }
}

struct ChatPane: View {
    let sessionID: String
    let vm: PBSessionViewModel?
    var focusComposerTrigger: Int = 0
    let onTerminal: () -> Void
    let onLoadEarlier: () -> Void
    let onToggleGroup: @MainActor @Sendable (String) -> Void

    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var open = ChatOpen()
    @State private var tracker = ChatScrollTracker()
    @State private var keyboard = KeyboardTrack()
    @State private var cache = MarkdownBlockCache()
    @State private var selecting: SelectedText?
    @State private var announcer = LiveAnnouncer()
    @State private var appliedScheduledJump: String?
    @State private var announced = false
    @State private var paneHeight: CGFloat = 0
    @State private var lastWindowFirstSeq: UInt64 = 0
    @State private var lastRowIDs: [String] = []
    @State private var joins = 0
    @State private var prepended = 0
    @FocusState private var typing: Bool

    private func selectMessage(_ text: String) {
        typing = false
        selecting = SelectedText(text: text)
    }

    private func toEnd() { tracker.controller?.toEnd() }

    private func applyScheduledJump(rows: [ChatTimelineRow], tl: PBSessionTimelineView, jump: SessionStore.ScheduledJump?) {
        guard let jump, appliedScheduledJump != jump.runID, tl.loaded, !rows.isEmpty else { return }
        tracker.leave()
        let anchor: ChatTimelineRow? = tl.anchorItemID.isEmpty ? nil : rows.first { $0.id == tl.anchorItemID }
        let exact: ChatTimelineRow? = rows.first {
            if case .userMessage(let u)? = $0.item.kind { return u.scheduledRunID == jump.runID }
            return false
        }
        let fallback: ChatTimelineRow? = rows.first { $0.item.seq >= jump.seq }
        let target: ChatTimelineRow? = anchor ?? exact ?? fallback
        if let target {
            ChatDiagnosticRing.shared.record(.proxyScheduled, entity: tracker.diagnosticEntity)
            tracker.toRow(target.id)
            appliedScheduledJump = jump.runID
        }
    }

    @ViewBuilder
    private func bottomInset(
        header: PBSessionHeaderView,
        dock: PBSessionDockView?,
        composer: PBSessionComposerView,
        actions: PBSessionActionsView
    ) -> some View {
        bottom(header: header, dock: dock, composer: composer, actions: actions)
            .frame(maxWidth: 720)
            .frame(maxWidth: .infinity)
            .background { EdgeFade(edge: .bottom) }
            .overlay(alignment: .top) {
                pill { followEnd() }
                    .alignmentGuide(.top) { $0[.bottom] + 8 }
                    .animation(Motion.enter(), value: tracker.fresh > 0)
                    .animation(Motion.enter(), value: tracker.away)
            }
            .offset(y: keyboard.lag)
    }

    private func followEnd() {
        tracker.jump()
        toEnd()
    }

    var body: some View {
        let header = vm?.header ?? PBSessionHeaderView()
        let tl = vm?.timeline ?? PBSessionTimelineView()
        let composer = vm?.composer ?? PBSessionComposerView()
        let actions = vm?.actions ?? PBSessionActionsView()
        let dock = vm?.dock
        let rows = cache.syncBuild(items: tl.items)
        let online = (header.computerOnline && !header.computerUnlinked) || header.computerReachability == .unknown
        let env = ChatEnv(
            agent: header.agent != .unspecified ? header.agent : defaultAgent,
            frozenAt: online ? 0 : (header.offlineSinceMs > 0 ? header.offlineSinceMs : context.core.syncState.disconnectedAtMs),
            session: sessionID
        )
        let jump = context.sessionStore.scheduledJump(sessionID)
        let ready = vm != nil && (!tl.loading || tl.loaded || !rows.isEmpty)
        ChatCollection(
            items: listItems(tl: tl, rows: rows, env: env, online: online),
            env: env,
            tracker: tracker,
            ready: ready,
            joins: joins,
            prepended: prepended,
            count: rows.count,
            interaction: { _ in },
            onNearTop: {
                if !tl.backfilling && !tl.fetching && (tl.hasEarlier_p || tl.canLoadEarlier) {
                    onLoadEarlier()
                }
            }
        )
        .safeAreaInset(edge: .bottom, spacing: 0) {
            bottomInset(header: header, dock: dock, composer: composer, actions: actions)
        }
        .onChange(of: rows.map(\.id), initial: true) { oldIDs, newIDs in
            let newFirstSeq = tl.windowFirstSeq
            if lastWindowFirstSeq > 0, newFirstSeq > 0, newFirstSeq < lastWindowFirstSeq, let oldFirst = oldIDs.first ?? lastRowIDs.first {
                let idx = newIDs.firstIndex(of: oldFirst) ?? 0
                if idx > 0 {
                    prepended = idx
                    joins += 1
                }
            }
            if newFirstSeq > 0 { lastWindowFirstSeq = newFirstSeq }
            lastRowIDs = newIDs
        }
        .onChange(of: focusComposerTrigger) { _, _ in
            typing = true
        }
        .modifier(ScheduledJumpObserver(rowIDs: rows.map(\.id), loaded: tl.loaded, jump: jump, anchorID: tl.anchorItemID) {
            applyScheduledJump(rows: rows, tl: tl, jump: jump)
        })
        .debugSignal("session.end") { followEnd() }
        .debugSignal("session.top") {
            tracker.leave()
            if let first = rows.first { ChatDiagnosticRing.shared.record(.proxyTop, entity: tracker.diagnosticEntity); tracker.toRow(first.id) }
        }
        .debugSignal("session.back") {
            tracker.leave()
            if let row = rows.dropFirst(rows.count * 3 / 4).first {
                ChatDiagnosticRing.shared.record(.proxyBack, entity: tracker.diagnosticEntity)
                tracker.toRow(row.id)
            }
        }
        .onAppear { ChatDiagnosticRing.shared.record(.chatEnter) }
        .onDisappear {
            ChatDiagnosticRing.shared.record(.chatLeave)
            tracker.cancelFollow()
            context.sessionStore.clearScheduledJump(sessionID)
        }
        .background { KeyboardProbe(track: keyboard).frame(width: 0, height: 0) }
        .onGeometryChange(for: CGFloat.self) { g in
            ChatDiagnosticRing.shared.measure(.keyboardInset) { g.safeAreaInsets.bottom }
        } action: {
            ChatDiagnosticRing.shared.record(.keyboardInset, Double($0))
            keyboard.inset = $0
        }
        .onGeometryChange(for: CGFloat.self) { g in ChatDiagnosticRing.shared.measure(.paneHeight) { g.size.height } } action: {
            ChatDiagnosticRing.shared.record(.paneHeight, Double($0)); paneHeight = $0
        }
        .environment(\.chatHeight, paneHeight)
        .environment(open)
        .environment(
            \.chatActions,
            ChatActions(
                image: { seq, index in nav.open(.image(session: sessionID, seq: seq, index: index)) },
                session: { nav.open(.session(id: $0)) },
                putBack: { requestId, _, _ in
                    Task {
                        await context.coreHost.putBackInput(sessionID: sessionID, requestID: requestId)
                    }
                },
                toggleGroup: onToggleGroup,
                select: selectMessage
            )
        )
        .sheet(item: $selecting) { picked in
            MessageTextSelection(text: picked.text) { selecting = nil }
        }
        .onChange(of: rows.last?.id, initial: true) { _, _ in announce(rows.last?.item, dock, header: header, ready: ready) }
        .onChange(of: dock?.itemID ?? "") { _, _ in announce(rows.last?.item, dock, header: header, ready: ready) }
        .debugSignal("session.focus") { typing = true }
        .debugSignal("session.blur") { typing = false }
        .debugSignal("session.select") {
            for row in rows.reversed() {
                if case .agentMessage(let reply)? = row.item.kind {
                    selectMessage(reply.markdown)
                    break
                }
            }
        }
    }

    private func listItems(
        tl: PBSessionTimelineView,
        rows: [ChatTimelineRow],
        env: ChatEnv,
        online: Bool
    ) -> [ChatCollectionItem] {
        var items = [ChatCollectionItem("chat-top") { Color.clear.frame(height: 6) }]
        if tl.backfilling {
            items.append(ChatCollectionItem("chat-earlier-loading") {
                Text(L10n.sessionLoadingEarlier()).font(.footnote).ink(DS.ink2)
                    .frame(maxWidth: .infinity).padding(.horizontal, chatMargin)
            })
        } else if tl.hasEarlier_p || tl.canLoadEarlier {
            items.append(ChatCollectionItem("chat-earlier") {
                TextAction(L10n.scheduleEarlier(), style: .footnote.weight(.semibold), alignment: .center) {
                    onLoadEarlier()
                }
                .frame(maxWidth: .infinity).padding(.horizontal, chatMargin)
            })
        }
        if tl.loading && rows.isEmpty {
            items.append(ChatCollectionItem("chat-loading") { Skeleton(inset: chatMargin) })
        }
        if tl.historyPartial && !tl.hasError && !tl.hasFetchError {
            items.append(ChatCollectionItem("chat-history") {
                Text(L10n.sessionIosHistoryPartial()).font(.subheadline).ink(DS.ink2).padding(.horizontal, chatMargin)
            })
        }
        if tl.unavailableCount > 0 {
            items.append(ChatCollectionItem("chat-unavailable") {
                Text(L10n.sessionHistoryUnavailable(count: Int(tl.unavailableCount)))
                    .font(.subheadline).ink(DS.ink2).padding(.horizontal, 16)
            })
        }
        items += rows.map { row in
            ChatCollectionItem(row.id, row: row) { ChatRowView(row: row, env: env).equatable() }
        }
        if tl.hasError || (online && tl.hasFetchError) {
            let failure = tl.hasError ? tl.error.rendered() : tl.fetchError.rendered()
            items.append(ChatCollectionItem("chat-fetch-error") {
                Banner(L10n.sessionIosHistoryFailed(reason: failure), tone: .error, action: L10n.commonRetry()) {
                    Task { await context.coreHost.retryLoadSession(sessionID) }
                }
                .padding(.horizontal, chatMargin).padding(.vertical, 8)
            })
        }
        items.append(ChatCollectionItem("chat-end") { Color.clear.frame(height: 8) })
        return items
    }

    private func announce(_ last: PBSessionTimelineItem?, _ dock: PBSessionDockView?, header: PBSessionHeaderView, ready: Bool) {
        let initial = !announced || !ready
        if ready { announced = true }
        let word = announcer.announce(
            last: last, dock: dock, agent: header.agentName.isEmpty ? agentName(header.agent) : header.agentName,
            now: nowMs(), initial: initial
        )
        if let word, UIAccessibility.isVoiceOverRunning {
            AccessibilityNotification.Announcement(word).post()
        }
    }

    @ViewBuilder private func pill(_ action: @escaping () -> Void) -> some View {
        if tracker.fresh > 0 {
            Button(action: action) {
                Label(L10n.sessionNewMessages(count: Int(tracker.fresh)), systemImage: "arrow.down")
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
            .accessibilityLabel(L10n.sessionIosLatest())
            .transition(Motion.appear(rise: Motion.bubbleRise))
        }
    }

    @ViewBuilder private func bottom(
        header: PBSessionHeaderView,
        dock: PBSessionDockView?,
        composer: PBSessionComposerView,
        actions: PBSessionActionsView
    ) -> some View {
        let taskOwned = header.taskOwned
        if header.exited {
            VStack(spacing: 0) {
                if taskOwned { Text(L10n.sessionReplyMoves()).font(.footnote).foregroundStyle(DS.ink2).padding(8) }
                EndedCard(sessionID: sessionID, header: header, actions: actions)
            }
        } else if header.computerUnlinked && header.computerReachability != .unknown {
            let name = header.hasComputerName ? header.computerName.rendered() : L10n.commonUnknownComputer()
            let offlineDesc = header.hasOfflineText ? header.offlineText.rendered() : L10n.projectsComputerOffline()
            SessionEndCard(
                L10n.newSessionMachineOffline(name: name),
                text: offlineDesc
            ) { EmptyView() }
        } else {
            VStack(spacing: 0) {
                if let dock, dock.hasItem_p {
                    let hide = { typing = false }
                    let store = context.sessionStore
                    let key = dockFoldKey(sessionID: sessionID, dock)
                    let draftingNow = composer.draft.hasUnsavedContent_p
                    let openBinding = Binding(
                        get: {
                            store.dockOpen(key) { draftingNow } && !SessionDebug.shows("fold")
                        },
                        set: { store.setDockOpen(key, $0) }
                    )
                    Group {
                        switch dock.kind {
                        case .approval:
                            ApprovalDock(
                                sessionID: sessionID,
                                machineID: header.machineID,
                                dock: dock,
                                open: openBinding,
                                startReason: SessionDebug.shows("reason"),
                                onOpen: hide
                            )
                        case .question:
                            QuestionDock(
                                sessionID: sessionID,
                                machineID: header.machineID,
                                dock: dock,
                                open: openBinding,
                                onOpen: hide,
                                onTerminal: onTerminal
                            ) { nav.open(.question(session: sessionID, question: dock.question.questionID)) }
                        case .plan:
                            PlanDock(
                                sessionID: sessionID,
                                machineID: header.machineID,
                                dock: dock,
                                open: openBinding,
                                onOpen: hide
                            ) { nav.open(.plan(session: sessionID)) }
                        case .terminalPopup:
                            PopupDock(
                                dock: dock,
                                open: openBinding,
                                onOpen: hide,
                                onTerminal: onTerminal
                            )
                        case .unspecified, .UNRECOGNIZED:
                            EmptyView()
                        }
                    }
                    .id(dockFoldKey(sessionID: sessionID, dock))
                    .transition(Motion.roomy(rise: Motion.dockRise))
                }
                if taskOwned { Text(L10n.sessionReplyMoves()).font(.footnote).foregroundStyle(DS.ink2).padding(8) }
                SessionComposer(
                    sessionID: sessionID,
                    header: header,
                    composer: composer,
                    actions: actions,
                    focus: $typing,
                    keyboardOpen: typing || keyboard.inset > keyboard.base + 1,
                    maxTrayHeight: paneHeight > 0 ? max(180, paneHeight * 0.5) : 340,
                    onTerminal: onTerminal,
                    onSent: followEnd
                )
            }
            .animation(dock?.hasItem_p != true ? Motion.exit() : Motion.enter(), value: dock?.itemID ?? "")
        }
    }
}

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

private struct EndedCard: View {
    let sessionID: String
    let header: PBSessionHeaderView
    let actions: PBSessionActionsView
    @Environment(AccountContext.self) private var context

    var body: some View {
        let resume = actions.resume
        let running = resume.status == .pending || resume.busy
        let err = (!running && resume.hasErrorText) ? resume.errorText.rendered() : (running ? "" : resume.errorCode)
        let endedDesc = header.hasEndedText ? header.endedText.rendered() : L10n.sessionEnded()
        SessionEndCard(
            running ? L10n.sessionResuming() : L10n.sessionEnded(),
            text: endedDesc,
            mark: running ? .running : .idle
        ) {
            if actions.canResume && !running {
                InkButton(err.isEmpty ? L10n.sessionResume() : L10n.sessionTryAgain()) {
                    restore()
                }
            }
            if !err.isEmpty {
                Text(L10n.sessionIosResumeFailed(reason: err)).font(.footnote).foregroundStyle(DS.red).padding(.top, 8)
            }
        }
        .debugSignal("session.restore") { if actions.canResume { restore() } }
    }

    private func restore() {
        Task {
            await context.coreHost.submitSessionCommand(
                sessionID: sessionID,
                command: .with { $0.resumeSession.sessionID = sessionID },
                machineID: header.machineID,
                slotKey: actions.resume.slotKey
            )
        }
    }
}

struct ScheduledJumpObserver: ViewModifier {
    let rowIDs: [String]
    let loaded: Bool
    let jump: SessionStore.ScheduledJump?
    let anchorID: String
    let apply: () -> Void
    private struct Input: Equatable {
        let rowIDs: [String]
        let loaded: Bool
        let jump: SessionStore.ScheduledJump?
        let anchorID: String
    }
    func body(content: Content) -> some View {
        content.onChange(of: Input(rowIDs: rowIDs, loaded: loaded, jump: jump, anchorID: anchorID), initial: true) { _, _ in apply() }
    }
}
