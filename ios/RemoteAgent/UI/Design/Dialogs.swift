import RACore
// Dialogs and the action menu (docs/design-system.md §6). The design gives
// them their own look (a card of r-lg, the title in the text colour, 取消 at
// the left and the action at the right, the one place a solid red button
// appears), so they are drawn here and not the system's alert; what the
// system does for its own is kept: they sit above everything, sheets
// included, VoiceOver stays inside them, the escape gesture cancels, a
// confirmation ticks, and with reduced motion they only fade.
import SwiftUI

/// Presents `overlay` over the whole window on a scrim, without the system's
/// slide from the bottom: what the dialogs here stand on, and what a
/// screen's own dialog should (`overlayCover`).
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
            Color.black.opacity(0.35).ignoresSafeArea()
                .opacity(shown ? 1 : 0)
                .contentShape(Rectangle())
                .onTapGesture { if canDismiss() { isPresented = false } }
                .accessibilityHidden(true)
            overlay()
                // Whatever page it is over, a dialog's own surface is the card.
                .opacity(shown ? 1 : 0)
                .scaleEffect(shown || reduceMotion || alignment != .center ? 1 : 0.94)
                .offset(y: shown || reduceMotion || alignment == .center ? 0 : 24)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: alignment)
        .accessibilityAddTraits(.isModal)
        .accessibilityAction(.escape) { if canDismiss() { isPresented = false } }
        .tint(DS.blue)
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

/// The card of a dialog: radius 30, at most 320 wide, 30 pt from the sides, with the shadow of what floats; it scrolls when text at a large size outgrows the screen.
struct DialogCard<Content: View>: View {
    private let content: Content

    init(@ViewBuilder content: () -> Content) { self.content = content() }

    var body: some View {
        let card = VStack(spacing: 0) { content }
            .padding(.init(top: 18, leading: 16, bottom: 12, trailing: 16))
            .frame(maxWidth: 320)
            .background(DS.raised, in: RoundedRectangle(cornerRadius: DS.Radius.darkCard, style: .continuous))
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
/// (中断, 结束会话…). Not `enabled`: greyed and not tappable. `note`: a
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
            Text(title).font(.headline).ink(DS.ink).multilineTextAlignment(.center).padding(.bottom, 12)
                .accessibilityAddTraits(.isHeader)
            // The keyboard comes up with the dialog: there is nothing else to do in it.
            InputField(
                text: $value, placeholder: placeholder, lines: lines, focusOnAppear: true,
                onSubmit: lines.upperBound == 1 ? { if !running { submit() } } : nil
            )
            .onChange(of: value) { _, new in typed(new) }
            .padding(.bottom, 8)
            if !runner.error.isEmpty {
                Text(L10n.commonIosSaveFailed(reason: runner.error)).font(.subheadline).ink(DS.red).multilineTextAlignment(.center).padding(.bottom, 8)
            } else if let text {
                Text(text).font(.subheadline).ink(DS.ink2).multilineTextAlignment(.center).padding(.bottom, 8)
            }
            Btns {
                Btn(dismissLabel, enabled: !running, action: onDismiss)
                Btn(running ? L10n.commonIosSaving() : confirm, kind: .primary, enabled: !running, action: submit)
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
                Text(title).font(.headline).ink(DS.ink).multilineTextAlignment(.center).padding(.bottom, 6)
                    .accessibilityAddTraits(.isHeader)
                Text(text).font(.subheadline).ink(DS.ink2).multilineTextAlignment(.center).padding(.bottom, 14)
                Btns {
                    Btn(L10n.commonCancel()) { isPresented.wrappedValue = false }
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
    func inputAlert(isPresented: Binding<Bool>, title: String, initial: String = "", placeholder: String, confirm: String, dismiss: String = L10n.commonCancel(), text: String? = nil, limit: Int = 40, onConfirm: @escaping (String) -> Void) -> some View {
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
        isPresented: Binding<Bool>, title: String, initial: String = "", placeholder: String, confirm: String = L10n.commonSave(),
        dismiss: String = L10n.commonCancel(), text: String? = nil, limit: Int = 40, lines: ClosedRange<Int> = 1...4,
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

    /// A ⋯ menu of `actions` as the system's bottom sheet (`menuSheet`), `info`
    /// at the top: the rows in one group, and what ends or removes in a red
    /// group of its own under them.
    func actionMenu<Info: View>(isPresented: Binding<Bool>, actions: [SheetAction], @ViewBuilder info: @escaping () -> Info) -> some View {
        let ending = actions.filter { $0.danger && $0.value == nil }
        let rest = actions.filter { !($0.danger && $0.value == nil) }
        return menuSheet(isPresented: isPresented, groups: [rest, ending], info: info)
    }

    func actionMenu(isPresented: Binding<Bool>, actions: [SheetAction]) -> some View {
        actionMenu(isPresented: isPresented, actions: actions) { EmptyView() }
    }
}
