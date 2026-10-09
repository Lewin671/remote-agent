import Foundation
import os

/// A value that changes, for anyone to read or follow: what Android's
/// StateFlow is to its screens. `value` is the snapshot now; `updates()`
/// gives it and then every later one, skipping those a slow reader missed,
/// so a `@MainActor` store can copy them into its `@Observable` state:
///
///     for await state in remote.state.updates() { self.state = state }
///
/// Only the owner sets it. Snapshots are values: one handed out never changes.
public final class Feed<Value: Sendable>: Sendable {
    private struct State {
        var value: Value
        var nextID = 0
        var followers: [Int: AsyncStream<Value>.Continuation] = [:]
    }

    private let state: OSAllocatedUnfairLock<State>
    /// Called, outside the lock, whenever a follower came or went.
    private let followersChanged: (@Sendable () -> Void)?

    init(_ value: Value, followersChanged: (@Sendable () -> Void)? = nil) {
        state = OSAllocatedUnfairLock(uncheckedState: State(value: value))
        self.followersChanged = followersChanged
    }

    public var value: Value { state.withLockUnchecked { $0.value } }

    /// How many `updates()` are being followed now.
    public var followers: Int { state.withLockUnchecked { $0.followers.count } }

    /// The value now and each one after it. Only the latest is kept for a
    /// reader that falls behind: every element is a whole snapshot.
    public func updates() -> AsyncStream<Value> {
        let (stream, continuation) = AsyncStream<Value>.makeStream(bufferingPolicy: .bufferingNewest(1))
        let id = state.withLockUnchecked { s -> Int in
            s.nextID += 1
            s.followers[s.nextID] = continuation
            // Under the lock, so that no later value can overtake this one.
            continuation.yield(s.value)
            return s.nextID
        }
        continuation.onTermination = { [weak self] _ in
            guard let self else { return }
            self.state.withLockUnchecked { _ = $0.followers.removeValue(forKey: id) }
            self.followersChanged?()
        }
        followersChanged?()
        return stream
    }

    func set(_ value: Value) {
        state.withLockUnchecked { s in
            s.value = value
            for c in s.followers.values { c.yield(value) }
        }
    }
}

extension Feed {
    /// The first value that satisfies `ok`, following the feed until then;
    /// nil when the task is cancelled first.
    public func first(where ok: @Sendable (Value) -> Bool) async -> Value? {
        for await v in updates() where ok(v) { return v }
        return nil
    }
}
