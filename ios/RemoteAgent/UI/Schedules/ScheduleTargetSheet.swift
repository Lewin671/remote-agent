import RACore
import RAProto
import SwiftUI

struct ScheduleTargetSheet: View {
    let machine: String
    let target: ScheduleTarget
    let onSave: (ScheduleTarget) -> Void
    let onClose: () -> Void
    @Environment(AppModel.self) private var model
    @State private var config: NewConfig?
    @State private var picker: ScheduleConfigPage?
    init(machine: String, target: ScheduleTarget, onSave: @escaping (ScheduleTarget) -> Void, onClose: @escaping () -> Void) {
        self.machine = machine; self.target = target; self.onSave = onSave; self.onClose = onClose
        let mode: Mode? = ["ask": .ask, "auto": .auto, "full": .full][target.mode ?? ""]
        _config = State(initialValue: NewConfig(machineId: machine, cwd: target.cwd ?? "", agent: target.agent == "claude" ? .claude : .codex, model: target.model ?? "", effort: target.effort ?? "", mode: mode))
    }
    var body: some View {
        VStack(spacing: 0) {
            SheetHead("在哪运行", action: "保存", onAction: save)
            if let c = config {
                GRow("项目", value: projectName(c.cwd), chevron: true) { picker = .project }
                GRow("CLI", value: agentName(c.agent), chevron: true) { picker = .agent }
                GRow("权限", value: effectiveModeText(chosen: c.mode, default: defaultModeOf(model.state.machines[machine], agent: c.agent)), chevron: true, valueColor: settingsModeRed(c, default: defaultModeOf(model.state.machines[machine], agent: c.agent)) ? Theme.red : nil) { picker = .mode }
                GRow("模型", value: c.model.isEmpty ? "默认" : c.model, chevron: true) { picker = .model }
            }
            Hint("任务保存在这台电脑上；更改只影响之后的运行。")
            TextAction("取消", alignment: .center, action: onClose)
        }.kitSheet(item: $picker, size: .large) { page in
            ScheduleConfigSheet(page: page, config: $config, fixedMachine: machine) { picker = nil }
        }
    }
    private func save() {
        guard let c = config, c.machineId == machine else { return }
        var next = target
        next.cwd = c.cwd; next.agent = c.agent == .claude ? "claude" : "codex"
        next.model = c.model.isEmpty ? nil : c.model; next.effort = c.effort.isEmpty ? nil : c.effort
        next.mode = c.mode.map { $0 == .full ? "full" : $0 == .auto ? "auto" : "ask" }
        onSave(next); onClose()
    }
}
