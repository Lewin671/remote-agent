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
    let attach: AttachMenu
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

    /// Which of the three buttons it is: they cross-fade when it changes (§4 动效, 快).
    private enum Kind { case interrupt, menu, send }
    private var kind: Kind {
        if !hasDraft, onInterrupt != nil { .interrupt } else if onQueue != nil, canSend { .menu } else { .send }
    }

    private var button: some View {
        ZStack { choice.id(kind).transition(.opacity) }
            .animation(Motion.enter(Motion.fast), value: kind)
    }

    @ViewBuilder private var choice: some View {
        if !hasDraft, let onInterrupt {
            Button(action: onInterrupt) { InkCircle(symbol: "stop.fill", enabled: canInterrupt) }
                .buttonStyle(PressScale())
                .disabled(!canInterrupt)
                .accessibilityLabel("中断")
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
                InkCircle(symbol: "arrow.up")
            } primaryAction: {
                onSend()
            }
            .menuOrder(.fixed)
            .buttonStyle(PressScale())
            .accessibilityLabel("发送")
            .accessibilityHint("插入这一轮")
            .accessibilityAction(named: "这一轮结束后再发", onQueue)
        } else {
            Button(action: onSend) { InkCircle(symbol: "arrow.up", enabled: canSend) }
                .buttonStyle(PressScale())
                .disabled(!canSend)
                .accessibilityLabel("发送")
                .accessibilityHint(sendHint)
        }
    }

    var body: some View {
        MessageCapsule(text: $text, placeholder: placeholder, label: "消息", focus: focus, heightChanges: "\(hasDraft)\(text)") {
            top
        } plus: {
            attach
        } trailing: {
            button
        }
        // A keyboard attached to an iPad or a Mac sends with ⌘↩; Return alone is a new line, as on the screen keyboard.
        .background {
            Button("发送", action: onSend).keyboardShortcut(.return, modifiers: .command).disabled(!canSend)
                .opacity(0).accessibilityHidden(true)
        }
    }
}

/// ＋ at the left of a message box (design-system §6 「输入框」). It does
/// one thing: open the library. A computer that takes files offers 照片和视频
/// and 文件 in the system's menu instead, the library then holding videos
/// too, which go as files (session-files.md 「从相册选视频」). With an image
/// on the clipboard the menu also offers to paste it, as the system's text
/// field cannot take a pasted image.
struct AttachMenu: View {
    let images: ComposerAttachments

    var body: some View {
        let files = images.filesOffered
        let paste = images.clipboardHasImage
        if images.blocked != nil || (!files && !paste) {
            Button { images.pickLibrary() } label: { PlusCircle(dimmed: images.blocked != nil) }
                .buttonStyle(.plain)
                .accessibilityLabel("添加图片")
        } else {
            Menu {
                Button { images.pickLibrary() } label: { Label(files ? "照片和视频" : "照片", systemImage: "photo") }
                if files {
                    Button { images.pickFiles() } label: { Label("文件", systemImage: "doc") }
                }
                if paste {
                    Button { images.paste() } label: { Label("粘贴图片", systemImage: "doc.on.clipboard") }
                }
                if files { Section { Text("文件原样发送，包含其中的元数据。") } }
            } label: {
                PlusCircle()
            }
            .menuOrder(.fixed)
            .accessibilityLabel("添加附件")
        }
    }
}

/// The message box of a session, wired to what it sends.
struct SessionComposer: View {
    let session: SessionView
    let status: Status
    let derived: Derived
    var focus: FocusState<Bool>.Binding
    /// A message is being sent from this box, with or without images. Sending is asking to see the answer.
    var onSent: () -> Void = {}
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var interrupt = ActionRunner()
    @State private var notice = ""

    private var images: ComposerAttachments {
        ComposerAttachments(state: model.state, key: ImageCenter.sessionKey(session.id), machineId: session.machineId, remote: context.remote)
    }

    private func send(_ mode: PBSendMode) {
        let store = context.sessionStore
        let sid = session.id
        let message = store.draft(sid).jvmTrim()
        let images = images
        let attached = images.hasImages
        if message.isEmpty && !attached { return }
        images.refresh()
        // Every image must be on the computer first: never a message with some of them.
        if attached && !images.ready { return }
        SessionHaptics.tap()
        onSent()
        let taken = SessionImages.send(context.remote, session: sid, store: store, machineId: session.machineId, text: message, mode: mode) { kept in
            switch kept {
            case .success:
                if store.draft(sid).jvmTrim() == message { store.setDraft(sid, "") }
            case .failure(let error):
                notice = "这条没有发出：\(errorText(error))"
            }
        }
        // The message's images are being kept before it is sent (`SessionStore.isKeeping`).
        if taken { return }
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
        let attached = images.hasImages
        let text = store.draft(sid)
        // A computer on another protocol version would refuse it: its notice says which side to update.
        let refused = protocolMismatch(state.machines[session.machineId]) != nil
        let remote = context.remote
        VStack(spacing: 0) {
            SessionComposeBox(
                text: Binding(get: { store.draft(sid) }, set: { store.setDraft(sid, $0) }),
                placeholder: composerPlaceholder(questionPending: !derived.questions.isEmpty, planOpen: session.info?.planOpen == true),
                canSend: composerCanSend(
                    text: text, online: online, keeping: store.isKeeping(sid), hasImages: attached, imagesReady: images.ready,
                    refused: refused),
                onSend: { send(.steer) },
                attach: AttachMenu(images: images),
                onQueue: running ? { send(.queue) } : nil,
                onInterrupt: running
                    ? {
                        interrupt.run {
                            var cmd = PBCommand()
                            cmd.interrupt = .with { $0.sessionID = sid }
                            try await remote.command(sid, cmd)
                        }
                    } : nil,
                canInterrupt: online && !interrupt.running,
                hasDraft: !text.jvmIsBlank || attached,
                focus: focus,
                sendHint: online ? "" : "连上服务器后才能发送"
            ) {
                images.strip(session: (sid, store))
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
            if !notice.isEmpty { Notice(notice, tone: .error).padding(.horizontal, DS.Space.m).padding(.bottom, 6) }
            if let error = store.draftErrors[sid] { Notice(error, tone: .error).padding(.horizontal, DS.Space.m).padding(.bottom, 6) }
            if !interrupt.error.isEmpty {
                Notice("没能中断：\(interrupt.error)", tone: .error).padding(.horizontal, DS.Space.m).padding(.bottom, 6)
            }
            if slashWarning(text) {
                Text(slashWarningText).font(.footnote).foregroundStyle(DS.orangeInk)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, DS.Space.xl).padding(.bottom, 6)
            }
        }
    }
}
