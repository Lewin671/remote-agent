import RACore
import SwiftUI

/// The root of the window: which of the app's few whole-screen states is
/// showing. It follows `AppModel.stage`, as MainActivity.kt follows RaApp:
/// nothing until the account has been read, the reason when it could not
/// be, the join flow without one, and with one the app itself, or only the
/// notice once the server has turned this device away.
struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        content
            .task(id: model.stage) { await LaunchDriver.stageChanged(model) }
    }

    @ViewBuilder private var content: some View {
        #if DEBUG
        if let page = Gallery.requested {
            GalleryRoot(page: page)
        } else {
            main
        }
        #else
        main
        #endif
    }

    @ViewBuilder private var main: some View {
        switch model.stage {
        case .loading:
            Page { Color.clear }
        case .failed(let why):
            Page {
                VStack(spacing: 0) {
                    Spacer()
                    ErrorRow(why) { model.loadAccount() }
                    SetupDiscardButton { await model.logout() }
                    Spacer()
                }
            }
        case .join:
            JoinScreen()
        case .ready:
            if model.removed {
                RemovedScreen()
            } else if let context = model.context {
                // A new account is a new stack: nothing of the one before stays on it.
                MainView(context: context, start: LaunchDriver.startRoute).id(ObjectIdentifier(context))
            }
        }
    }
}

/// Available before sign-in; no account navigation or remote environment is required.
struct SetupDiscardButton: View {
    let discard: @MainActor () async -> Void
    @State private var asking = false
    var body: some View {
        Button("退出并重新开始", role: .destructive) { asking = true }
            .confirmationDialog("退出并删除本机密钥？", isPresented: $asking, titleVisibility: .visible) {
                Button("确认退出", role: .destructive) { Task { await discard() } }
                Button("取消", role: .cancel) {}
            } message: {
                Text("本机账户及未完成的注册和安全操作会被清除，需要重新配对。服务器上的会话和档案不会删除；结果未明的操作可能已经成功，请在管理端核实。")
            }
    }
}
