import RACore
import RAProto
import SwiftUI
import UIKit

/// The CLI is an inline choice, shared by creation and the execution-target editor.
struct ScheduleAgentRow: View {
    let availableAgents: [PBCoreAgentOptionView]
    let selected: PBAgent
    var enabled: Bool = true
    let onSelect: (PBAgent) -> Void

    var body: some View {
        if availableAgents.count > 1 {
            let picker = ScheduleAgentPicker(
                agents: availableAgents,
                selection: Binding(
                    get: { selected.rawValue },
                    set: { raw in
                        if let picked = availableAgents.first(where: { $0.agent.rawValue == raw }) {
                            onSelect(picked.agent)
                        }
                    }
                )
            )
            .disabled(!enabled)
            .accessibilityIdentifier("schedule-cli-picker")
            // Beside its name where the row has room, under it where it does not.
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 10) {
                    Text(L10n.newSessionCliPickerLabel()).font(.body).foregroundStyle(DS.ink).fixedSize()
                    Spacer(minLength: 10)
                    picker.fixedSize()
                }
                VStack(alignment: .leading, spacing: 6) {
                    Text(L10n.newSessionCliPickerLabel()).font(.body).foregroundStyle(DS.ink)
                    picker
                }
                .padding(.vertical, 8)
            }
            .padding(.horizontal, 14)
            .frame(minHeight: DS.minHit)
        } else if let only = availableAgents.first {
            GRow("CLI", value: only.displayName.isEmpty ? agentName(only.agent) : only.displayName)
        }
    }
}

/// Keep native segment behavior while applying the shared text colors locally.
private struct ScheduleAgentPicker: UIViewRepresentable {
    let agents: [PBCoreAgentOptionView]
    @Binding var selection: Int
    @Environment(\.isEnabled) private var enabled

    func makeCoordinator() -> Coordinator { Coordinator($selection, agents: agents) }

    func makeUIView(context: Context) -> UISegmentedControl {
        let control = UISegmentedControl()
        control.accessibilityLabel = "CLI"
        control.addTarget(context.coordinator, action: #selector(Coordinator.changed(_:)), for: .valueChanged)
        return control
    }

    func updateUIView(_ control: UISegmentedControl, context: Context) {
        context.coordinator.selection = $selection
        context.coordinator.agents = agents
        let titles = agents.map { $0.displayName.isEmpty ? agentName($0.agent) : $0.displayName }
        if control.numberOfSegments != agents.count || titles.enumerated().contains(where: { control.titleForSegment(at: $0.offset) != $0.element }) {
            control.removeAllSegments()
            for (index, title) in titles.enumerated() {
                control.insertSegment(withTitle: title, at: index, animated: false)
            }
        }
        control.selectedSegmentIndex = agents.firstIndex { $0.agent.rawValue == selection } ?? UISegmentedControl.noSegment
        control.isEnabled = enabled
        control.selectedSegmentTintColor = UIColor(DS.raised)
        control.setTitleTextAttributes([.foregroundColor: UIColor(DS.ink)], for: .normal)
        control.setTitleTextAttributes([.foregroundColor: UIColor(DS.ink)], for: .selected)
    }

    /// As wide as offered; asked for its ideal, as wide as its titles, so a row too narrow puts it under its name.
    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UISegmentedControl, context: Context) -> CGSize? {
        CGSize(width: proposal.width ?? uiView.intrinsicContentSize.width, height: 32)
    }

    @MainActor final class Coordinator: NSObject {
        var selection: Binding<Int>
        var agents: [PBCoreAgentOptionView]
        init(_ selection: Binding<Int>, agents: [PBCoreAgentOptionView]) { self.selection = selection; self.agents = agents }
        @objc func changed(_ control: UISegmentedControl) {
            guard agents.indices.contains(control.selectedSegmentIndex) else { return }
            selection.wrappedValue = agents[control.selectedSegmentIndex].agent.rawValue
        }
    }
}
