// What the settings pages share: the grey page they are all drawn on, the
// grey explanation under a group, the words for sizes and for a computer's
// state, and the sample values the pages show with `-demo`, where nothing
// answers a command.
import RACore
import RAProto
import SwiftUI
import UIKit

/// A settings page: the grey surface with its readable column, the
/// connection bar at the top, and the groups under it, scrolling. `refresh`
/// is what pulling down does. `scrollTo` is the `id` of a group to bring to the top once `ready` (it is
/// drawn only then): where a link into a page's middle leads.
struct SettingsPage<Content: View>: View {
    private let refresh: (@MainActor @Sendable () async -> Void)?
    private let scrollTo: (id: AnyHashable, ready: Bool)?
    private let content: Content

    init(
        refresh: (@MainActor @Sendable () async -> Void)? = nil, scrollTo: (id: AnyHashable, ready: Bool)? = nil,
        @ViewBuilder content: () -> Content
    ) {
        self.refresh = refresh
        self.scrollTo = scrollTo
        self.content = content()
    }

    var body: some View {
        Page(gray: true) {
            ScrollViewReader { proxy in
                let list = ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        ConnectionBar()
                        content
                        Color.clear.frame(height: 24)
                    }
                    .padding(.top, 8)
                }
                .task(id: scrollTo?.ready) {
                    guard let scrollTo, scrollTo.ready else { return }
                    try? await Task.sleep(for: .milliseconds(80))
                    proxy.scrollTo(scrollTo.id, anchor: .top)
                }
                if let refresh {
                    list.refreshable { await refresh() }.startAtEndForScreenshots()
                } else {
                    list.startAtEndForScreenshots()
                }
            }
        }
    }
}

/// The grey explanation under a group (design-system §1, level three): 13
/// muted, in line with the group's label. Orange or red only when it warns
/// of something the rows above cannot say.
struct GroupNote: View {
    private let text: String
    private let color: Color

    init(_ text: String, color: Color = DS.ink2) {
        self.text = text
        self.color = color
    }

    var body: some View {
        Text(text).font(.subheadline).ink(color)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 30).padding(.bottom, 12)
    }
}

/// A notice between groups, at the cards' margin.
struct GroupBanner: View {
    private let text: String
    private let tone: BannerTone
    private let action: String?
    private let onAction: (() -> Void)?

    init(
        _ text: String, tone: BannerTone = .plain, action: String? = nil,
        onAction: (() -> Void)? = nil
    ) {
        self.text = text
        self.tone = tone
        self.action = action
        self.onAction = onAction
    }

    var body: some View {
        Banner(text, tone: tone, action: action, onAction: onAction)
            .padding(.horizontal, 16).padding(.bottom, 12)
    }
}

extension String {
    /// This text, or `other` when there is none.
    func or(_ other: @autoclosure () -> String) -> String { isEmpty ? other() : self }
}

/// Bytes of images as the storage rows write them (Android's `imageBytes`):
/// one decimal, and anything under a tenth of a MiB said as such rather
/// than as 0.0.
func imageBytes(_ n: Int64) -> String {
    if n >= 1 && n < 104_858 { return L10n.settingsLessThan01Mib() }
    if n >= 1 << 30 { return String(format: "%.1f GiB", Double(n) / 1_073_741_824) }
    return String(format: "%.1f MiB", Double(n) / 1_048_576)
}

/// A limit in whole units: 256 MiB, 1 GiB.
func wholeBytes(_ n: Int64) -> String {
    n >= 1 << 30 && n % (1 << 30) == 0 ? "\(n >> 30) GiB" : "\(n >> 20) MiB"
}

/// A computer's row, the same wherever computers are listed (设置 and
/// 设备): its badge, its name, its quota in short under it, and at the
/// right whether it is online or when it last was. A long press offers its
/// quota and the connection check.
struct MachineRow: View {
    let machine: PBMachineListCardView
    @Environment(Navigator.self) private var nav

    var body: some View {
        let id = machine.machineID
        let quotaSub = machine.quotaSummaries.first.flatMap { sec -> String? in
            let line = sec.summaryLine.rendered()
            return line.isEmpty ? nil : "\(sec.agentName) · \(line)"
        }
        let statusText = machine.member ? machine.statusText.rendered() : nil
        GRow(
            machine.name,
            sub: quotaSub,
            value: statusText,
            chevron: true
        ) { nav.open(.machine(id: id)) }
        .lead { DeviceIcon("laptopcomputer") }
        .valueLead { if machine.member { Dot(machine.online ? DS.green : DS.ink3) } }
        .contextMenu {
            Button(L10n.settingsAccountQuota()) { nav.open(.quota(machine: id)) }
            Button(L10n.homeConnectionCheck()) { nav.open(.connectionCheck) }
        }
    }
}

/// What a client is, drawn as a computer's row draws its laptop: a browser, a tablet or a phone.
func clientIcon(description: String) -> String {
    if description.localizedCaseInsensitiveContains("iPad") { return "ipad" }
    let lower = description.lowercased()
    if lower.contains("web") || lower.contains("chrome") || lower.contains("firefox")
        || lower.contains("safari") || lower.contains("edge") || lower.contains("browser")
        || lower.contains("浏览器") || description.contains(" · macOS")
        || description.contains(" · Windows") || description.contains(" · Linux")
    {
        return "globe"
    }
    return "iphone"
}

/// A client's row, the same wherever clients are listed (设置 and 设备): its
/// badge, its name, what it runs on under it, and at the right 当前客户端 or
/// when it was last used. A long press copies its ID, and renames or removes it
/// where the list offers that.
struct ClientRow: View {
    let client: PBClientRowView
    var onRename: (() -> Void)?
    @Environment(Navigator.self) private var nav

    var body: some View {
        let roleOrUsed = client.isSelf || client.isAuthority
            ? client.roleLabel.rendered()
            : client.lastUsedText.rendered()
        let sub = [client.description_p, client.shortID].filter { !$0.isEmpty }.joined(separator: " · ")
        GRow(
            client.displayName,
            sub: sub.isEmpty ? nil : sub,
            value: roleOrUsed.isEmpty ? nil : roleOrUsed,
            chevron: true
        ) { nav.open(.client(id: client.deviceID)) }
        .lead { DeviceIcon(clientIcon(description: client.description_p)) }
        .contextMenu {
            if let onRename { Button(L10n.sessionRename(), action: onRename) }
            Button(L10n.settingsIosCopyDeviceId()) { UIPasteboard.general.string = client.deviceID }
        }
    }
}

extension View {
    /// `-settingsEnd` (debug builds) opens a settings page scrolled to its end, to look at what is below the fold.
    @ViewBuilder func startAtEndForScreenshots() -> some View {
        #if DEBUG
            if ProcessInfo.processInfo.arguments.contains("-settingsEnd") {
                defaultScrollAnchor(.bottom)
            } else {
                self
            }
        #else
            self
        #endif
    }
}

#if DEBUG
    /// With `-demo` nothing answers a command, so the pages that ask a computer
    /// or the server show fixed answers instead, and `-settingsState <name>`
    /// picks which: a page's loading, failed and unknown-outcome states can be
    /// looked at without a computer that misbehaves on cue.
    ///
    ///   machine/<id>      loading, failed, saveFailed, saveUnknown, mismatch
    ///   connection        checking, serverDown, machineDown
    ///   image-storage     empty, clearing
    ///   licenses          <a licence's id>: opened on its text
    ///   client/<id>, devices   renaming, removing: with the dialog up
    enum SettingsDemo {
        static let state: String? = {
            let args = ProcessInfo.processInfo.arguments
            guard let i = args.firstIndex(of: "-settingsState"), i + 1 < args.count else {
                return nil
            }
            return args[i + 1]
        }()

        static var daemonSettings: PBDaemonSettings {
            var s = PBDaemonSettings.with {
                $0.approvalTimeoutSeconds = 600
                $0.idleCloseMinutes = 360
                $0.imageUploadQuotaBytes = 1 << 30
            }
            if let first = AGENTS.first {
                s = withDefaults(s, agent: first.agent, model: "gpt-6-astra", effort: "medium", mode: Mode.ask)
            }
            if AGENTS.count > 1 {
                s = withDefaults(s, agent: AGENTS[1].agent, model: "claude-opus-5-5-preview-20261001-extended")
            }
            s = withDefaults(s, agent: .antigravity, model: "gemini-3.1-pro", effort: "high", mode: Mode.ask)
            return s
        }

        static var imageStorage: PBImageStorageUsage {
            .with {
                $0.uploadBytes = 187 << 20
                $0.reclaimableBytes = 96 << 20
                $0.reservedBytes = 3 << 20
                $0.cacheBytes = 41 << 20
                $0.emergencyReserveBytes = 256 << 20
            }
        }
    }
#endif
