// A model panel in the composer's hierarchy. It never presents a controller,
// becomes first responder, or writes the text field's FocusState.
import Observation
import RACore
import RAProto
import SwiftUI
import UIKit

@MainActor @Observable
final class NewTaskModelPicker {
    struct Target: Equatable {
        var path: String
        var machine: String
        var agent: PBAgent
        init(_ config: NewConfig) {
            path = config.cwd
            machine = config.machineId
            agent = config.agent
        }
    }

    private(set) var initial: NewConfig?
    var edited: NewConfig?
    var allModels = false
    private var settingsMachine: String?
    private var daemonSettings: PBDaemonSettings?
    func settings(for config: NewConfig) -> PBDaemonSettings? {
        settingsMachine == config.machineId ? daemonSettings : nil
    }
    func received(_ settings: PBDaemonSettings, machine: String) {
        settingsMachine = machine
        daemonSettings = settings
    }
    func chooseModel(_ slug: String, models: PBListModelsResponse) {
        guard let config else { return }
        edited = chooseNewTaskModel(config, slug: slug, models: models, settings: settings(for: config))
        allModels = false
    }
    var isOpen: Bool { initial != nil }
    var config: NewConfig? { edited ?? initial }

    func open(_ config: NewConfig) {
        initial = config
        edited = nil
        allModels = false
    }

    /// Follow settings that were still loading on open, but never replace a
    /// user's pending choice. A different destination invalidates the panel.
    func follow(_ config: NewConfig?) {
        guard let initial else { return }
        guard let config, Target(initial) == Target(config) else { cancel(); return }
        if edited == nil { self.initial = config }
    }

    func cancel() {
        initial = nil
        edited = nil
        allModels = false
    }

    /// Consume before dispatching the prefs op. Multiple close notifications
    /// cannot write twice; a changed destination must never receive this edit.
    func close(target: NewConfig?, settled: Bool, blocked: Bool) -> NewConfig? {
        let next = edited
        let from = initial
        cancel()
        guard settled, !blocked, let next, let from, let target,
              Target(from) == Target(target), Target(next) == Target(target) else { return nil }
        return next
    }
}

struct NewTaskPanelAnchors: PreferenceKey {
    static let defaultValue: [String: Anchor<CGRect>] = [:]
    static func reduce(value: inout [String: Anchor<CGRect>], nextValue: () -> [String: Anchor<CGRect>]) {
        value.merge(nextValue(), uniquingKeysWith: { _, new in new })
    }
}

/// The composer has its own compact controls; the settings sheets keep their layout.
struct NewTaskModelPanel: View {
    let picker: NewTaskModelPicker
    let models: SummaryModels
    let settled: Bool
    let onClose: () -> Void
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var error = ""
    @State private var controlsHeight: CGFloat = 0
    @AccessibilityFocusState private var announced: Bool

    var body: some View {
        if let c = picker.config {
            let blocked = prefsBlocked(model.state)
            NewTaskPanelLayout {
                VStack(alignment: .leading, spacing: 0) {
                    HStack {
                        if picker.allModels {
                            Button { picker.allModels = false } label: {
                                Label(L10n.modelsAll(), systemImage: "chevron.left").font(.headline)
                            }
                            .buttonStyle(.plain).tint(DS.blue)
                            .accessibilityIdentifier("new-model-back")
                        } else {
                            Text(L10n.modelsTitle()).font(.headline).ink(DS.ink)
                        }
                        Spacer()
                        Button(L10n.commonDone(), action: onClose).tint(DS.blue)
                            .frame(minWidth: 44, minHeight: 44)
                    }
                    .frame(minHeight: 44)
                    .accessibilityFocused($announced)
                    if let blocked { Banner(blocked).padding(.bottom, 8) }
                    if !settled { Hint(L10n.newSessionReadingPrevious()) }
                }
                if let ms = models.list {
                    let selected = projectModel(c, models: ms, settings: picker.settings(for: c))
                    let shown = shownModels(ms.models, picked: c.model.isEmpty ? nil : selected, all: picker.allModels)
                    let more = !picker.allModels && shown.count < ms.models.count
                    ScrollView {
                        VStack(spacing: 0) {
                            if !picker.allModels {
                                controls(c, ms)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .disabled(!settled || blocked != nil)
                                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { controlsHeight = $0 }
                                    .background(DS.popover)
                                    // One scroll owner in both modes. Change only the paint
                                    // position, preserving the list's layout and scroll offset.
                                    .visualEffect { effect, geometry in
                                        let pinned = shouldPinModelControls(
                                            bodyHeight: geometry.bounds(of: .scrollView)?.height ?? 0,
                                            controlsHeight: geometry.size.height)
                                        return effect.offset(y: pinned ? max(0, -geometry.frame(in: .scrollView).minY) : 0)
                                    }
                                    .zIndex(1)
                            }
                            modelRows(c, ms, selected: selected, shown: shown, more: more)
                                .disabled(!settled || blocked != nil)
                        }
                    }
                    .id(picker.allModels)
                    .scrollBounceBehavior(.basedOnSize)
                    .scrollDismissesKeyboard(.never)
                    .frame(idealHeight: CGFloat(shown.count + 1 + (more ? 1 : 0)) * 44 + (picker.allModels ? 0 : controlsHeight))
                    .accessibilityIdentifier("new-model-list")
                } else {
                    Group {
                        if !error.isEmpty { Banner(L10n.modelsIosLoadFailed(agent: agentName(c.agent), reason: error), tone: .warn) }
                        else { LoadingRow(L10n.modelsIosReadingMachine(name: machineLabel(model.state, id: c.machineId)), inset: 0) }
                    }.frame(height: 120, alignment: .top)
                }
            }
            .padding(12)
            .background(DS.popover)
            .clipShape(RoundedRectangle(cornerRadius: 14))
            .overlay { RoundedRectangle(cornerRadius: 14).strokeBorder(DS.rim, lineWidth: 0.5) }
            .floatShadow()
            .accessibilityElement(children: .contain)
            .accessibilityLabel(L10n.modelsIosPicker())
            .accessibilityIdentifier("new-model-popover")
            .accessibilityAction(.escape, onClose)
            .onAppear { announced = true }
            .task {
                let key = SummaryModels.Key(machine: c.machineId, agent: c.agent)
                await models.load(key) { key in
                    await HomeLaunch.settled(model)
                    do { return try await context.remote.listModels(key.machine, agent: key.agent, fresh: !model.isDemo) }
                    catch {
                        if !Task.isCancelled { self.error = errorText(error) }
                        return nil
                    }
                }
                guard !Task.isCancelled else { return }
                if case .getSettings(let r)? = try? await context.remote.machineCommand(c.machineId, getSettingsCommand()).kind,
                   !Task.isCancelled { picker.received(r.settings, machine: c.machineId) }
            }
        }
    }

    private func modelRows(_ c: NewConfig, _ ms: PBListModelsResponse, selected: PBModelInfo?, shown: [PBModelInfo], more: Bool) -> some View {
        VStack(spacing: 0) {
            let defaultID = defaultModelName(c, models: ms, settings: picker.settings(for: c))
            let standing = findModel(ms.models, id: defaultID) { $0.slug }
            let name = standing.map { $0.displayName.isEmpty ? $0.slug : $0.displayName } ?? defaultID
            row((name.isEmpty ? L10n.modelsDefault() : L10n.modelsDefaultNamed(model: name)), checked: c.model.isEmpty) { pick("", ms) }
            ForEach(shown, id: \.slug) { m in
                row(m.displayName.isEmpty ? m.slug : m.displayName,
                    checked: !c.model.isEmpty && selected?.slug == m.slug) { pick(m.slug, ms) }
            }
            if more {
                Button { picker.allModels = true } label: {
                    HStack {
                        Text(L10n.modelsAll())
                        Spacer()
                        Image(systemName: "chevron.right")
                    }.foregroundStyle(DS.ink2).frame(height: 44).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("new-model-show-all")
            }
        }
        .font(.subheadline)
    }

    @ViewBuilder private func controls(_ c: NewConfig, _ ms: PBListModelsResponse) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            if let effort = effortChoices(c, models: ms, settings: picker.settings(for: c)) {
                if effort.efforts.count == 1 {
                    Text(L10n.modelsFixedEffort(effort: effort.efforts[0])).font(.footnote).ink(DS.ink2).padding(.vertical, 4)
                } else {
                    Text(L10n.modelsEffort()).font(.caption).ink(DS.ink2)
                    NewTaskModelSegments(items: effort.efforts, selected: effort.selected) {
                        var next = c; next.effort = $0; picker.edited = next
                    }
                }
            }
            if agentInfo(c.agent).standardContextOption {
                let window = settingsContext(c, shown: projectModel(c, models: ms, settings: picker.settings(for: c)))
                if window.fixed {
                    Text(window.text).font(.footnote).ink(DS.ink2).padding(.vertical, 4)
                } else {
                    Text(L10n.modelsContextWindow()).font(.caption).ink(DS.ink2)
                    NewTaskModelSegments(items: ["extended", "standard"], selected: c.standardContext ? "standard" : "extended", label: { $0 == "standard" ? L10n.modelsStandardWindow() : L10n.modelsExtendedWindow() }) {
                        var next = c; next.standardContext = $0 == "standard"; picker.edited = next
                    }
                }
            }
        }.accessibilityIdentifier("new-model-controls")
    }

    private func pick(_ slug: String, _ ms: PBListModelsResponse) {
        Haptics.selection()
        picker.chooseModel(slug, models: ms)
    }

    private func row(_ title: String, checked: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Text(title).lineLimit(1).ink(DS.ink)
                Spacer(minLength: 0)
                if checked { Image(systemName: "checkmark").foregroundStyle(DS.blue) }
            }.frame(height: 44).contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(checked ? .isSelected : [])
    }
}

private struct NewTaskModelSegments: View {
    let items: [String]
    let selected: String
    var label: (String) -> String = { $0 }
    let onPick: (String) -> Void
    var body: some View {
        HStack(spacing: 0) {
            ForEach(items, id: \.self) { item in
                Button { Haptics.selection(); onPick(item) } label: {
                    Text(label(item)).font(.footnote).lineLimit(1).minimumScaleFactor(0.75)
                        .frame(maxWidth: .infinity, minHeight: 40)
                        .foregroundStyle(item == selected ? DS.blue : DS.ink2)
                        .background(item == selected ? DS.popover : .clear, in: RoundedRectangle(cornerRadius: 12))
                        .padding(2).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(item == selected ? .isSelected : [])
            }
        }.background(DS.fill, in: RoundedRectangle(cornerRadius: 14))
    }
}

func shouldPinModelControls(bodyHeight: CGFloat, controlsHeight: CGFloat, rowHeight: CGFloat = 44) -> Bool {
    rowHeight > 0 && bodyHeight - controlsHeight >= rowHeight * 2 + 8
}

/// Pin the header, give the scrolling body the remaining height, and wrap
/// short content. The overlay receives the available height, never sizes the composer.
struct NewTaskPanelLayout: Layout {
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? 320
        let natural = subviews.map { $0.sizeThatFits(.init(width: width, height: nil)).height }.reduce(0, +)
        return CGSize(width: width, height: min(natural, proposal.height ?? natural))
    }
    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for (index, view) in subviews.enumerated() {
            let height = index == subviews.count - 1 ? max(0, bounds.maxY - y) : view.sizeThatFits(.init(width: bounds.width, height: nil)).height
            view.place(at: CGPoint(x: bounds.minX, y: y), anchor: .topLeading, proposal: .init(width: bounds.width, height: height))
            y += height
        }
    }
}

struct NewTaskModelSummary: Equatable {
    var model: String
    var effort = ""
    var prefix = ""
    var accessibilityLabel: String { effort.isEmpty ? L10n.modelsSummaryLabel(model: model) : L10n.modelsSummaryEffortLabel(effort: effort, model: model) }
}

func newTaskModelSummary(_ c: NewConfig, models: PBListModelsResponse?, settings: PBDaemonSettings?, singleAgent: Bool) -> NewTaskModelSummary {
    let m = models.flatMap { projectModel(c, models: $0, settings: settings) }
    let id = c.model.isEmpty ? (defaultsFor(settings, agent: c.agent).model.isEmpty ? models?.defaultModel ?? "" : defaultsFor(settings, agent: c.agent).model) : c.model
    let name = m.map { $0.displayName.isEmpty ? $0.slug : $0.displayName } ?? (id.isEmpty ? (models == nil ? L10n.newSessionReadingSettings() : L10n.newSessionDefaultModel()) : id)
    let effort = models.flatMap { effortChoices(c, models: $0, settings: settings) }
    return NewTaskModelSummary(model: name, effort: (effort?.efforts.count ?? 0) > 1 ? effort?.selected ?? "" : "",
                               prefix: singleAgent ? agentName(c.agent) + " · " : "")
}

func chooseNewTaskModel(_ c: NewConfig, slug: String, models: PBListModelsResponse, settings: PBDaemonSettings?) -> NewConfig {
    var next = c
    next.model = slug
    next.effort = ""
    let m = projectModel(next, models: models, settings: settings)
    let effort = effortChoices(c, models: models, settings: settings)?.selected ?? ""
    next.effort = m?.efforts.contains(effort) == true ? effort : effortChoices(next, models: models, settings: settings)?.selected ?? ""
    if m?.autoUnavailable == true && next.mode == .auto { next.mode = nil }
    return next
}

/// Observe outside touches without a scrim or a competing SwiftUI gesture.
/// The original touch still reaches the field, native CLI menu or page pan.
/// Only this page's window is observed; the system keyboard may be in another
/// process. Text edits also close the panel, covering typing there.
struct NewTaskOutsideTouches: UIViewRepresentable {
    var panel: CGRect?
    var chip: CGRect
    var cli: CGRect?
    var onCLI: () -> Void
    var onClose: () -> Void

    func makeUIView(context: Context) -> Observer { Observer() }
    func updateUIView(_ view: Observer, context: Context) {
        view.panel = panel
        view.chip = chip
        view.cli = cli
        view.onCLI = onCLI
        view.onClose = onClose
    }
    static func dismantleUIView(_ view: Observer, coordinator: ()) { view.detach() }

    final class Observer: UIView {
        var panel: CGRect?
        var chip = CGRect.zero
        var cli: CGRect?
        var onCLI: (() -> Void)?
        var onClose: (() -> Void)?
        private var observer: TouchObserver?
        override func didMoveToWindow() {
            super.didMoveToWindow()
            detach()
            guard let window else { return }
            let observer = TouchObserver { [weak self] point in
                guard let self else { return }
                let local = self.convert(point, from: self.window)
                guard self.bounds.contains(local) else { return }
                if self.cli?.contains(local) == true { self.onCLI?() }
                if let panel = self.panel, !panel.contains(local), !self.chip.contains(local) { self.onClose?() }
            }
            window.addGestureRecognizer(observer)
            self.observer = observer
        }
        func detach() {
            if let observer { observer.view?.removeGestureRecognizer(observer) }
            observer = nil
        }
    }

    /// Fails immediately, so it cannot cancel or delay any native recognizer.
    final class TouchObserver: UIGestureRecognizer {
        let touched: (CGPoint) -> Void
        init(touched: @escaping (CGPoint) -> Void) {
            self.touched = touched
            super.init(target: nil, action: nil)
            cancelsTouchesInView = false
            delaysTouchesBegan = false
            delaysTouchesEnded = false
        }
        override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent) {
            if let touch = touches.first { touched(touch.location(in: view)) }
            state = .failed
        }
        override func canPrevent(_ preventedGestureRecognizer: UIGestureRecognizer) -> Bool { false }
        override func canBePrevented(by preventingGestureRecognizer: UIGestureRecognizer) -> Bool { false }
    }
}
