import Foundation
import RACore
import SwiftUI
import Testing
import UIKit
@testable import RemoteAgent

/// Actual native screens with synthetic in-memory context; no account or RPC is used.
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
                let create = AuthoritySetupModel(
                    mode: .create, server: "https://relay.example.com",
                    makeHost: { try StandaloneSecurityHost(store: MemorySecurityStoreV4(), http: AppModel.http, allowPlainHttp: true) })
                let recover = AuthoritySetupModel(
                    mode: .recover, server: "https://relay.example.com",
                    makeHost: { try StandaloneSecurityHost(store: MemorySecurityStoreV4(), http: AppModel.http, allowPlainHttp: true) })
                let screens: [(String, AnyView)] = [
                    ("first-use", AnyView(JoinScreen())),
                    ("create-account", AnyView(AuthoritySetupScreen(flow: create))),
                    ("recover-management", AnyView(AuthoritySetupScreen(flow: recover))),
                    ("delete-account", AnyView(AccountDeletionScreen())),
                    ("retained-identity-denied", AnyView(RetainedIdentityDeniedScreen())),
                    ("removed", AnyView(RemovedScreen())),
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
                // The confirmation guard records security facts rather than translated wording.
                let warning = AppModel.RejoinAdvice.sensitive(authority: true, unresolved: true)
                #expect(warning == .sensitive(authority: true, unresolved: true))
                #expect(warning != .sensitive(authority: true, unresolved: false))
                #expect(warning.text.contains(L10n.securityIosASecurityOperationIsUnfinishedRetryOrVerifyIts()))
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
