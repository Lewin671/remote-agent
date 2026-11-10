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
// The pane is under the session screen's tabs and fills what is left, down
// to the keyboard. It is connected only while the main connection is up
// and the app is in front (architecture §10 「后台和内存」): going to the
// background, losing the server or leaving the tab closes the stream, and
// coming back opens another, which starts from a fresh snapshot.
//
// The screen itself is `UI/Terminal`: `RemoteTerminal` is the connection,
// `TerminalHostView` the SwiftTerm view and what output may not do to the
// phone, `TerminalKeyBar` the keys.
import RACore
import SwiftUI

struct TerminalPane: View {
    let sessionId: String
    /// The session has not ended.
    let running: Bool

    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var terminal = RemoteTerminal()
    @State private var font = TerminalFont.preferred
    @State private var details = false

    /// The main connection is up: a terminal needs it for its opening command.
    private var online: Bool { model.state.conn == .online }
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
                        pages: history.pages, status: history.status, online: state == .live, entry: history.entryPull,
                        metrics: { (terminal.host.shownPoints, terminal.host.shownCell) },
                        offset: { terminal.host.horizontalOffset },
                        gridWidth: { terminal.host.gridWidth },
                        loadEarlier: { terminal.loadEarlier() }, retry: { terminal.retryHistory() },
                        leave: { terminal.leaveReading() }
                    )
                    .overlay(alignment: .bottomTrailing) { pill }
                } else if let opening = history.opening {
                    // Only the line: the live screen stays, and so does the live toolbar.
                    openingLine(opening)
                }
            }
            TerminalKeyBar(enabled: state == .live, onKey: { terminal.send($0) }, onKeyboard: keyboard)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(TermColors.bg)
        .onAppear { start() }
        .onDisappear { terminal.close() }
        .onChange(of: active) { _, now in
            if now { open() } else { terminal.close() }
        }
        .task(id: terminal.resizedElsewhere) {
            // The toolbar says it for a while; nothing pops up over the screen.
            guard terminal.resizedElsewhere, (try? await Task.sleep(for: .seconds(10))) != nil else { return }
            terminal.dismissResized()
        }
        .terminalDebug(terminal, font: $font, details: $details, reconnect: open)
    }

    private func start() {
        let (remote, app, sid) = (context.remote, model, sessionId)
        terminal.use(
            RemoteTerminal.Link(
                open: { try await remote.openTerminal(sid, listener: $0) },
                online: { app.state.conn == .online && app.foreground },
                hold: { app.holdTerminal($0) },
                release: { app.releaseTerminal($0) }))
        if active { open() }
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
            if open { "回看中 · 只读" }
            else if state != .live { "终端" }
            else if terminal.resizedElsewhere { "远端尺寸已变化 · \(size)" }
            else if terminal.fitted { "已适配这块屏幕 · \(size)" }
            else { "电脑上 \(size)" }
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
                            Text(terminal.fitted ? "还原" : "适配").font(.footnote.weight(.semibold)).ink(TermColors.link)
                        } else {
                            IconText("arrow.up.left.and.arrow.down.right", "适配我的屏幕", color: TermColors.link, style: .footnote.weight(.semibold))
                        }
                    }
                    .lineLimit(1).minimumScaleFactor(0.7)
                    .padding(.horizontal, 8)
                    .frame(minWidth: DS.minHit, minHeight: DS.minHit)
                    .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .accessibilityHint(terminal.fitted ? "让电脑上的终端回到原来的尺寸" : "把电脑上的终端调整成这块屏幕放得下的尺寸")
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
    private func openingLine(_ opening: TerminalHistory.Opening) -> some View {
        HStack(spacing: 8) {
            if opening == .failed {
                Text("读取失败").font(.footnote).ink(TermColors.tool)
                Button {
                    terminal.retryHistory()
                } label: {
                    Text("重试").font(.footnote.weight(.semibold)).ink(TermColors.link)
                        .padding(.vertical, 6)
                        .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .accessibilityHint("再读一次这台终端的记录")
            } else {
                Text("读取记录…").font(.footnote).ink(TermColors.tool)
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
                if terminal.history.hasNewOutput {
                    HStack(spacing: 6) {
                        Circle().fill(TermColors.link).frame(width: 7, height: 7)
                        Text("有新输出").font(.subheadline.weight(.semibold)).ink(TermColors.fg)
                        SymbolImage("arrow.down", size: 15, color: TermColors.fg)
                    }
                } else {
                    IconText("arrow.down", "回到实时", color: TermColors.fg, style: .subheadline.weight(.semibold))
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
        .accessibilityLabel(terminal.history.hasNewOutput ? "有新输出，回到实时" : "回到实时")
        .accessibilityHint("离开回看，回到实时画面")
    }

    private var fontRow: some View {
        HStack(spacing: 20) {
            Text("字号").font(TerminalPane.fontStyle).ink(TermColors.dim).frame(maxWidth: .infinity, alignment: .leading)
            fontButton("A−", "减小字号", font > TerminalFont.sizes.lowerBound) { setFont(font - 1) }
            Text("\(font)").font(TerminalPane.fontStyle).ink(TermColors.dim).accessibilityLabel("字号 \(font)")
            fontButton("A+", "增大字号", font < TerminalFont.sizes.upperBound) { setFont(font + 1) }
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
                IconText(Mark.progress, "连接终端…", color: TermColors.dim, style: .subheadline)
            case .closed(let reason):
                Text("终端连接断开了").font(.body.weight(.semibold)).ink(TermColors.fg)
                    .accessibilityAddTraits(.isHeader)
                Text(
                    !online ? "等待服务器连接恢复后，会重新获取终端画面。"
                        : running ? "下面是断开前的画面。会话还在运行，聊天照常更新。" : "会话不在运行。"
                )
                .font(.subheadline).ink(TermColors.dim)
                .padding(.top, 6).padding(.bottom, 14)
                Btn("重新连接", kind: .primary, enabled: online) { if online { open() } }
                Button {
                    details.toggle()
                } label: {
                    Text(details ? "收起详情" : "查看详情").font(.footnote).ink(TermColors.link)
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
        .padding(12)
    }
}
