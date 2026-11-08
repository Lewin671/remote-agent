import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

/// A repeatable geometry stress case, not a substitute for a device drag.
#if DEBUG
@MainActor
@Suite struct ChatScrollStressTests {
    @Test(.timeLimit(.minutes(1)))
    func tallRowsAtViewportBoundary() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let remote = Demo.remote()
        let state = Demo.state(.everyday, now: 1_791_000_000_000)
        let defaultsName = "chat-boundary-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: defaultsName))
        defer { defaults.removePersistentDomain(forName: defaultsName) }
        let model = AppModel(settings: SettingsStore(defaults: defaults))
        model.showDemo(account: Demo.account, remote: remote, initial: state) {}
        defer { ImageCenter.remove(remote) }
        let context = try #require(model.context)
        let session = try #require(state.sessions["api"])
        let chat = SessionModel()
        let events = SessionSamples.boundaryEvents()
        await chat.show(SessionEvents(events: events, loaded: true))
        let host = UIHostingController(rootView:
            ChatPane(session: session, status: .idle, chat: chat, onTerminal: {})
                .environment(model).environment(context).environment(Navigator()))
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 740)
        window.rootViewController = host
        let previousKey = scene.windows.first { $0.isKeyWindow }
        window.makeKeyAndVisible()
        defer {
            window.isHidden = true
            if let previousKey, !previousKey.isHidden { previousKey.makeKey() }
        }
        try await Task.sleep(for: .milliseconds(500))
        func descendants(_ view: UIView) -> [UIView] { [view] + view.subviews.flatMap(descendants) }
        let scroll = try #require(descendants(host.view).compactMap { $0 as? UIScrollView }
            .max { $0.contentSize.height < $1.contentSize.height })
        // Visit each item once to populate UIKit's measured-size cache before
        // revisiting the same 40k–90k boundaries as the lazy-stack baseline.
        if let collection = scroll as? UICollectionView,
           let source = collection.dataSource as? UICollectionViewDiffableDataSource<Int, String> {
            for id in source.snapshot().itemIdentifiers {
                if let index = source.indexPath(for: id) {
                    collection.scrollToItem(at: index, at: .top, animated: false)
                    collection.layoutIfNeeded()
                }
            }
        }
        // Fix the scroll viewport itself to the reported 609 pt. This is a
        // debug/test geometry trigger, not a user gesture or keyboard test.
        scroll.frame.size.height = 609
        print("BOUNDARY start height=\(scroll.contentSize.height) viewport=\(scroll.bounds.height)")
        Attachment.record("height=\(scroll.contentSize.height), viewport=\(scroll.bounds.height)", named: "boundary-start.txt")
        let start = ChatDiagnosticRing.shared.snapshot().total
        for n in 0..<240 {
            let bottom = max(0, scroll.contentSize.height - scroll.bounds.height)
            // Cross the tall/short realization boundary in both directions,
            // near the reported offsets and throughout the transcript.
            let center = [CGFloat(36_765), 39_000, 88_000, bottom * 0.7][n % 4]
            let y = min(bottom, max(0, center + CGFloat(n % 17 - 8) * 80))
            scroll.setContentOffset(CGPoint(x: 0, y: y), animated: n.isMultiple(of: 13))
            try await Task.sleep(for: .milliseconds(16))
        }
        scroll.setContentOffset(scroll.contentOffset, animated: false)
        try await Task.sleep(for: .milliseconds(500))
        let settled = scroll.contentSize.height
        let before = ChatDiagnosticRing.shared.snapshot().total
        try await Task.sleep(for: .milliseconds(500))
        let snapshot = ChatDiagnosticRing.shared.snapshot()
        print("BOUNDARY end height=\(settled) events=\(snapshot.total - start) idleEvents=\(snapshot.total - before) counters=\(snapshot.counters.map { "\($0.path):\($0.count)" }.joined(separator: ","))")
        Attachment.record(try JSONEncoder().encode(snapshot), named: "boundary-diagnostics.json")
        Attachment.record("events=\(snapshot.total - start), idleEvents=\(snapshot.total - before), height=\(settled)", named: "boundary-end.txt")
        #expect(abs(scroll.contentSize.height - settled) < 0.5)
        #expect(snapshot.total - before < 2_000, "no layout storm after scrolling stops")
        let layouts = snapshot.counters.first { $0.path == ChatDiagnosticPath.layoutBegin.rawValue }?.count ?? 0
        #expect(layouts < 10_000, "the recent five-second buckets must not contain a layout-pass storm")
        #expect(!snapshot.events.contains { $0.path == ChatDiagnosticPath.stall.rawValue })
        await model.logout()
    }
}

#endif
