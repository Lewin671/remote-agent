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
/// is what pulling down does.
struct SettingsPage<Content: View>: View {
    private let refresh: (@MainActor @Sendable () async -> Void)?
    private let content: Content

    init(
        refresh: (@MainActor @Sendable () async -> Void)? = nil, @ViewBuilder content: () -> Content
    ) {
        self.refresh = refresh
        self.content = content()
    }

    var body: some View {
        Page(gray: true) {
            let list = ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    ConnectionBar()
                    content
                    Color.clear.frame(height: 24)
                }
                .padding(.top, 8)
            }
            if let refresh {
                list.refreshable { await refresh() }.startAtEndForScreenshots()
            } else {
                list.startAtEndForScreenshots()
            }
        }
    }
}

/// The grey explanation under a group (design-system §1, level three): 13
/// muted, in line with the group's label. Orange or red only when it warns
/// of something the rows above cannot say.
/// Where devices are added: only the phone app adds and removes them (architecture §9 「配对」), so
/// this client has no page for it, only these words, the same wherever it says so.
let addDeviceHint = "要添加电脑或客户端，在手机 App 的「设置 › 添加设备」里操作。"
/// An account with no computer, on the home page and in 设置.
let noMachinesHint = "还没有电脑。在手机 App 的「设置 › 添加设备」里把电脑加进来。"

struct GroupNote: View {
    private let text: String
    private let color: Color

    init(_ text: String, color: Color = Theme.muted) {
        self.text = text
        self.color = color
    }

    var body: some View {
        Text(text).type(T.sm, color)
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
    if n >= 1 && n < 104_858 { return "不到 0.1 MiB" }
    if n >= 1 << 30 { return String(format: "%.1f GiB", Double(n) / 1_073_741_824) }
    return String(format: "%.1f MiB", Double(n) / 1_048_576)
}

/// A limit in whole units: 256 MiB, 1 GiB.
func wholeBytes(_ n: Int64) -> String {
    n >= 1 << 30 && n % (1 << 30) == 0 ? "\(n >> 30) GiB" : "\(n >> 20) MiB"
}

/// What a computer's row says of it at the right: 在线, or when it was last
/// seen. Nothing while this client is not connected to the server: what the
/// cache says of the computers is not known to still hold (architecture §9
/// 「断线时显示什么」), and the connection bar already says why.
func machineStatus(_ m: MachineView, state: RemoteState, now: Int64) -> (
    text: String, online: Bool
)? {
    if state.unlinked { return nil }
    if m.online { return ("在线", true) }
    return (whenText(m.lastSeenMs, now: now).or("不在线"), false)
}

/// The computer as far as it is known from here: while this client is not
/// connected its last word of being online is not repeated, so what is
/// derived from it (quota numbers above all) reads as a last record
/// (architecture §9 「为什么不沿用旧状态」).
func asKnown(_ m: MachineView, state: RemoteState) -> MachineView {
    guard state.unlinked, m.online else { return m }
    var known = m
    known.online = false
    return known
}

/// A computer's quota in one short line under its name: the first CLI's
/// last window that has a number (Android's SettingsScreen).
func machineQuotaSummary(_ m: MachineView, now: Int64) -> String? {
    guard let g = quotaGroups(m, now: now).first,
        let row = g.rows.last(where: { $0.percent != nil })
    else { return nil }
    return "\(agentName(g.agent)) · \(row.label)\(row.used)"
}

/// The operating system as people call it.
func osName(_ os: String) -> String {
    switch os.lowercased() {
    case "darwin": "macOS"
    case "linux": "Linux"
    case "windows": "Windows"
    case "": "未知"
    default: os
    }
}

/// Computers in the order every list of them uses: by name.
func sortedMachines(_ state: RemoteState) -> [MachineView] {
    state.machines.values.sorted {
        let (a, b) = (machineLabel(state, id: $0.id), machineLabel(state, id: $1.id))
        return a == b ? $0.id < $1.id : a.localizedStandardCompare(b) == .orderedAscending
    }
}

/// A computer's row, the same wherever computers are listed (设置 and
/// 设备): its badge, its name, its quota in short under it, and at the
/// right whether it is online or when it last was. A long press offers its
/// quota and the connection check.
struct MachineRow: View {
    let machine: MachineView
    let state: RemoteState
    let now: Int64
    @Environment(Navigator.self) private var nav

    var body: some View {
        let id = machine.id
        let status = machineStatus(machine, state: state, now: now)
        GRow(
            machineLabel(state, id: id),
            sub: machineQuotaSummary(asKnown(machine, state: state), now: now),
            value: status?.text,
            chevron: true
        ) { nav.open(.machine(id: id)) }
        .lead { IconBadge(Lucide.laptop, color: Theme.blueBg) }
        .valueLead { if let status { Dot(status.online ? Theme.green : Theme.gray) } }
        .contextMenu {
            Button("账户额度") { nav.open(.quota(machine: id)) }
            Button("连接检测") { nav.open(.connectionCheck) }
        }
    }
}

/// A client's row, the same wherever clients are listed (设置 and 设备): its
/// name, what it runs on under it, and at the right 当前客户端 or when it
/// was last used. A long press copies its ID, and renames or removes it
/// where the list offers that.
struct ClientRow: View {
    let client: DeviceView?
    let id: String
    let me: String
    let now: Int64
    var authority: String = ""
    var onRename: (() -> Void)?
    @Environment(Navigator.self) private var nav

    var body: some View {
        let mine = id == me
        let role = id == authority ? "管理手机" : mine ? "当前客户端" : clientUsed(client, now: now)
        GRow(
            clientName(client, id: id), sub: client.map { clientDetail($0) },
            value: role,
            chevron: true
        ) { nav.open(.client(id: id)) }
        .contextMenu {
            if let onRename { Button("重命名", action: onRename) }
            Button("复制设备 ID") { UIPasteboard.general.string = id }
        }
    }
}

enum DeviceNotes {
    /// This client is a normal member: only the phone app manages the domain.
    static let phoneOnly = "添加和移除设备只能在手机 App 上进行。电脑、网页和 iOS 只作为普通成员加入账户。"
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
            .with {
                $0.approvalTimeoutSeconds = 600
                $0.defaultModel = "gpt-6-astra"
                $0.defaultEffort = "medium"
                $0.defaultMode = .ask
                $0.claude = .with { $0.model = "claude-opus-5-5-preview-20261001-extended" }
                $0.imageUploadQuotaBytes = 1 << 30
            }
        }

        static var imageStorage: PBImageStorageUsage {
            .with {
                $0.uploadBytes = 187 << 20
                $0.reclaimableBytes = 96 << 20
                $0.reservedBytes = 3 << 20
                $0.cacheBytes = 41 << 20
            }
        }
    }
#endif
