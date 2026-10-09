// The terminal's key bar and whether its window is fitted to this screen
// (architecture §11 「尺寸」; Android's term/TerminalPane.kt and
// term/RemoteTerminal.kt).
import Foundation

/// A key of the key bar (client-v2): the arrows and Enter on the first row
/// with Esc, Tab and Ctrl+C next to the keyboard toggle on the second; none
/// hidden in a scrolling row. An arrow is a letter sent as ESC [ x, or
/// ESC O x when the program asked for application cursor keys, as a
/// keyboard would.
public struct TerminalKey: Sendable, Equatable {
    public enum Kind: Sendable, Equatable {
        case text(String)
        case arrow(Character)
    }

    public let label: String
    public let kind: Kind
    /// The Lucide icon drawn in place of `label`, which then only names the key to a screen reader.
    public let icon: String?

    public static func text(_ label: String, _ seq: String, icon: String? = nil) -> TerminalKey {
        TerminalKey(label: label, kind: .text(seq), icon: icon)
    }

    public static func arrow(_ label: String, _ letter: Character, icon: String) -> TerminalKey {
        TerminalKey(label: label, kind: .arrow(letter), icon: icon)
    }
}

public let terminalKeyRows: [[TerminalKey]] = [
    [
        .text("Esc", "\u{1b}"), .arrow("左", "D", icon: "arrow-left"), .arrow("上", "A", icon: "arrow-up"),
        .arrow("下", "B", icon: "arrow-down"), .arrow("右", "C", icon: "arrow-right"),
        .text("回车", "\r", icon: "corner-down-left"),
    ],
    [.text("Tab", "\t"), .text("Ctrl+C", "\u{03}")],
]

/// What a key sends; `appCursor` is the terminal's application cursor keys mode.
public func terminalKeySequence(_ key: TerminalKey, appCursor: Bool) -> String {
    switch key.kind {
    case .text(let seq): seq
    case .arrow(let letter): (appCursor ? "\u{1b}O" : "\u{1b}[") + String(letter)
    }
}

/// Columns and rows.
public struct TerminalSize: Sendable, Equatable {
    public var cols: Int
    public var rows: Int

    public init(_ cols: Int, _ rows: Int) {
        self.cols = cols
        self.rows = rows
    }
}

/// Whether the shared tmux window is fitted to this screen: only while the
/// latest snapshot has the size this screen asked for. Once another screen
/// or the computer changes it, it no longer is, whatever this screen did
/// before.
public struct FitTracker: Sendable {
    private var requested: TerminalSize?
    private var confirmed = false
    private var last: TerminalSize?
    private var releasing = false

    public init() {}

    /// Shown as fitted: asked for, and not changed since by anyone else.
    public var fitted: Bool { requested != nil }

    public mutating func request(_ size: TerminalSize) {
        requested = size
        // A window already this size sends no new snapshot to confirm it.
        confirmed = size == last
        releasing = false
    }

    /// Let the window follow the attached terminals again; the next size change is ours.
    public mutating func release() {
        requested = nil
        confirmed = false
        releasing = true
    }

    public mutating func reset() {
        self = FitTracker()
    }

    /// A new snapshot's size; true when it changed without this screen asking.
    public mutating func onSnapshot(_ size: TerminalSize) -> Bool {
        let before = last
        last = size
        if let r = requested {
            if size == r {
                confirmed = true
                return false
            }
            // A snapshot in flight before the resize took effect.
            if !confirmed { return false }
            requested = nil
            return true
        }
        if releasing {
            if let before, size != before { releasing = false }
            return false
        }
        if let before { return size != before }
        return false
    }
}
