import RACore
import RAProto
import SwiftUI

/// 定时: renders `PBSchedulesViewModel` from Core and dispatches Core events.
struct SchedulesScreen: View {
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.sidebarCreate) private var create
    @AppStorage("schedules.endedExpanded") private var endedExpanded = false

    var body: some View {
        let vm = context.core.schedules
        SchedulesContent(
            vm: vm,
            sidebarCreate: create,
            connectionBar: { ConnectionBar() },
            onToggleEnded: {
                let next = !vm.endedExpanded
                endedExpanded = next
                context.dispatch { $0.scheduleToggleEnded.value = next }
            },
            onRetryMachine: { mid in
                context.dispatch { $0.scheduleRefresh.value = mid }
            },
            onMarkAllRead: {
                context.dispatch { $0.scheduleMarkRead = .with { $0.allFresh = true } }
            },
            onMarkRead: { item in
                context.dispatch {
                    $0.scheduleMarkRead = .with {
                        $0.tasks = [.with { $0.machineID = item.machineID; $0.taskID = item.taskID }]
                    }
                }
            },
            onMarkUnread: { item in
                context.dispatch {
                    $0.scheduleMarkUnread = .with {
                        $0.machineID = item.machineID
                        $0.taskID = item.taskID
                        $0.value = true
                    }
                }
            },
            onOpenTask: { item in
                context.dispatch {
                    $0.scheduleSelectTask = .with {
                        $0.machineID = item.machineID
                        $0.taskID = item.taskID
                    }
                }
                nav.open(.schedule(machine: item.machineID, id: item.taskID))
            }
        )
        .task {
            let desired = endedExpanded
            if desired != vm.endedExpanded {
                await context.coreHost.dispatch { @Sendable ev in ev.scheduleToggleEnded.value = desired }
            }
        }
    }
}

/// Pure presentation body for `SchedulesScreen`, driven solely by `PBSchedulesViewModel`.
struct SchedulesContent<ConnectionView: View>: View {
    let vm: PBSchedulesViewModel
    var sidebarCreate: SidebarCreate? = nil
    @ViewBuilder var connectionBar: () -> ConnectionView
    var onToggleEnded: () -> Void = {}
    var onRetryMachine: (String) -> Void = { _ in }
    var onMarkAllRead: () -> Void = {}
    var onMarkRead: (PBScheduleTaskItemView) -> Void = { _ in }
    var onMarkUnread: (PBScheduleTaskItemView) -> Void = { _ in }
    var onOpenTask: (PBScheduleTaskItemView) -> Void = { _ in }

    var body: some View {
        Page {
            List {
                Group {
                    if let sidebarCreate {
                        HStack {
                            Spacer()
                            SidebarCreateButton(create: sidebarCreate, schedules: true)
                        }
                        .padding(.horizontal, DS.Space.card)
                    }
                    ScheduleHeadlineView(headline: vm.headline)
                    connectionBar()
                    if !vm.anyMachineSupported && vm.hasUnsupportedNotice {
                        Banner(vm.unsupportedNotice.rendered())
                            .padding(.horizontal, DS.Space.l).padding(.vertical, 4)
                    } else if vm.hasOfflineNotice {
                        let text = vm.offlineNotice.rendered()
                        if !text.isEmpty {
                            Banner(text).padding(.horizontal, DS.Space.l).padding(.vertical, 4)
                        }
                    }
                    ForEach(vm.machineErrors, id: \.machineID) { err in
                        Banner(
                            err.error.rendered(),
                            tone: .error,
                            action: err.canRetry ? L10n.commonRetry() : nil,
                            onAction: err.canRetry ? { onRetryMachine(err.machineID) } : nil
                        )
                        .padding(.horizontal, DS.Space.l).padding(.vertical, 4)
                    }
                    if vm.showEmptyHint && vm.hasEmptyHint {
                        Text(vm.emptyHint.rendered())
                            .font(.body).foregroundStyle(DS.ink2)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal, DS.Space.page).padding(.top, 8)
                    }
                    ForEach(vm.attention, id: \.listRowID) { item in
                        ScheduleCard(
                            item: item,
                            attention: true,
                            onMarkRead: { onMarkRead(item) },
                            onOpen: { onOpenTask(item) }
                        )
                    }
                    if !vm.fresh.isEmpty {
                        ScheduleSectionTitle(
                            L10n.scheduleNewResultsSchedules(),
                            action: vm.canMarkAllRead ? L10n.scheduleMarkAllAsRead() : nil,
                            onAction: onMarkAllRead
                        )
                        ForEach(vm.fresh, id: \.listRowID) { item in
                            ScheduleCard(
                                item: item,
                                attention: false,
                                onMarkRead: { onMarkRead(item) },
                                onOpen: { onOpenTask(item) }
                            )
                        }
                    }
                    if !vm.others.isEmpty {
                        let othersTitle = vm.othersSectionTitle.rendered()
                        ScheduleSectionTitle(othersTitle.isEmpty ? L10n.scheduleOtherTasks() : othersTitle)
                        ForEach(Array(vm.others.enumerated()), id: \.element.listRowID) { i, item in
                            ScheduleListRow(
                                item: item,
                                last: i == vm.others.count - 1,
                                onMarkUnread: { onMarkUnread(item) },
                                onOpen: { onOpenTask(item) }
                            )
                        }
                    }
                    if !vm.ended.isEmpty || vm.totalEnded > 0 {
                        let endedTitle = vm.endedLabel.rendered()
                        Button(action: onToggleEnded) {
                            HStack(spacing: 6) {
                                Text(endedTitle.isEmpty ? L10n.projectsIosEndedCount(count: Int(vm.totalEnded)) : endedTitle)
                                    .font(.subheadline).foregroundStyle(DS.ink2)
                                Image(systemName: vm.endedExpanded ? "chevron.up" : "chevron.down")
                                    .font(.footnote.weight(.semibold)).foregroundStyle(DS.ink3)
                                Spacer()
                            }
                            .frame(minHeight: DS.minHit).padding(.horizontal, DS.Space.page).padding(.top, 8)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(PressStyle())
                        .accessibilityValue(vm.endedExpanded ? L10n.projectsIosExpanded() : L10n.sessionIosCollapsed())
                        if vm.endedExpanded {
                            ForEach(Array(vm.ended.enumerated()), id: \.element.listRowID) { i, item in
                                ScheduleListRow(
                                    item: item,
                                    last: i == vm.ended.count - 1,
                                    onMarkUnread: { onMarkUnread(item) },
                                    onOpen: { onOpenTask(item) }
                                )
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

private extension PBScheduleTaskItemView {
    var listRowID: String { "\(machineID)/\(taskID)" }
}

/// The sentence at the top, 34 / 700 with the number of what stopped in orange, and the line under it.
private struct ScheduleHeadlineView: View {
    let headline: PBScheduleHeadlineView

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            Group {
                if headline.kind == .stopped && headline.highlightCount > 0 {
                    stoppedTitle(Int(headline.highlightCount))
                } else {
                    Text(headline.title.rendered())
                }
            }
            .font(.largeTitle.bold()).foregroundStyle(DS.ink)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityAddTraits(.isHeader)
            let sub = headline.sub.rendered()
            if !sub.isEmpty {
                Text(sub).font(.body).foregroundStyle(DS.ink2)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, DS.Space.page).padding(.top, 6).padding(.bottom, 12)
    }

    /// Preserve the highlighted number while allowing the sentence to reorder it.
    private func stoppedTitle(_ count: Int) -> Text {
        let marker = "{total}"
        let parts = L10n.scheduleStoppedCount(count: count, total: marker).components(separatedBy: marker)
        if parts.count >= 2 {
            return Text(parts[0]) + Text(String(count)).foregroundStyle(DS.orange) + Text(parts[1])
        }
        return Text(headline.title.rendered())
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
    let item: PBScheduleTaskItemView
    let attention: Bool
    let onMarkRead: () -> Void
    let onOpen: () -> Void

    var body: some View {
        let canMark = item.actions.canMarkRead
        let when = item.timeText.rendered()
        Button(action: onOpen) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    StatusMark(attention ? .waiting : .unread)
                    Text(item.title).font(.body.weight(.semibold)).foregroundStyle(DS.ink).lineLimit(2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if !when.isEmpty {
                        Text(when).font(.footnote).foregroundStyle(DS.ink2).fixedSize()
                    }
                }
                if attention {
                    let lead = item.attentionLead.rendered()
                    let reason = item.attentionReason.rendered()
                    if !lead.isEmpty {
                        (Text(lead).foregroundStyle(DS.orangeInk).fontWeight(.semibold)
                            + Text(reason).foregroundStyle(DS.ink2))
                            .font(.subheadline).lineLimit(2)
                    } else {
                        Text(item.preview.rendered()).font(.subheadline).foregroundStyle(DS.ink2).lineLimit(2)
                    }
                    HStack {
                        let meta = item.metadata.rendered()
                        if !meta.isEmpty {
                            Text(meta).font(.footnote).foregroundStyle(DS.ink2).lineLimit(1)
                        }
                        Spacer(minLength: 8)
                        Text(L10n.scheduleIosTakeAction()).font(.subheadline.weight(.semibold)).foregroundStyle(DS.blue).fixedSize()
                    }
                } else {
                    Text(item.preview.rendered()).font(.body).foregroundStyle(DS.ink).lineLimit(4)
                    HStack(alignment: .firstTextBaseline) {
                        Text(item.metadata.rendered())
                            .font(.footnote).foregroundStyle(DS.ink2).fixedSize(horizontal: false, vertical: true)
                        Spacer(minLength: 8)
                        Text(L10n.scheduleIosReadFullText()).font(.subheadline.weight(.semibold)).foregroundStyle(DS.blue).fixedSize()
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
        .accessibilityHint(attention ? L10n.scheduleIosNeedsAttention() : L10n.homeIosNewResults())
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            if canMark {
                Button(action: onMarkRead) { Label(L10n.scheduleIosRead(), systemImage: "checkmark") }.tint(DS.blue)
            }
        }
        .accessibilityActions {
            if canMark { Button(L10n.scheduleMarkAsRead(), action: onMarkRead) }
        }
    }
}

/// A task without a new result: one row, no mark at its head.
struct ScheduleListRow: View {
    let item: PBScheduleTaskItemView
    let last: Bool
    var onMarkUnread: () -> Void = {}
    var onOpen: () -> Void = {}

    var body: some View {
        let when = item.timeText.rendered()
        let canMarkUnread = item.actions.canMarkUnread
        Button(action: onOpen) {
            VStack(alignment: .leading, spacing: 3) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(item.title)
                        .font(.body.weight(.medium))
                        .foregroundStyle(item.paused || item.group == .ended ? DS.ink2 : DS.ink)
                        .lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                    if !when.isEmpty {
                        Text(when).font(.footnote).foregroundStyle(DS.ink2).fixedSize()
                    }
                }
                Text(item.preview.rendered())
                    .font(.subheadline)
                    .foregroundStyle(DS.ink2)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                HStack(spacing: 5) {
                    if item.inProgress { Spinner().frame(width: 10, height: 10) }
                    Text(item.metadata.rendered())
                        .font(.footnote).foregroundStyle(DS.ink2)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(.horizontal, DS.Space.page).padding(.vertical, 12)
            .overlay(alignment: .bottom) { if !last { RowSeparator().padding(.leading, DS.Space.page) } }
            .contentShape(Rectangle())
        }
        .buttonStyle(RowPressStyle())
        .accessibilityElement(children: .combine)
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            if canMarkUnread {
                Button(action: onMarkUnread) { Text(L10n.scheduleIosUnread()) }.tint(DS.blue)
            }
        }
        .accessibilityActions {
            if canMarkUnread { Button(L10n.scheduleMarkAsUnread(), action: onMarkUnread) }
        }
    }
}

/// Shared banner for pending/frozen/failed schedule create or edit mutations.
struct ScheduleMutationBanner: View {
    let mutation: PBScheduleMutationView
    var onRetry: () -> Void = {}
    var onAbandon: () -> Void = {}

    var body: some View {
        let error = mutation.hasError ? mutation.error.rendered() : ""
        let notice = mutation.hasNotice ? mutation.notice.rendered() : ""
        let frozenHint = mutation.frozen && mutation.hasFrozenHint ? mutation.frozenHint.rendered() : ""
        if !error.isEmpty || !notice.isEmpty || !frozenHint.isEmpty || mutation.canRetry || mutation.canAbandon {
            VStack(alignment: .leading, spacing: 6) {
                if !error.isEmpty { Banner(error, tone: .error) }
                if !notice.isEmpty { Banner(notice) }
                if !frozenHint.isEmpty { Banner(frozenHint, tone: .warn) }
                if mutation.canRetry || mutation.canAbandon {
                    HStack(spacing: 12) {
                        if mutation.canRetry { TextAction(L10n.commonRetry(), action: onRetry) }
                        if mutation.canAbandon { TextAction(L10n.newSessionIosAbandon(), color: DS.red, action: onAbandon) }
                    }
                    .padding(.horizontal, 4)
                }
            }
        }
    }
}
