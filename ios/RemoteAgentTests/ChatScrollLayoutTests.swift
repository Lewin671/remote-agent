import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

@MainActor
@Suite struct ChatScrollLayoutTests {
    /// Exercises the actual lazy chat, including its geometry callbacks. Pure
    /// tracker tests cannot detect a layout that never returns to the run loop.
    @Test(.timeLimit(.minutes(1)))
    func scrollingWhileHistoryAndRepliesArrive() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let remote = Demo.remote()
        let state = Demo.state(.everyday, now: 1_791_000_000_000)
        let defaultsName = "chat-scroll-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: defaultsName))
        defer { defaults.removePersistentDomain(forName: defaultsName) }
        let model = AppModel(settings: SettingsStore(defaults: defaults))
        model.showDemo(account: Demo.account, remote: remote, initial: state) {}
        defer { ImageCenter.remove(remote) }
        let context = try #require(model.context)
        let session = try #require(state.sessions["api"])
        let chat = SessionModel()
        var events: [PBEvent] = (1...600).map { n in
            .with {
                $0.seq = UInt64(n)
                $0.kind = n.isMultiple(of: 2)
                    ? .agentMessage(.with { $0.text = String(repeating: "A reply with **variable height** and `code`.\n\n", count: n % 13 + 1) })
                    : .userMessage(.with { $0.text = "Message \(n)" })
            }
        }
        await chat.show(SessionEvents(events: Array(events.suffix(60)), stateEvents: [], historyPartial: true))
        let host = UIHostingController(rootView:
            ChatPane(session: session, status: .idle, chat: chat, onTerminal: {})
                .environment(model).environment(context).environment(Navigator()))
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        window.rootViewController = host
        let previousKey = scene.windows.first { $0.isKeyWindow }
        window.makeKeyAndVisible()
        defer {
            window.isHidden = true
            if let previousKey, !previousKey.isHidden { previousKey.makeKey() }
        }
        try await Task.sleep(for: .milliseconds(300))
        func find(_ view: UIView) -> UIScrollView? {
            if let scroll = view as? UIScrollView { return scroll }
            return view.subviews.lazy.compactMap { find($0) }.first
        }
        let scroll = try #require(find(host.view))
        for n in 0..<80 {
            let bottom = max(0, scroll.contentSize.height - scroll.bounds.height)
            let fraction = CGFloat(n % 11) / 10
            scroll.setContentOffset(CGPoint(x: 0, y: bottom * fraction), animated: false)
            if n < 10 {
                await chat.show(SessionEvents(events: Array(events.suffix(60 + (n + 1) * 54)), stateEvents: [], historyPartial: true))
            } else {
                events.append(.with {
                    $0.seq = UInt64(events.count + 1)
                    $0.agentMessage.text = "Streaming reply \(n)\n\n" + String(repeating: "More text.\n\n", count: n % 9 + 1)
                })
                await chat.show(SessionEvents(events: events, loaded: true))
            }
            try await Task.sleep(for: .milliseconds(20))
        }
        #expect(chat.loaded && chat.joins > 0)
        #expect(scroll.contentSize.height.isFinite && scroll.contentSize.height > scroll.bounds.height)
        await model.logout()
    }
}
