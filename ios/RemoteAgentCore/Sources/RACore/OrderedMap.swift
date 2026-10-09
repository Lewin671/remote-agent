/// A dictionary that keeps its keys in the order they were first put, as
/// java.util.LinkedHashMap does: the event reduction relies on it (pending
/// approvals in the order asked, the queue in the order the daemon accepted
/// it). Putting a key again keeps its place; a key removed and put again goes
/// to the end.
public struct OrderedMap<Key: Hashable, Value> {
    public private(set) var keys: [Key] = []
    private var dict: [Key: Value] = [:]

    public init() {}

    public var count: Int { keys.count }
    public var isEmpty: Bool { keys.isEmpty }
    public var values: [Value] { keys.map { dict[$0]! } }
    public var entries: [(key: Key, value: Value)] { keys.map { ($0, dict[$0]!) } }

    public func contains(_ key: Key) -> Bool { dict[key] != nil }

    public subscript(key: Key) -> Value? {
        get { dict[key] }
        set {
            if let newValue {
                if dict.updateValue(newValue, forKey: key) == nil { keys.append(key) }
            } else {
                removeValue(forKey: key)
            }
        }
    }

    @discardableResult
    public mutating func removeValue(forKey key: Key) -> Value? {
        guard let old = dict.removeValue(forKey: key) else { return nil }
        keys.remove(at: keys.firstIndex(of: key)!)
        return old
    }

    public mutating func removeAll() {
        keys.removeAll()
        dict.removeAll()
    }
}

extension OrderedMap: Sendable where Key: Sendable, Value: Sendable {}

extension OrderedMap: Equatable where Value: Equatable {
    /// Equal with the same entries in the same order.
    public static func == (a: OrderedMap, b: OrderedMap) -> Bool { a.keys == b.keys && a.dict == b.dict }
}
