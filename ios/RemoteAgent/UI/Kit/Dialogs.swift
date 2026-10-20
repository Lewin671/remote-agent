// Dialogs and the action menu (docs/design-system.md §6). The design gives
// them their own look (a card of r-lg, the title in the text colour, 取消 at
// the left and the action at the right, the one place a solid red button
// appears), so they are drawn here and not the system's alert; what the
// system does for its own is kept: they sit above everything, sheets
// included, VoiceOver stays inside them, the escape gesture cancels, a
// confirmation ticks, and with reduced motion they only fade.
import SwiftUI

/// Presents `overlay` over the whole window on a scrim, without the system's
/// slide from the bottom: what the dialogs and the action menu here stand
/// on, and what a screen's own dialog should (`overlayCover`).
struct OverlayCover<Overlay: View>: ViewModifier {
    @Binding var isPresented: Bool
    let alignment: Alignment
    /// Whether a tap on the scrim or the escape gesture may close it now.
    let canDismiss: () -> Bool
    @ViewBuilder let overlay: () -> Overlay
    /// The cover is up; it comes down only after the overlay has faded out.
    @State private var covered = false

    func body(content: Content) -> some View {
        content
            .fullScreenCover(isPresented: $covered) {
                OverlayStage(isPresented: $isPresented, covered: $covered, alignment: alignment, canDismiss: canDismiss, overlay: overlay)
                    .presentationBackground(.clear)
            }
            .onChange(of: isPresented, initial: true) { _, shown in
                guard shown, !covered else { return }
                var now = Transaction()
                now.disablesAnimations = true
                withTransaction(now) { covered = true }
            }
    }
}

private struct OverlayStage<Overlay: View>: View {
    @Binding var isPresented: Bool
    @Binding var covered: Bool
    let alignment: Alignment
    let canDismiss: () -> Bool
    @ViewBuilder let overlay: () -> Overlay
    @State private var shown = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        ZStack(alignment: alignment) {
            Theme.scrim.ignoresSafeArea()
                .opacity(shown ? 1 : 0)
                .contentShape(Rectangle())
                .onTapGesture { if canDismiss() { isPresented = false } }
                .accessibilityHidden(true)
            overlay()
                // Whatever page it is over, a dialog's own surface is the card.
                .environment(\.onSoft, false)
                .opacity(shown ? 1 : 0)
                .scaleEffect(shown || reduceMotion || alignment != .center ? 1 : 0.94)
                .offset(y: shown || reduceMotion || alignment == .center ? 0 : 24)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: alignment)
        .accessibilityAddTraits(.isModal)
        .accessibilityAction(.escape) { if canDismiss() { isPresented = false } }
        .tint(Theme.blue)
        .onAppear { withAnimation(.easeOut(duration: 0.18)) { shown = true } }
        .onChange(of: isPresented) { _, still in
            guard !still else { return }
            withAnimation(.easeIn(duration: 0.14)) {
                shown = false
            } completion: {
                var now = Transaction()
                now.disablesAnimations = true
                withTransaction(now) { covered = false }
            }
        }
    }
}

/// The card of a dialog: radius 20, at most 320 wide, 30 pt from the sides; it scrolls when text at a large size outgrows the screen.
struct DialogCard<Content: View>: View {
    private let content: Content

    init(@ViewBuilder content: () -> Content) { self.content = content() }

    var body: some View {
        let card = VStack(spacing: 0) { content }
            .padding(.init(top: 18, leading: 16, bottom: 12, trailing: 16))
            .frame(maxWidth: 320)
            .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.Radius.lg, style: .continuous))
            // One shadow for the card; without the group each button and line of text inside would cast its own.
            .compositingGroup()
            .shadow(color: .black.opacity(0.2), radius: 15, y: 10)
            .padding(.horizontal, 30).padding(.vertical, 20)
        ViewThatFits(in: .vertical) {
            card
            ScrollView { card }.scrollBounceBehavior(.basedOnSize)
        }
    }
}

/// One row of a ⋯ menu.
///
/// `value`: what a setting is now; the row then reads 「名称 …… 值 ›」 and
/// opens where it is changed, and `danger` (完全放行) or `warn` (a nearly
/// full context) colour the value. Without one, `danger` makes the row red
/// (中断本轮, 结束会话…). Not `enabled`: greyed and not tappable. `note`: a
/// small line under it, why it is not available or (`noteError`) how it
/// failed. `busy`: it is running, shown with a spinner; the other choices are
/// then given not enabled and the menu stays until it ends. `keepOpen`:
/// tapping it leaves the menu open.
struct SheetAction {
    var text: String
    var value: String?
    var danger = false
    var warn = false
    var enabled = true
    var note = ""
    var noteError = false
    var busy = false
    var keepOpen = false
    var action: () -> Void

    init(_ text: String, value: String? = nil, danger: Bool = false, warn: Bool = false, enabled: Bool = true, note: String = "", noteError: Bool = false, busy: Bool = false, keepOpen: Bool = false, action: @escaping () -> Void) {
        self.text = text
        self.value = value
        self.danger = danger
        self.warn = warn
        self.enabled = enabled
        self.note = note
        self.noteError = noteError
        self.busy = busy
        self.keepOpen = keepOpen
        self.action = action
    }
}

/// A ⋯ menu, drawn as the kit's sheets are (选项目 and the rest): a card from
/// the bottom with a grabber, what it is about at the top, and one
/// left-aligned row per choice (design-system §6 弹层与对话框). A tap
/// outside or a pull down closes it, unless a choice is running.
private struct ActionMenu<Info: View>: View {
    let actions: [SheetAction]
    let hasInfo: Bool
    let info: Info
    let dismiss: () -> Void
    @State private var pull: CGFloat = 0

    private func row(_ a: SheetAction, last: Bool) -> some View {
        let ink = a.value == nil && a.danger ? Theme.red : a.value == nil && a.warn ? Theme.orangeInk : Theme.fg
        return Button {
            if a.danger && a.value == nil { Haptics.destructive() }
            if !a.keepOpen { dismiss() }
            a.action()
        } label: {
            HStack(spacing: 10) {
                VStack(alignment: .leading, spacing: 1) {
                    if a.busy {
                        IconText(Lucide.loaderCircle, a.text, color: Theme.muted, style: T.body)
                    } else {
                        Text(a.text).type(T.body, ink).opacity(a.enabled ? 1 : 0.4)
                    }
                    if !a.note.isEmpty {
                        Text(a.note).type(T.meta, a.noteError ? Theme.red : Theme.muted)
                    }
                }
                .padding(.vertical, 8)
                // The name keeps its room; a long value (a model) is what gives way.
                .layoutPriority(1)
                Spacer(minLength: 0)
                if let value = a.value {
                    HStack(spacing: 4) {
                        Text(value).type(T.body, a.danger ? Theme.red : a.warn ? Theme.orangeInk : Theme.fg2).lineLimit(1)
                        Arrow(Theme.faint, style: T.body)
                    }
                    .opacity(a.enabled ? 1 : 0.4)
                }
            }
            .padding(.horizontal, 2)
            .frame(maxWidth: .infinity, minHeight: 50, alignment: .leading)
            .bottomLine(last ? .clear : Theme.line)
        }
        .buttonStyle(RowPressStyle())
        .disabled(!a.enabled || a.busy)
    }

    var body: some View {
        // Leaving while a choice is running would drop its answer.
        let busy = actions.contains { $0.busy }
        let shape = UnevenRoundedRectangle(topLeadingRadius: Theme.Radius.lg, topTrailingRadius: Theme.Radius.lg, style: .continuous)
        let menu = VStack(alignment: .leading, spacing: 0) {
            if hasInfo {
                info.frame(maxWidth: .infinity, alignment: .leading).padding(.bottom, 6)
            }
            ForEach(actions.indices, id: \.self) { i in
                row(actions[i], last: i == actions.count - 1)
            }
        }
        .padding(.init(top: 26, leading: 16, bottom: 16, trailing: 16))
        VStack(spacing: 0) {
            ViewThatFits(in: .vertical) {
                menu
                ScrollView { menu }.scrollBounceBehavior(.basedOnSize)
            }
        }
        .frame(maxWidth: 640)
        .background {
            shape.fill(Theme.card).ignoresSafeArea(edges: .bottom)
        }
        .overlay(alignment: .top) {
            Capsule().fill(Theme.track).frame(width: 36, height: 5).padding(.top, 6).accessibilityHidden(true)
        }
        // Not to the top edge: the scrim above it is what closes it.
        .padding(.top, 48)
        .offset(y: max(0, pull))
        .gesture(
            DragGesture()
                .onChanged { pull = busy ? 0 : $0.translation.height }
                .onEnded { end in
                    if !busy && end.translation.height > 80 { dismiss() } else { withAnimation(.easeOut(duration: 0.15)) { pull = 0 } }
                })
    }
}

/// What 保存 of an input dialog does with the text: `.now` closes the dialog
/// and hands the text over; `.saving` keeps the dialog while the work runs,
/// closes it when the work is done, and when it fails stays open with what
/// was typed and why (the kit's dialogs never say something went through
/// before it did).
enum InputSave {
    case now((String) -> Void)
    case saving((String) async throws -> Void)
}

private struct InputDialog: View {
    let title: String
    let placeholder: String
    let confirm: String
    let dismissLabel: String
    let text: String?
    let limit: Int
    let lines: ClosedRange<Int>
    /// What may be typed, as a title without line breaks; applied to every change.
    let filter: ((String) -> String)?
    let onDismiss: () -> Void
    let save: InputSave
    /// Says whether the dialog may be left now: not while it saves.
    let busy: Binding<Bool>
    /// A signal that saves `debugText()` as if it had been typed (debug builds).
    let debugSignal: String?
    let debugText: () -> String
    @State var value: String
    @State private var runner = ActionRunner()

    private func typed(_ new: String) {
        var kept = filter?(new) ?? new
        if kept.count > limit { kept = String(kept.prefix(limit)) }
        if kept != new { value = kept }
    }

    private func submit() {
        switch save {
        case .now(let done):
            Haptics.confirm()
            onDismiss()
            done(value)
        case .saving(let work):
            let text = value
            runner.run({ try await work(text) }, onDone: {
                Haptics.confirm()
                onDismiss()
            })
        }
    }

    var body: some View {
        let running = runner.running
        DialogCard {
            Text(title).type(T.h17, Theme.fg).multilineTextAlignment(.center).padding(.bottom, 12)
                .accessibilityAddTraits(.isHeader)
            // The keyboard comes up with the dialog: there is nothing else to do in it.
            InputField(
                text: $value, placeholder: placeholder, lines: lines, focusOnAppear: true,
                onSubmit: lines.upperBound == 1 ? { if !running { submit() } } : nil
            )
            .onChange(of: value) { _, new in typed(new) }
            .padding(.bottom, 8)
            if !runner.error.isEmpty {
                Text("没能保存：\(runner.error)").type(T.sm, Theme.red).multilineTextAlignment(.center).padding(.bottom, 8)
            } else if let text {
                Text(text).type(T.sm, Theme.muted).multilineTextAlignment(.center).padding(.bottom, 8)
            }
            Btns {
                Btn(dismissLabel, enabled: !running, action: onDismiss)
                Btn(running ? "保存中…" : confirm, kind: .primary, enabled: !running, action: submit)
            }
            .padding(.top, 6)
        }
        .onChange(of: running, initial: true) { _, now in busy.wrappedValue = now }
        .modifier(DebugSave(name: debugSignal) {
            value = filter?(debugText()) ?? debugText()
            submit()
        })
    }
}

/// `debugSignal` when there is a name for it.
private struct DebugSave: ViewModifier {
    let name: String?
    let action: () -> Void

    func body(content: Content) -> some View {
        if let name { content.debugSignal(name, action) } else { content }
    }
}

/// An input dialog with whether it is saving, which decides whether it may be left.
private struct InputAlert: ViewModifier {
    @Binding var isPresented: Bool
    let title: String
    let initial: String
    let placeholder: String
    let confirm: String
    let dismiss: String
    let text: String?
    let limit: Int
    let lines: ClosedRange<Int>
    let filter: ((String) -> String)?
    let debugSignal: String?
    let debugText: () -> String
    let save: InputSave
    @State private var busy = false

    func body(content: Content) -> some View {
        content.modifier(OverlayCover(isPresented: $isPresented, alignment: .center, canDismiss: { !busy }) {
            InputDialog(
                title: title, placeholder: placeholder, confirm: confirm, dismissLabel: dismiss, text: text, limit: limit, lines: lines,
                filter: filter, onDismiss: { isPresented = false }, save: save, busy: $busy, debugSignal: debugSignal,
                debugText: debugText, value: initial)
        })
    }
}

extension View {
    /// A confirmation: the title in the text colour, the danger is the
    /// button's. 取消 at the left, the action at the right, solid red when it
    /// deletes, removes, leaves, ends, or switches to full access.
    func confirm(isPresented: Binding<Bool>, title: String, text: String, confirm: String, danger: Bool = false, onConfirm: @escaping () -> Void) -> some View {
        modifier(OverlayCover(isPresented: isPresented, alignment: .center, canDismiss: { true }) {
            DialogCard {
                Text(title).type(T.h17, Theme.fg).multilineTextAlignment(.center).padding(.bottom, 6)
                    .accessibilityAddTraits(.isHeader)
                Text(text).type(T.sm, Theme.muted).multilineTextAlignment(.center).padding(.bottom, 14)
                Btns {
                    Btn("取消") { isPresented.wrappedValue = false }
                    Btn(confirm, kind: danger ? .danger : .primary) {
                        if danger { Haptics.destructive() } else { Haptics.confirm() }
                        isPresented.wrappedValue = false
                        onConfirm()
                    }
                }
            }
        })
    }

    /// A confirmation with a line or two of text to enter, such as a name; at
    /// most `limit` characters. The dialog closes on 保存 and hands the text over.
    func inputAlert(isPresented: Binding<Bool>, title: String, initial: String = "", placeholder: String, confirm: String, dismiss: String = "取消", text: String? = nil, limit: Int = 40, onConfirm: @escaping (String) -> Void) -> some View {
        modifier(InputAlert(
            isPresented: isPresented, title: title, initial: initial, placeholder: placeholder, confirm: confirm, dismiss: dismiss,
            text: text, limit: limit, lines: 1...4, filter: nil, debugSignal: nil, debugText: { "" }, save: .now(onConfirm)))
    }

    /// The same dialog for a text that is saved somewhere else, as a name
    /// kept on a computer: it stays open, its buttons locked, while `save`
    /// runs; it closes when that is done, and when it throws it keeps what
    /// was typed and says why under the field. `lines` of one makes the
    /// keyboard's 完成 save; `filter` says what may be typed. `debugSignal`
    /// names a signal that saves `debugText()` as if typed (debug builds).
    func inputAlert(
        isPresented: Binding<Bool>, title: String, initial: String = "", placeholder: String, confirm: String = "保存",
        dismiss: String = "取消", text: String? = nil, limit: Int = 40, lines: ClosedRange<Int> = 1...4,
        filter: ((String) -> String)? = nil, debugSignal: String? = nil, debugText: @escaping () -> String = { "" },
        save: @escaping (String) async throws -> Void
    ) -> some View {
        modifier(InputAlert(
            isPresented: isPresented, title: title, initial: initial, placeholder: placeholder, confirm: confirm, dismiss: dismiss,
            text: text, limit: limit, lines: lines, filter: filter, debugSignal: debugSignal, debugText: debugText, save: .saving(save)))
    }

    /// A dialog of a screen's own over the whole window, on the scrim the
    /// kit's dialogs stand on; give it a `DialogCard`. `canDismiss` says
    /// whether a tap outside or the escape gesture may close it now.
    func overlayCover<Overlay: View>(
        isPresented: Binding<Bool>, alignment: Alignment = .center, canDismiss: @escaping () -> Bool = { true },
        @ViewBuilder content: @escaping () -> Overlay
    ) -> some View {
        modifier(OverlayCover(isPresented: isPresented, alignment: alignment, canDismiss: canDismiss, overlay: content))
    }

    /// A ⋯ menu: a sheet of left-aligned rows from the bottom; `info` is what stands at its top.
    func actionMenu<Info: View>(isPresented: Binding<Bool>, actions: [SheetAction], @ViewBuilder info: @escaping () -> Info) -> some View {
        modifier(OverlayCover(isPresented: isPresented, alignment: .bottom, canDismiss: { !actions.contains { $0.busy } }) {
            ActionMenu(actions: actions, hasInfo: Info.self != EmptyView.self, info: info()) { isPresented.wrappedValue = false }
        })
    }

    /// The more menu without anything at its top.
    func actionMenu(isPresented: Binding<Bool>, actions: [SheetAction]) -> some View {
        actionMenu(isPresented: isPresented, actions: actions) { EmptyView() }
    }
}
