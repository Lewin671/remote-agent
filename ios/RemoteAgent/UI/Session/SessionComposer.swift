// The message box of a session (docs/design-system.md §6 「输入框」): a plus that
// adds images, the text from one line up to six, and one button at the right
// rendered from Core's `PBSessionComposerView` and submitted via `CoreHost`.
import RACore
import RAProto
import SwiftUI
import UIKit

/// The box itself, without what it sends to.
struct SessionComposeBox<Tray: View, Top: View>: View {
    @Binding var text: String
    let placeholder: String
    let canSend: Bool
    let onSend: () -> Void
    let attach: AttachMenu
    var onQueue: (() -> Void)?
    var onInterrupt: (() -> Void)?
    var canInterrupt = false
    var hasDraft: Bool
    var hasTray = false
    var focus: FocusState<Bool>.Binding
    var sendHint = ""
    @ViewBuilder var tray: Tray
    @ViewBuilder var top: Top

    private enum Kind { case interrupt, send }
    private var kind: Kind {
        if !hasDraft, onInterrupt != nil { .interrupt } else { .send }
    }

    private var twoRows: Bool {
        (hasDraft && onQueue != nil) || text.contains("\n") || text.count > 20
    }

    private var button: some View {
        HStack(spacing: 6) {
            if hasDraft, let onQueue {
                Button(action: onQueue) {
                    Text(L10n.composerSendAfter())
                        .font(.subheadline.weight(.medium))
                        .foregroundStyle(DS.ink)
                        .lineLimit(1)
                        .padding(.horizontal, 14)
                        .frame(height: 36)
                        .background(DS.fill2, in: Capsule())
                        .frame(minHeight: DS.minHit)
                        .contentShape(Capsule())
                }
                .buttonStyle(PressScale())
                .disabled(!canSend)
                .opacity(canSend ? 1 : 0.35)
                .accessibilityIdentifier("session-send-after")
                .accessibilityHint(sendHint)
                .transition(.opacity)
            }
            ZStack { choice.id(kind).transition(.opacity) }
                .animation(Motion.enter(Motion.fast), value: kind)
        }
        .animation(Motion.enter(Motion.fast), value: hasDraft && onQueue != nil)
    }

    @ViewBuilder private var choice: some View {
        if !hasDraft, let onInterrupt {
            Button(action: onInterrupt) { InkCircle(symbol: "stop.fill", enabled: canInterrupt) }
                .buttonStyle(PressScale())
                .disabled(!canInterrupt)
                .accessibilityLabel(L10n.sessionInterrupt())
        } else {
            Button(action: onSend) { InkCircle(symbol: "arrow.up", enabled: canSend) }
                .buttonStyle(PressScale())
                .disabled(!canSend)
                .accessibilityLabel(L10n.composerSend())
                .accessibilityIdentifier("session-send")
                .accessibilityHint(sendHint)
        }
    }

    var body: some View {
        MessageCapsule(
            text: $text,
            placeholder: placeholder,
            label: L10n.composerMessage(),
            focus: focus,
            heightChanges: "\(hasTray)\(twoRows)\(hasDraft)\(text)",
            hasTray: hasTray,
            twoRows: twoRows
        ) {
            tray
        } top: {
            top
        } plus: {
            attach
        } trailing: {
            button
        }
        .background {
            Group {
                Button(L10n.composerSend(), action: onSend).keyboardShortcut(.return, modifiers: .command).disabled(!canSend)
                if let onQueue {
                    Button(L10n.composerSendAfter(), action: onQueue).keyboardShortcut(.return, modifiers: .option).disabled(!canSend)
                }
            }
            .opacity(0).accessibilityHidden(true)
        }
    }
}

/// ＋ at the left of a message box.
struct AttachMenu: View {
    let images: ComposerAttachments

    var body: some View {
        let files = images.filesOffered
        let paste = images.clipboardHasImage
        if images.blocked != nil || (!files && !paste) {
            Button { images.pickLibrary() } label: { PlusCircle(dimmed: images.blocked != nil) }
                .buttonStyle(.plain)
                .accessibilityLabel(L10n.imageAdd())
        } else {
            Menu {
                Button { images.pickLibrary() } label: { Label(files ? L10n.attachmentPhotosVideos() : L10n.attachmentIosPhotos(), systemImage: "photo") }
                if files {
                    Button { images.pickFiles() } label: { Label(L10n.attachmentFile(), systemImage: "doc") }
                }
                if paste {
                    Button { images.paste() } label: { Label(L10n.attachmentIosPasteImage(), systemImage: "doc.on.clipboard") }
                }
                if files, !images.fileMetadataNotice.isEmpty {
                    Section { Text(images.fileMetadataNotice) }
                }
            } label: {
                PlusCircle()
            }
            .menuOrder(.fixed)
            .accessibilityLabel(L10n.newSessionIosAttach())
        }
    }
}

/// The message box of a session, wired to `PBSessionViewModel` and `CoreHost`.
struct SessionComposer: View {
    let sessionID: String
    let header: PBSessionHeaderView
    let composer: PBSessionComposerView
    let actions: PBSessionActionsView
    var focus: FocusState<Bool>.Binding
    var keyboardOpen = false
    var maxTrayHeight: CGFloat = 340
    var onTerminal: () -> Void = {}
    /// A message is being sent from this box, with or without attachments.
    var onSent: () -> Void = {}
    @Environment(AccountContext.self) private var context
    @State private var text = ""
    @State private var inFlightTextEdits = 0

    private var machineID: String { header.machineID }
    private var scope: PBDraftScope { .session(sessionID) }
    private var draft: PBAttachmentDraftView { composer.draft }

    private var images: ComposerAttachments {
        ComposerAttachments(context: context, scope: scope, draft: draft)
    }

    private func setDraftInput(_ next: String) {
        guard next != text else { return }
        text = next
        inFlightTextEdits += 1
        let host = context.coreHost
        let scope = self.scope
        Task {
            await host.setDraftText(scope: scope, text: next)
            await MainActor.run {
                inFlightTextEdits = max(0, inFlightTextEdits - 1)
            }
        }
    }

    private func send(_ mode: PBSendMode) {
        guard composer.enabled && !header.protocolMismatch else { return }
        let draft = self.draft
        guard draft.canSend && !draft.sending && !draft.restoring else { return }
        let currentText = text
        SessionHaptics.tap()
        onSent()
        let host = context.coreHost
        let sid = sessionID
        let scope = self.scope
        Task {
            await host.setDraftText(scope: scope, text: currentText)
            await host.sendSessionDraft(sessionID: sid, mode: mode)
        }
    }

    private func sendInterrupt() {
        let interruptState = actions.interrupt
        Task {
            await context.coreHost.submitSessionCommand(
                sessionID: sessionID,
                command: .with { $0.interrupt = .with { $0.sessionID = sessionID } },
                machineID: machineID,
                slotKey: interruptState.slotKey
            )
        }
    }

    var body: some View {
        let draft = self.draft
        let images = self.images
        let hasDraft = draft.hasUnsavedContent_p || !text.jvmIsBlank
        let canSend = composer.enabled && !header.protocolMismatch && draft.canSend && !draft.sending && !draft.restoring
        let placeholder = composer.hasPlaceholder ? composer.placeholder.rendered() : L10n.composerMessage()
        let interruptBusy = actions.interrupt.status == .pending || actions.interrupt.busy
        let interruptError: String = {
            let s = actions.interrupt
            if commandRunning(s) { return "" }
            if s.hasErrorText { return s.errorText.rendered() }
            return s.errorCode
        }()
        let sendHint: String = {
            if !composer.enabled {
                return composer.hasDisabledReason ? composer.disabledReason.rendered() : L10n.composerIosConnectSend()
            }
            if !canSend && draft.hasSendBlockedReason {
                return draft.sendBlockedReason.rendered()
            }
            return ""
        }()
        let tray = composer.pendingTray
        let showTray = tray.totalCount > 0 || (tray.hasUndo && tray.undo.visible)
        VStack(spacing: 0) {
            SessionComposeBox(
                text: Binding(get: { text }, set: { setDraftInput($0) }),
                placeholder: placeholder,
                canSend: canSend,
                onSend: { send(composer.defaultSendMode) },
                attach: AttachMenu(images: images),
                onQueue: composer.hasActiveTurn_p && composer.supportsQueueMode ? { send(.queue) } : nil,
                onInterrupt: composer.interruptVisible ? { sendInterrupt() } : nil,
                canInterrupt: actions.canInterrupt && !interruptBusy,
                hasDraft: hasDraft,
                hasTray: showTray,
                focus: focus,
                sendHint: sendHint
            ) {
                PendingTrayView(
                    sessionID: sessionID,
                    tray: tray,
                    keyboardOpen: keyboardOpen,
                    maxHeight: maxTrayHeight,
                    onTerminal: onTerminal
                )
            } top: {
                images.strip(focus: focus)
            }
            Color.clear.frame(height: 0)
                .debugSignal("session.draft") { setDraftInput(SessionDebug.text) }
                .debugSignal("session.send") { send(composer.defaultSendMode) }
                .debugSignal("session.queue") {
                    send((composer.hasActiveTurn_p && composer.supportsQueueMode) ? .queue : composer.defaultSendMode)
                }
                .debugSignal("session.interrupt") { sendInterrupt() }
            if !interruptError.isEmpty {
                Notice(L10n.sessionIosInterruptFailed(reason: interruptError), tone: .error).padding(.horizontal, DS.Space.m).padding(.bottom, 6)
            }
            if slashWarning(text) {
                let warningText = composer.hasSlashWarningText ? composer.slashWarningText.rendered() : L10n.composerSlashNote()
                Text(warningText).font(.footnote).foregroundStyle(DS.orangeInk)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, DS.Space.xl).padding(.bottom, 6)
            }
        }
        .onAppear {
            if inFlightTextEdits == 0 && text != draft.text {
                text = draft.text
            }
        }
        .onChange(of: draft.textRevision) { _, _ in
            if inFlightTextEdits == 0 && text != draft.text {
                text = draft.text
            }
        }
        .onChange(of: draft.text) { _, coreText in
            if inFlightTextEdits == 0 && text != coreText {
                text = coreText
            }
        }
        .onChange(of: inFlightTextEdits) { _, pending in
            if pending == 0 && text != draft.text {
                text = draft.text
            }
        }
    }

    private func commandRunning(_ st: PBSessionCommandState) -> Bool {
        st.status == .pending || st.busy
    }
}
