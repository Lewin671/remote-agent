import SwiftUI

/// What the Keychain says of this device's account.
private enum AccountState: Equatable {
    case checking
    case none
    case joined
    /// The Keychain could not be asked; the system's reason.
    case failed(String)
}

/// The root of the window: a placeholder until the screens exist. It says
/// whether this device has joined an account, which only the Keychain knows.
struct RootView: View {
    let vault: Vault
    @State private var state = AccountState.checking

    var body: some View {
        content.task { await check() }
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

    private var main: some View {
        Page {
            switch state {
            case .checking:
                Color.clear
            case .none:
                NoAccountView()
            case .joined:
                // The screens proper are not part of the shell.
                PageNote("已加入账户").frame(maxHeight: .infinity)
            case .failed(let why):
                VStack(spacing: 0) {
                    Spacer()
                    ErrorRow(why) { Task { await check() } }
                    Spacer()
                }
            }
        }
    }

    private func check() async {
        let vault = vault
        // The Keychain call blocks; it is made off the main actor.
        let result = await Task.detached { Result { try vault.hasAccount() } }.value
        switch result {
        case .success(let has): state = has ? .joined : .none
        case .failure(let error): state = .failed(error.localizedDescription)
        }
    }
}

/// Shown while this device has not joined an account: what to do, and which build this is.
struct NoAccountView: View {
    var body: some View {
        VStack(spacing: 0) {
            Spacer(minLength: 24)
            AppMark.view(size: 44, color: Theme.blueBg).padding(.bottom, 20)
            EmptyState(
                "未加入账户",
                text: "在已加入的设备上打开「设置 › 添加设备」，扫二维码，或把 8 位配对码填在这台设备上。")
            Spacer(minLength: 24)
            Text("remote-agent \(AppInfo.versionLine)").type(T.meta, Theme.muted)
                .multilineTextAlignment(.center)
                .padding(.horizontal, Theme.Space.page).padding(.bottom, 12)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

#Preview("未加入账户") {
    Page { NoAccountView() }
}
