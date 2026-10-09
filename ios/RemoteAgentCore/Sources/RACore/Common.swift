// Words several screens share, from Android's ui/Common.kt, ConnCheck.kt,
// Manage.kt, ChatImages.kt and Sheets.kt.
import Foundation
import RAProto

public func machineOf(_ state: RemoteState, _ s: SessionView) -> MachineView? { state.machines[s.machineId] }

public func machineLabel(_ state: RemoteState, id: String) -> String { machineName(state.machines[id], id: id) }

/// The link to the server in a word: what 设置 says before anything is measured.
public func connWord(_ state: RemoteState) -> String {
    if state.conn == .online { return "已连接" }
    if state.synced { return "重连中" }
    if state.link == .noServer { return "连不上" }
    return "连接中"
}

/// Why the preferences cannot be changed now, or nil when they can.
public func prefsBlocked(_ state: RemoteState) -> String? {
    if state.conn != .online { return "连上服务器后才能修改" }
    if state.prefs == nil { return "正在读取项目列表…" }
    return nil
}

/// Width over height of an image as the event says, so its place is kept before anything loads.
public func aspect(_ ref: PBImageRef) -> Double {
    let w: Int32, h: Int32
    if ref.previewWidth > 0 && ref.previewHeight > 0 {
        (w, h) = (ref.previewWidth, ref.previewHeight)
    } else if ref.displayWidth > 0 && ref.displayHeight > 0 {
        (w, h) = (ref.displayWidth, ref.displayHeight)
    } else if ref.width > 0 && ref.height > 0 {
        (w, h) = (ref.width, ref.height)
    } else {
        (w, h) = (4, 3)
    }
    return min(2.5, max(0.4, Double(w) / Double(h)))
}

/// A file's name from its path, for an image shown by name only.
public func fileName(_ path: String) -> String { (path.split(ascii: UInt8(ascii: "/")).last ?? "").ifEmpty("图片") }

/// What each approval mode means, under its name in the mode sheet.
public func modeHelp(_ m: Mode) -> String {
    switch m {
    case .ask: return "需要审批时由你确认，超时拒绝"
    case .auto: return "由 CLI 自己判断，拿不准的在对话里问你"
    case .plan: return "只读和出计划，你批准后才开始改"
    case .full: return "所有操作都不审批"
    }
}
