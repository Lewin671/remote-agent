import RACore
import SwiftUI

/// 定时 (design-system §6 「定时列表」): a sentence for a title, the tasks
/// that stopped as orange cards, those with new results as blue ones, the
/// rest one a row, and the ended ones folded. 新定时 is the ink button of
/// the bottom bar (UI/Shell); the page has no second way to it.
struct SchedulesScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(\.sidebarCreate) private var create
    @AppStorage("schedules.endedExpanded") private var endedExpanded = false

    var body: some View {
        let store = context.schedules
        let stopped = store.rows(.attention)
        let recent = store.rows(.recent)
        let fresh = recent.filter(store.hasNewResult)
        let others = recent.filter { !store.hasNewResult($0) }
        let ended = store.rows(.ended)
        let headline = scheduleHeadline(
            stopped: stopped.count, fresh: fresh.count, visible: stopped.count + recent.count + ended.count, loading: !store.loading.isEmpty)
        Page {
            Ticking { now in
                List {
                    Group {
                        if let create {
                            HStack { Spacer(); SidebarCreateButton(create: create, schedules: true) }.padding(.horizontal, DS.Space.card)
                        }
                        ScheduleHeadlineView(headline: headline)
                        ConnectionBar()
                        if model.state.conn == .online,
                           let offline = scheduleOfflineNotice(store.tasks, state: model.state, online: store.online) {
                            Banner(offline).padding(.horizontal, DS.Space.l).padding(.vertical, 4)
                        }
                        ForEach(store.errors.keys.sorted(), id: \.self) { id in
                            Banner(store.errors[id] ?? "", tone: .error, action: "重试") { Task { await store.refresh(id) } }
                                .padding(.horizontal, DS.Space.l).padding(.vertical, 4)
                        }
                        if store.tasks.isEmpty && store.loading.isEmpty {
                            Text(scheduleEmpty).font(.body).foregroundStyle(DS.ink2)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .padding(.horizontal, DS.Space.page).padding(.top, 8)
                        }
                        ForEach(stopped) { ScheduleCard(entry: $0, now: now, attention: true) }
                        if !fresh.isEmpty {
                            ScheduleSectionTitle("新结果", action: fresh.count > 1 ? "全部标为已读" : nil) { store.markRead(fresh) }
                            ForEach(fresh) { ScheduleCard(entry: $0, now: now, attention: false) }
                        }
                        if !others.isEmpty {
                            ScheduleSectionTitle("其他任务")
                            ForEach(Array(others.enumerated()), id: \.element.id) { i, entry in
                                ScheduleListRow(entry: entry, now: now, last: i == others.count - 1)
                            }
                        }
                        if !ended.isEmpty {
                            Button { endedExpanded.toggle() } label: {
                                HStack(spacing: 6) {
                                    Text("已结束 \(ended.count)").font(.subheadline).foregroundStyle(DS.ink2)
                                    Image(systemName: endedExpanded ? "chevron.up" : "chevron.down")
                                        .font(.footnote.weight(.semibold)).foregroundStyle(DS.ink3)
                                    Spacer()
                                }
                                .frame(minHeight: DS.minHit).padding(.horizontal, DS.Space.page).padding(.top, 8)
                                .contentShape(Rectangle())
                            }
                            .buttonStyle(PressStyle())
                            .accessibilityValue(endedExpanded ? "已展开" : "已折叠")
                            if endedExpanded {
                                ForEach(Array(ended.enumerated()), id: \.element.id) { i, entry in
                                    ScheduleListRow(entry: entry, now: now, last: i == ended.count - 1)
                                }
                            }
                        }
                        // Room for the bottom bar that floats over the end of the list.
                        Color.clear.frame(height: 24)
                    }
                    .listRowInsets(EdgeInsets()).listRowSeparator(.hidden).listRowBackground(Color.clear)
                }
                .listStyle(.plain).scrollContentBackground(.hidden)
                .environment(\.defaultMinListRowHeight, 0)
                .readableColumn()
            }
        }
    }
}

/// The sentence at the top, 34 / 700 with the number of what stopped in orange, and the line under it.
private struct ScheduleHeadlineView: View {
    let headline: ScheduleHeadline

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            Group {
                if let n = headline.count {
                    Text("\(Text("\(n)").foregroundStyle(DS.orange)) 个任务停下了")
                } else {
                    Text(headline.title)
                }
            }
            .font(.largeTitle.bold()).foregroundStyle(DS.ink)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityAddTraits(.isHeader)
            if !headline.sub.isEmpty { Text(headline.sub).font(.body).foregroundStyle(DS.ink2) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, DS.Space.page).padding(.top, 6).padding(.bottom, 12)
    }
}

/// 新结果, 其他任务: 15 / 600, with a blue action at the right.
private struct ScheduleSectionTitle: View {
    let title: String
    var action: String?
    var onAction: () -> Void = {}

    init(_ title: String, action: String? = nil, onAction: @escaping () -> Void = {}) {
        self.title = title
        self.action = action
        self.onAction = onAction
    }

    var body: some View {
        HStack {
            Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink).accessibilityAddTraits(.isHeader)
            Spacer()
            if let action { LinkButton(action, action: onAction).font(.subheadline) }
        }
        .frame(minHeight: DS.minHit)
        .padding(.horizontal, DS.Space.page).padding(.top, 8)
    }
}

/// A task that stopped (orange) or has a new result (blue): a `gbg` card of radius 28.
private struct ScheduleCard: View {
    let entry: MachineSchedule
    let now: Int64
    let attention: Bool
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav

    var body: some View {
        let task = entry.task
        let store = context.schedules
        let date = Date(timeIntervalSince1970: Double(now) / 1000)
        let when = attention ? task.stoppedAt : task.latestNotification?.notificationTime
        Button { nav.open(.schedule(machine: entry.machineID, id: task.id)) } label: {
            VStack(alignment: .leading, spacing: 8) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    StatusMark(attention ? .waiting : .unread)
                    Text(task.spec.title).font(.body.weight(.semibold)).foregroundStyle(DS.ink).lineLimit(2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if let when { Text(scheduleRowTime(when, now: date)).font(.footnote).foregroundStyle(DS.ink2).fixedSize() }
                }
                if attention {
                    (Text(task.attentionWord).foregroundStyle(DS.orangeInk).fontWeight(.semibold)
                        + Text(" · " + task.notificationPreview(now: date, shortReason: true)).foregroundStyle(DS.ink2))
                        .font(.subheadline).lineLimit(2)
                    HStack { Spacer(); Text("去处理 ›").font(.subheadline.weight(.semibold)).foregroundStyle(DS.blue) }
                } else {
                    Text(task.listPreview(now: date)).font(.body).foregroundStyle(DS.ink).lineLimit(4)
                    HStack(alignment: .firstTextBaseline) {
                        Text(scheduleListMetadata(entry, entries: store.tasks, state: model.state, online: store.online(entry.machineID)))
                            .font(.footnote).foregroundStyle(DS.ink2).lineLimit(1)
                        Spacer(minLength: 8)
                        Text("看全文 ›").font(.subheadline.weight(.semibold)).foregroundStyle(DS.blue).fixedSize()
                    }
                }
            }
            .padding(.horizontal, 20).padding(.vertical, 18)
            .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.card, style: .continuous))
            .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .padding(.horizontal, DS.Space.l).padding(.bottom, 12)
        .accessibilityElement(children: .combine)
        .accessibilityHint(attention ? "需要处理" : "有新结果")
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            if !attention {
                Button { store.markRead([entry]) } label: { Label("已读", systemImage: "checkmark") }.tint(DS.blue)
            }
        }
        .accessibilityActions {
            if !attention { Button("标为已读") { store.markRead([entry]) } }
        }
    }
}

/// A task without a new result: one row, no mark at its head.
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
        let canMark = task.latestNotification != nil && task.group != .attention
        Button { nav.open(.schedule(machine: entry.machineID, id: task.id)) } label: {
            VStack(alignment: .leading, spacing: 3) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(task.spec.title).font(.body.weight(.medium)).foregroundStyle(task.group == .paused ? DS.ink2 : DS.ink)
                        .lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                    if let when = task.latestNotification?.notificationTime {
                        Text(scheduleRowTime(when, now: date)).font(.footnote).foregroundStyle(DS.ink2).fixedSize()
                    }
                }
                Text(task.listPreview(now: date)).font(.subheadline).foregroundStyle(DS.ink2).lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                HStack(spacing: 5) {
                    if task.running != nil { Spinner().frame(width: 10, height: 10) }
                    Text(scheduleListMetadata(entry, entries: store.tasks, state: model.state, online: store.online(entry.machineID)))
                        .font(.footnote).foregroundStyle(DS.ink2).lineLimit(1)
                }
            }
            .padding(.horizontal, DS.Space.page).padding(.vertical, 12)
            .overlay(alignment: .bottom) { if !last { RowSeparator().padding(.leading, DS.Space.page) } }
            .contentShape(Rectangle())
        }
        .buttonStyle(RowPressStyle())
        .accessibilityElement(children: .combine)
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            if canMark {
                Button { store.markUnread(entry) } label: { Text("未读") }.tint(DS.blue)
            }
        }
        .accessibilityActions {
            if canMark { Button("标为未读") { store.markUnread(entry) } }
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
