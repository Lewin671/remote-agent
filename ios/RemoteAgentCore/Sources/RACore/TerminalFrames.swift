// What the terminal stream delivered and the screen has yet to show
// (architecture §11 「积压」; Android's term/TerminalFrames.kt). The stream's
// reader puts frames in, the main thread takes them out in batches. When
// more waits than the limits allow the queue closes for good: the caller
// drops the connection and opens another for a fresh snapshot, because
// skipping bytes would leave a wrong screen.
import Foundation
import RAProto
import os

/// One queue per connection: stale readers cannot affect the next connection's accounting.
public final class TerminalFrames: Sendable {
    public enum Offer: Sendable, Equatable {
        /// Kept; a drain is already on its way.
        case queued
        /// Kept; the caller arranges the next drain.
        case schedule
        /// Too much waits: said once, and everything waiting is gone.
        case overflow
        case closed
    }

    /// Bytes received but not yet shown before the stream is restarted.
    public static let maxBacklog = 2 * 1024 * 1024

    private struct State {
        var frames: [PBTerminalFrame] = []
        /// The frames before this one have been taken.
        var head = 0
        var bytes = 0
        var scheduled = false
        var closed = false
    }

    private let maxBytes: Int
    private let maxFrames: Int
    private let state = OSAllocatedUnfairLock(initialState: State())

    public init(maxBytes: Int = TerminalFrames.maxBacklog, maxFrames: Int = 512) {
        self.maxBytes = maxBytes
        self.maxFrames = maxFrames
    }

    public func offer(_ frame: PBTerminalFrame) -> Offer {
        let n = Self.size(frame)
        return state.withLock { s in
            if s.closed { return .closed }
            if s.bytes + n > maxBytes || s.frames.count - s.head >= maxFrames {
                s = State(closed: true)
                return .overflow
            }
            s.frames.append(frame)
            s.bytes += n
            if s.scheduled { return .queued }
            s.scheduled = true
            return .schedule
        }
    }

    /// The next frames in order, about `maxBatchBytes` of them and at least one, and whether more wait.
    public func drain(maxBatchBytes: Int = 64 * 1024) -> (batch: [PBTerminalFrame], more: Bool) {
        state.withLock { s in
            var batch: [PBTerminalFrame] = []
            var n = 0
            while s.head < s.frames.count, batch.isEmpty || n + Self.size(s.frames[s.head]) <= maxBatchBytes {
                let f = s.frames[s.head]
                s.head += 1
                batch.append(f)
                n += Self.size(f)
            }
            s.bytes -= n
            if s.head == s.frames.count {
                s.frames.removeAll(keepingCapacity: true)
                s.head = 0
            }
            s.scheduled = s.head < s.frames.count
            return (batch, s.scheduled)
        }
    }

    public func close() {
        state.withLock { $0 = State(closed: true) }
    }

    private static func size(_ f: PBTerminalFrame) -> Int {
        switch f.kind {
        case .output(let d)?: d.count
        case .snapshot(let s)?: s.data.count
        default: 0
        }
    }
}

/// What a batch of frames does to the screen, in order.
public enum TerminalStep: Sendable, Equatable {
    case output(Data)
    case snapshot(cols: Int, rows: Int, data: Data)
    case closed(String)
}

/// Joins adjacent output so that the emulator is fed once per run of it; snapshots and closes keep their place.
public func terminalSteps(_ batch: [PBTerminalFrame]) -> [TerminalStep] {
    var steps: [TerminalStep] = []
    var output = Data()
    func flush() {
        if output.isEmpty { return }
        steps.append(.output(output))
        output = Data()
    }
    for frame in batch {
        switch frame.kind {
        case .output(let d)?: output.append(d)
        case .snapshot(let s)?:
            flush()
            steps.append(.snapshot(cols: Int(s.cols), rows: Int(s.rows), data: s.data))
        case .closed(let reason)?:
            flush()
            steps.append(.closed(reason.isEmpty ? "终端已关闭" : reason))
        default: flush()
        }
    }
    flush()
    return steps
}

/// The emulator's messages can quote terminal output (a bad OSC 52 payload,
/// an unknown control string): only their size is logged. The size is in
/// UTF-16 units, as Android counts it.
public func terminalLogRedact(_ message: String?) -> String {
    "terminal: emulator message of \(message?.utf16.count ?? 0) chars"
}
