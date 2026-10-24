import RACore
import RAProto
import SwiftUI

/// The CLI is an inline choice, shared by creation and the execution-target editor.
struct ScheduleAgentRow: View {
    let state: RemoteState
    @Binding var config: NewConfig?
    var body: some View {
        if let c = config {
            let agents = machineAgents(state.machines[c.machineId])
            if agents.count > 1 {
                GRow("CLI").trailing {
                    Picker("CLI", selection: Binding(get: { c.agent.rawValue }, set: { raw in
                        guard let agent = agents.first(where: { $0.rawValue == raw }) else { return }
                        let defaults = projectStart(state, key: c.cwd, pick: agent, machine: c.machineId)?.config
                        config = RACore.switchAgent(c, project: defaults, agent: agent)
                    })) {
                        ForEach(agents, id: \.rawValue) { agent in Text(agentName(agent)).tag(agent.rawValue) }
                    }
                    .pickerStyle(.segmented).labelsHidden().frame(width: 208)
                    .accessibilityIdentifier("schedule-cli-picker")
                }
            }
        }
    }
}
