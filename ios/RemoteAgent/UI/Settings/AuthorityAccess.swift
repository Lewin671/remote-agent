import RACore
import SwiftUI

/// The signed directory chooses roles; platform metadata never grants management.
struct AuthorityAccess {
    let state: RemoteState
    let me: String
    var isClient: Bool { state.devices[me]?.kind == .client && !state.removed }
    var isAuthority: Bool { isClient && !me.isEmpty && state.authorityDeviceId == me }
    var name: String {
        clientName(state.devices[state.authorityDeviceId], id: state.authorityDeviceId, among: state.devices)
    }
    var addHint: String { "要添加电脑或客户端，在管理端（\(name)）上打开「设置 › 添加设备」。" }
    var emptyHint: String { "还没有电脑。在管理端（\(name)）上打开「设置 › 添加设备」，把电脑加进来。" }
    static func exitAdvice(authority: Bool, unresolved: Bool) -> String {
        if unresolved { return "还有未完成的安全操作。请先重试或在管理端核实结果；退出后，这台手机将无法继续原操作。" }
        if authority { return "本机是唯一管理端，建议先交接；退出会删除管理密钥，没有恢复码备份将丢失管理权。" }
        return "退出会删除本机密钥，需要重新配对。"
    }
}

struct AddDeviceEntry: View {
    var grouped = true
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    var body: some View {
        let access = AuthorityAccess(state: app.state, me: context.me)
        if access.isAuthority {
            if grouped { GroupCard { row } } else { row }
        } else { GroupNote(access.addHint) }
    }
    private var row: some View {
        GRow("添加设备", action: true, icon: Lucide.plus) { nav.open(.addDevice()) }
    }

}
