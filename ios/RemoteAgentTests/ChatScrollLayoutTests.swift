import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

@MainActor
@Observable
private final class SessionHolder {
    var vm: PBSessionViewModel

    init(vm: PBSessionViewModel) {
        self.vm = vm
    }
}

private struct ChatPaneTestHost: View {
    let sessionID: String
    var holder: SessionHolder

    var body: some View {
        ChatPane(
            sessionID: sessionID,
            vm: holder.vm,
            onTerminal: {},
            onLoadEarlier: {},
            onToggleGroup: { _ in }
        )
    }
}

private func sessionViewModel(
    sessionID: String,
    items: [PBSessionTimelineItem],
    totalCount: Int,
    loaded: Bool,
    dock: PBSessionDockView? = nil
) -> PBSessionViewModel {
    let firstSeq = items.first?.seq ?? 0
    return .with {
        $0.sessionID = sessionID
        $0.header = .with {
            $0.found = true
            $0.machineID = "m1"
            $0.computerOnline = true
            $0.agent = .claude
        }
        $0.timeline = .with {
            $0.items = items
            $0.windowFirstSeq = firstSeq
            $0.totalItems = Int32(totalCount)
            $0.loaded = loaded
            $0.hasEarlier_p = !loaded && firstSeq > 1
            $0.canLoadEarlier = !loaded && firstSeq > 1
            $0.historyPartial = !loaded
        }
        if let dock {
            $0.dock = dock
        }
    }
}

@MainActor
@Suite struct ChatScrollLayoutTests {
    /// Exercises the actual collection-backed chat, including its geometry callbacks. Pure
    /// tracker tests cannot detect a layout that never returns to the run loop.
    @Test(.timeLimit(.minutes(1)))
    func scrollingWhileHistoryAndRepliesArrive() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let remote = Demo.remote()
        let world = Demo.world(.everyday, now: 1_791_000_000_000)
        let defaultsName = "chat-scroll-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: defaultsName))
        defer { defaults.removePersistentDomain(forName: defaultsName) }
        let model = AppModel(settings: SettingsStore(defaults: defaults))
        model.showDemo(account: Demo.account, remote: remote, world: world, events: [:])
        defer { model.closeSyncForTest() }
        let context = try #require(model.context)
        let session = try #require(world.sessions["api"])
        let sessionID = session.sessionID
        var items: [PBSessionTimelineItem] = (1...120).map { n in
            .with {
                $0.id = "e\(n)"
                $0.seq = UInt64(n)
                if n.isMultiple(of: 2) {
                    $0.agentMessage = .with {
                        $0.markdown = String(repeating: "A reply with **variable height** and `code`.\n\n", count: n % 7 + 1)
                    }
                } else {
                    $0.userMessage = .with {
                        $0.text = "Message \(n)"
                        if n == 81 { $0.scheduledRunID = "target-run" }
                    }
                }
            }
        }
        let holder = SessionHolder(
            vm: sessionViewModel(sessionID: sessionID, items: Array(items.suffix(20)), totalCount: items.count, loaded: false)
        )
        let host = UIHostingController(rootView:
            ChatPaneTestHost(sessionID: sessionID, holder: holder)
                .environment(model).environment(context).environment(Navigator()))
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        window.rootViewController = host
        let previousKey = scene.windows.first { $0.isKeyWindow }
        window.makeKeyAndVisible()
        defer {
            window.isHidden = true
            window.rootViewController = nil
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
                holder.vm = sessionViewModel(
                    sessionID: sessionID,
                    items: Array(items.suffix(20 + (n + 1) * 25)),
                    totalCount: items.count,
                    loaded: false
                )
            } else {
                let seq = UInt64(items.count + 1)
                items.append(.with {
                    $0.id = "e\(seq)"
                    $0.seq = seq
                    $0.agentMessage = .with {
                        $0.markdown = "Streaming reply \(n)\n\n" + String(repeating: "More text.\n\n", count: n % 5 + 1)
                    }
                })
                holder.vm = sessionViewModel(sessionID: sessionID, items: items, totalCount: items.count, loaded: true)
            }
            var frames = 0
            try await waitForLayout { frames += 1; return frames >= 2 }
        }
        #expect(scroll.contentSize.height.isFinite && scroll.contentSize.height > scroll.bounds.height)
        let collection = try #require(scroll as? UICollectionView)
        let source = try #require(collection.dataSource as? UICollectionViewDiffableDataSource<Int, String>)
        // The seq fallback deliberately cannot match: this must find the
        // scheduled-run user row and position it at the viewport's top.
        context.sessionStore.jumpToScheduledRun(sessionID, runID: "target-run", seq: 999_999)
        try await waitForLayout {
            guard let index = source.indexPath(for: "e81"),
                  let frame = collection.layoutAttributesForItem(at: index)?.frame
            else { return false }
            return abs(frame.minY - collection.contentOffset.y) < 1
        }
        let index = try #require(source.indexPath(for: "e81"))
        let frame = try #require(collection.layoutAttributesForItem(at: index)).frame
        #expect(abs(frame.minY - collection.contentOffset.y) < 1)
        window.isHidden = true
        window.rootViewController = nil
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
        let world = Demo.world(.everyday, now: 1_791_000_000_000)
        let defaultsName = "mixed-chat-scroll-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: defaultsName))
        defer { defaults.removePersistentDomain(forName: defaultsName) }
        let model = AppModel(settings: SettingsStore(defaults: defaults))
        model.showDemo(account: Demo.account, remote: remote, world: world, events: [:])
        defer { model.closeSyncForTest() }
        let context = try #require(model.context)
        let session = try #require(world.sessions["api"])
        let sessionID = session.sessionID
        var items: [PBSessionTimelineItem] = (1...80).map { n in
            .with {
                $0.id = "m\(n)"
                $0.seq = UInt64(n)
                switch n % 4 {
                case 0:
                    $0.plan = .with {
                        $0.markdown = String(repeating: "A plan that wraps beyond the preview. ", count: n % 9 + 6)
                    }
                case 1:
                    $0.userMessage = .with { $0.text = "Message \(n)" }
                case 2:
                    $0.agentMessage = .with {
                        $0.markdown = "| Column | Value |\n| --- | --- |\n| A wrapping table cell with `code` | \(n) |\n\n"
                            + String(repeating: "A reply with **variable height**.\n\n", count: n % 7 + 1)
                    }
                default:
                    $0.toolCall = .with {
                        $0.callID = "call-\(n)"
                        $0.title = "echo example"
                        $0.status = .ok
                    }
                }
            }
        }
        // The open question dock also has a measured, capped scroll body.
        let questionDock = PBSessionDockView.with {
            $0.kind = .question
            $0.itemID = "question"
            $0.title = .with { $0.literal = "Question" }
            $0.question = .with {
                $0.questionID = "question"
                $0.totalQuestions = 1
                $0.question = .with {
                    $0.questionText = String(repeating: "Choose an option. ", count: 30)
                    $0.header = "Option"
                }
            }
        }
        let holder = SessionHolder(
            vm: sessionViewModel(sessionID: sessionID, items: Array(items.suffix(21)), totalCount: items.count, loaded: false, dock: questionDock)
        )
        let host = UIHostingController(rootView:
            ChatPaneTestHost(sessionID: sessionID, holder: holder)
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
            window.rootViewController = nil
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
        for n in 0..<12 {
            let bottom = max(0, scroll.contentSize.height - scroll.bounds.height)
            scroll.setContentOffset(CGPoint(x: 0, y: bottom * CGFloat(n % 5) / 4), animated: false)
            if n < 4 {
                holder.vm = sessionViewModel(
                    sessionID: sessionID,
                    items: Array(items.suffix(21 + (n + 1) * 15)),
                    totalCount: items.count,
                    loaded: false,
                    dock: questionDock
                )
            } else {
                // Grow the same last row: following must also work when IDs and
                // row count stay unchanged, not just when new rows are appended.
                if n == 4 {
                    items.append(.with {
                        $0.id = "m82"
                        $0.seq = 82
                        $0.agentMessage = .with { $0.markdown = "Streaming reply" }
                    })
                }
                items[items.count - 1].agentMessage.markdown += "\n\nMore streaming text with `code`."
                holder.vm = sessionViewModel(sessionID: sessionID, items: items, totalCount: items.count, loaded: true, dock: questionDock)
            }
            if n.isMultiple(of: 4) {
                window.frame.size = n.isMultiple(of: 8)
                    ? CGSize(width: 700, height: 390) : CGSize(width: 390, height: 844)
            }
            var frames = 0
            try await waitForLayout { frames += 1; return frames >= 2 }
        }
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
        window.isHidden = true
        window.rootViewController = nil
        await model.logout()
    }
}
