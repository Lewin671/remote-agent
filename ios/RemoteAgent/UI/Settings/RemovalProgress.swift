import RACore
import SwiftUI

struct RemovalProgress: View {
    let state: RemoteState
    var target: String?
    @Environment(AccountContext.self) private var context
    @State private var retry = ActionRunner()
    var body: some View {
        ForEach(
            state.removals.filter { target == nil || $0.targets.contains(target!) }, id: \.operation
        ) { op in
            GroupLabel(op.phase == "complete" ? "已移除" : op.phase == "pending" ? "正在撤销" : "尚未确认")
            GroupCard {
                if op.phase == "complete" {
                    GRow("已验证全部保留电脑的撤销回执")
                } else {
                    GRow(op.phase == "pending" ? "等待保留电脑自动同步" : "请求尚未获得管理电脑确认")
                    ForEach(op.waiting, id: \.self) { id in GRow(machineLabel(state, id: id)) }
                    GRow("重试", enabled: !retry.running) {
                        retry.run { try await context.remote.retryDeviceRemoval(op.operation) }
                    }
                }
            }
            if !op.error.isEmpty { PageNote("暂时无法确认，请重试。尚未显示移除成功。") }
        }
    }
}
