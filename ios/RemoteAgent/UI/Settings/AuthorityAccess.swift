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
        if unresolved { return "安全操作结果尚未核实，请先继续原操作；退出会删除唯一完成材料。" }
        if authority { return "本机是唯一管理端，建议先交接；退出会删除管理密钥，没有恢复码备份将丢失管理权。" }
        return "退出会删除本机密钥，需要重新配对。"
    }
}

struct AddDeviceEntry: View {
    @Environment(AppModel.self) private var app
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    var body: some View {
        let access = AuthorityAccess(state: app.state, me: context.me)
        if access.isAuthority {
            GroupCard { GRow("添加设备", chevron: true, action: true) { nav.open(.addDevice()) } }
        } else { GroupNote(access.addHint) }
    }
}
