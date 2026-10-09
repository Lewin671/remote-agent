import RACore
import SwiftUI

/// 会话: the timeline of sessions, the app's first page (Android's HomeScreen).
///
/// A placeholder: the screen itself is not written yet. It says what the
/// sync engine holds, which is enough to see that an account syncs and
/// that sample data arrived. Whoever writes the screen replaces this
/// file's body.
struct HomeScreen: View {
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav

    var body: some View {
        let state = model.state
        let waiting = state.sessions.values.filter { needsAttention($0, machine: state.machines[$0.machineId]) }.count
        let ended = state.sessions.values.filter(exited).count
        Page {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    LargeTitle("会话") { nav.open(.pickProject) }
                    ConnectionBar()
                    PageNote("这个页面还没有做")
                    SectionLabel("同步到的内容")
                    Row44 { Text("电脑").type(T.body, Theme.fg); Spacer(); Text("\(state.machines.values.filter(\.online).count) 台在线，共 \(state.machines.count) 台").type(T.sm, Theme.muted) }
                    Row44 { Text("会话").type(T.body, Theme.fg); Spacer(); Text("\(state.sessions.count) 个，\(waiting) 个需要处理，\(ended) 个已结束").type(T.sm, Theme.muted) }
                    Row44 { Text("客户端").type(T.body, Theme.fg); Spacer(); Text("\(state.devices.values.filter { $0.kind == .client }.count) 个").type(T.sm, Theme.muted) }
                }
            }
        }
    }
}
