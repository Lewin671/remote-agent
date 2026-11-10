import RACore
import SwiftUI

struct SchedulesScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @AppStorage("schedules.endedExpanded") private var endedExpanded = false
    var body: some View {
        let store = context.schedules
        Page {
            VStack(spacing: 0) {
                // 新定时 is the ink button of the bottom bar (UI/Shell); the page has no second way to it.
                Text("定时任务").font(.largeTitle.bold()).ink(DS.ink).padding(.vertical, 3).frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.init(top: 4, leading: 16, bottom: 2, trailing: 12))
                ConnectionBar()
                Ticking { now in
                    List {
                        if model.state.conn == .online,
                           let offline = scheduleOfflineNotice(store.tasks, state: model.state, online: store.online) {
                            Banner(offline).padding(16)
                                .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(DS.gbg)
                        }
                        ForEach(store.errors.keys.sorted(), id: \.self) { id in
                            Banner(store.errors[id] ?? "", tone: .error, action: "重试") { Task { await store.refresh(id) } }.padding(16)
                        }
                        .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(DS.gbg)
                        if store.tasks.isEmpty {
                            VStack(spacing: 8) {
                                Text(store.loading.isEmpty ? "还没有定时任务" : "读取任务…").font(.headline).ink(DS.ink)
                                Text(scheduleEmpty).font(.subheadline).ink(DS.ink2).multilineTextAlignment(.center)
                            }.frame(maxWidth: .infinity).padding(.horizontal, 24).padding(.vertical, 64)
                                .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(DS.gbg)
                        }
                        ForEach(ScheduleInboxGroup.allCases, id: \.rawValue) { group in
                            let rows = store.rows(group)
                            if !rows.isEmpty {
                                if group == .ended {
                                    Button { endedExpanded.toggle() } label: {
                                        HStack(spacing: 8) {
                                            Text("已结束 \(rows.count)").font(.subheadline.weight(.semibold)).ink(DS.ink2)
                                            Spacer()
                                            Arrow(DS.ink2, direction: endedExpanded ? .up : .down)
                                        }.frame(minHeight: 44).padding(.horizontal, 16)
                                    }.buttonStyle(PressStyle()).accessibilityValue(endedExpanded ? "已展开" : "已折叠")
                                } else {
                                    HStack {
                                        Text(group.title).font(.subheadline.weight(.semibold)).ink(group == .attention ? DS.orangeInk : DS.ink2).accessibilityAddTraits(.isHeader)
                                        Spacer()
                                        if group == .attention { Text("\(rows.count)").font(.subheadline).ink(DS.orangeInk) }
                                        else if rows.contains(where: store.hasNewResult) {
                                            TextAction("全部标为已读") { store.markRead(rows) }
                                        }
                                    }.frame(minHeight: 44).padding(.horizontal, 16)
                                }
                                if group != .ended || endedExpanded {
                                    ForEach(Array(rows.enumerated()), id: \.element.id) { index, entry in
                                        ScheduleListRow(entry: entry, now: now, last: index == rows.count - 1)
                                    }
                                }
                            }
                        }
                        .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(DS.gbg)
                    }
                    .listStyle(.plain).scrollContentBackground(.hidden)
                    .environment(\.defaultMinListRowHeight, 0)
                    .readableColumn()
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
        let store = context.schedules
        let date = Date(timeIntervalSince1970: Double(now) / 1000)
        let attention = task.group == .attention
        let unread = store.hasNewResult(entry)
        let resultDate = attention ? task.stoppedAt : task.latestNotification?.notificationTime
        let action = unread ? "标为已读" : "标为未读"
        let canMark = task.latestNotification != nil && (!attention || unread)
        Button { nav.open(.schedule(machine: entry.machineID, id: task.id)) } label: {
            HStack(alignment: .top, spacing: 0) {
                ZStack {
                    if attention || unread {
                        Circle().fill(attention ? DS.orange : DS.blue).frame(width: 8, height: 8)
                            .accessibilityLabel(attention ? "需要你处理" : "有新结果")
                    }
                }.frame(width: 22, height: 20)
                VStack(alignment: .leading, spacing: 4) {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(task.spec.title).font(attention || unread ? .body.weight(.semibold) : .body.weight(.medium)).ink(task.group == .paused ? DS.ink2 : DS.ink)
                            .lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                        if let resultDate { Text(scheduleRowTime(resultDate, now: date)).font(.footnote).ink(DS.ink2).fixedSize() }
                    }
                    preview(task, unread: unread, now: date).lineLimit(2).frame(maxWidth: .infinity, alignment: .leading)
                    HStack(spacing: 4) {
                        if task.running != nil { SymbolImage(Mark.progress, size: 12, color: DS.ink2) }
                        Text(scheduleListMetadata(entry, entries: store.tasks, state: model.state, online: store.online(entry.machineID)))
                            .font(.footnote).ink(DS.ink2).lineLimit(1)
                    }
                }
            }
            .padding(.horizontal, 16).padding(.vertical, 12)
            .background(DS.gbg)
            .overlay(alignment: .bottom) { if !last { DS.sep.frame(height: 0.5).padding(.leading, 38) } }
            .contentShape(Rectangle())
        }
        .buttonStyle(RowPressStyle()).accessibilityElement(children: .combine)
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            if canMark {
                Button { toggleRead(unread) } label: {
                    if unread { Label("已读", systemImage: "checkmark") }
                    else { Text("未读") }
                }.tint(DS.blue)
            }
        }
        .accessibilityActions {
            if canMark { Button(action) { toggleRead(unread) } }
        }
    }
    private func toggleRead(_ unread: Bool) {
        if unread { context.schedules.markRead([entry]) } else { context.schedules.markUnread(entry) }
    }
    @ViewBuilder private func preview(_ task: ScheduleTask, unread: Bool, now: Date) -> some View {
        if task.awaitingFirstResult {
            Text(task.listPreview(now: now)).font(.subheadline).ink(DS.ink2)
        } else if task.group == .attention {
            (Text(task.attentionWord).foregroundColor(DS.orangeInk).fontWeight(.semibold) +
             Text(" · " + task.notificationPreview(now: now).replacingOccurrences(of: "，任务已停下", with: "")))
                .font(.subheadline).ink(DS.ink2)
        } else {
            Text(task.listPreview(now: now)).font(.subheadline).ink(unread ? DS.ink : DS.ink2)
        }
    }
}

func scheduleListMetadata(_ entry: MachineSchedule, entries: [MachineSchedule], state: RemoteState, online: Bool) -> String {
    func location(_ entry: MachineSchedule) -> (String, String) {
        let target = entry.task.spec.target
        if target.type == "resume_session" {
            guard let sid = target.session, let session = state.sessions[sid], let info = session.info else { return ("", "") }
            return (info.cwd, agentName(session.agent))
        }
        return (target.cwd ?? "", agentById(target.agent).name)
    }
    let task = entry.task, place = location(entry)
    let places = entries.map(location)
    var parts = [String]()
    if task.running != nil { parts.append("正在运行") }
    parts.append(scheduleListFrequency(task.spec.trigger))
    if !place.0.isEmpty && Set(places.map { $0.0 }.filter { !$0.isEmpty }).count > 1 { parts.append(projectName(place.0)) }
    if !place.1.isEmpty && Set(places.map { $0.1 }.filter { !$0.isEmpty }).count > 1 { parts.append(place.1) }
    if Set(entries.map(\.machineID)).count > 1 { parts.append(machineLabel(state, id: entry.machineID)) }
    if task.spec.target.type == "resume_session" {
        let loaded = task.spec.target.session.flatMap { state.sessions[$0]?.info } != nil
        parts.append(loaded ? "在「" + scheduleDestination(task, state: state) + "」里" : "在对话里")
    }
    if task.group == .paused { parts.append("已暂停") }
    if !online { parts.append(scheduleOfflineRow) }
    return parts.joined(separator: " · ")
}
func scheduleLocation(_ task: ScheduleTask, state: RemoteState, machine: String, frequency: Bool = false) -> String {
    let target = task.spec.target
    let place: String
    if target.type == "resume_session" {
        if let sid = target.session, let session = state.sessions[sid], session.info != nil {
            place = "在「" + titleOf(session) + "」里" + (frequency ? " · " + agentName(session.agent) : "")
        } else { place = "在对话里" }
    } else { place = projectName(target.cwd ?? "") + " · " + agentById(target.agent).name }
    return (frequency ? scheduleFrequency(task.spec.trigger, includeZone: true) + " · " : "") + place + (state.machines.count > 1 ? " · " + machineLabel(state, id: machine) : "")
}

func scheduleOfflineNotice(_ entries: [MachineSchedule], state: RemoteState, online: (String) -> Bool) -> String? {
    let ids = Set(entries.filter { !online($0.machineID) }.map(\.machineID)).sorted()
    guard !ids.isEmpty else { return nil }
    return ids.map { machineLabel(state, id: $0) }.joined(separator: "、") + " " + scheduleOffline
}
