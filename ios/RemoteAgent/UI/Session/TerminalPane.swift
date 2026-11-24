// A session's live terminal with its toolbar and key bar (architecture §11;
// Android's term/TerminalPane.kt). The font is scaled so the whole tmux
// window fits. Opening it fits the remote window to this screen; the
// keyboard and rotation never resize it. Font changes refit only an already
// fitted window.
//
// There is no history button: pulling the screen down (or a wheel up) is
// what reads back, and the same area becomes the reader, which is read-only
// and never moves for output (`architecture §11 「回看历史」`; the reader is
// `UI/Terminal/TerminalHistoryReader`). The key bar stays put, and any key
// leaves reading before it is sent.
//
// The pane is under the session screen's tabs. Backgrounding or leaving
// the tab closes it. Main-path probes preserve a healthy terminal; a real
// terminal-leg failure opens a new stream and snapshot when the link is ready.
//
// The screen itself is `UI/Terminal`: `RemoteTerminal` is the connection,
// `TerminalHostView` the SwiftTerm view and what output may not do to the
// phone, `TerminalKeyBar` the keys.
import RACore
import RAProto
import SwiftUI

/// Resolves terminal reachability from Core's `PBCoreSyncStateView` (`terminal_ready` / `terminal_waiting`),
/// matching Web (`Terminal.tsx`) and Android (`TerminalPane.kt`).
enum TerminalReadiness {
    static func state(sync: PBCoreSyncStateView, sessionID: String, machineID: String = "") -> (ready: Bool, waiting: Bool) {
        if let s = sync.sessions.first(where: { $0.sessionID == sessionID }) {
            return (s.terminalReady, s.terminalWaiting)
        }
        if !machineID.isEmpty, let m = sync.machines.first(where: { $0.machineID == machineID }) {
            let ready = m.terminalReady
            return (ready, !ready && (sync.reachabilityUnknown || sync.displayGrace))
        }
        let ready = sync.connection == .connected
        return (ready, !ready && (sync.reachabilityUnknown || sync.displayGrace))
    }
}

struct TerminalPane: View {
    let sessionId: String
    /// The session has not ended.
    let running: Bool

    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var terminal = RemoteTerminal()
    @State private var font = TerminalFont.preferred
    @State private var details = false

    private var readiness: (ready: Bool, waiting: Bool) {
        TerminalReadiness.state(
            sync: context.core.syncState,
            sessionID: sessionId,
            machineID: context.core.sessions[sessionId]?.header.machineID ?? ""
        )
    }

    /// Either the relay or the session's direct peer path is ready to open a terminal.
    private var online: Bool { readiness.ready }
    private var waitingReachability: Bool { readiness.waiting }
    private var active: Bool { online && model.foreground }

    var body: some View {
        let state = terminal.state
        let history = terminal.history
        VStack(spacing: 0) {
            toolbar(state)
            fontRow
            ZStack(alignment: .top) {
                TerminalScreen(terminal: terminal)
                    .overlay(alignment: .top) {
                        // At the largest text sizes, or under the keyboard, the notice can be taller than the area.
                        if state != .live && !history.reading {
                            ScrollView { notice(state) }.scrollBounceBehavior(.basedOnSize)
                        }
                    }
                if history.open {
                    TerminalHistoryReader(
                        pages: history.pages, status: history.status, statusText: history.statusText,
                        canRetry: history.canRetry, canLoadEarlier: history.canLoadEarlier,
                        online: state == .live, entry: terminal.entryPull,
                        metrics: { (terminal.host.shownPoints, terminal.host.shownCell) },
                        offset: { terminal.host.horizontalOffset },
                        gridWidth: { terminal.host.gridWidth },
                        loadEarlier: { terminal.loadEarlier() }, retry: { terminal.retryHistory() },
                        leave: { terminal.leaveReading() }
                    )
                    .overlay(alignment: .bottomTrailing) { pill }
                } else if history.opening {
                    // Only the line: the live screen stays, and so does the live toolbar.
                    openingLine(statusText: history.statusText, canRetry: history.canRetry)
                }
            }
            TerminalKeyBar(enabled: state == .live, onKey: { terminal.send($0) }, onKeyboard: keyboard)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(TermColors.bg)
        .onAppear { start() }
        .onDisappear { terminal.close() }
        .onChange(of: active) { _, now in
            if !model.foreground { terminal.close() }
            else if now { terminal.connectIfNeeded() }
            else if !waitingReachability { terminal.close() }
        }
        .onChange(of: waitingReachability) { _, waiting in
            if !waiting && !online { terminal.close() }
        }
        .task(id: terminal.resizedElsewhere) {
            // The toolbar says it for a while; nothing pops up over the screen.
            guard terminal.resizedElsewhere, (try? await Task.sleep(for: .seconds(10))) != nil else { return }
            terminal.dismissResized()
        }
        .terminalDebug(terminal, font: $font, details: $details, reconnect: open)
    }

    private func start() {
        let (remote, app, sid, core, host) = (context.remote, model, sessionId, context.core, context.coreHost)
        let historyQueue = SerialMainQueue()
        terminal.use(
            RemoteTerminal.Link(
                open: { try await remote.openTerminal(sid, listener: $0) },
                online: {
                    TerminalReadiness.state(
                        sync: core.syncState,
                        sessionID: sid,
                        machineID: core.sessions[sid]?.header.machineID ?? ""
                    ).ready && app.foreground
                },
                hold: { app.holdTerminal($0) },
                release: { app.releaseTerminal($0) },
                reportStream: { id, kind, opened in
                    Task { await host.sendTerminalStream(terminalID: id, kind: kind, opened: opened) }
                },
                subscribeReopen: { id, handler in
                    let token = UUID()
                    let task = Task { await host.registerTerminalReopen(terminalID: id, token: token, handler: handler) }
                    return {
                        task.cancel()
                        Task { await host.unregisterTerminalReopen(terminalID: id, token: token) }
                    }
                },
                sendHistory: { id, kind, page in
                    historyQueue.enqueue {
                        await host.sendTerminalHistory(terminalID: id, kind: kind, page: page)
                    }
                },
                subscribeHistory: { id, onRequest, onUpdated in
                    let token = UUID()
                    historyQueue.enqueue {
                        await host.registerTerminalHistory(
                            terminalID: id, token: token, onRequest: onRequest, onUpdated: onUpdated)
                    }
                    return {
                        historyQueue.enqueue {
                            await host.unregisterTerminalHistory(terminalID: id, token: token)
                        }
                    }
                }))
        if active { open() }
        else if !waitingReachability { terminal.close() }
    }

    private func open() {
        #if DEBUG
        if TerminalDebug.sample(terminal, demo: model.isDemo) { return }
        #endif
        terminal.connect()
    }

    /// The 键盘 key: reading first, then the keyboard, which reading keeps away.
    private func keyboard() {
        terminal.leaveReading()
        terminal.host.toggleKeyboard()
    }

    // MARK: The toolbar

    /// Says the size, and that it changed; only with the reader on screen, that it is reading.
    private func toolbar(_ state: RemoteTerminal.State) -> some View {
        let open = terminal.history.open
        let size = "\(terminal.size.cols)×\(terminal.size.rows)"
        let text =
            if open { L10n.terminalReadonly() }
            else if state != .live { L10n.sessionTerminal() }
            else if terminal.resizedElsewhere { L10n.terminalIosSizeChanged(size: size) }
            else if terminal.fitted { L10n.terminalFittedSize(cols: terminal.size.cols, rows: terminal.size.rows) }
            else { L10n.terminalComputerDimensions(cols: terminal.size.cols, rows: terminal.size.rows) }
        return HStack(spacing: 8) {
            Text(text).font(.footnote).ink(TermColors.tool)
                .lineLimit(2)
                .frame(maxWidth: .infinity, alignment: .leading)
            if state == .live && !open {
                Button {
                    terminal.toggleFit()
                } label: {
                    Group {
                        if terminal.fitted || terminal.resizedElsewhere {
                            Text(terminal.fitted ? L10n.terminalRestore() : L10n.terminalFitShort()).font(.footnote.weight(.semibold)).ink(TermColors.link)
                        } else {
                            IconText("arrow.up.left.and.arrow.down.right", L10n.terminalFit(), color: TermColors.link, style: .footnote.weight(.semibold))
                        }
                    }
                    .lineLimit(1).minimumScaleFactor(0.7)
                    .padding(.horizontal, 8)
                    .frame(minWidth: DS.minHit, minHeight: DS.minHit)
                    .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .accessibilityHint(terminal.fitted ? L10n.terminalIosRestoreHint() : L10n.terminalIosFitHint())
            }
        }
        .padding(.leading, 12).padding(.trailing, 4)
        .frame(minHeight: DS.minHit)
        .background(TermColors.bar)
        .bottomLine(TermColors.rule)
        // The two bars keep the screen its room: their words stop growing at the first of the largest sizes.
        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
    }

    /// The one line over the live screen while the first page is on its way, or that
    /// it could not be read, when 重试 asks for it again (or another pull up does).
    private func openingLine(statusText: PBCoreText?, canRetry: Bool) -> some View {
        HStack(spacing: 8) {
            Text(renderCoreText(statusText)).font(.footnote).ink(TermColors.tool)
            if canRetry {
                Button {
                    terminal.retryHistory()
                } label: {
                    Text(L10n.commonRetry()).font(.footnote.weight(.semibold)).ink(TermColors.link)
                        .padding(.vertical, 6)
                        .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .accessibilityHint(L10n.terminalIosRetryHistory())
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, 12)
        .frame(minHeight: 26)
        .background(TermColors.bar)
        .bottomLine(TermColors.rule)
        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
    }

    /// What came in while reading, or only the way back; either way it leaves reading.
    private var pill: some View {
        Button {
            terminal.leaveReading()
        } label: {
            Group {
                if terminal.hasNewOutput {
                    HStack(spacing: 6) {
                        Circle().fill(TermColors.link).frame(width: 7, height: 7)
                        Text(L10n.terminalFresh()).font(.subheadline.weight(.semibold)).ink(TermColors.fg)
                        SymbolImage("arrow.down", size: 15, color: TermColors.fg)
                    }
                } else {
                    IconText("arrow.down", L10n.terminalLive(), color: TermColors.fg, style: .subheadline.weight(.semibold))
                }
            }
            .lineLimit(1)
            .padding(.horizontal, 14).padding(.vertical, 9)
            .background(TermColors.key, in: Capsule())
            .overlay { Capsule().strokeBorder(TermColors.line, lineWidth: 1) }
            .compositingGroup()
            .shadow(color: .black.opacity(0.45), radius: 14, y: 4)
        }
        .buttonStyle(PressStyle())
        .padding(12)
        .accessibilityLabel(terminal.hasNewOutput ? L10n.terminalFreshLive() : L10n.terminalLive())
        .accessibilityHint(L10n.terminalIosLeaveHistory())
    }

    private var fontRow: some View {
        HStack(spacing: 20) {
            Text(L10n.terminalIosFont()).font(TerminalPane.fontStyle).ink(TermColors.dim).frame(maxWidth: .infinity, alignment: .leading)
            fontButton("A−", L10n.terminalIosFontSmaller(), font > TerminalFont.sizes.lowerBound) { setFont(font - 1) }
            Text("\(font)").font(TerminalPane.fontStyle).ink(TermColors.dim).accessibilityLabel(L10n.terminalIosFontValue(size: Int(font)))
            fontButton("A+", L10n.terminalIosFontLarger(), font < TerminalFont.sizes.upperBound) { setFont(font + 1) }
        }
        .lineLimit(1)
        .padding(.horizontal, 12)
        .frame(minHeight: 40)
        .background(TermColors.bar)
        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
    }

    private static let fontStyle: Font = .footnote

    private func fontButton(_ label: String, _ says: String, _ enabled: Bool, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(label).font(TerminalPane.fontStyle.weight(.semibold)).ink(enabled ? TermColors.link : TermColors.tool)
                .fixedSize()
                .frame(minWidth: DS.minHit, minHeight: DS.minHit)
                .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .disabled(!enabled)
        .accessibilityLabel(says)
    }

    private func setFont(_ points: Int) {
        font = min(max(points, TerminalFont.sizes.lowerBound), TerminalFont.sizes.upperBound)
        TerminalFont.preferred = font
        terminal.setFont(font)
    }

    // MARK: Not live

    /// What is over the screen while there is no stream: the last picture stays under it.
    private func notice(_ state: RemoteTerminal.State) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            switch state {
            case .connecting:
                IconText(Mark.progress, L10n.terminalConnecting(), color: TermColors.dim, style: .subheadline)
            case .confirmRebuild(let kind):
                let body = switch kind {
                case .restart: L10n.terminalConfirmRebuildRestartBody()
                case .input: L10n.terminalConfirmRebuildInputBody()
                }
                Text(L10n.terminalConfirmRebuildTitle()).font(.body.weight(.semibold)).ink(TermColors.fg)
                    .accessibilityAddTraits(.isHeader)
                Text(body)
                    .font(.subheadline).ink(TermColors.dim)
                    .padding(.top, 6).padding(.bottom, 14)
                Btns {
                    Btn(L10n.terminalConfirmCloseAction(), kind: .secondary) { terminal.closeFromConfirm() }
                    Btn(L10n.terminalConfirmRebuildAction(), kind: .primary, enabled: online) { terminal.confirmRebuild() }
                }
            case .closed(let reason):
                Text(L10n.terminalClosed()).font(.body.weight(.semibold)).ink(TermColors.fg)
                    .accessibilityAddTraits(.isHeader)
                Text(
                    !online ? L10n.terminalIosWaitConnection()
                        : running ? L10n.terminalClosedRunningNote() : L10n.terminalIosNotRunning()
                )
                .font(.subheadline).ink(TermColors.dim)
                .padding(.top, 6).padding(.bottom, 14)
                Btn(L10n.terminalReconnect(), kind: .primary, enabled: online) { if online { open() } }
                Button {
                    details.toggle()
                } label: {
                    Text(details ? L10n.terminalIosHideDetails() : L10n.terminalViewDetails()).font(.footnote).ink(TermColors.link)
                        .frame(minHeight: DS.minHit, alignment: .leading)
                        .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                if details {
                    Text(reason).font(.footnote.monospaced()).ink(TermColors.dim).textSelection(.enabled)
                }
            case .live:
                EmptyView()
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(TermColors.bar, in: RoundedRectangle(cornerRadius: DS.Radius.small, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: DS.Radius.small, style: .continuous).strokeBorder(TermColors.line, lineWidth: 1))
        .environment(\.colorScheme, .dark)
        .padding(12)
    }
}

/// Preserves FIFO order across fire-and-forget async actor calls from the main actor.
@MainActor
private final class SerialMainQueue {
    private var tail: Task<Void, Never>?

    func enqueue(_ operation: @escaping @Sendable () async -> Void) {
        let prev = tail
        tail = Task {
            await prev?.value
            await operation()
        }
    }
}
