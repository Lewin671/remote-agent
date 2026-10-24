import RACore
import SwiftUI

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
                            Text(note.text).type(T.meta, Theme.muted).multilineTextAlignment(.leading)
                            Arrow(Theme.muted, direction: expanded ? .up : .down)
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
            HStack(spacing: 8) {
                Ic(Lucide.clock, style: T.meta, color: Theme.muted)
                Text(scheduleBarText(tasks, online: context.schedules.online(session.machineId))).type(T.meta, Theme.muted).lineLimit(2)
                Spacer(minLength: 0)
                TextAction("管理", arrow: true) {
                    if let task = tasks.first, tasks.count == 1 { open(task) } else { picking = true }
                }.fixedSize()
            }.padding(.horizontal, 16).background(Theme.soft)
                .kitSheet(isPresented: $picking) {
                    SheetHead("管理定时任务")
                    ForEach(tasks) { entry in ListItem(entry.task.spec.title, desc: entry.task.stateText(online: context.schedules.online(entry.machineID))) { picking = false; open(entry) } }
                }
        }
    }
    private func open(_ entry: MachineSchedule) { nav.open(.schedule(machine: entry.machineID, id: entry.task.id)) }
}
