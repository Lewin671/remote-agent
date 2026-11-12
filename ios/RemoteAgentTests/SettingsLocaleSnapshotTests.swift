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
        let defaults = UserDefaults(suiteName: "settings-snapshot-\(UUID())")!
        let settings = SettingsStore(defaults: defaults)
        let model = AppModel(settings: settings)
        model.showDemo(account: Demo.account, remote: Demo.remote(), initial: Demo.state(.everyday)) {}
        let context = try #require(model.context)
        let directory = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        for locale in ["zh-CN", "en"] {
            try await MessageLocale.$overrideForTesting.withValue(locale) {
                let create = AuthoritySetupModel(mode: .create, bootstrap: model.authorityBootstrap, server: "https://relay.example.com")
                let recover = AuthoritySetupModel(mode: .recover, bootstrap: model.authorityBootstrap, server: "https://relay.example.com")
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
                            Text(errorText(DeviceSecurity.Failure.lease))
                            ForEach(["20 MB", "pixels", "source missing", "decode", "storage full", "offline", "opaque"], id: \.self) { raw in
                                Text(rawImageProblem(raw).message)
                            }
                        }.padding(16)
                    }))
                ]
                for (name, content) in screens {
                    let root = content.environment(model).environment(context).environment(settings).environment(Navigator())
                        .environment(\.locale, Locale(identifier: MessageLocale.appleTags[locale]!))
                        .environment(\.colorScheme, .light)
                    let host = UIHostingController(rootView: root)
                    let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
                    let previous = scene.windows.first(where: \.isKeyWindow)
                    let window = UIWindow(windowScene: scene)
                    window.frame = CGRect(x: 0, y: 0, width: 390, height: 1000)
                    window.rootViewController = host
                    window.makeKeyAndVisible()
                    host.view.frame = window.bounds
                    host.view.setNeedsLayout(); host.view.layoutIfNeeded()
                    try await Task.sleep(for: .milliseconds(500))
                    host.view.setNeedsLayout(); host.view.layoutIfNeeded()
                    let renderer = UIGraphicsImageRenderer(size: window.bounds.size)
                    let picture = renderer.image { _ in window.drawHierarchy(in: window.bounds, afterScreenUpdates: true) }
                    try #require(picture.pngData()).write(to: directory.appendingPathComponent("settings-\(name)-\(locale).png"))
                    window.isHidden = true; previous?.makeKeyAndVisible()
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
