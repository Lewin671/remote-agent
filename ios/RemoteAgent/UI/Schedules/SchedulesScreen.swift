import RACore
import SwiftUI

struct SchedulesScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    var body: some View {
        let store = context.schedules
        Page {
            VStack(spacing: 0) {
                Text("定时任务").type(T.title30, Theme.fg).frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, 16).padding(.vertical, 8)
                ConnectionBar()
                ScrollView {
                    VStack(spacing: 0) {
                        if model.state.conn == .online && store.tasks.contains(where: { !store.online($0.machineID) }) {
                            Banner(scheduleOffline).padding(16)
                        }
                        ForEach(store.errors.keys.sorted(), id: \.self) { id in
                            Banner(store.errors[id] ?? "", tone: .error, action: "重试") { Task { await store.refresh(id) } }.padding(16)
                        }
                        if store.tasks.isEmpty {
                            VStack(spacing: 8) {
                                Ic(Lucide.clock, size: 32, color: Theme.muted)
                                Text(store.loading.isEmpty ? "还没有定时任务" : "读取任务…").type(T.h17, Theme.fg)
                                Text(scheduleEmpty).type(T.sm, Theme.muted).multilineTextAlignment(.center)
                            }.padding(.horizontal, 24).padding(.vertical, 64)
                        }
                        Ticking { now in
                            ForEach(ScheduleGroup.allCases, id: \.rawValue) { group in
                                let rows = sortedSchedules(store.tasks, group: group)
                                if !rows.isEmpty {
                                    SectionLabel(group.title, count: rows.count)
                                    ForEach(Array(rows.enumerated()), id: \.element.id) { index, entry in
                                        ScheduleListRow(entry: entry, now: now, last: index == rows.count - 1)
                                    }
                                }
                            }
                        }
                        Button { nav.open(.newSchedule()) } label: {
                            IconText(Lucide.plus, "新任务…", color: Theme.blue, style: T.body).frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                        }.buttonStyle(PressStyle()).padding(.horizontal, 16)
                    }.readableColumn()
                }
            }
        }
        .onAppear { store.setListOpen(true) }
        .onDisappear { store.setListOpen(false) }
    }
}
struct ScheduleListRow: View {
    let entry: MachineSchedule
    let now: Int64
    let last: Bool
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    var body: some View {
        let task = entry.task
        let online = context.schedules.online(entry.machineID)
        let right = task.stateText(online: online, now: Date(timeIntervalSince1970: Double(now) / 1000))
        let subtitle = scheduleLocation(task, state: model.state, machine: entry.machineID, frequency: true)
        let result = task.lastResult(now: Date(timeIntervalSince1970: Double(now) / 1000))
        Button { nav.open(.schedule(machine: entry.machineID, id: task.id)) } label: {
            SessionListRowContent(title: task.spec.title, right: right, third: result,
                                  color: !online ? Theme.gray : task.group == .attention ? Theme.orange : task.group == .active ? (task.active != nil ? Theme.blue : Theme.green) : Theme.gray, last: last) {
                Text(subtitle).type(T.sm, Theme.muted)
            }
        }.buttonStyle(RowPressStyle()).accessibilityElement(children: .combine)
    }
}
func scheduleLocation(_ task: ScheduleTask, state: RemoteState, machine: String, frequency: Bool = false) -> String {
    let target = task.spec.target
    let place: String
    if target.type == "resume_session" {
        if let sid = target.session, let session = state.sessions[sid], session.info != nil {
            place = "在「" + titleOf(session) + "」里"
        } else { place = "在对话里" }
    } else { place = projectName(target.cwd ?? "") + " · " + (target.agent == "claude" ? "Claude Code" : "Codex") }
    return (frequency ? scheduleFrequency(task.spec.trigger, includeZone: true) + " · " : "") + place + (state.machines.count > 1 ? " · " + machineLabel(state, id: machine) : "")
}
