// The message box of a session (docs/design-system.md §6 「输入框」; Android's
// ui/ComposeBox.kt and `Composer` in ui/Session.kt): a plus that adds
// images, the text from one line up to six, and one button at the right.
//
// That button is send. In a running session with nothing typed it is the
// square that interrupts the turn; with something typed, a tap inserts the
// message into the running turn (steer, what the CLIs' own Enter does) and
// a long press offers to queue it for when the turn ends (architecture §6).
// The draft is the user's: it is kept per session for the account
// (`SessionStore`), stays while the server is out of reach, and is cleared
// only by a send.
import RACore
import RAProto
import SwiftUI
import UIKit

/// The box itself, without what it sends to.
struct SessionComposeBox<Top: View>: View {
    @Binding var text: String
    let placeholder: String
    let canSend: Bool
    let onSend: () -> Void
    let onPlus: () -> Void
    var plusEnabled = true
    /// Queues instead of inserting; nil when there is no running turn to wait for.
    var onQueue: (() -> Void)?
    /// Interrupts the turn; nil when none is running.
    var onInterrupt: (() -> Void)?
    var canInterrupt = false
    var hasDraft: Bool
    var focus: FocusState<Bool>.Binding
    /// Why sending is not possible now, for VoiceOver, which cannot see the notice that says so.
    var sendHint = ""
    @ViewBuilder var top: Top

    private func circle<Icon: View>(_ fill: Color, enabled: Bool, @ViewBuilder icon: () -> Icon) -> some View {
        icon()
            // The circle is a fixed 32 pt: what is drawn in it does not outgrow it.
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            .frame(width: 32, height: 32)
            .background(fill, in: Circle())
            .opacity(enabled ? 1 : 0.4)
            .frame(width: Theme.minHit, height: Theme.minHit)
            .contentShape(Rectangle())
    }

    private var sendCircle: some View {
        circle(Theme.blueBg, enabled: canSend) { Ic(Lucide.arrowUp, size: 20, color: Theme.onSolid) }
    }

    @ViewBuilder private var button: some View {
        if !hasDraft, let onInterrupt {
            Button(action: onInterrupt) {
                circle(Theme.chip, enabled: canInterrupt) { Ic(Lucide.square, size: 14, color: Theme.red) }
            }
            .buttonStyle(PressStyle())
            .disabled(!canInterrupt)
            .accessibilityLabel("中断本轮")
        } else if let onQueue, canSend {
            // A tap sends; a long press opens the choice, as the system's own send buttons do.
            Menu {
                Button(action: onSend) {
                    Text("现在插入这一轮")
                    Text("点 ↑ 就是这个")
                }
                Button(action: onQueue) {
                    Text("这一轮结束后再发")
                    Text("先排队，结束后按顺序发出")
                }
            } label: {
                sendCircle
            } primaryAction: {
                onSend()
            }
            .menuOrder(.fixed)
            .accessibilityLabel("发送")
            .accessibilityHint("插入这一轮")
            .accessibilityAction(named: "这一轮结束后再发", onQueue)
        } else {
            Button(action: onSend) { sendCircle }
                .buttonStyle(PressStyle())
                .disabled(!canSend)
                .accessibilityLabel("发送")
                .accessibilityHint(sendHint)
        }
    }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: 24, style: .continuous)
        VStack(spacing: 0) {
            top
            HStack(alignment: .bottom, spacing: 0) {
                Button(action: onPlus) {
                    Ic(Lucide.plus, size: 22, color: Theme.muted).hitTarget()
                }
                .buttonStyle(PressStyle())
                .disabled(!plusEnabled)
                .opacity(plusEnabled ? 1 : 0.4)
                .accessibilityLabel("添加图片")
                TextField("", text: $text, prompt: Text(placeholder).foregroundStyle(Theme.muted), axis: .vertical)
                    .lineLimit(1...6)
                    .type(T.body, Theme.fg)
                    .tint(Theme.blue)
                    .focused(focus)
                    .padding(.horizontal, 4).padding(.vertical, 10)
                    .frame(maxWidth: .infinity, minHeight: Theme.minHit)
                    // The whole of the box is the field to the finger, not just the line of text.
                    .contentShape(Rectangle())
                    .onTapGesture { focus.wrappedValue = true }
                    .accessibilityLabel("消息")
                button
            }
        }
        .padding(3)
        .background(Theme.card, in: shape)
        .overlay { shape.strokeBorder(Theme.line, lineWidth: 1) }
        .padding(.init(top: 4, leading: 10, bottom: 8, trailing: 10))
        // A keyboard attached to an iPad or a Mac sends with ⌘↩; Return alone is a new line, as on the screen keyboard.
        .background {
            Button("发送", action: onSend).keyboardShortcut(.return, modifiers: .command).disabled(!canSend)
                .opacity(0).accessibilityHidden(true)
        }
    }
}

/// The message box of a session, wired to what it sends.
struct SessionComposer: View {
    let session: SessionView
    let status: Status
    let derived: Derived
    var focus: FocusState<Bool>.Binding
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var interrupt = ActionRunner()
    /// A message's images are being kept before it is sent.
    @State private var keeping = false
    @State private var notice = ""

    private var images: SessionImageContext {
        let machine = model.state.machines[session.machineId]
        return SessionImageContext(
            sessionId: session.id, machineId: session.machineId, remote: context.remote, store: context.sessionStore,
            blocked: uploadBlocked(
                machine, online: model.state.conn == .online, name: machineLabel(model.state, id: session.machineId)),
            limit: imageLimit(for: machine))
    }

    private func send(_ mode: PBSendMode) {
        let store = context.sessionStore
        let sid = session.id
        let message = SessionText.trim(store.draft(sid))
        let images = images
        let attached = SessionImages.hasImages(images)
        if message.isEmpty && !attached { return }
        // Every image must be on the computer first: never a message with some of them.
        if attached && !SessionImages.ready(images) { return }
        SessionHaptics.tap()
        let taken = SessionImages.send(images, text: message, mode: mode) { kept in
            keeping = false
            switch kept {
            case .success:
                if SessionText.trim(store.draft(sid)) == message { store.setDraft(sid, "") }
            case .failure(let error):
                notice = "没能保存待发的图，这条没有发出：\(errorText(error))"
            }
        }
        if taken {
            keeping = true
            return
        }
        notice = ""
        store.setDraft(sid, "")
        store.send(context.remote, sid: sid, text: message, mode: mode)
    }

    var body: some View {
        let state = model.state
        let store = context.sessionStore
        let sid = session.id
        let online = state.conn == .online
        let running = status == .running || derived.running
        let images = images
        let attached = SessionImages.hasImages(images)
        let text = store.draft(sid)
        // A computer on another protocol version would refuse it: its notice says which side to update.
        let refused = protocolMismatch(state.machines[session.machineId]) != nil
        let remote = context.remote
        VStack(spacing: 0) {
            SessionComposeBox(
                text: Binding(get: { store.draft(sid) }, set: { store.setDraft(sid, $0) }),
                placeholder: composerPlaceholder(derived),
                canSend: composerCanSend(
                    text: text, online: online, keeping: keeping, hasImages: attached, imagesReady: SessionImages.ready(images),
                    refused: refused),
                onSend: { send(.steer) },
                onPlus: { SessionImages.plusTapped(images) },
                plusEnabled: images.blocked == nil,
                onQueue: running ? { send(.queue) } : nil,
                onInterrupt: running
                    ? {
                        SessionHaptics.decide()
                        interrupt.run {
                            var cmd = PBCommand()
                            cmd.interrupt = .with { $0.sessionID = sid }
                            try await remote.command(sid, cmd)
                        }
                    } : nil,
                canInterrupt: online && !interrupt.running,
                hasDraft: !SessionText.isBlank(text) || attached,
                focus: focus,
                sendHint: online ? "" : "连上服务器后才能发送"
            ) {
                SessionImages.DraftStrip(context: images)
            }
            Color.clear.frame(height: 0)
                .debugSignal("session.draft") { store.setDraft(sid, SessionDebug.text) }
                .debugSignal("session.send") { send(.steer) }
                .debugSignal("session.queue") { send(.queue) }
                .debugSignal("session.interrupt") {
                    interrupt.run {
                        var cmd = PBCommand()
                        cmd.interrupt = .with { $0.sessionID = sid }
                        try await remote.command(sid, cmd)
                    }
                }
            if !notice.isEmpty { Banner(notice, tone: .error).padding(.horizontal, 12).padding(.bottom, 6) }
            if !interrupt.error.isEmpty {
                Banner("没能中断：\(interrupt.error)", tone: .error).padding(.horizontal, 12).padding(.bottom, 6)
            }
            if slashWarning(text) {
                Text(slashWarningText).type(T.meta, Theme.orangeInk)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 16).padding(.bottom, 6)
            }
        }
    }
}
