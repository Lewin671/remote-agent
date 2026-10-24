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
                GRow("CLI").trailing {
                    ScheduleAgentPicker(agents: agents, selection: Binding(get: { c.agent.rawValue }, set: { raw in
                        guard let agent = agents.first(where: { $0.rawValue == raw }) else { return }
                        let defaults = projectStart(state, key: c.cwd, pick: agent, machine: c.machineId)?.config
                        config = RACore.switchAgent(c, project: defaults, agent: agent)
                    })).frame(width: 208, height: 32)
                    .accessibilityIdentifier("schedule-cli-picker")
                }
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
        if control.numberOfSegments != agents.count {
            control.removeAllSegments()
            for (index, agent) in agents.enumerated() { control.insertSegment(withTitle: agentName(agent), at: index, animated: false) }
        }
        control.selectedSegmentIndex = agents.firstIndex { $0.rawValue == selection } ?? UISegmentedControl.noSegment
        control.isEnabled = enabled
        control.setTitleTextAttributes([.foregroundColor: UIColor(Theme.muted)], for: .normal)
        control.setTitleTextAttributes([.foregroundColor: UIColor(Theme.fg)], for: .selected)
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
