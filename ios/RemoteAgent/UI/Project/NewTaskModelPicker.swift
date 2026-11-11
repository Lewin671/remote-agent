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
    var isOpen: Bool { initial != nil }
    var config: NewConfig? { edited ?? initial }

    func open(_ config: NewConfig) {
        initial = config
        edited = nil
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

struct NewTaskModelPanel: View {
    let picker: NewTaskModelPicker
    let models: SummaryModels
    let settled: Bool
    let onClose: () -> Void
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var settings: PBDaemonSettings?
    @State private var error = ""
    @AccessibilityFocusState private var announced: Bool

    var body: some View {
        if let c = picker.config {
            let blocked = prefsBlocked(model.state)
            VStack(spacing: 0) {
                SheetHead("模型", action: "完成", onAction: onClose)
                    .accessibilityFocused($announced)
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        if let blocked { Banner(blocked).padding(.bottom, 8) }
                        if !settled { Hint("正在读取这个项目上次的设置…") }
                        ModelSettingsBody(config: c, models: models.list, modelsError: error, settings: settings,
                                          machineName: machineLabel(model.state, id: c.machineId),
                                          onEdit: { picker.edited = $0 })
                            .disabled(!settled || blocked != nil)
                    }
                }
                .scrollBounceBehavior(.basedOnSize)
                .scrollDismissesKeyboard(.never)
            }
            .padding(12)
            .background(DS.popover)
            .clipShape(RoundedRectangle(cornerRadius: 14))
            .overlay { RoundedRectangle(cornerRadius: 14).strokeBorder(DS.rim, lineWidth: 0.5) }
            .floatShadow()
            .accessibilityElement(children: .contain)
            .accessibilityLabel("模型选择")
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
                   !Task.isCancelled { settings = r.settings }
            }
        }
    }
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
