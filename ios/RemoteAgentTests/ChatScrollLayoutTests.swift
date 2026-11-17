import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

@MainActor
@Suite struct ChatScrollLayoutTests {
    /// Exercises the actual collection-backed chat, including its geometry callbacks. Pure
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
        var events: [PBEvent] = (1...120).map { n in
            .with {
                $0.seq = UInt64(n)
                $0.kind = n.isMultiple(of: 2)
                    ? .agentMessage(.with { $0.text = String(repeating: "A reply with **variable height** and `code`.\n\n", count: n % 7 + 1) })
                    : .userMessage(.with { $0.text = "Message \(n)" })
            }
        }
        events[80].origin.scheduledRunID = "target-run"
        await chat.show(SessionEvents(events: Array(events.suffix(20)), stateEvents: [], historyPartial: true))
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
        func find(_ view: UIView) -> UIScrollView? {
            if let scroll = view as? UIScrollView { return scroll }
            return view.subviews.lazy.compactMap { find($0) }.first
        }
        try await waitForLayout {
            guard let scroll = find(host.view) else { return false }
            return scroll.bounds.height > 0 && scroll.contentSize.height > scroll.bounds.height
        }
        let scroll = try #require(find(host.view))
        for n in 0..<10 {
            let bottom = max(0, scroll.contentSize.height - scroll.bounds.height)
            let fraction = CGFloat(n % 5) / 4
            scroll.setContentOffset(CGPoint(x: 0, y: bottom * fraction), animated: false)
            if n < 4 {
                await chat.show(SessionEvents(events: Array(events.suffix(20 + (n + 1) * 25)), stateEvents: [], historyPartial: true))
            } else {
                events.append(.with {
                    $0.seq = UInt64(events.count + 1)
                    $0.agentMessage.text = "Streaming reply \(n)\n\n" + String(repeating: "More text.\n\n", count: n % 5 + 1)
                })
                await chat.show(SessionEvents(events: events, loaded: true))
            }
            var frames = 0
            try await waitForLayout { frames += 1; return frames >= 2 }
        }
        #expect(chat.loaded && chat.joins > 0)
        #expect(scroll.contentSize.height.isFinite && scroll.contentSize.height > scroll.bounds.height)
        let collection = try #require(scroll as? UICollectionView)
        let source = try #require(collection.dataSource as? UICollectionViewDiffableDataSource<Int, String>)
        // The seq fallback deliberately cannot match: this must find the
        // scheduled-run user row and position it at the viewport's top.
        context.sessionStore.jumpToScheduledRun(session.id, runID: "target-run", seq: 999_999)
        try await waitForLayout {
            guard let index = source.indexPath(for: "e81"),
                  let frame = collection.layoutAttributesForItem(at: index)?.frame
            else { return false }
            return abs(frame.minY - collection.contentOffset.y) < 1
        }
        let index = try #require(source.indexPath(for: "e81"))
        let frame = try #require(collection.layoutAttributesForItem(at: index)).frame
        #expect(abs(frame.minY - collection.contentOffset.y) < 1)
        await model.logout()
    }

    /// A baseline for the recurring watchdog investigation, not a reproduction
    /// of the freeze. Include the rows that measure themselves (plan previews
    /// and Markdown tables), an open dock, and changes to the viewport. Moving
    /// UIScrollView here does not exercise touch tracking or deceleration.
    @Test(.timeLimit(.minutes(1)), arguments: [(DynamicTypeSize.large, ColorScheme.light), (.accessibility3, .dark)])
    func mixedRowsSettleAfterHistoryStreamingAndViewportChanges(size: DynamicTypeSize, scheme: ColorScheme) async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let remote = Demo.remote()
        let state = Demo.state(.everyday, now: 1_791_000_000_000)
        let defaultsName = "mixed-chat-scroll-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: defaultsName))
        defer { defaults.removePersistentDomain(forName: defaultsName) }
        let model = AppModel(settings: SettingsStore(defaults: defaults))
        model.showDemo(account: Demo.account, remote: remote, initial: state) {}
        defer { ImageCenter.remove(remote) }
        let context = try #require(model.context)
        let session = try #require(state.sessions["api"])
        let chat = SessionModel()
        var events: [PBEvent] = (1...80).map { n in
            .with {
                $0.seq = UInt64(n)
                switch n % 4 {
                case 0:
                    $0.planProposed.text = String(repeating: "A plan that wraps beyond the preview. ", count: n % 9 + 6)
                case 1:
                    $0.userMessage.text = "Message \(n)"
                case 2:
                    $0.agentMessage.text = "| Column | Value |\n| --- | --- |\n| A wrapping table cell with `code` | \(n) |\n\n"
                        + String(repeating: "A reply with **variable height**.\n\n", count: n % 7 + 1)
                default:
                    $0.toolCall = .with {
                        $0.callID = "call-\(n)"; $0.title = "echo example"
                        $0.kind = .command; $0.status = "completed"
                    }
                }
            }
        }
        // The open question dock also has a measured, capped scroll body.
        events.append(.with {
            $0.seq = 81
            $0.questionAsked = .with {
                $0.questionID = "question"
                $0.questions = [.with { $0.text = String(repeating: "Choose an option. ", count: 30) }]
            }
        })
        let live = PBLiveSnapshot.with {
            $0.session.id = session.id
            $0.session.lastSeq = 81
            $0.actions = Array(events.suffix(1))
        }
        func show(_ visible: [PBEvent], loaded: Bool = false) async {
            var snapshot = SessionEvents(events: visible, loaded: loaded, stateEvents: [], historyPartial: !loaded)
            snapshot.live = live
            await chat.show(snapshot)
        }
        await show(Array(events.suffix(21)))
        let host = UIHostingController(rootView:
            ChatPane(session: session, status: .idle, chat: chat, onTerminal: {})
                .environment(model).environment(context).environment(Navigator())
                .environment(\.dynamicTypeSize, size).preferredColorScheme(scheme))
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
        func descendants(_ view: UIView) -> [UIView] {
            [view] + view.subviews.flatMap(descendants)
        }
        try await waitForLayout {
            let scrolls = descendants(host.view).compactMap { $0 as? UIScrollView }
            guard let scroll = scrolls.max(by: { $0.contentSize.height < $1.contentSize.height }) else { return false }
            return scroll.bounds.height > 0 && scroll.contentSize.height > scroll.bounds.height
        }
        let scrolls = descendants(host.view).compactMap { $0 as? UIScrollView }
        let scroll = try #require(scrolls.max { $0.contentSize.height < $1.contentSize.height })
        #expect(chat.rows.contains { if case .plan = $0.item { return true }; return false })
        #expect(!chat.derived.questions.isEmpty)
        for n in 0..<12 {
            let bottom = max(0, scroll.contentSize.height - scroll.bounds.height)
            scroll.setContentOffset(CGPoint(x: 0, y: bottom * CGFloat(n % 5) / 4), animated: false)
            if n < 4 {
                await show(Array(events.suffix(21 + (n + 1) * 15)))
            } else {
                // Grow the same last row: following must also work when IDs and
                // row count stay unchanged, not just when new rows are appended.
                if n == 4 { events.append(.with { $0.seq = 82; $0.agentMessage.text = "Streaming reply" }) }
                events[events.count - 1].agentMessage.text += "\n\nMore streaming text with `code`."
                await show(events, loaded: true)
            }
            if n.isMultiple(of: 4) {
                window.frame.size = n.isMultiple(of: 8)
                    ? CGSize(width: 700, height: 390) : CGSize(width: 390, height: 844)
            }
            var frames = 0
            try await waitForLayout { frames += 1; return frames >= 2 }
        }
        #expect(chat.loaded && chat.joins > 0)
        // With all external inputs stopped, size and position must settle across frames.
        var lastSize = CGSize.zero
        var lastOffset = CGPoint.zero
        var stableFrames = 0
        try await waitForLayout {
            let size = scroll.contentSize
            let offset = scroll.contentOffset
            if abs(size.height - lastSize.height) < 0.5 && abs(offset.y - lastOffset.y) < 0.5 {
                stableFrames += 1
            } else {
                stableFrames = 0
            }
            lastSize = size
            lastOffset = offset
            return stableFrames >= 3
        }
        #expect(scroll.contentSize.height.isFinite && scroll.contentSize.height > scroll.bounds.height)
        await model.logout()
    }
}
