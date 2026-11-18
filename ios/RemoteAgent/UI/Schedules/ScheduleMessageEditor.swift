import RACore
import RAProto
import SwiftUI

/// Measures the unabridged text at the same width and Dynamic Type size.
struct ScheduleMessageText: View {
    let text: String
    var caption: String? = nil
    var bubble = false
    @State private var expanded = false
    @State private var fullHeight: CGFloat = 0
    @State private var clippedHeight: CGFloat = 0

    var body: some View {
        VStack(alignment: caption == nil ? .leading : .trailing, spacing: 0) {
            Text(text).font(.body).ink(DS.ink).lineLimit(expanded ? nil : 6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background {
                    GeometryReader { proxy in
                        Color.clear.onAppear { clippedHeight = proxy.size.height }
                            .onChange(of: proxy.size.height) { _, height in if !expanded { clippedHeight = height } }
                    }
                }
                .background {
                    Text(text).font(.body).ink(DS.ink).fixedSize(horizontal: false, vertical: true).hidden()
                        .background {
                            GeometryReader { proxy in
                                Color.clear.onAppear { fullHeight = proxy.size.height }
                                    .onChange(of: proxy.size.height) { _, height in fullHeight = height }
                            }
                        }
                }
                .padding(.horizontal, bubble ? 12 : 0).padding(.vertical, bubble ? 8 : 0)
                .background {
                    if bubble {
                        UnevenRoundedRectangle(
                            topLeadingRadius: 18,
                            bottomLeadingRadius: 18,
                            bottomTrailingRadius: 5,
                            topTrailingRadius: 18
                        ).fill(DS.gbg)
                    }
                }
                .textSelection(.enabled)
            if expanded || fullHeight > clippedHeight + 1 {
                Button { expanded.toggle() } label: {
                    HStack(spacing: 4) {
                        Text((caption.map { $0 + " · " } ?? "") + (expanded ? L10n.markdownCollapse() : L10n.markdownExpand()))
                            .font(.footnote).ink(DS.ink2)
                        Arrow(DS.ink2, direction: expanded ? .up : .down)
                    }.frame(minHeight: 44)
                }.buttonStyle(PressStyle())
            } else if let caption {
                Text(caption).font(.footnote).ink(DS.ink2).padding(.top, 4)
            }
        }
        .onChange(of: text) { _, _ in expanded = false }
        .debugSignal("schedule.description.toggle") { expanded.toggle() }
    }
}

struct ScheduleMessageEditor: View {
    let onClose: () -> Void
    @Environment(AccountContext.self) private var context

    var body: some View {
        let vm = context.core.scheduleForm
        ScheduleMessageEditorContent(
            vm: vm,
            onSetMessage: { text in
                context.dispatch {
                    $0.scheduleFormSetMessage = .with {
                        $0.formKey = vm.formKey
                        $0.value = text
                    }
                }
            },
            onSave: {
                context.dispatch {
                    $0.scheduleFormSubmit = .with {
                        $0.formKey = vm.formKey
                        $0.value = false
                    }
                }
            },
            onRetryMutation: {
                context.dispatch { $0.scheduleRetryMutation = .with { $0.formKey = vm.formKey } }
            },
            onAbandonMutation: {
                context.dispatch { $0.scheduleAbandonMutation = .with { $0.formKey = vm.formKey } }
            },
            onClose: {
                context.dispatch {
                    $0.scheduleFormClose = .with {
                        $0.formKey = vm.formKey
                        $0.value = true
                    }
                }
                onClose()
            }
        )
    }
}

struct ScheduleMessageEditorContent: View {
    let vm: PBScheduleFormViewModel
    let onSetMessage: (String) -> Void
    let onSave: () -> Void
    let onRetryMutation: () -> Void
    let onAbandonMutation: () -> Void
    let onClose: () -> Void

    @State private var text = ""
    @State private var initialMessage = ""
    @State private var capturedFormKey = ""
    @FocusState private var focused: Bool
    @State private var discard = false

    var body: some View {
        let busy = vm.mutation.busy
        NavigationStack {
            VStack(alignment: .leading, spacing: 0) {
                ScheduleMutationBanner(
                    mutation: vm.mutation,
                    onRetry: onRetryMutation,
                    onAbandon: onAbandonMutation
                )
                .padding(.horizontal, 16).padding(.top, 8)
                TextEditor(text: $text)
                    .font(.body).ink(DS.ink).scrollContentBackground(.hidden)
                    .padding(.horizontal, 12).padding(.top, 8).focused($focused)
                    .disabled(busy || !vm.editable)
                    .onChange(of: text) { _, new in
                        if new != vm.message { onSetMessage(new) }
                    }
                if vm.messageTooLong && vm.hasMessageError {
                    Hint(vm.messageError.rendered()).padding(.horizontal, 16).padding(.bottom, 16)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(DS.paper.ignoresSafeArea())
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button(L10n.commonCancel()) { cancel() }
                        .tint(DS.ink)
                        .disabled(busy)
                }
                ToolbarItem(placement: .principal) {
                    Text(L10n.scheduleWhatShouldTheAgentDo()).font(.headline).ink(DS.ink)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    let canSave = vm.editable && vm.canSubmit && !busy
                    Button(busy ? L10n.commonIosSaving() : L10n.commonSave()) { onSave() }
                        .tint(DS.blue)
                        .disabled(!canSave)
                }
            }
            .confirm(
                isPresented: $discard,
                title: L10n.scheduleDiscardChanges(),
                text: L10n.scheduleIosUnsavedChangesWillBeLost(),
                confirm: L10n.newSessionIosAbandon(),
                danger: true
            ) {
                onClose()
            }
            .onChange(of: vm.formKey, initial: true) { _, key in
                if !key.isEmpty && key != capturedFormKey {
                    capturedFormKey = key
                    initialMessage = vm.message
                    text = vm.message
                }
            }
            .onAppear { focused = true }
        }
    }

    private func cancel() {
        if text != initialMessage {
            discard = true
        } else {
            onClose()
        }
    }
}
