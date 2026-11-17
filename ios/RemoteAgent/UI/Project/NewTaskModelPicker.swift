// A model panel in the composer's hierarchy rendered directly from `PBCoreNewSessionModelPanelView`.
import RACore
import RAProto
import SwiftUI
import UIKit

struct NewTaskPanelAnchors: PreferenceKey {
    static let defaultValue: [String: Anchor<CGRect>] = [:]
    static func reduce(value: inout [String: Anchor<CGRect>], nextValue: () -> [String: Anchor<CGRect>]) {
        value.merge(nextValue(), uniquingKeysWith: { _, new in new })
    }
}

/// Core-rendered composer model panel (`PBCoreNewSessionModelPanelView`).
struct NewTaskModelPanel: View {
    let vm: PBCoreNewSessionViewModel
    let settled: Bool
    let blocked: String?
    let onRetryModels: () -> Void
    let onSelectModel: (_ slug: String, _ effort: String, _ standardContext: Bool?) -> Void
    let onClose: () -> Void

    @State private var allModels = false
    @State private var controlsHeight: CGFloat = 0
    @AccessibilityFocusState private var announced: Bool

    var body: some View {
        let panel = vm.modelPanel
        let errorText = panel.modelsError.rendered()
        NewTaskPanelLayout {
            VStack(alignment: .leading, spacing: 0) {
                HStack {
                    if allModels {
                        Button { allModels = false } label: {
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
            if !errorText.isEmpty {
                Banner(
                    L10n.modelsIosLoadFailed(agent: vm.agentName, reason: errorText),
                    tone: .warn,
                    action: L10n.commonRetry(),
                    onAction: onRetryModels
                )
                .frame(height: 120, alignment: .top)
            } else if panel.modelsLoading && panel.models.isEmpty {
                LoadingRow(L10n.modelsIosReadingMachine(name: vm.machineName), inset: 0)
                    .frame(height: 120, alignment: .top)
            } else {
                let shown = shownList(panel)
                let more = !allModels && shown.count < panel.models.count
                ScrollView {
                    VStack(spacing: 0) {
                        if !allModels {
                            controls(panel)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .disabled(!settled || blocked != nil)
                                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { controlsHeight = $0 }
                                .background(DS.popover)
                                .visualEffect { effect, geometry in
                                    let pinned = shouldPinModelControls(
                                        bodyHeight: geometry.bounds(of: .scrollView)?.height ?? 0,
                                        controlsHeight: geometry.size.height
                                    )
                                    return effect.offset(y: pinned ? max(0, -geometry.frame(in: .scrollView).minY) : 0)
                                }
                                .zIndex(1)
                        }
                        modelRows(panel, shown: shown, more: more)
                            .disabled(!settled || blocked != nil)
                    }
                }
                .id(allModels)
                .scrollBounceBehavior(.basedOnSize)
                .scrollDismissesKeyboard(.never)
                .frame(idealHeight: CGFloat(shown.count + 1 + (more ? 1 : 0)) * 44 + (allModels ? 0 : controlsHeight))
                .accessibilityIdentifier("new-model-list")
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
    }

    private func shownList(_ panel: PBCoreNewSessionModelPanelView) -> [PBCoreModelOptionView] {
        if allModels || panel.models.count <= 8 { return panel.models }
        var out = Array(panel.models.prefix(8))
        if let picked = panel.models.first(where: { $0.checked }), !out.contains(where: { $0.slug == picked.slug }) {
            out[out.count - 1] = picked
        }
        return out
    }

    private func modelRows(_ panel: PBCoreNewSessionModelPanelView, shown: [PBCoreModelOptionView], more: Bool) -> some View {
        VStack(spacing: 0) {
            row(panel.defaultModelLabel.rendered(), checked: panel.defaultModelChecked) {
                Haptics.selection()
                onSelectModel("", "", nil)
                allModels = false
            }
            ForEach(shown, id: \.slug) { m in
                row(m.displayName.isEmpty ? m.slug : m.displayName, checked: m.checked) {
                    Haptics.selection()
                    onSelectModel(m.slug, "", nil)
                    allModels = false
                }
            }
            if more {
                Button { allModels = true } label: {
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

    @ViewBuilder private func controls(_ panel: PBCoreNewSessionModelPanelView) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            if !panel.effortChoices.isEmpty {
                if panel.effortChoices.count == 1 {
                    Text(L10n.modelsFixedEffort(effort: panel.effortChoices[0])).font(.footnote).ink(DS.ink2).padding(.vertical, 4)
                } else {
                    Text(L10n.modelsEffort()).font(.caption).ink(DS.ink2)
                    NewTaskModelSegments(items: panel.effortChoices, selected: panel.selectedEffort) { effort in
                        onSelectModel(vm.config.model, effort, nil)
                    }
                }
            }
            if panel.supportsContextWindow {
                let contextText = panel.contextNote.rendered()
                if panel.contextFixed {
                    if !contextText.isEmpty {
                        Text(contextText).font(.footnote).ink(DS.ink2).padding(.vertical, 4)
                    }
                } else {
                    Text(L10n.modelsContextWindow()).font(.caption).ink(DS.ink2)
                    NewTaskModelSegments(
                        items: ["extended", "standard"],
                        selected: panel.standardContext ? "standard" : "extended",
                        label: { $0 == "standard" ? L10n.modelsStandardWindow() : L10n.modelsExtendedWindow() }
                    ) { choice in
                        onSelectModel(vm.config.model, vm.config.effort, choice == "standard")
                    }
                }
            }
        }.accessibilityIdentifier("new-model-controls")
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

/// Pin the header, give the scrolling body the remaining height, and wrap short content.
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
    var accessibilityLabel: String {
        effort.isEmpty
            ? L10n.modelsSummaryLabel(model: model)
            : L10n.modelsSummaryEffortLabel(effort: effort, model: model)
    }

    init(vm: PBCoreNewSessionViewModel) {
        self.model = vm.modelLabel.rendered().ifEmpty(L10n.newSessionDefaultModel())
        self.effort = vm.effortLabel
        let singleAgent = vm.availableAgents.count <= 1
        self.prefix = singleAgent && !vm.agentName.isEmpty ? vm.agentName + " · " : ""
    }

    init(model: String, effort: String = "", prefix: String = "") {
        self.model = model
        self.effort = effort
        self.prefix = prefix
    }
}

/// Closes the model panel when a tap falls outside it, letting the original touch pass through.
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
