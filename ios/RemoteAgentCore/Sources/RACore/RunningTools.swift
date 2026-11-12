// How a tool without a result reads in the chat, from Android's ui/Tools.kt.
import Foundation
import RAProto

/// How a tool without a result reads now.
public enum ToolState: Sendable {
    case running, approval, sync, unconfirmed

    public var text: String {
        switch self {
        case .running: return L10n.toolsIosProcessing()
        case .approval: return L10n.listStateApproval()
        case .sync: return L10n.toolsWaitingSync()
        case .unconfirmed: return L10n.toolsIosUnconfirmed()
        }
    }
}

/// `unfinished`: the tool's turn ended without its result. `frozenAt`: when the connection dropped, 0 while online.
public func toolState(_ t: RunningTool, unfinished: Bool, d: Derived, frozenAt: Int64) -> ToolState {
    if unfinished { return .unconfirmed }
    if frozenAt > 0 { return .sync }
    if awaitingApproval(t, d) { return .approval }
    return .running
}

/// A timer reading: 已运行 2:12 in the chat; a dash while nothing runs for sure; nothing when the start is unknown.
public func toolTimer(_ t: RunningTool, state: ToolState, now: Int64, frozenAt: Int64) -> String {
    if state == .unconfirmed || state == .approval { return "—" }
    guard let ms = toolElapsed(t, now: now, frozenAt: frozenAt) else { return "" }
    // Says what the time is: how long it has been running.
    return L10n.toolsElapsed(duration: clock(ms))
}
