import Foundation
import os

/// Owns logical stream tracking over the relay WebSocket path;
/// Core owns route selection, command lifecycle, retries, and outcome classification.
final class Link: Sendable {
    let relay: RelayPath
    private struct Streams {
        var closed = false
        var active: [UUID: Stream] = [:]
    }
    private let streams = OSAllocatedUnfairLock(initialState: Streams())

    init(relay: RelayPath) {
        self.relay = relay
    }

    func openStream(stop: @escaping @Sendable (Bool) -> Void, onClose: @escaping @Sendable (String) -> Void) -> Stream {
        let stream = Stream(owner: self, stop: stop, onClose: onClose)
        let registered = streams.withLock { state in
            guard !state.closed else { return false }
            state.active[stream.id] = stream
            return true
        }
        if !registered { stream.end() }
        return stream
    }

    final class Stream: Sendable {
        let id = UUID()
        private let remove: @Sendable (UUID) -> Void
        private let ended = OSAllocatedUnfairLock(initialState: false)
        private let stop: @Sendable (Bool) -> Void
        private let onClose: @Sendable (String) -> Void
        init(owner: Link, stop: @escaping @Sendable (Bool) -> Void, onClose: @escaping @Sendable (String) -> Void) {
            self.remove = { [weak owner] id in _ = owner?.streams.withLock { $0.active.removeValue(forKey: id) } }
            self.stop = stop
            self.onClose = onClose
        }
        func end(_ reason: String? = nil, cancelPath: Bool = true) {
            guard ended.withLock({ value in if value { return false }; value = true; return true }) else { return }
            remove(id)
            stop(cancelPath)
            if let reason { onClose(reason) }
        }
    }

    func closeStreams() {
        let owned = streams.withLock { state in
            state.closed = true
            return Array(state.active.values)
        }
        for stream in owned { stream.end() }
    }
}
