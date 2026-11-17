import Foundation
import RAProto

/// The direct path in words (design-system §6 「连接」组), driven by the shared Go clientcore.
public struct DirectView: Equatable, Sendable {
    public enum Kind: Sendable { case direct, measuring, relay, trying, off, unsupported }
    public enum Mark: Sendable { case online, run, off }
    public enum Tone: Sendable { case muted, live, warn }
    public enum Purpose: Sendable { case file, read }
    public struct Speed: Equatable, Sendable { public let purpose: Purpose; public let name: String; public let value: String }
    public struct ConnCheckRow: Equatable, Sendable {
        public let visible: Bool
        public let mark: Mark
        public let tone: Tone
        public let label: String
        public let status: String
        public let detail: String?
        public let metric: String?
    }

    public var kind: Kind
    public var mark: Mark
    public var tone: Tone = .muted
    /// The state word; ink2, never colored.
    public var word: String
    /// Second line of the status row: which single purpose goes direct, or why the relay is in use.
    public var sub: String?
    /// Active RTT metric label when measured.
    public var metric: String?
    /// The one line the connection check summarizes.
    public var summary: String?
    /// Measured speeds only, one per purpose; a file not yet measured says it is compared on the next transfer.
    public var speeds: [Speed]
    public var showDiagnosticsEntry = false

    // One decimal below 100, whole numbers from there; 99.96 is already 100.
    static func mibps(_ n: Double) -> String { (n * 10).rounded() / 10 >= 100 ? String(Int(n.rounded())) : String(format: "%.1f", n) }
    static func ms(_ n: Double) -> String { String(Int(n.rounded())) }

    static func speeds(_ s: DirectStatus, connection: PBConnectionGroupView?) -> [Speed] {
        var out: [Speed] = []
        let fileText = connection?.hasFileSpeedText == true ? renderCoreText(connection?.fileSpeedText) : ""
        let readText = connection?.hasReadLatencyText == true ? renderCoreText(connection?.readLatencyText) : ""
        if !fileText.isEmpty {
            out.append(Speed(purpose: .file, name: L10n.transportSpeedFile(), value: fileText))
        } else if s.state == .connected {
            out.append(Speed(purpose: .file, name: L10n.transportSpeedFile(), value: L10n.transportFileSpeedPending()))
        }
        if !readText.isEmpty {
            out.append(Speed(purpose: .read, name: L10n.transportSpeedRead(), value: readText))
        }
        return out
    }

    private static func toCoreReason(_ r: DirectReason?) -> PBDirectRouteReason {
        switch r {
        case .disabled: .disabled
        case .peerUnsupported: .peerUnsupported
        case .noResponse: .noResponse
        case .noTraversal: .noTraversal
        case .failed: .failed
        case .atCap: .atCap
        case .securityPending: .securityPending
        case .starting: .starting
        case nil: .unspecified
        }
    }

    private static func toCoreState(_ s: DirectState) -> PBDirectRouteState {
        switch s {
        case .unsupported: .unsupported
        case .connected: .connected
        case .connecting: .connecting
        case .none: .none
        }
    }

    private static func mark(for stage: PBConnectionGroupStage) -> Mark {
        switch stage {
        case .direct: .online
        case .connecting, .measuring: .run
        default: .off
        }
    }

    private static func tone(for t: PBConnectionGroupTone) -> Tone {
        switch t {
        case .live: .live
        case .warn: .warn
        default: .muted
        }
    }

    private static func kind(for stage: PBConnectionGroupStage, toggleVisible: Bool) -> Kind {
        if !toggleVisible { return .unsupported }
        switch stage {
        case .direct: return .direct
        case .connecting, .measuring: return .trying
        case .disabled: return .off
        case .unsupported: return .unsupported
        default: return .relay
        }
    }

    private static func buildCore(_ s: DirectStatus, online: Bool = true) -> PBDirectViewsResponse {
        let enabled = s.reason != .disabled
        let activeRtt = s.readDirect ? s.readMs[DirectPath.direct] : (s.state == .connected ? s.readMs[DirectPath.relay] : nil)
        let readSlow = s.state == .connected && s.compared && !s.readDirect
        let fileSlow = s.state == .connected && s.compared && !s.fileDirect
        let report = PBDirectRouteReport.with {
            $0.enabled = enabled
            $0.capable = s.state != .unsupported
            $0.state = toCoreState(s.state)
            $0.reason = toCoreReason(s.reason)
            $0.fileRoute = s.fileDirect ? .direct : .relay
            $0.readRoute = s.readDirect ? .direct : .relay
            $0.warming = s.warming
            $0.compared = s.compared
            $0.readSlow = readSlow
            $0.fileSlow = fileSlow
            $0.hasDiagnostics_p = s.reason == .noResponse || s.reason == .noTraversal || s.reason == .failed
            if let fd = s.fileMiBps[DirectPath.direct] { $0.hasDirectFileMibps_p = true; $0.directFileMibps = fd }
            if let fr = s.fileMiBps[DirectPath.relay] { $0.hasRelayFileMibps_p = true; $0.relayFileMibps = fr }
            if let rd = s.readMs[DirectPath.direct] { $0.hasDirectReadMs_p = true; $0.directReadMs = Int32(rd.rounded()) }
            if let rr = s.readMs[DirectPath.relay] { $0.hasRelayReadMs_p = true; $0.relayReadMs = Int32(rr.rounded()) }
            if let rtt = activeRtt { $0.hasActiveRttMs_p = true; $0.activeRttMs = Int32(rtt.rounded()) }
        }
        let req = PBDirectViewsRequest.with {
            $0.online = online
            $0.prefEnabled = enabled
            $0.report = report
        }
        return (try? CoreClient.buildDirectViews(req)) ?? PBDirectViewsResponse()
    }

    /// What a computer's direct path looks like to the user right now.
    public static func make(_ s: DirectStatus, online: Bool = true) -> DirectView {
        let views = buildCore(s, online: online)
        let cg = views.connection
        let row = views.connCheckDirect
        let shown = speeds(s, connection: views.hasConnection ? cg : nil)
        if s.state == .connected && !(s.fileDirect || s.readDirect) && s.warming {
            let word = L10n.transportStatusMeasuring()
            return DirectView(kind: .measuring, mark: .run, tone: .muted, word: word, metric: cg.hasMetricText ? renderCoreText(cg.metricText) : nil, summary: word, speeds: shown)
        }
        let word = s.state == .unsupported ? "" : (cg.hasStatusText ? renderCoreText(cg.statusText) : L10n.transportStatusRelay())
        let detailText = cg.hasDetailText ? renderCoreText(cg.detailText) : ""
        let sub = detailText.isEmpty ? nil : detailText
        let metricText = cg.hasMetricText ? renderCoreText(cg.metricText) : ""
        let metric = metricText.isEmpty ? nil : metricText
        let rowStatus = row.hasStatusText ? renderCoreText(row.statusText) : ""
        let rowDetail = row.hasDetailText ? renderCoreText(row.detailText) : ""
        let summaryParts = [rowStatus, rowDetail].filter { !$0.isEmpty }
        let summary = row.visible ? (summaryParts.isEmpty ? nil : summaryParts.joined(separator: " · ")) : nil
        return DirectView(
            kind: kind(for: cg.stage, toggleVisible: s.state != .unsupported),
            mark: mark(for: cg.stage),
            tone: tone(for: cg.tone),
            word: word,
            sub: sub,
            metric: metric,
            summary: summary,
            speeds: shown,
            showDiagnosticsEntry: cg.showDiagnosticsEntry
        )
    }

    public static func connCheckRow(_ s: DirectStatus, online: Bool = true) -> ConnCheckRow {
        let views = buildCore(s, online: online)
        let row = views.connCheckDirect
        let label = row.hasLabelText ? renderCoreText(row.labelText) : L10n.transportDirect()
        let status = row.hasStatusText ? renderCoreText(row.statusText) : ""
        let detailText = row.hasDetailText ? renderCoreText(row.detailText) : ""
        let metricText = row.hasMetricText ? renderCoreText(row.metricText) : ""
        return ConnCheckRow(
            visible: row.visible,
            mark: mark(for: row.stage),
            tone: tone(for: row.tone),
            label: label.isEmpty ? L10n.transportDirect() : label,
            status: status,
            detail: detailText.isEmpty ? nil : detailText,
            metric: metricText.isEmpty ? nil : metricText
        )
    }

    /// The state shown for a computer whose direct path was only just turned on and has not
    /// reported anything yet: trying, which is what is happening.
    public static func trying() -> DirectView {
        make(DirectStatus(state: .connecting))
    }
}
