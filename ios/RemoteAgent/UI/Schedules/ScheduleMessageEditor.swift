import RACore
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
            Text(text).type(T.body, Theme.fg).lineLimit(expanded ? nil : 6)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background {
                    GeometryReader { proxy in
                        Color.clear.onAppear { clippedHeight = proxy.size.height }
                            .onChange(of: proxy.size.height) { _, height in if !expanded { clippedHeight = height } }
                    }
                }
                .background {
                    Text(text).type(T.body, Theme.fg).fixedSize(horizontal: false, vertical: true).hidden()
                        .background { GeometryReader { proxy in
                            Color.clear.onAppear { fullHeight = proxy.size.height }
                                .onChange(of: proxy.size.height) { _, height in fullHeight = height }
                        } }
                }
                .padding(.horizontal, bubble ? 12 : 0).padding(.vertical, bubble ? 8 : 0)
                .background {
                    if bubble {
                        UnevenRoundedRectangle(topLeadingRadius: 18, bottomLeadingRadius: 18, bottomTrailingRadius: 5, topTrailingRadius: 18).fill(Theme.soft)
                    }
                }
                .textSelection(.enabled)
            if expanded || fullHeight > clippedHeight + 1 {
                Button { expanded.toggle() } label: {
                    HStack(spacing: 4) {
                        Text((caption.map { $0 + " · " } ?? "") + (expanded ? "收起" : "展开全文")).type(T.meta, Theme.muted)
                        Arrow(Theme.muted, direction: expanded ? .up : .down)
                    }.frame(minHeight: 44)
                }.buttonStyle(PressStyle())
            } else if let caption { Text(caption).type(T.meta, Theme.muted).padding(.top, 4) }
        }
        .onChange(of: text) { _, _ in expanded = false }
        .debugSignal("schedule.description.toggle") { expanded.toggle() }
    }
}

struct ScheduleMessageEditor: View {
    let initial: String
    let enabled: Bool
    let onSave: (String) async throws -> Void
    let onClose: () -> Void
    @State private var text: String
    @FocusState private var focused: Bool
    @State private var discard = false
    @State private var saving = false
    @State private var error = ""
    init(initial: String, enabled: Bool, onSave: @escaping (String) async throws -> Void, onClose: @escaping () -> Void) {
        self.initial = initial; self.enabled = enabled; self.onSave = onSave; self.onClose = onClose
        _text = State(initialValue: initial)
    }
    private func save() {
        saving = true
        Task { do { if text != initial { try await onSave(text) }; onClose() }
            catch { self.error = errorText(error) }; saving = false }
    }
    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                TextEditor(text: $text).type(T.body, Theme.fg)
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
                    .scrollContentBackground(.hidden).padding(12).focused($focused).disabled(saving || !enabled)
                    .accessibilityLabel("让 agent 做什么")
                if text.utf8.count > scheduleMessageLimit { Hint("太长了，最多 32 KB", color: Theme.red).padding(.horizontal, 16) }
                if !error.isEmpty { Banner(error, tone: .error).padding(16) }
            }
            .background(Theme.card)
            .navigationTitle("让 agent 做什么").navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    TextAction("取消", enabled: !saving) { if text == initial { onClose() } else { discard = true } }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    TextAction(saving ? "保存中…" : "保存", enabled: enabled && !saving && scheduleMessageValid(text)) {
                        save()
                    }
                }
            }
        }
        .task {
            #if DEBUG
            if HomeLaunch.value("-scheduleEditorKeyboard") == "yes" { focused = true }
            #endif
        }
        .debugSignal("schedule.message.change") {
            #if DEBUG
            if let replacement = HomeLaunch.value("-scheduleEditedMessage") { text = replacement }
            #endif
        }
        .debugSignal("schedule.message.cancel") { if text == initial { onClose() } else { discard = true } }
        .debugSignal("schedule.message.save") {
            #if DEBUG
            if let replacement = HomeLaunch.value("-scheduleEditedMessage") { text = replacement }
            if enabled && !saving && scheduleMessageValid(text) { save() }
            #endif
        }
        .interactiveDismissDisabled(text != initial || saving)
        .confirm(isPresented: $discard, title: "放弃修改？", text: "未保存的修改会丢失。", confirm: "放弃修改", danger: true, onConfirm: onClose)
    }
}
