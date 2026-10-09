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
