import RACore
import SwiftUI

struct SchedulesScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @AppStorage("schedules.endedExpanded") private var endedExpanded = false
    var body: some View {
        let store = context.schedules
        Page {
            VStack(spacing: 0) {
                HStack(spacing: 8) {
                    Text("定时任务").type(T.title30, Theme.fg).padding(.vertical, 3).frame(maxWidth: .infinity, alignment: .leading)
                    IconButton(Lucide.plus, label: "新任务") { nav.open(.newSchedule()) }
                }.padding(.init(top: 4, leading: 16, bottom: 2, trailing: 12))
                ConnectionBar()
                ScrollView {
                    VStack(spacing: 0) {
                        if model.state.conn == .online,
                           let offline = scheduleOfflineNotice(store.tasks, state: model.state, online: store.online) {
                            Banner(offline).padding(16)
                        }
                        ForEach(store.errors.keys.sorted(), id: \.self) { id in
                            Banner(store.errors[id] ?? "", tone: .error, action: "重试") { Task { await store.refresh(id) } }.padding(16)
                        }
                        if store.tasks.isEmpty {
                            VStack(spacing: 8) {
                                Text(store.loading.isEmpty ? "还没有定时任务" : "读取任务…").type(T.h17, Theme.fg)
                                Text(scheduleEmpty).type(T.sm, Theme.muted).multilineTextAlignment(.center)
                            }.padding(.horizontal, 24).padding(.vertical, 64)
                        }
                        Ticking { now in
                            ForEach(ScheduleGroup.allCases, id: \.rawValue) { group in
                                let rows = sortedSchedules(store.tasks, group: group)
                                if !rows.isEmpty {
                                    if group == .ended {
                                        Button { endedExpanded.toggle() } label: {
                                            HStack(spacing: 8) {
                                                Text("已结束 \(rows.count)").type(T.smB, Theme.muted)
                                                Arrow(Theme.muted, direction: endedExpanded ? .up : .down)
                                                Spacer()
                                            }.frame(minHeight: 44).padding(.horizontal, 16)
                                        }.buttonStyle(PressStyle()).accessibilityValue(endedExpanded ? "已展开" : "已折叠")
                                    } else { SectionLabel(group.title, count: rows.count) }
                                    if group != .ended || endedExpanded {
                                        ForEach(Array(rows.enumerated()), id: \.element.id) { index, entry in
                                            ScheduleListRow(entry: entry, now: now, last: index == rows.count - 1)
                                        }
                                    }
                                }
                            }
                        }
                    }.readableColumn()
                }
            }
        }
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
        let date = Date(timeIntervalSince1970: Double(now) / 1000)
        let right = task.latestNotification.map { whenText(Int64($0.notificationTime.timeIntervalSince1970 * 1000), now: now) } ?? ""
        let status = online ? task.stateText(online: true, now: date) : scheduleOfflineRow
        let metadata = scheduleLocation(task, state: model.state, machine: entry.machineID, frequency: true) + " · " + status
        let unread = context.schedules.unreadCount(entry)
        Button { nav.open(.schedule(machine: entry.machineID, id: task.id)) } label: {
            SessionListRowContent(title: task.spec.title, right: right, third: metadata,
                                  color: !online ? Theme.gray : task.group == .attention ? Theme.orange : task.group == .active ? (task.active != nil ? Theme.blue : Theme.green) : Theme.gray, last: last,
                                  titleStyle: unread > 0 ? T.bodyB : T.body.with(weight: .medium), singleLinePreview: true) {
                HStack(spacing: 8) {
                    Text(task.notificationPreview(now: date)).type(T.sm, unread > 0 ? Theme.fg : Theme.muted)
                        .lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                    if unread > 0 { ScheduleUnreadBadge(count: unread) }
                }
            }
        }.buttonStyle(RowPressStyle()).accessibilityElement(children: .combine)
    }
}

struct ScheduleUnreadBadge: View {
    let count: Int
    var text: String { count > 99 ? "99+" : String(count) }
    var body: some View {
        Text(text).type(T.meta.with(weight: .bold), Theme.onSolid)
            .padding(.horizontal, 8).padding(.vertical, 2)
            .background(Theme.blueBg, in: Capsule()).fixedSize()
            .accessibilityLabel("\(count) 条未读通知")
    }
}
func scheduleLocation(_ task: ScheduleTask, state: RemoteState, machine: String, frequency: Bool = false) -> String {
    let target = task.spec.target
    let place: String
    if target.type == "resume_session" {
        if let sid = target.session, let session = state.sessions[sid], session.info != nil {
            place = "在「" + titleOf(session) + "」里" + (frequency ? " · " + agentName(session.agent) : "")
        } else { place = "在对话里" }
    } else { place = projectName(target.cwd ?? "") + " · " + (target.agent == "claude" ? "Claude Code" : "Codex") }
    return (frequency ? scheduleFrequency(task.spec.trigger, includeZone: true) + " · " : "") + place + (state.machines.count > 1 ? " · " + machineLabel(state, id: machine) : "")
}

func scheduleOfflineNotice(_ entries: [MachineSchedule], state: RemoteState, online: (String) -> Bool) -> String? {
    let ids = Set(entries.filter { !online($0.machineID) }.map(\.machineID)).sorted()
    guard !ids.isEmpty else { return nil }
    return ids.map { machineLabel(state, id: $0) }.joined(separator: "、") + " " + scheduleOffline
}
