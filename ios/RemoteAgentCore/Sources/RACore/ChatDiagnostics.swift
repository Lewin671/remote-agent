// Local, numeric evidence for a main-thread stall. This module has no UI,
// transport, message IDs or text. File encoding and I/O never run in append.
import Foundation
import os

// Keep raw IDs contiguous and append new paths; renumbering requires a new report format.
public enum ChatDiagnosticPath: UInt16, Codable, CaseIterable, Sendable {
    case turnBegin, turnEnd, layoutBegin, layoutEnd
    case chatBody, chatEnter, chatLeave, content, floor, sight, row, rowHidden, paneHeight
    case keyboardInset, keyboardGuide, keyboardLag, dockBody
    case cutShown, cutWhole, cutBinding, codeShown, codeWhole, codeOne, codeTwo, scheduleBlock
    case tableViewport, tableMore, tableSize, tablePlace
    case workSchedule, workExecute, workCancel, followRequest, followExecute
    case proxyEnd, proxyTop, proxyBack, proxyScheduled, positionY, positionID, scrollStop
    case dragging, keep, kept, fresh, away, count, prepend, joins, keepTarget
    case modelRebuild, modelApply, stall, recovered
    case listLayout, keyboardLayout, collapseToggle, leave, jump
}

public struct ChatDiagnosticEvent: Codable, Sendable, Equatable {
    public var sequence: UInt64 = 0
    public var time: UInt64 = 0
    public var turn: UInt64 = 0
    public var layout: UInt64 = 0
    public var entity: UInt64 = 0
    public var a: Double = 0
    public var b: Double = 0
    public var c: Double = 0
    public var d: Double = 0
    public var path: UInt16 = 0
    public var mainThread: UInt8 = 0
    /// Bits mark non-finite inputs, which JSON cannot represent.
    public var nonFinite: UInt8 = 0
}

public struct ChatDiagnosticSnapshot: Codable, Sendable {
    public struct Counter: Codable, Sendable {
        public let path: UInt16
        public let count: UInt64
    }
    public let time: UInt64
    public let total: UInt64
    public let capacity: Int
    public let mainLayout: UInt64
    public let events: [ChatDiagnosticEvent]
    /// Current and previous four monotonic second buckets, even after wraparound.
    public let counterStartSecond: UInt64
    public let counters: [Counter]
}

/// Preallocated POD storage. The lock covers only scalar updates or bounded
/// memory copies; snapshot allocation/encoding happens outside the lock.
public final class ChatDiagnosticRing: @unchecked Sendable {
    public static let shared = ChatDiagnosticRing()
    public static let nanosecondsPerSecond: UInt64 = 1_000_000_000
    private static let pathCount = ChatDiagnosticPath.allCases.count
    private struct Bucket { var second: UInt64 = .max; var count: UInt64 = 0 }
    private struct State {
        var used = 0; var next = 0
        var total: UInt64 = 0; var turn: UInt64 = 0; var lastTime: UInt64 = 0
        var layout: UInt64 = 0; var activeLayout: UInt64 = 0; var identity: UInt64 = 0
    }
    public struct Layout: Sendable { fileprivate let id: UInt64; fileprivate let parent: UInt64; fileprivate let main: Bool }
    private let capacity: Int
    private let timeOrigin: UInt64
    private let events: UnsafeMutablePointer<ChatDiagnosticEvent>
    private let buckets: UnsafeMutablePointer<Bucket>
    private let state = OSAllocatedUnfairLock(initialState: State())

    public init(capacity: Int = 4096, timeOrigin: UInt64 = DispatchTime.now().uptimeNanoseconds) {
        precondition(capacity > 0)
        self.capacity = capacity; self.timeOrigin = timeOrigin
        events = .allocate(capacity: capacity); events.initialize(repeating: ChatDiagnosticEvent(), count: capacity)
        buckets = .allocate(capacity: Self.pathCount * 8)
        buckets.initialize(repeating: Bucket(), count: Self.pathCount * 8)
    }
    deinit {
        events.deinitialize(count: capacity); events.deallocate()
        buckets.deinitialize(count: Self.pathCount * 8); buckets.deallocate()
    }

    /// Export only elapsed time since this ring was created, never device uptime.
    public func relativeTime(_ absolute: UInt64) -> UInt64 { absolute >= timeOrigin ? absolute - timeOrigin : 0 }

    public func identity() -> UInt64 {
        state.withLock { s in s.identity &+= 1; return s.identity }
    }
    @inline(__always)
    public func record(_ path: ChatDiagnosticPath, entity: UInt64 = 0,
                       _ a: Double = 0, _ b: Double = 0, _ c: Double = 0, _ d: Double = 0) {
        record(path, at: DispatchTime.now().uptimeNanoseconds, entity: entity, a, b, c, d)
    }
    public func record(_ path: ChatDiagnosticPath, at time: UInt64, entity: UInt64 = 0,
                       _ a: Double = 0, _ b: Double = 0, _ c: Double = 0, _ d: Double = 0) {
        let main = Thread.isMainThread
        state.withLock { s in append(path, time, entity, a, b, c, d, main, nil, &s) }
    }
    private func append(_ path: ChatDiagnosticPath, _ time: UInt64, _ entity: UInt64,
                        _ a: Double, _ b: Double, _ c: Double, _ d: Double, _ main: Bool, _ layout: UInt64?, _ s: inout State) {
        let time = max(relativeTime(time), s.lastTime)
        s.lastTime = time; s.total &+= 1
        events[s.next] = ChatDiagnosticEvent(
            sequence: s.total, time: time, turn: s.turn, layout: layout ?? (main ? s.activeLayout : 0),
            entity: entity, a: a.isFinite ? a : 0, b: b.isFinite ? b : 0, c: c.isFinite ? c : 0, d: d.isFinite ? d : 0,
            path: path.rawValue, mainThread: main ? 1 : 0, nonFinite: (a.isFinite ? 0 : 1) | (b.isFinite ? 0 : 2) | (c.isFinite ? 0 : 4) | (d.isFinite ? 0 : 8))
        s.next = (s.next + 1) % capacity; s.used = min(capacity, s.used + 1)
        let second = time / Self.nanosecondsPerSecond
        let index = Int(second % 8) * Self.pathCount + Int(path.rawValue)
        if buckets[index].second != second { buckets[index] = Bucket(second: second, count: 1) }
        else { buckets[index].count &+= 1 }
    }
    public func turn(begin: Bool) {
        let now = DispatchTime.now().uptimeNanoseconds
        state.withLock { s in
            if begin { s.turn &+= 1 }
            append(begin ? .turnBegin : .turnEnd, now, 0, 0, 0, 0, 0, true, nil, &s)
        }
    }
    public func beginLayout(_ scope: ChatDiagnosticPath, entity: UInt64 = 0) -> Layout {
        let now = DispatchTime.now().uptimeNanoseconds
        let main = Thread.isMainThread
        return state.withLock { s in
            let parent = s.activeLayout; s.layout &+= 1
            if main { s.activeLayout = s.layout }
            append(.layoutBegin, now, entity, Double(scope.rawValue), 0, 0, 0, main, s.layout, &s)
            return Layout(id: s.layout, parent: parent, main: main)
        }
    }
    public func endLayout(_ layout: Layout) {
        let now = DispatchTime.now().uptimeNanoseconds
        state.withLock { s in
            append(.layoutEnd, now, 0, 0, 0, 0, 0, layout.main, layout.id, &s)
            if layout.main { s.activeLayout = layout.parent }
        }
    }
    /// A scope around existing geometry reads/custom Layout methods, not a new
    /// SwiftUI layout container or a hook into framework-private implementations.
    @inline(__always)
    public func measure<T>(_ scope: ChatDiagnosticPath, entity: UInt64 = 0, _ body: () -> T) -> T {
        let layout = beginLayout(scope, entity: entity)
        defer { endLayout(layout) }
        return body()
    }
    public func snapshot(at now: UInt64 = DispatchTime.now().uptimeNanoseconds) -> ChatDiagnosticSnapshot {
        let copy = UnsafeMutablePointer<ChatDiagnosticEvent>.allocate(capacity: capacity)
        let counts = UnsafeMutablePointer<Bucket>.allocate(capacity: Self.pathCount * 8)
        defer { copy.deallocate(); counts.deallocate() }
        // Scratch pointers stay in this synchronous locked copy.
        let captured = state.withLockUnchecked { s -> State in
            let first = s.used == capacity ? s.next : 0
            let head = min(s.used, capacity - first)
            copy.initialize(from: events + first, count: head)
            if head < s.used { (copy + head).initialize(from: events, count: s.used - head) }
            counts.initialize(from: buckets, count: Self.pathCount * 8)
            return s
        }
        defer { copy.deinitialize(count: captured.used); counts.deinitialize(count: Self.pathCount * 8) }
        let now = max(relativeTime(now), captured.lastTime)
        let second = now / Self.nanosecondsPerSecond
        let start = second > 4 ? second - 4 : 0
        let counters = ChatDiagnosticPath.allCases.map { path in
            var n: UInt64 = 0
            for bucket in 0..<8 {
                let cell = counts[bucket * Self.pathCount + Int(path.rawValue)]
                if cell.second >= start && cell.second <= second { n &+= cell.count }
            }
            return ChatDiagnosticSnapshot.Counter(path: path.rawValue, count: n)
        }
        return ChatDiagnosticSnapshot(time: now, total: captured.total, capacity: capacity, mainLayout: captured.activeLayout,
            events: Array(UnsafeBufferPointer(start: copy, count: captured.used)),
            counterStartSecond: start, counters: counters)
    }
}

public struct ChatStallReport: Codable, Sendable {
    public let format: Int
    public let createdAtUnixMilliseconds: UInt64
    public let startedAt: UInt64
    public let detectedAt: UInt64
    public var recoveredAt: UInt64?
    public var duration: UInt64?
    public let snapshot: ChatDiagnosticSnapshot
}

/// Polled by one background serial queue. Returning a ping ID lets tests hold
/// the reply without blocking/sleeping on a real main thread.
public struct ChatStallRecorder {
    private let ring: ChatDiagnosticRing
    private let threshold: UInt64
    private let save: (ChatStallReport) -> Void
    private var next: UInt64 = 0
    private var pending: (id: UInt64, at: UInt64)?
    private var report: ChatStallReport?

    public init(ring: ChatDiagnosticRing, threshold: UInt64 = 2_000_000_000,
                save: @escaping (ChatStallReport) -> Void) {
        self.ring = ring; self.threshold = threshold; self.save = save
    }
    public mutating func poll(at now: UInt64, unixMilliseconds: UInt64) -> UInt64? {
        if let pending {
            if report == nil && now >= pending.at && now - pending.at > threshold {
                ring.record(.stall, at: now, Double(now - pending.at))
                let captured = ChatStallReport(format: 1, createdAtUnixMilliseconds: unixMilliseconds,
                    startedAt: ring.relativeTime(pending.at), detectedAt: ring.relativeTime(now), snapshot: ring.snapshot(at: now))
                report = captured; save(captured)
            }
            return nil
        }
        next &+= 1; pending = (next, now)
        return next
    }
    public mutating func acknowledge(_ id: UInt64, at now: UInt64) {
        guard pending?.id == id else { return }
        finish(at: now)
    }
    /// Called after the main thread handles leaving the foreground, which is
    /// also evidence that it is responsive. Stale queued replies are ignored.
    public mutating func suspend(at now: UInt64) { finish(at: now) }
    private mutating func finish(at now: UInt64) {
        if var captured = report {
            captured.recoveredAt = ring.relativeTime(now)
            captured.duration = captured.recoveredAt! >= captured.startedAt ? captured.recoveredAt! - captured.startedAt : 0
            ring.record(.recovered, at: now, Double(captured.duration ?? 0))
            save(captured)
        }
        pending = nil; report = nil
    }
}

/// Only immutable configuration; callers serialize save on their diagnostics
/// queue. The directory is local to this app's data container and not backed up.
public struct ChatStallFiles: Sendable {
    public let directory: URL
    public init(directory: URL = URL.applicationSupportDirectory.appendingPathComponent("Diagnostics", isDirectory: true)) {
        self.directory = directory
    }
    public func save(_ report: ChatStallReport) throws {
        let manager = FileManager.default
        try manager.createDirectory(at: directory, withIntermediateDirectories: true)
        var excluded = directory
        var resources = URLResourceValues(); resources.isExcludedFromBackup = true
        try excluded.setResourceValues(resources)
        #if os(iOS)
        try manager.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication], ofItemAtPath: directory.path)
        #endif
        let name = "chat-stall-\(report.createdAtUnixMilliseconds)-\(report.startedAt).json"
        try JSONEncoder().encode(report).write(to: directory.appendingPathComponent(name), options: .atomic)
        let files = try manager.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
            .filter { $0.lastPathComponent.hasPrefix("chat-stall-") && $0.pathExtension == "json" }
            .sorted { numbers($0).lexicographicallyPrecedes(numbers($1)) }
        for old in files.dropLast(5) { try manager.removeItem(at: old) }
    }
    private func numbers(_ url: URL) -> [UInt64] {
        url.deletingPathExtension().lastPathComponent.split(separator: "-").compactMap { UInt64($0) }
    }
}
