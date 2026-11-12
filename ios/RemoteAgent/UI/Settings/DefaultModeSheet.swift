import RACore
import RAProto
import SwiftUI

/// New-project defaults use the same account/model restrictions as project settings.
struct DefaultModeSheet: View {
    let machine: String
    let agent: PBAgent
    let settings: PBDaemonSettings
    let effective: DefaultMode
    let onPick: (Mode?) -> Void
    @Environment(AccountContext.self) private var context
    @Environment(AppModel.self) private var model
    @State private var models: PBListModelsResponse?
    @State private var error = ""
    @State private var attempt = 0

    var body: some View {
        let defaults = settings.defaults(for: agent)
        let current = modeOf(defaults.mode)
        let config = NewConfig(machineId: machine, cwd: "", agent: agent, model: defaults.model)
        let choices = modeChoices(config, models: models, settings: settings)
        SheetHead(L10n.modesTitle())
        ForEach(Array(defaultModeChoices.enumerated()), id: \.offset) { index, mode in
            let choice = choices.first { $0.mode == mode }
            ListItem(
                mode?.text ?? L10n.modelsDefault(),
                desc: mode == nil
                    ? (effective.known
                        ? L10n.settingsIosWhenUnspecifiedUses(value: String(describing: modeLabel(chosen: nil, default: effective, agent: agent, includeDefault: false)))
                        : unknownModeText)
                    : choice?.desc,
                checked: mode == current || (mode == nil && !defaultModeChoices.contains(current)),
                enabled: choice?.enabled ?? true,
                last: index == defaultModeChoices.count - 1
            ) { onPick(mode) }
        }
        if !error.isEmpty {
            Banner(L10n.sessionIosModelsFailed(reason: String(describing: error)), tone: .error, action: L10n.commonRetry()) { attempt += 1 }
        }
        Color.clear.frame(height: 0).task(id: attempt) {
            guard agentInfo(agent).autoMayBeUnavailable else { return }
            do {
                models = try await context.remote.listModels(machine, agent: agent, fresh: !model.isDemo)
                error = ""
            } catch is CancellationError {
            } catch {
                self.error = errorText(error)
            }
        }
    }
}
