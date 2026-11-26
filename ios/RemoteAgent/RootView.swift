import RACore
import RAProto
import SwiftUI

/// The root of the window: routes strictly on Go `clientcore`'s `AppRootViewModel.stage`
/// (matching Android `MainActivity.kt` / Web `App.tsx`).
struct RootView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        content
            .modifier(ExitConfirmOverlay())
            .task(id: model.screens.appRoot.stage) {
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
        let root = model.screens.appRoot
        switch root.stage {
        case .unspecified, .loading, .UNRECOGNIZED:
            StartupShell()
        case .bootstrapError:
            let err = root.bootstrapError
            Page {
                VStack(spacing: 12) {
                    Spacer()
                    ErrorRow(err.message.rendered().or(VaultError.unreadable.localizedDescription)) {
                        if err.canRetry {
                            Task { await model.coreHost.appRootRetryBootstrap() }
                        }
                    }
                    if err.canReset {
                        Button(L10n.securityExitAndStartOver(), role: .destructive) {
                            Task { await model.coreHost.exitRequest(intent: .setupReset) }
                        }
                    }
                    Spacer()
                }
            }
        case .onboarding:
            JoinScreen()
        case .retainedDenied:
            RetainedIdentityDeniedScreen()
        case .removed:
            RemovedScreen()
        case .ready:
            if let context = model.context {
                // A new account is a new stack: nothing of the one before stays on it.
                MainView(context: context, start: LaunchDriver.startRoute)
                    .id(ObjectIdentifier(context))
            } else {
                StartupShell()
            }
        }
    }
}

/// Core-driven exit/reset confirmation dialog rendered at root from `ExitConfirmView`.
/// Tapping confirm ONLY dispatches `ExitConfirmAction(confirm: true)` and never cancels first.
struct ExitConfirmOverlay: ViewModifier {
    @Environment(AppModel.self) private var model

    func body(content: Content) -> some View {
        let vm = model.screens.exitConfirm
        let presented = vm.step == .awaitingConfirm || vm.step == .executing || vm.step == .failed
        let title = vm.intent == .rejoinPairing
            ? L10n.securityDeleteLocalKeysAndRejoin()
            : L10n.securityExitAndDeleteLocalKeys()
        let confirmLabel = vm.intent == .rejoinPairing
            ? L10n.securityRejoin()
            : L10n.securityExit()
        let warning = vm.warningBody.rendered()
        let fullText = vm.showRejoinDeletesCacheNote && !warning.isEmpty
            ? warning + L10n.errorsIosThisDeletesTheAccountKeysAndCacheOnThis()
            : warning
        content.confirm(
            isPresented: presented,
            title: title,
            text: fullText,
            confirm: confirmLabel,
            danger: true,
            busy: vm.busy || vm.step == .executing,
            errorText: vm.step == .failed ? vm.errorText.rendered() : "",
            canDismiss: vm.step != .executing,
            onCancel: {
                Task { await model.coreHost.exitConfirm(false) }
            },
            onConfirm: {
                Task { await model.coreHost.exitConfirm(true) }
            }
        )
    }
}

/// Legacy bridge button kept until Commit 2 migrates `JoinScreen`/`AuthoritySetupScreen`.
struct SetupDiscardButton: View {
    let discard: @MainActor () async -> Void
    @Environment(AppModel.self) private var model
    var body: some View {
        Button(L10n.securityExitAndStartOver(), role: .destructive) {
            Task { await model.coreHost.exitRequest(intent: .setupReset) }
        }
    }
}
