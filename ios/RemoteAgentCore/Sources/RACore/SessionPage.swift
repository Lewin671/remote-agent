// From Android's ui/Session.kt.

/// Whether the chat follows new messages. Only the user turns it off, by
/// dragging away from the bottom; reaching the bottom turns it back on. A
/// scroll the app makes to the end ends before the list has laid out the new
/// position, so judging by it would stop following on a long history.
public struct Follow: Equatable, Sendable {
    public private(set) var on = true

    public init() {}

    public mutating func observe(userDragging: Bool, atBottom: Bool) {
        if atBottom { on = true } else if userDragging { on = false }
    }
}
