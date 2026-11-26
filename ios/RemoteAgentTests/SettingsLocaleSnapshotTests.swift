import Foundation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit
@testable import RemoteAgent

/// Actual native screens with synthetic in-memory Core view models; no account or RPC is used.
@MainActor @Suite struct SettingsLocaleSnapshotTests {
    @Test func firstUseSecurityAndLocalErrorsInBothLanguages() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let suite = "settings-snapshot-\(UUID())"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let settings = SettingsStore(defaults: defaults)
        let model = AppModel(settings: settings)
        model.showDemo(account: Demo.account, remote: Demo.remote(), world: Demo.world(.everyday))
        let context = try #require(model.context)
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let previous = scene.windows.first(where: \.isKeyWindow)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 1000)
        window.makeKeyAndVisible()
        defer {
            model.closeSyncForTest()
            window.isHidden = true
            previous?.makeKeyAndVisible()
        }
        for locale in ["zh-CN", "en"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                let createVM = PBOnboardingViewModel.with {
                    $0.mode = .create
                    $0.canBack = true
                    $0.create = .with {
                        $0.step = .form
                        $0.serverInput = "https://relay.example.com"
                        $0.canSubmit = true
                    }
                }
                let recoverVM = PBOnboardingViewModel.with {
                    $0.mode = .recovery
                    $0.canBack = true
                    $0.recovery = .with {
                        $0.phase = .form
                        $0.serverInput = "https://relay.example.com"
                        $0.canPrepare = true
                    }
                }
                let screens: [(String, AnyView)] = [
                    ("first-use", AnyView(JoinScreen())),
                    ("create-account", AnyView(CreateAccountScreen(vm: createVM))),
                    ("recover-management", AnyView(EmergencyRecoveryScreen(vm: recoverVM))),
                    ("delete-account", AnyView(AccountDeletionScreen())),
                    ("retained-identity-denied", AnyView(RetainedIdentityDeniedScreen())),
                    ("removed", AnyView(RemovedScreen())),
                    ("account-security", AnyView(AccountSecurityScreen())),
                    ("recovery-code", AnyView(AccountSecurityScreen(initialSubpage: .rotateRecovery))),
                    ("handoff", AnyView(HandoffScreen())),
                    ("add-device", AnyView(AddDeviceScreen())),
                    ("local-errors", AnyView(ScrollView {
                        VStack(alignment: .leading, spacing: 16) {
                            Text(L10n.securityAccountSecurity())
                            Text(errorText(SecurityFailure.lease))
                            Text(L10n.imageThisImageCouldNotBeProcessed())
                        }.padding(16)
                    }))
                ]
                for (_, content) in screens {
                    let root = content.environment(model).environment(context).environment(settings).environment(Navigator())
                        .environment(\.locale, Locale(identifier: MessageLocale.appleTags[locale]!))
                        .environment(\.colorScheme, .light)
                    let host = UIHostingController(rootView: root)
                    window.rootViewController = host
                    host.view.frame = window.bounds
                    host.view.setNeedsLayout(); host.view.layoutIfNeeded()
                    let fitted = host.sizeThatFits(in: CGSize(width: 390, height: 2000))
                    #expect(fitted.width <= 390.5 && fitted.height > 0)
                }
            }
        }
    }
}

@Suite struct NativePermissionLocaleTests {
    @Test func usageDescriptionsAreAvailableFromTheAppBundle() throws {
        for key in ["NSCameraUsageDescription", "NSPhotoLibraryAddUsageDescription"] {
            let value = try #require(Bundle.main.object(forInfoDictionaryKey: key) as? String)
            #expect(!value.isEmpty)
            for tag in ["en", "zh-Hans"] {
                let path = try #require(Bundle.main.path(forResource: tag, ofType: "lproj"))
                let bundle = try #require(Bundle(path: path))
                #expect(bundle.localizedString(forKey: key, value: nil, table: "InfoPlist") != key)
            }
        }
    }
}
