import RACore
import RAProto
import SwiftUI
import UIKit

/// The CLI is an inline choice, shared by creation and the execution-target editor.
struct ScheduleAgentRow: View {
    let state: RemoteState
    @Binding var config: NewConfig?
    var fixedMachine: String? = nil
    var body: some View {
        let state = fixedMachine.map { scheduleEditingState(state, machine: $0) } ?? state
        if let c = config {
            let agents = machineAgents(state.machines[c.machineId])
            if agents.count > 1 {
                let picker = ScheduleAgentPicker(agents: agents, selection: Binding(get: { c.agent.rawValue }, set: { raw in
                    guard let agent = agents.first(where: { $0.rawValue == raw }) else { return }
                    let defaults = projectStart(state, key: c.cwd, pick: agent, machine: c.machineId)?.config
                    config = RACore.switchAgent(c, project: defaults, agent: agent)
                }))
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
            } else if let agent = agents.first {
                GRow("CLI", value: agentName(agent))
            }
        }
    }
}

/// Keep native segment behavior while applying the shared text colors locally.
private struct ScheduleAgentPicker: UIViewRepresentable {
    let agents: [PBAgent]
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
        if control.numberOfSegments != agents.count || agents.enumerated().contains(where: { control.titleForSegment(at: $0.offset) != agentName($0.element) }) {
            control.removeAllSegments()
            for (index, agent) in agents.enumerated() { control.insertSegment(withTitle: agentName(agent), at: index, animated: false) }
        }
        control.selectedSegmentIndex = agents.firstIndex { $0.rawValue == selection } ?? UISegmentedControl.noSegment
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
        var agents: [PBAgent]
        init(_ selection: Binding<Int>, agents: [PBAgent]) { self.selection = selection; self.agents = agents }
        @objc func changed(_ control: UISegmentedControl) {
            guard agents.indices.contains(control.selectedSegmentIndex) else { return }
            selection.wrappedValue = agents[control.selectedSegmentIndex].rawValue
        }
    }
}
