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

struct MachineRow: View {
    let machine: PBMachineListCardView

    var body: some View {
        MachineCard(machine: machine)
    }
}

/// Maps Core's `PBMachineStatusDot` onto a visible `Dot` or empty space.
@MainActor
@ViewBuilder
func machineStatusDotView(_ dot: PBMachineStatusDot, size: CGFloat = 8) -> some View {
    switch dot {
    case .greenLive:
        Dot(DS.green, size: size)
    case .orangeStatic:
        Dot(DS.orange, size: size)
    case .redStatic:
        Dot(DS.red, size: size)
    case .grayStatic:
        Dot(DS.ink3, size: size)
    case .unspecified, .UNRECOGNIZED:
        EmptyView()
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
    var onRemove: (() -> Void)?
    @Environment(Navigator.self) private var nav

    var body: some View {
        let roleOrUsed: String = if client.pairingIncomplete {
            L10n.settingsClientsPairingIncompleteBadge()
        } else if client.isSelf || client.isAuthority {
            client.roleLabel.rendered()
        } else {
            client.lastUsedText.rendered()
        }
        let baseSub = [client.description_p, client.shortID].filter { !$0.isEmpty }.joined(separator: " · ")
        let sub = client.pairingIncomplete
            ? [L10n.settingsClientsPairingIncompleteDesc(), baseSub].filter { !$0.isEmpty }.joined(separator: " · ")
            : baseSub
        GRow(
            client.displayName,
            sub: sub.isEmpty ? nil : sub,
            value: roleOrUsed.isEmpty ? nil : roleOrUsed,
            chevron: true,
            valueColor: client.pairingIncomplete ? DS.orangeInk : nil
        ) { nav.open(.client(id: client.deviceID)) }
        .lead {
            SymbolImage(clientIcon(description: client.description_p), size: 20, color: DS.ink2)
                .frame(width: 22, alignment: .center)
        }
        .contextMenu {
            if let onRename { Button(L10n.sessionRename(), action: onRename) }
            Button(L10n.settingsIosCopyDeviceId()) { UIPasteboard.general.string = client.deviceID }
            if let onRemove {
                Button(L10n.commonRemove(), role: .destructive, action: onRemove)
            }
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
            PBDaemonSettings.with {
                $0.approvalTimeoutSeconds = 600
                $0.idleCloseMinutes = 360
                $0.imageUploadQuotaBytes = 1 << 30
                $0.defaultModel = "gpt-6-astra"
                $0.defaultEffort = "medium"
                $0.defaultMode = .ask
                $0.claude = .with {
                    $0.model = "claude-opus-5-5-preview-20261001-extended"
                }
                $0.antigravity = .with {
                    $0.model = "gemini-3.1-pro"
                    $0.effort = "high"
                    $0.mode = .ask
                }
            }
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

        static func usageStats(
            request req: PBGetUsageStatsRequest,
            machineID _: String,
            now: Int64
        ) -> PBUsageStatsResponse {
            let bounds = req.bucketBoundsMs
            let avgSpan = bounds.count >= 2 ? Double(bounds.last! - bounds.first!) / Double(bounds.count - 1) : 86_400_000
            let gran: PBUsageBucketGranularity = avgSpan <= 7_200_000 ? .hour : (avgSpan <= 43_200_000 ? .sixHours : .day)
            let startMs = bounds.first ?? (now - 7 * 86_400_000)
            let endMs = bounds.last ?? now
            if state == "usageEmpty" {
                return .with {
                    $0.preset = req.preset == .unspecified ? .sevenDays : req.preset
                    $0.granularity = gran
                    $0.startMs = startMs
                    $0.endMs = endMs
                    $0.generatedAtMs = now
                    $0.sources = [
                        .with { $0.agent = .codex; $0.state = .ok },
                        .with { $0.agent = .claude; $0.state = .ok },
                        .with { $0.agent = .antigravity; $0.state = .unavailable },
                    ]
                }
            }

            func makeSeries(
                agent: PBAgent,
                model: String,
                cacheWrite: Bool,
                baseTok: Double,
                baseSpeed: Double,
                baseTtft: Double,
                phase: Double
            ) -> PBUsageSeries {
                var points: [PBUsageBucketPoint] = []
                var uncachedSum: UInt64 = 0
                var readSum: UInt64 = 0
                var writeSum: UInt64 = 0
                var outSum: UInt64 = 0
                var reasSum: UInt64 = 0
                var reqSum: UInt64 = 0
                var speedReqSum: UInt64 = 0
                let count = max(0, bounds.count - 1)
                for i in 0..<count {
                    let sMs = bounds[i]
                    let eMs = bounds[i + 1]
                    let wave = 0.65 + 0.45 * sin(Double(i) * 0.85 + phase) + 0.2 * cos(Double(i) * 0.4)
                    let active = !(gran == .day && count >= 7 && i == 2 && phase > 1.0)
                    let factor = active ? max(0.25, wave) : 0.0
                    let uncached = UInt64((baseTok * 0.22 * factor).rounded())
                    let read = UInt64((baseTok * 0.62 * factor).rounded())
                    let write = cacheWrite ? UInt64((baseTok * 0.08 * factor).rounded()) : 0
                    let out = UInt64((baseTok * 0.11 * factor).rounded())
                    let reas = agent == .codex ? out / 3 : 0
                    let reqs: UInt64 = active ? UInt64(max(2, Int((6.0 * factor).rounded()))) : 0
                    let sReqs: UInt64 = active ? reqs : 0
                    let p50 = active ? baseSpeed + 8.0 * sin(Double(i) * 0.7 + phase) : 0
                    let t50 = active ? baseTtft + 140.0 * cos(Double(i) * 0.6 + phase) : 0
                    uncachedSum += uncached
                    readSum += read
                    writeSum += write
                    outSum += out
                    reasSum += reas
                    reqSum += reqs
                    speedReqSum += sReqs
                    points.append(.with {
                        $0.startMs = sMs
                        $0.endMs = eMs
                        $0.uncachedInputTokens = uncached
                        $0.cacheReadTokens = read
                        $0.cacheWriteTokens = write
                        $0.outputTokens = out
                        $0.reasoningOutputTokens = reas
                        $0.usageRequestCount = reqs
                        $0.speedRequestCount = sReqs
                        if sReqs > 0 {
                            $0.speedP10 = max(12, p50 * 0.74)
                            $0.speedP50 = p50
                            $0.speedP90 = p50 * 1.28
                            $0.ttftP10Ms = max(180, t50 * 0.68)
                            $0.ttftP50Ms = t50
                            $0.ttftP90Ms = t50 * 1.52
                        }
                    })
                }
                let inDenom = Double(uncachedSum + readSum + writeSum)
                let cur = PBUsagePeriodSummary.with {
                    $0.uncachedInputTokens = uncachedSum
                    $0.cacheReadTokens = readSum
                    $0.cacheWriteTokens = writeSum
                    $0.outputTokens = outSum
                    $0.reasoningOutputTokens = reasSum
                    $0.usageRequestCount = reqSum
                    $0.speedRequestCount = speedReqSum
                    if inDenom > 0 {
                        $0.hasCacheHitRate_p = true
                        $0.cacheHitRate = Double(readSum) / inDenom
                    }
                    if speedReqSum > 0 {
                        $0.hasSpeed_p = true
                        $0.speedP10 = baseSpeed * 0.75
                        $0.speedP50 = baseSpeed
                        $0.speedP90 = baseSpeed * 1.29
                        $0.hasTtft_p = true
                        $0.ttftP10Ms = baseTtft * 0.7
                        $0.ttftP50Ms = baseTtft
                        $0.ttftP90Ms = baseTtft * 1.5
                        $0.recent10SpeedCount = 10
                        $0.recent10SpeedP50 = baseSpeed + 4.5
                    }
                }
                let prev = PBUsagePeriodSummary.with {
                    $0.uncachedInputTokens = UInt64(Double(uncachedSum) * 0.85)
                    $0.cacheReadTokens = UInt64(Double(readSum) * 0.82)
                    $0.cacheWriteTokens = UInt64(Double(writeSum) * 0.88)
                    $0.outputTokens = UInt64(Double(outSum) * 0.84)
                    $0.usageRequestCount = max(3, reqSum - 2)
                    $0.speedRequestCount = max(3, speedReqSum - 2)
                    if inDenom > 0 {
                        $0.hasCacheHitRate_p = true
                        $0.cacheHitRate = max(0, (Double(readSum) / inDenom) - 0.04)
                    }
                    if speedReqSum > 0 {
                        $0.hasSpeed_p = true
                        $0.speedP10 = baseSpeed * 0.72
                        $0.speedP50 = baseSpeed * 0.94
                        $0.speedP90 = baseSpeed * 1.24
                        $0.hasTtft_p = true
                        $0.ttftP10Ms = baseTtft * 0.74
                        $0.ttftP50Ms = baseTtft * 1.08
                        $0.ttftP90Ms = baseTtft * 1.55
                    }
                }
                return .with {
                    $0.agent = agent
                    $0.model = model
                    $0.cacheWriteSupported = cacheWrite
                    $0.current = cur
                    $0.previous = prev
                    $0.buckets = points
                }
            }

            return .with {
                $0.preset = req.preset == .unspecified ? .sevenDays : req.preset
                $0.granularity = gran
                $0.startMs = startMs
                $0.endMs = endMs
                $0.generatedAtMs = now
                $0.sources = [
                    .with { $0.agent = .codex; $0.state = .ok },
                    .with { $0.agent = .claude; $0.state = .ok },
                    .with { $0.agent = .antigravity; $0.state = .unavailable },
                ]
                $0.series = [
                    makeSeries(agent: .codex, model: "gpt-6-astra", cacheWrite: false, baseTok: 18_500_000, baseSpeed: 74, baseTtft: 820, phase: 0.2),
                    makeSeries(agent: .claude, model: "claude-sonnet-5-5", cacheWrite: true, baseTok: 11_200_000, baseSpeed: 62, baseTtft: 1_150, phase: 1.4),
                    makeSeries(agent: .claude, model: "claude-opus-5-5-preview", cacheWrite: true, baseTok: 4_800_000, baseSpeed: 48, baseTtft: 1_480, phase: 2.3),
                ]
            }
        }
    }
#endif
