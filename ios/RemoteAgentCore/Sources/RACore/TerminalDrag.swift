// Which way a drag on the live terminal goes (architecture §11 「尺寸」).
//
// The dominant axis decides it, once and for the rest of the gesture: a
// sideways drag pans a window too wide to fit, a downward pull reads the
// record, and an upward one does nothing. A sideways drag is the phone
// looking around; nothing of it goes to the remote.
import Foundation

/// What a drag on the live screen is for.
public enum TerminalDrag: Sendable, Equatable {
    /// Sideways: the screen is panned; the remote is untouched.
    case pan
    /// Downwards: the record is read, as a pull always has.
    case read
    /// Upwards: nothing.
    case none
}

/// The direction a drag has taken from how far it has gone, or `nil` while
/// it is still too short to say. A tie goes to the sideways pan.
public func terminalDrag(dx: CGFloat, dy: CGFloat, threshold: CGFloat = 8) -> TerminalDrag? {
    guard abs(dx) >= threshold || abs(dy) >= threshold else { return nil }
    if abs(dx) >= abs(dy) { return .pan }
    return dy > 0 ? .read : .none
}
