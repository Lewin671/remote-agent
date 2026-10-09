import Testing
import UIKit

@testable import RemoteAgent

@MainActor
@Suite struct PrivacyShieldTests {
    @Test func aSceneThatIsNotInUseIsCoveredAndUncoveredWhenItIsAgain() throws {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let shield = PrivacyShield.shared
        shield.uncover(scene)
        let before = scene.windows.count
        shield.cover(scene)
        #expect(shield.isCovering(scene))
        let cover = try #require(scene.windows.max { $0.windowLevel < $1.windowLevel })
        #expect(scene.windows.count == before + 1)
        // Above every window of the app, alerts included, and it takes no touches.
        #expect(cover.windowLevel > .alert)
        #expect(!cover.isHidden && !cover.isUserInteractionEnabled)
        // Covering twice does not stack a second window.
        shield.cover(scene)
        #expect(scene.windows.count == before + 1)
        shield.uncover(scene)
        #expect(!shield.isCovering(scene))
    }

    @Test func theSystemsNoticeOfLeavingCoversTheScene() async throws {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let shield = PrivacyShield.shared
        shield.install()
        shield.uncover(scene)
        NotificationCenter.default.post(name: UIScene.willDeactivateNotification, object: scene)
        #expect(shield.isCovering(scene))
        NotificationCenter.default.post(name: UIScene.didActivateNotification, object: scene)
        #expect(!shield.isCovering(scene))
    }
}
