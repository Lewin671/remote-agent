import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

/// Relative simulator evidence. State changes and scroll offsets are debug
/// triggers; rows, cell sizing, virtualization and drawing are production code.
@MainActor
@Suite struct ToolExpansionPerformanceTests {
    @MainActor private final class Frames: NSObject {
        struct Sample: Codable {
            let gapMS: Double
            let budgetMS: Double
            let height: Double
            let rowHeight: Double
        }
        let list: UICollectionView
        var samples: [Sample] = []
        var last: Double = 0
        var target = IndexPath(item: 200, section: 0)
        var link: CADisplayLink?
        init(_ list: UICollectionView) { self.list = list }
        func start() {
            last = 0
            samples = []
            link = CADisplayLink(target: self, selector: #selector(tick))
            link?.add(to: .main, forMode: .common)
        }
        @objc func tick(_ link: CADisplayLink) {
            if last > 0 {
                samples.append(Sample(gapMS: (link.timestamp - last) * 1_000,
                                      budgetMS: link.duration * 1_000, height: list.contentSize.height,
                                      rowHeight: Double(list.cellForItem(at: target)?.bounds.height ?? 0)))
            }
            last = link.timestamp
        }
        func stop() { link?.invalidate(); link = nil }
    }

    @Test(.timeLimit(.minutes(2)))
    func commandAndGroupComparedWithScrolling() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let tracker = ChatScrollTracker()
        let controller = ChatCollectionController(tracker: tracker)
        tracker.controller = controller
        let open = ChatOpen()
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let previous = scene.windows.first { $0.isKeyWindow }
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 402, height: 874)
        window.rootViewController = controller
        window.makeKeyAndVisible()
        defer {
            controller.stopObserving()
            window.isHidden = true
            previous?.makeKey()
        }
        let command = (1...8).map { "printf 'long command line \($0) with wrapping content for measurement\\n'" }.joined(separator: "\n")
        let calls: [PBToolCall] = (0..<3).map { n in .with {
            $0.callID = "call-\(n)"; $0.kind = .command
            $0.title = "printf command \(n)"; $0.input = command; $0.status = "completed"
        } }
        let rows = (0..<400).map { n in
            ChatRow(item: n.isMultiple(of: 2)
                ? .tool(.init(key: "row-\(n)", call: calls[0]))
                : .tools(.init(key: "row-\(n)", calls: calls)), blocks: [])
        }
        let items = rows.map { row in
            ChatCollectionItem(row.id, row: row) {
                ChatRowView(row: row, env: ChatEnv()).equatable().environment(open)
            }
        }
        controller.update(ChatCollection(items: items, env: ChatEnv(), tracker: tracker, ready: true,
                                         joins: 0, prepended: 0, count: rows.count, interaction: { _ in }),
                          environment: EnvironmentValues())
        try await Task.sleep(for: .milliseconds(500))
        controller.toRow("row-200")
        try await Task.sleep(for: .milliseconds(500))
        let list = controller.collection!
        let frames = Frames(list)
        struct Result: Codable {
            let action: String
            let iteration: Int
            let elapsedMS: Double
            let layouts: Int
            let beforeHeight: Double
            let afterHeight: Double
            let heightSteps: Int
            let lateFrames: Int
            let samples: [Frames.Sample]
        }
        var results: [Result] = []
        for action in ["command", "group", "group-command", "scroll"] {
            if action == "group-command" {
                open.toggle("row-201")
                controller.toRow("row-201")
                try await Task.sleep(for: .milliseconds(450))
            }
            frames.target = IndexPath(item: action == "group" || action == "group-command" ? 201 : 200, section: 0)
            for n in 0..<8 {
                let ring = ChatDiagnosticRing.shared
                let before = ring.snapshot().total
                let start = CACurrentMediaTime()
                let beforeHeight = list.contentSize.height
                frames.start()
                switch action {
                case "command":
                    // Same transaction as ExpandableToolRow.toggle().
                    withAnimation(Motion.reduced ? nil : Motion.enter()) { open.toggle("command/row-200") }
                case "group": open.toggle("row-201")
                case "group-command":
                    withAnimation(Motion.reduced ? nil : Motion.enter()) { open.toggle("command/row-201/call-0") }
                default:
                    list.setContentOffset(CGPoint(x: 0, y: list.contentOffset.y + (n.isMultiple(of: 2) ? 200 : -200)), animated: true)
                }
                try await Task.sleep(for: .milliseconds(450))
                frames.stop()
                let snapshot = ring.snapshot()
                let layouts = snapshot.events.filter {
                    $0.sequence > before && $0.path == ChatDiagnosticPath.layoutBegin.rawValue && $0.a == Double(ChatDiagnosticPath.listLayout.rawValue)
                }.count
                let heights = Set(frames.samples.map { Int(($0.height * 2).rounded()) })
                let late = frames.samples.filter { $0.gapMS > $0.budgetMS * 2.5 }.count
                let result = Result(action: action, iteration: n, elapsedMS: (CACurrentMediaTime() - start) * 1_000,
                                    layouts: layouts, beforeHeight: beforeHeight, afterHeight: list.contentSize.height,
                                    heightSteps: heights.count, lateFrames: late, samples: frames.samples)
                results.append(result)
                print("TOOL_PERF action=\(action) iteration=\(n) layouts=\(layouts) heightSteps=\(heights.count) frames=\(frames.samples.count) late=\(late) worstMS=\(frames.samples.map(\.gapMS).max() ?? 0)")
                if action != "scroll" {
                    #expect(!frames.samples.isEmpty)
                    #expect(heights.count <= 2, "expansion must not resize the long list on every display frame")
                    #expect(frames.samples.contains { $0.rowHeight > 0 }, "measure a visible production row")
                }
            }
        }
        Attachment.record(try JSONEncoder().encode(results), named: "tool-expansion-frames.json")
        Attachment.record(try JSONEncoder().encode(ChatDiagnosticRing.shared.snapshot()), named: "tool-expansion-diagnostics.json")
    }
}
