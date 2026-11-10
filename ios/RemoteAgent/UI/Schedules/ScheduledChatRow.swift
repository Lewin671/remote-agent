import RACore
import SwiftUI
import RAMarkdown
import RAMarkdownUI

struct ScheduledChatRow: View {
    let row: ChatRow
    let env: ChatEnv
    @State private var expanded = false
    var body: some View {
        if row.folded.isEmpty { ChatRowView(row: row, env: env).equatable() }
        else {
            VStack(spacing: 0) {
                if case .note(let note) = row.item {
                    Button { expanded.toggle() } label: {
                        HStack(spacing: 6) {
                            Text(note.text).font(.footnote).ink(DS.ink2).multilineTextAlignment(.leading)
                            Arrow(DS.ink2, direction: expanded ? .up : .down)
                        }.frame(maxWidth: .infinity, minHeight: 44, alignment: .leading).padding(.horizontal, 16)
                    }.buttonStyle(PressStyle()).accessibilityValue(expanded ? "已展开" : "已折叠")
                }
                if expanded { ForEach(row.folded) { child in ChatRowView(row: child, env: env).equatable().id(child.id) } }
            }
        }
    }
}
struct SessionScheduleBar: View {
    let session: SessionView
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var picking = false
    var body: some View {
        let tasks = sessionSchedules(context.schedules.tasks, session: session)
        if !tasks.isEmpty {
            // A strip of glass under the session's title (design-system §6 「提示条」).
            GlassStrip(text: scheduleBarText(tasks, online: context.schedules.online(session.machineId)), symbol: "clock", action: "管理") {
                if let task = tasks.first, tasks.count == 1 { open(task) } else { picking = true }
            }
                .kitSheet(isPresented: $picking) {
                    SheetHead("管理定时任务")
                    ForEach(tasks) { entry in ListItem(entry.task.spec.title, desc: entry.task.stateText(online: context.schedules.online(entry.machineID))) { picking = false; open(entry) } }
                }
        }
    }
    private func open(_ entry: MachineSchedule) { nav.open(.schedule(machine: entry.machineID, id: entry.task.id)) }
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
    let run: ScheduleRun
    let unread: Bool
    let showConversation: Bool
    let open: () -> Void
    @Environment(AccountContext.self) private var context
    @Environment(\.horizontalSizeClass) private var sizeClass
    @State private var history = SessionHistory()
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 6) {
                if unread { Circle().fill(DS.blue).frame(width: 8, height: 8).accessibilityLabel("进入时未读") }
                Text(scheduleMoment(run.notificationTime)).font(.footnote).ink(DS.ink2)
                Spacer(minLength: 0)
                if showConversation, !(run.sessionID ?? "").isEmpty { TextAction("对话", arrow: true, action: open) }
            }
            if let title = run.resultTitle {
                Text(title).font(.body.weight(.semibold)).ink(DS.ink).frame(maxWidth: .infinity, alignment: .leading)
            }
            let full = scheduleFinalAnswer(history.events.events, runID: run.id)
            let fallback = (run.finalSummary?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false ? run.finalSummary : nil) ?? scheduleReason(run.reason)
            ScheduleResultBody(text: full ?? fallback, folds: sizeClass != .regular)
        }
        .padding(16).background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group))
        .padding(.horizontal, 16).padding(.top, 16)
        .task(id: run.sessionID) {
            guard let sid = run.sessionID, !sid.isEmpty else { return }
            if context.remote.state.value.sessions[sid] == nil { _ = try? await context.remote.fetchSession(sid) }
            await history.follow(context.remote, sid)
        }
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
                        Text("展开全文").font(.subheadline).ink(DS.blue)
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
                        .onGeometryChange(for: CGFloat.self) { g in ChatDiagnosticRing.shared.measure(.scheduleBlock) { g.size.height } } action: {
                            ChatDiagnosticRing.shared.record(.scheduleBlock, Double(index), Double($0)); heights[index] = $0
                        }
                }
            }.hidden().allowsHitTesting(false).accessibilityHidden(true)
        }

    }
}
