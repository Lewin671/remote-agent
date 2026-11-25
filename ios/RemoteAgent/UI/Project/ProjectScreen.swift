// A project's page (Android's ProjectScreen): renders `PBCoreProjectDetailView`
// and `PBCoreNewSessionViewModel` directly from the shared Go core.
import RACore
import RAProto
import SwiftUI

struct ProjectScreen: View {
    let key: String
    let focusNew: Bool

    var body: some View {
        ProjectPageView(key: key, machine: nil, focus: focusNew)
    }
}

struct ProjectPageView: View {
    let key: String
    let machine: String?
    let focus: Bool

    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var route
    @Environment(AccountContext.self) private var context

    @State private var showEnded = HomeLaunch.flag("-projectEnded")
    @State private var scrolledAway = false
    @State private var settings: ProjectSettingsRequest? = HomeLaunch.value("-projectSheet")
        .map { ProjectSettingsRequest(page: ProjectSettingsPage(rawValue: $0) ?? .main) }

    var body: some View {
        let detail = context.core.projectDetail
        let newSession = context.core.newSession
        ProjectDetailContent(
            detail: detail,
            newSession: newSession,
            fresh: machine != nil && detail.active.isEmpty && detail.endedTotal == 0,
            showEnded: $showEnded,
            scrolledAway: $scrolledAway,
            onOpenSettings: { page in settings = ProjectSettingsRequest(page: page) },
            onUnhide: {
                Haptics.confirm()
                context.dispatch { $0.projectToggleHide.projectKey = detail.key.or(key) }
            },
            onRetrySaveConfig: {
                context.dispatch { $0.newSessionRetrySaveConfig.projectKey = detail.key.or(key) }
            },
            onLoadMoreEnded: {
                context.dispatch { $0.projectLoadMoreEnded.projectKey = detail.key.or(key) }
            },
            composer: {
                NewSessionBox(
                    key: detail.key.or(key),
                    focus: focus,
                    onMachine: { settings = ProjectSettingsRequest(page: .machine) },
                    onModel: { settings = ProjectSettingsRequest(page: .model) },
                    onMachineSettings: { nav.open(.machine(id: $0), from: route) },
                    onEnableAgent: { id, agent in
                        nav.open(.machine(id: id, initialCliAgent: MachineScreenContent<EmptyView>.agentSlug(agent)), from: route)
                    },
                    onMachineStatus: { nav.open(.machineStatus(id: $0), from: route) }
                )
            }
        )
        .kitSheet(item: $settings) { request in
            CoreProjectSettingsSheet(open: request.page) { settings = nil }
        }
        .task(id: "\(key):\(machine ?? "")") {
            await context.coreHost.showProject(key: key)
            await context.coreHost.showNewSession(projectKey: key)
            if let machine, !machine.isEmpty {
                await context.coreHost.dispatch(PBCoreEvent.with { $0.projectAdd.path = key })
                await context.coreHost.dispatch(PBCoreEvent.with { $0.newSessionSelectMachine.machineID = machine })
            }
        }
        .homeLaunchTweaks()
    }
}

private let projectDetailScrollID = "project-scroll"

private struct ProjectHeaderEndKey: PreferenceKey {
    static let defaultValue: CGFloat? = nil
    static func reduce(value: inout CGFloat?, nextValue: () -> CGFloat?) { value = nextValue() ?? value }
}

/// Pure presentation body for `ProjectPageView`, driven by `PBCoreProjectDetailView` and `PBCoreNewSessionViewModel`.
struct ProjectDetailContent<Composer: View>: View {
    let detail: PBCoreProjectDetailView
    let newSession: PBCoreNewSessionViewModel
    var fresh = false
    @Binding var showEnded: Bool
    @Binding var scrolledAway: Bool
    var onOpenSettings: (ProjectSettingsPage) -> Void = { _ in }
    var onUnhide: () -> Void = {}
    var onRetrySaveConfig: () -> Void = {}
    var onLoadMoreEnded: () -> Void = {}
    @ViewBuilder var composer: () -> Composer

    var body: some View {
        let renderedName = detail.name.rendered()
        let name = renderedName.isEmpty ? newSession.projectName.rendered() : renderedName
        let folded = scrolledAway || fresh
        let summary = settingsSummary()
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        if fresh {
                            if !summary.isEmpty {
                                ProjectSummaryChip(summary: summary) { onOpenSettings(.main) }
                                    .padding(.horizontal, DS.Space.l)
                                    .padding(.vertical, 4)
                            }
                        } else {
                            header(name: name, summary: summary)
                                .background {
                                    GeometryReader { g in
                                        Color.clear.preference(key: ProjectHeaderEndKey.self, value: g.frame(in: .named(projectDetailScrollID)).maxY)
                                    }
                                }
                                .onDisappear { scrolledAway = true }
                        }
                        changeNotice
                        if detail.isHidden || detail.hiddenNotice.active {
                            CoreHiddenProjectNotice(
                                notice: detail.hiddenNotice,
                                onUnhide: onUnhide
                            )
                        }
                        sessionsSection
                        Color.clear.frame(height: 12)
                    }
                    .readableColumn()
                }
                .coordinateSpace(.named(projectDetailScrollID))
                .onPreferenceChange(ProjectHeaderEndKey.self) { end in
                    if let end { scrolledAway = end <= 0 }
                }
                .scrollDismissesKeyboard(.interactively)
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if !detail.isHidden {
                    composer()
                }
            }
        }
        .navBar(
            folded ? name : "",
            subtitle: !folded ? nil : (fresh || summary.isEmpty ? detail.shortPath : summary),
            onMore: { onOpenSettings(.main) }
        )
    }

    private func settingsSummary() -> String {
        let sep = L10n.commonIosListSeparator()
        var parts: [String] = []
        if newSession.machineSelectable && !newSession.machineName.isEmpty {
            parts.append(newSession.machineName)
        }
        if !newSession.agentName.isEmpty {
            parts.append(newSession.agentName)
        }
        if newSession.defaultMode.known {
            let mode = newSession.defaultMode.label.rendered()
            if !mode.isEmpty { parts.append(mode) }
        }
        let model = newSession.modelLabel.rendered()
        if !model.isEmpty {
            parts.append(newSession.effortLabel.isEmpty ? model : "\(model) (\(newSession.effortLabel))")
        }
        return parts.joined(separator: sep)
    }

    private func header(name: String, summary: String) -> some View {
        HStack(alignment: .top, spacing: 14) {
            Avatar(detail.key, size: 52)
            VStack(alignment: .leading, spacing: 0) {
                Text(name).font(.title2.bold()).ink(DS.ink).lineLimit(2).accessibilityAddTraits(.isHeader)
                Text(detail.subtitle.rendered()).font(.subheadline).ink(DS.ink2).lineLimit(2)
                HStack(spacing: 0) {
                    Text(L10n.projectsIosActivePrefix(count: Int(detail.activeCount))).font(.subheadline).ink(DS.ink2)
                    Button {
                        showEnded.toggle()
                    } label: {
                        Group {
                            if detail.endedTotal == 0 {
                                Text(L10n.projectsIosEndedCount(count: 0)).font(.subheadline).ink(DS.ink2)
                            } else {
                                ArrowText(
                                    L10n.projectsIosEndedCount(count: Int(detail.endedTotal)),
                                    style: .subheadline.weight(.semibold),
                                    direction: showEnded ? .down : .right
                                )
                            }
                        }
                        .padding(.vertical, 12).contentShape(Rectangle()).padding(.vertical, -12)
                    }
                    .buttonStyle(PressStyle())
                    .disabled(detail.endedTotal == 0)
                    .accessibilityValue(showEnded ? L10n.projectsIosExpanded() : L10n.projectsIosCollapsed())
                }
                .padding(.top, 2)
                if !summary.isEmpty {
                    ProjectSummaryChip(summary: summary) { onOpenSettings(.main) }
                        .padding(.top, 6)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, DS.Space.l).padding(.bottom, 2)
    }

    @ViewBuilder private var changeNotice: some View {
        let errorText = detail.settingsError.rendered()
        if detail.savingSettings {
            Banner(L10n.projectsIosSavingSettings(), icon: Mark.progress)
                .padding(.horizontal, 12).padding(.vertical, 4)
        } else if !errorText.isEmpty {
            Banner(
                L10n.projectsIosSettingsFailed(reason: errorText),
                tone: .error,
                action: L10n.commonRetry(),
                onAction: onRetrySaveConfig
            )
            .padding(.horizontal, 12).padding(.vertical, 4)
        }
    }

    @ViewBuilder private var sessionsSection: some View {
        if !detail.active.isEmpty {
            SectionLabel(L10n.homeLive(), count: Int(detail.activeCount)).accessibilityElement(children: .combine)
            ForEach(Array(detail.active.enumerated()), id: \.element.sessionID) { idx, row in
                HomeSessionRow(row: row, last: idx == detail.active.count - 1)
            }
        }
        let recent = showEnded ? detail.ended : Array(detail.ended.prefix(3))
        if !recent.isEmpty || (showEnded && detail.hasMoreEnded_p) {
            SectionLabel(
                showEnded ? L10n.listStateEnded() : L10n.projectsIosRecentEnded(),
                count: showEnded ? Int(detail.endedTotal) : nil
            ).accessibilityElement(children: .combine)
            ForEach(recent, id: \.sessionID) { row in
                HomeEndedRow(row: row)
            }
            if showEnded && (detail.canLoadEarlier || detail.hasMoreEnded_p) {
                let tailText = detail.tail.text.rendered()
                if !tailText.isEmpty || detail.tail.kind != .unspecified {
                    EarlierTail(tailText.or(L10n.commonLoading()), retry: detail.tail.retry, onRetry: onLoadMoreEnded)
                        .padding(.init(top: 12, leading: 35, bottom: 4, trailing: DS.Space.l))
                        .accessibilityIdentifier("project-earlier")
                        .task(id: "\(detail.ended.count):\(detail.hasMoreEnded_p)") {
                            if detail.canLoadEarlier || detail.hasMoreEnded_p {
                                onLoadMoreEnded()
                            }
                        }
                }
            }
        }
        let emptyNote = detail.emptyNote.rendered()
        if !emptyNote.isEmpty && detail.active.isEmpty && detail.endedTotal == 0 && !detail.isHidden {
            Text(emptyNote).font(.subheadline).ink(DS.ink2).padding(DS.Space.l)
        }
    }
}

private struct CoreHiddenProjectNotice: View {
    let notice: PBCoreHiddenProjectNoticeView
    let onUnhide: () -> Void

    var body: some View {
        let errorText = notice.error.rendered()
        VStack(alignment: .leading, spacing: 0) {
            Banner(
                notice.busy ? L10n.projectsIosAdding() : notice.text.rendered(),
                action: notice.actionEnabled && !notice.busy ? L10n.projectsAddAgain() : nil,
                onAction: onUnhide
            )
            if !errorText.isEmpty {
                Hint(L10n.commonIosSaveFailed(reason: errorText), color: DS.red)
            }
        }
        .padding(.horizontal, DS.Space.l).padding(.vertical, 4)
    }
}

#if DEBUG
#Preview("Project Detail") {
    NavigationStack {
        ProjectDetailContent(
            detail: ScreenGoldens.projectDetail("project_manage_detail"),
            newSession: ScreenGoldens.newSession("new_session_lifecycle"),
            showEnded: .constant(false),
            scrolledAway: .constant(false)
        ) {
            NewSessionContent(
                vm: ScreenGoldens.newSession("new_session_lifecycle"),
                text: .constant("")
            )
        }
    }
}
#endif
