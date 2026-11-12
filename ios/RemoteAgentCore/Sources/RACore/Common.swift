// Words several screens share, from Android's ui/Common.kt, ConnCheck.kt,
// Manage.kt, ChatImages.kt and Sheets.kt.
import Foundation
import RAProto

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
    if state.conn != .online { return L10n.projectsConnectToEdit() }
    if state.prefs == nil { return L10n.projectsLoading() }
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
public func fileName(_ path: String) -> String { (path.split(ascii: UInt8(ascii: "/")).last ?? "").ifEmpty(L10n.imageTitle()) }

/// What each approval mode means, under its name in the mode sheet.
public func modeHelp(_ m: Mode, agent: String = "CLI") -> String {
    switch m {
    case .ask: return L10n.modesAskDesc()
    case .auto: return L10n.modesAutoDesc(agent: agent)
    case .plan: return L10n.modesPlanDesc()
    case .full: return L10n.modesSkipAll()
    }
}
