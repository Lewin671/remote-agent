import RACore
import RAProto
import RAMarkdown
import RAMarkdownUI
import SwiftUI

struct SessionScheduleBar: View {
    let sessionID: String
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var watchToken: UUID?

    var body: some View {
        let vm = context.core.scheduleStrips[sessionID] ?? PBScheduleSessionStripViewModel()
        SessionScheduleStripContent(vm: vm) { task in
            nav.open(.schedule(machine: task.machineID, id: task.taskID))
        }
        .task(id: sessionID) {
            if let prev = watchToken {
                watchToken = nil
                await context.coreHost.releaseScheduleStripWatch(prev)
            }
            watchToken = await context.coreHost.acquireScheduleStripWatch(sessionID)
        }
        .onDisappear {
            let token = watchToken
            watchToken = nil
            Task { await context.coreHost.releaseScheduleStripWatch(token) }
        }
    }
}

struct SessionScheduleStripContent: View {
    let vm: PBScheduleSessionStripViewModel
    let onOpenTask: (PBScheduleSessionStripTaskView) -> Void
    @State private var picking = false

    var body: some View {
        if vm.visible && !vm.tasks.isEmpty {
            // A strip of glass under the session's title (design-system §6 「提示条」).
            GlassStrip(text: vm.summaryText.rendered(), symbol: "clock", action: L10n.scheduleManage()) {
                if let only = vm.tasks.first, vm.tasks.count == 1 {
                    onOpenTask(only)
                } else {
                    picking = true
                }
            }
            .kitSheet(isPresented: $picking) {
                SheetHead(L10n.scheduleIosManageScheduledTask())
                ForEach(vm.tasks, id: \.stripID) { task in
                    let desc = [task.timingText.rendered(), task.nextRunText.rendered()]
                        .filter { !$0.isEmpty }
                        .joined(separator: " · ")
                    ListItem(task.title, desc: desc) {
                        picking = false
                        onOpenTask(task)
                    }
                }
            }
        }
    }
}

private extension PBScheduleSessionStripTaskView {
    var stripID: String { "\(machineID)/\(taskID)" }
}

/// Keep whole blocks within roughly eight body lines, always showing the first.
/// An oversized first block is clipped by the view until expanded.
func scheduleResultBlockCount(_ heights: [CGFloat], budget: CGFloat) -> Int {
    var used: CGFloat = 0
    for (index, height) in heights.enumerated() {
        used += height + (index == 0 ? 0 : 16)
        if used > budget { return max(1, index) }
    }
    return heights.count
}

struct ScheduleResultCard: View {
    let card: PBScheduleResultCardView
    let open: () -> Void
    @Environment(\.horizontalSizeClass) private var sizeClass

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 6) {
                if card.unreadOnEntry {
                    Circle().fill(DS.blue).frame(width: 8, height: 8)
                        .accessibilityLabel(L10n.scheduleUnreadOnEntry())
                }
                Text(card.timeText.rendered()).font(.footnote).ink(DS.ink2)
                Spacer(minLength: 0)
                if card.showConversationAction && !card.sessionID.isEmpty {
                    TextAction(L10n.scheduleConversation(), arrow: true, action: open)
                }
            }
            if !card.resultTitle.isEmpty {
                Text(card.resultTitle).font(.body.weight(.semibold)).ink(DS.ink)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            if card.resultLoading {
                LoadingRow(L10n.scheduleReadingTasks())
            } else {
                let text = card.markdownBody.isEmpty ? card.fallbackReasonText.rendered() : card.markdownBody
                if !text.isEmpty {
                    ScheduleResultBody(text: text, folds: sizeClass != .regular)
                }
            }
        }
        .padding(16).background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group))
        .padding(.horizontal, 16).padding(.top, 16)
    }
}

struct ScheduleResultBody: View {
    let text: String
    let folds: Bool
    @State private var expanded = false
    @State private var heights: [Int: CGFloat] = [:]
    @ScaledMetric(relativeTo: .body) private var lineHeight: CGFloat = 24

    var body: some View {
        let blocks = Markdown.parse(text)
        let measured = (0..<blocks.count).map { heights[$0] ?? lineHeight * 8 + 1 }
        let count = !folds || expanded ? blocks.count : scheduleResultBlockCount(measured, budget: lineHeight * 8)
        let clippedFirst = folds && !expanded && count == 1 && measured.first.map { $0 > lineHeight * 8 } == true
        VStack(alignment: .leading, spacing: 0) {
            MarkdownView(blocks: Array(blocks.prefix(count)))
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxHeight: clippedFirst ? lineHeight * 8 : nil, alignment: .top)
                .clipped()
            if folds && (count < blocks.count || clippedFirst) {
                Button { expanded = true } label: {
                    HStack(spacing: 4) {
                        Text(L10n.markdownExpand()).font(.subheadline).ink(DS.blue)
                        Arrow(DS.blue, direction: .down)
                    }.frame(minHeight: 44)
                }.buttonStyle(PressStyle())
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(alignment: .topLeading) {
            VStack(spacing: 0) {
                ForEach(Array(blocks.enumerated()), id: \.offset) { index, block in
                    MarkdownView(blocks: [block], selectable: false)
                        .fixedSize(horizontal: false, vertical: true)
                        .onGeometryChange(for: CGFloat.self) { g in
                            ChatDiagnosticRing.shared.measure(.scheduleBlock) { g.size.height }
                        } action: {
                            ChatDiagnosticRing.shared.record(.scheduleBlock, Double(index), Double($0))
                            heights[index] = $0
                        }
                }
            }
            .hidden()
            .allowsHitTesting(false)
            .accessibilityHidden(true)
        }
    }
}
