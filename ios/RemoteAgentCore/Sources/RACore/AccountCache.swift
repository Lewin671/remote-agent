// From Android's ui/AccountCache.kt.
import Foundation

/// Plaintext belongs to one active owner. Old asynchronous loads cannot
/// repopulate the cache after logout, even when identifiers are reused. It
/// holds `limit` entries at most and lets the least recently used one go.
public final class AccountCache<T: Sendable>: @unchecked Sendable {
    // Everything below is only touched under the lock.
    private let lock = NSLock()
    private let limit: Int
    private var owner: AnyObject?
    private var entries = [String: T]()
    /// The keys, the least recently used first.
    private var order = [String]()

    public init(limit: Int) {
        self.limit = limit
    }

    public func use(_ owner: AnyObject) {
        lock.lock()
        defer { lock.unlock() }
        if self.owner !== owner { reset() }
        self.owner = owner
    }

    public func get(_ owner: AnyObject, key: String) -> T? {
        lock.lock()
        defer { lock.unlock() }
        guard self.owner === owner, let v = entries[key] else { return nil }
        touch(key)
        return v
    }

    public func put(_ owner: AnyObject, key: String, value: T) {
        lock.lock()
        defer { lock.unlock() }
        guard self.owner === owner else { return }
        entries[key] = value
        touch(key)
        while order.count > limit { entries[order.removeFirst()] = nil }
    }

    public func clear() {
        lock.lock()
        defer { lock.unlock() }
        reset()
    }

    private func touch(_ key: String) {
        if let i = order.firstIndex(of: key) { order.remove(at: i) }
        order.append(key)
    }

    private func reset() {
        entries.removeAll()
        order.removeAll()
        owner = nil
    }
}
