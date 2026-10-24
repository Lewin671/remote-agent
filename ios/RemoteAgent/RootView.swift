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
            .task(id: model.stage) {
                await LaunchDriver.stageChanged(model)
                #if DEBUG
                await ScheduleAcceptance.bootstrap(model)
                #endif
            }
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
            JoinScreen().id(model.joinGeneration)
        case .ready:
            if model.removed {
                RemovedScreen()
            } else if let context = model.context {
                // A new account is a new stack: nothing of the one before stays on it.
                MainView(context: context, start: model.recoveryRotationRequired ? .recoveryCode : LaunchDriver.startRoute).id(ObjectIdentifier(context))
            }
        }
    }
}

/// Available before sign-in; no account navigation or remote environment is required.
struct SetupDiscardButton: View {
    let discard: @MainActor () async -> Void
    @Environment(AppModel.self) private var model
    @State private var asking = false
    @State private var advice = ""
    @State private var error = ""
    var body: some View {
        if !error.isEmpty { Banner(error, tone: .error) }
        Button("退出并重新开始", role: .destructive) {
            do {
                if let warning = try model.rejoinAdvice() { advice = warning; asking = true }
                else { Task { await discard() } }
            } catch { self.error = errorText(error) }
        }
            .confirmationDialog("退出并删除本机密钥？", isPresented: $asking, titleVisibility: .visible) {
                Button("确认退出", role: .destructive) {
                    Task {
                        do {
                            guard try model.rejoinAdvice() == advice else { throw UserError("安全操作状态已变化，请重新选择退出。") }
                            await discard()
                        } catch { self.error = errorText(error) }
                    }
                }
                Button("取消", role: .cancel) {}
            } message: {
                Text(advice)
            }
    }
}
