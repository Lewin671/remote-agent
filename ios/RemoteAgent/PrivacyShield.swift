// Covers the app's windows whenever a scene is not the one in use: in the
// app switcher, under Control Centre, and for the snapshot the system takes
// when the app goes to the background. Decrypted content is then never in
// a picture the system keeps.
//
// The cover is a window of its own above the app's, not a view in the
// SwiftUI tree: sheets and full-screen covers are presented over the root
// view, and a view inside it would leave them showing.
import SwiftUI
import UIKit

@MainActor
final class PrivacyShield {
    static let shared = PrivacyShield()

    private var covers: [ObjectIdentifier: UIWindow] = [:]
    private var observers: [NSObjectProtocol] = []

    /// Starts watching the scenes. Called once, when the app starts.
    func install() {
        guard observers.isEmpty else { return }
        let center = NotificationCenter.default
        // Inactive comes before background, so the cover is up before the snapshot is taken.
        observers.append(center.addObserver(forName: UIScene.willDeactivateNotification, object: nil, queue: .main) { note in
            let scene = note.object as? UIWindowScene
            MainActor.assumeIsolated { if let scene { PrivacyShield.shared.cover(scene) } }
        })
        observers.append(center.addObserver(forName: UIScene.didActivateNotification, object: nil, queue: .main) { note in
            let scene = note.object as? UIWindowScene
            MainActor.assumeIsolated { if let scene { PrivacyShield.shared.uncover(scene) } }
        })
        observers.append(center.addObserver(forName: UIScene.didDisconnectNotification, object: nil, queue: .main) { note in
            let scene = note.object as? UIWindowScene
            MainActor.assumeIsolated { if let scene { PrivacyShield.shared.covers[ObjectIdentifier(scene)] = nil } }
        })
    }

    func isCovering(_ scene: UIWindowScene) -> Bool { covers[ObjectIdentifier(scene)] != nil }

    func cover(_ scene: UIWindowScene) {
        guard covers[ObjectIdentifier(scene)] == nil else { return }
        let window = UIWindow(windowScene: scene)
        // Above alerts, so that nothing of the app is over it.
        window.windowLevel = .alert + 1
        let host = UIHostingController(rootView: PrivacyCover())
        // The cover follows the appearance chosen in settings, as the window under it does.
        host.overrideUserInterfaceStyle = scene.windows.first?.overrideUserInterfaceStyle ?? .unspecified
        window.rootViewController = host
        window.isUserInteractionEnabled = false
        window.isHidden = false
        covers[ObjectIdentifier(scene)] = window
    }

    func uncover(_ scene: UIWindowScene) {
        guard let window = covers.removeValue(forKey: ObjectIdentifier(scene)) else { return }
        if UIAccessibility.isReduceMotionEnabled {
            window.isHidden = true
            return
        }
        UIView.animate(withDuration: 0.15) {
            window.alpha = 0
        } completion: { _ in
            window.isHidden = true
        }
    }
}

/// What stands in for the app's content: its mark on the card surface.
struct PrivacyCover: View {
    var body: some View {
        ZStack {
            Theme.card.ignoresSafeArea()
            AppMark.view(size: 56, color: Theme.blueBg)
        }
        .accessibilityHidden(true)
    }
}

#Preview("隐私遮罩") {
    PrivacyCover()
}
