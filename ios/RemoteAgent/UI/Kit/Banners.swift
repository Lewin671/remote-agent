// Notices (docs/design-system.md §6): one soft surface, the text's colour
// alone telling a warning from an error, and the bar under the navigation
// bar that says how the way to the server stands.
import RACore
import SwiftUI

enum BannerTone: Sendable { case plain, warn, error }

/// A notice: soft, 13, at most one text action at the right.
struct Banner: View {
    private let text: String
    private let tone: BannerTone
    private let action: String?
    private let onAction: (() -> Void)?
    private let icon: LucideIcon?
    @Environment(\.onSoft) private var onSoft

    init(_ text: String, tone: BannerTone = .plain, action: String? = nil, onAction: (() -> Void)? = nil, icon: LucideIcon? = nil) {
        self.text = text
        self.tone = tone
        self.action = action
        self.onAction = onAction
        self.icon = icon
    }

    var body: some View {
        let ink: Color = switch tone {
        case .plain: Theme.fg2
        case .warn: Theme.orangeInk
        case .error: Theme.red
        }
        HStack(spacing: 8) {
            if let icon { Mark(icon, color: ink, size: 13, relativeTo: .footnote) }
            Text(text).type(T.sm, ink).padding(.vertical, 9).frame(maxWidth: .infinity, alignment: .leading)
            if let action, let onAction {
                Button(action: onAction) {
                    Text(action).type(T.smB, Theme.blue).lineLimit(1).fixedSize().padding(.horizontal, 8).hitTarget()
                }
                .buttonStyle(PressStyle())
            }
        }
        .padding(.leading, 12).padding(.trailing, action != nil && onAction != nil ? 4 : 12)
        .frame(minHeight: 38)
        .background(onSoft ? Theme.card : Theme.soft, in: RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous))
    }
}

/// The way to the server, under the navigation bar and apart from a
/// session's state; nothing when online. It names the nearest stretch that
/// is down (mockup 「断线」): this device's network, or the server, whose
/// details open from the bar.
struct ConnBanner: View {
    private let state: RemoteState
    private let onDetails: (() -> Void)?

    /// `onDetails` opens the server's details; the bar offers 详情 only when the server is the stretch that is down.
    init(_ state: RemoteState, onDetails: (() -> Void)? = nil) {
        self.state = state
        self.onDetails = onDetails
    }

    var body: some View {
        if state.link != .online {
            // Nothing kept from before to show: the bar says what the connection is needed for.
            let failed = state.link == .noServer && !state.loaded && !state.error.isEmpty
            let icon: LucideIcon = switch state.link {
            case .noNetwork: Lucide.wifiOff
            case .noServer: failed ? Lucide.x : Lucide.cloudOff
            default: Lucide.loaderCircle
            }
            let text: String = switch state.link {
            case .noNetwork: "本机网络不通 · " + (state.loaded ? "显示的是上次的内容" : "联网后才能读取会话")
            case .noServer: failed ? "连不上服务器：\(state.error)" : "连不上服务器 · 正在重试"
            default: state.synced ? "正在重连…" : "连接中…"
            }
            let color = failed ? Theme.red : Theme.muted
            let details = state.link == .noServer ? onDetails : nil
            let bar = HStack(spacing: 6) {
                Mark(icon, color: color)
                Text(text).type(T.meta, color).lineLimit(2).frame(maxWidth: .infinity, alignment: .leading)
                if details != nil { Text("详情").type(T.metaB, Theme.blue).lineLimit(1).fixedSize().padding(.leading, 2) }
            }
            .padding(.horizontal, Theme.Space.page).padding(.vertical, 5)
            // The bar is a thin line of text; one that opens the details is as tall as anything else to tap.
            .frame(maxWidth: .infinity, minHeight: details == nil ? nil : Theme.minHit, alignment: .leading)
            // Only the bar: left to itself the colour would run up under the navigation bar.
            .background(Theme.soft, ignoresSafeAreaEdges: [])
            if let details {
                Button(action: details) { bar }.buttonStyle(PressStyle())
            } else {
                bar.accessibilityElement(children: .combine)
            }
        }
    }
}
