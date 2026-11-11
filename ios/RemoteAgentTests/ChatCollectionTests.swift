import RACore
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

@MainActor
@Suite struct ChatCollectionTests {
    @MainActor private final class Harness {
        let tracker = ChatScrollTracker()
        let controller: ChatCollectionController
        let window: UIWindow
        let previous: UIWindow?
        var ids: [String] = []
        var heights: [String: CGFloat] = [:]
        var joins = 0
        var inserted = 0
        var interactions: [Bool] = []

        init() throws {
            controller = ChatCollectionController(tracker: tracker)
            tracker.controller = controller
            let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
            previous = scene.windows.first { $0.isKeyWindow }
            window = UIWindow(windowScene: scene)
            window.frame = CGRect(x: 0, y: 0, width: 390, height: 609)
            window.rootViewController = controller
            window.makeKeyAndVisible()
            controller.loadViewIfNeeded()
        }

        var list: UICollectionView { controller.collection }
        var source: UICollectionViewDiffableDataSource<Int, String> {
            list.dataSource as! UICollectionViewDiffableDataSource<Int, String>
        }
        var gap: CGFloat { max(0, list.contentSize.height - list.bounds.height - list.contentOffset.y) }
        func update() {
            let items = ids.map { id in
                ChatCollectionItem(id) { Text(id).frame(maxWidth: .infinity).frame(height: heights[id] ?? 80) }
            }
            apply(items)
        }
        func apply(_ items: [ChatCollectionItem], environment: EnvironmentValues = EnvironmentValues()) {
            controller.update(ChatCollection(items: items, env: ChatEnv(), tracker: tracker, ready: true,
                                              joins: joins, prepended: inserted, count: items.count,
                                              interaction: { [weak self] in self?.interactions.append($0) }),
                              environment: environment)
        }
        func y(_ id: String) throws -> CGFloat {
            let index = try #require(source.indexPath(for: id))
            return try #require(list.layoutAttributesForItem(at: index)).frame.minY - list.contentOffset.y
        }
        func close() {
            controller.stopObserving()
            window.isHidden = true
            if let previous, !previous.isHidden { previous.makeKey() }
        }
        func wait() async throws { try await Task.sleep(for: .milliseconds(180)) }
    }

    @Test func opensAtEndAndShortChatsStartAtTop() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let h = try Harness()
        defer { h.close() }
        h.ids = ["short"]
        h.update()
        try await h.wait()
        #expect(abs(h.list.contentOffset.y) < 0.5)
        #expect(h.list.keyboardDismissMode == .interactive)
        h.ids += (0..<80).map { "row-\($0)" }
        h.update()
        try await h.wait()
        #expect(h.gap < 1)
        #expect(h.tracker.scroll.following)
    }

    @Test func scheduledTargetQueuedBeforeMountWinsTheInitialEnd() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let h = try Harness()
        defer { h.close() }
        h.tracker.controller = nil
        h.tracker.leave()
        h.tracker.toRow("row-40")
        h.tracker.controller = h.controller
        h.ids = (0..<80).map { "row-\($0)" }
        h.update()
        try await h.wait()
        #expect(abs(try h.y("row-40")) < 1)
        #expect(!h.tracker.scroll.following)
    }

    @Test func followsGrowthButPreservesReadingPositionAndCountsFreshRows() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let h = try Harness()
        defer { h.close() }
        h.ids = (0..<80).map { "row-\($0)" }
        h.update()
        try await h.wait()
        #expect(h.gap < 1)
        h.heights["row-79"] = 400
        h.update()
        try await h.wait()
        #expect(h.gap < 1, "same-ID streaming row follows its new height")
        h.tracker.leave()
        h.controller.toRow("row-40")
        try await h.wait()
        let y = try h.y("row-40")
        let offset = h.list.contentOffset.y
        h.ids += ["new-1", "new-2"]
        h.update()
        try await h.wait()
        #expect(abs(try h.y("row-40") - y) < 1)
        #expect(abs(h.list.contentOffset.y - offset) < 1)
        #expect(h.tracker.fresh == 2 && h.tracker.away)
        h.tracker.jump()
        h.controller.toEnd()
        try await h.wait()
        #expect(h.gap < 1 && h.tracker.fresh == 0 && !h.tracker.away)
    }

    /// Someone else's rows leave a reader far up where they are; sending a message (the composer's `onSent`, which
    /// is the same request for the end as the 回到最新 pill) takes them to the end and the list follows again.
    @Test func sendingFromFarUpReturnsToTheEndAndFollowsTheAnswer() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let h = try Harness()
        defer { h.close() }
        h.ids = (0..<80).map { "row-\($0)" }
        h.update()
        try await h.wait()
        h.tracker.leave()
        h.controller.toRow("row-10")
        try await h.wait()
        let offset = h.list.contentOffset.y
        h.ids += ["other-1"]
        h.update()
        try await h.wait()
        #expect(abs(h.list.contentOffset.y - offset) < 1 && h.gap > 1_000, "a message from elsewhere does not pull the reader down")
        // The send: the end is asked for before the message's own row exists, as in `SessionComposer.send`.
        h.tracker.jump()
        h.controller.toEnd()
        h.ids += ["mine-1"]
        h.update()
        try await h.wait()
        #expect(h.gap < 1 && h.tracker.scroll.following && h.tracker.fresh == 0, "the sent message is in view")
        h.heights["mine-1"] = 300
        h.ids += ["answer-1"]
        h.update()
        try await h.wait()
        #expect(h.gap < 1, "and the answer is followed")
    }

    @Test func prependKeepsASurvivingRowAndDoesNotCountHistoryAsFresh() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let h = try Harness()
        defer { h.close() }
        h.ids = (0..<80).map { "row-\($0)" }
        h.update()
        try await h.wait()
        h.tracker.leave()
        h.controller.toRow("row-40")
        try await h.wait()
        let y = try h.y("row-40")
        let older = (0..<30).map { "older-\($0)" }
        for (i, id) in older.enumerated() { h.heights[id] = i.isMultiple(of: 3) ? 1_600 : 40 }
        h.ids.insert(contentsOf: older, at: 0)
        h.joins += 1
        h.inserted = older.count
        h.update()
        try await h.wait()
        #expect(abs(try h.y("row-40") - y) < 1)
        #expect(h.tracker.fresh == 0 && !h.tracker.scroll.following && !h.tracker.keeping)
    }

    /// Delegate callbacks deliberately exercise ownership without synthesizing
    /// a touch: real drag/deceleration is covered separately in simulator UI QA.
    @Test func dragAndDecelerationWinAndNewGestureCancelsQueuedJump() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let h = try Harness()
        defer { h.close() }
        h.ids = (0..<50).map { "row-\($0)" }
        h.update()
        try await h.wait()
        h.controller.scrollViewWillBeginDragging(h.list)
        let offset = h.list.contentOffset.y
        h.heights["row-49"] = 92
        h.update()
        h.controller.scrollViewDidEndDragging(h.list, willDecelerate: true)
        try await h.wait()
        #expect(h.tracker.dragging)
        #expect(abs(h.list.contentOffset.y - offset) < 1)
        h.controller.scrollViewDidEndDecelerating(h.list)
        try await h.wait()
        #expect(!h.tracker.dragging && h.gap < 1)
        #expect(h.interactions == [true, false])
        h.controller.toRow("row-5")
        h.controller.scrollViewWillBeginDragging(h.list)
        try await h.wait()
        #expect(h.gap < 1, "the new gesture canceled the queued programmatic target")
        h.controller.scrollViewDidEndDragging(h.list, willDecelerate: false)
    }

    @MainActor private final class AppearanceProbe { var sizeClass: UserInterfaceSizeClass? }
    private struct EnvironmentProbe: View {
        let probe: AppearanceProbe
        @Environment(\.horizontalSizeClass) private var sizeClass
        var body: some View {
            Text("Environment").frame(height: 80)
                .onChange(of: sizeClass, initial: true) { _, value in probe.sizeClass = value }
        }
    }

    @Test func unchangedVisibleMessageReceivesTheCurrentSwiftUIEnvironment() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let h = try Harness()
        defer { h.close() }
        let probe = AppearanceProbe()
        let row = ChatRow(item: .agent(.init(key: "probe", text: "Example")), blocks: [])
        let items = [ChatCollectionItem("probe", row: row) { EnvironmentProbe(probe: probe) }]
        var environment = EnvironmentValues()
        environment.horizontalSizeClass = .compact
        h.apply(items, environment: environment)
        try await h.wait()
        #expect(probe.sizeClass == .compact)
        environment.horizontalSizeClass = .regular
        h.apply(items, environment: environment)
        try await h.wait()
        #expect(probe.sizeClass == .regular, "a stable row ID and value must not freeze its hosted environment")
    }

    @MainActor @Observable final class Height { var value: CGFloat = 80 }
    private struct ExpandingRow: View {
        let height: Height
        var body: some View { Text("Expandable row").frame(maxWidth: .infinity).frame(height: height.value) }
    }

    @Test func swiftUIStateResizesCellAndWideLayoutStaysCentered() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let h = try Harness()
        defer { h.close() }
        let height = Height()
        h.ids = (0..<20).map { "row-\($0)" }
        let items = h.ids.map { id in
            ChatCollectionItem(id) { ExpandingRow(height: id == "row-19" ? height : Height()) }
        }
        h.apply(items)
        try await h.wait()
        let before = h.list.contentSize.height
        height.value = 600
        try await h.wait()
        #expect(h.list.contentSize.height > before + 500, "SwiftUI state invalidates the cell without a snapshot update")
        #expect(h.gap < 1)
        h.tracker.leave()
        h.controller.toRow("row-15")
        try await h.wait()
        let y = try h.y("row-15")
        height.value = 900
        try await h.wait()
        #expect(abs(try h.y("row-15") - y) < 1)
        h.window.frame.size.width = 1_024
        h.window.layoutIfNeeded()
        try await h.wait()
        let path = try #require(h.source.indexPath(for: "row-15"))
        let frame = try #require(h.list.layoutAttributesForItem(at: path)).frame
        #expect(abs(frame.width - 760) < 1)
        #expect(abs(frame.midX - h.list.bounds.midX) < 1)
    }
}
