import Foundation

/// The direct path in words (design-system §6 「连接」组). One place says it, so the computer's
/// settings page and the connection check never disagree: the page draws the whole of it, the
/// connection check one line. The status says only the result, which path is in use now; the
/// graphic is a green dot (direct), an ink ring that turns (setting up, or measuring in the first
/// seconds) or a gray dot (relay). Never a hollow ring.
public struct DirectView: Equatable, Sendable {
    public enum Kind: Sendable { case direct, measuring, relay, trying, off, unsupported }
    public enum Mark: Sendable { case online, run, off }
    public enum Purpose: Sendable { case file, read }
    public struct Speed: Equatable, Sendable { public let purpose: Purpose; public let name: String; public let value: String }
    public var kind: Kind
    public var mark: Mark
    /// The state word; ink2, never colored.
    public var word: String
    /// Second line of the status row: which single purpose goes direct, or why the relay is in use.
    public var sub: String?
    /// The one line the connection check puts under a computer's name.
    public var summary: String?
    /// Measured speeds only, one per purpose; a file not yet measured says it is compared on the next transfer.
    public var speeds: [Speed]

    // One decimal below 100, whole numbers from there; 99.96 is already 100.
    static func mibps(_ n: Double) -> String { (n * 10).rounded() / 10 >= 100 ? String(Int(n.rounded())) : String(format: "%.1f", n) }
    static func ms(_ n: Double) -> String { String(Int(n.rounded())) }

    static func reason(_ r: DirectReason) -> String? {
        switch r {
        case .peerUnsupported: L10n.transportReasonPeerUnsupported()
        case .noResponse: L10n.transportReasonNoResponse()
        case .noTraversal: L10n.transportReasonNoTraversal()
        case .failed: L10n.transportReasonFailed()
        case .disabled: nil
        }
    }

    /// Whether the relay measured no slower than the direct path: at least one purpose was measured
    /// on both paths, and on none of them is direct ahead.
    static func relayNotSlower(_ s: DirectStatus) -> Bool {
        let fd = s.fileMiBps[DirectPath.direct], fr = s.fileMiBps[DirectPath.relay], rd = s.readMs[DirectPath.direct], rr = s.readMs[DirectPath.relay]
        let file = fd != nil && fr != nil, read = rd != nil && rr != nil
        return (file || read) && (!file || fr! >= fd!) && (!read || rr! <= rd!)
    }

    static func speeds(_ s: DirectStatus) -> [Speed] {
        var out: [Speed] = []
        let f = s.fileMiBps, r = s.readMs, fd = f[DirectPath.direct], fr = f[DirectPath.relay], rd = r[DirectPath.direct], rr = r[DirectPath.relay]
        func file(_ v: String) { out.append(Speed(purpose: .file, name: L10n.transportSpeedFile(), value: v)) }
        func read(_ v: String) { out.append(Speed(purpose: .read, name: L10n.transportSpeedRead(), value: v)) }
        if s.state == .connected && fd == nil && fr == nil { file(L10n.transportFileSpeedPending()) }
        else if let fd, let fr { file(L10n.transportFileSpeedBoth(direct: mibps(fd), relay: mibps(fr))) }
        else if let fd { file(L10n.transportFileSpeedDirect(direct: mibps(fd))) }
        else if let fr { file(L10n.transportFileSpeedRelay(relay: mibps(fr))) }
        if let rd, let rr { read(L10n.transportReadLatencyBoth(direct: ms(rd), relay: ms(rr))) }
        else if let rd { read(L10n.transportReadLatencyDirect(direct: ms(rd))) }
        else if let rr { read(L10n.transportReadLatencyRelay(relay: ms(rr))) }
        return out
    }

    /// What a computer's direct path looks like to the user right now.
    public static func make(_ s: DirectStatus) -> DirectView {
        if s.state == .unsupported { return DirectView(kind: .unsupported, mark: .off, word: "", summary: nil, speeds: []) }
        if s.state == .none && s.reason == .disabled { return DirectView(kind: .off, mark: .off, word: "", summary: L10n.transportSummaryOff(), speeds: []) }
        let shown = speeds(s)
        func line(_ word: String, _ sub: String?) -> String { sub.map { "\(word) · \($0)" } ?? word }
        if s.state == .connected {
            if s.fileDirect || s.readDirect {
                let word = L10n.transportStatusDirect()
                // Both purposes direct say nothing more; one says which.
                let sub = s.fileDirect && s.readDirect ? nil : s.fileDirect ? L10n.transportUseFile() : L10n.transportUseRead()
                return DirectView(kind: .direct, mark: .online, word: word, sub: sub, summary: line(word, sub), speeds: shown)
            }
            if s.warming {
                let word = L10n.transportStatusMeasuring()
                return DirectView(kind: .measuring, mark: .run, word: word, summary: word, speeds: shown)
            }
            // Only a finished comparison says how the paths compare; without live samples on both
            // nothing is claimed, whatever older numbers are still shown.
            let word = L10n.transportStatusRelay()
            let sub = !s.compared ? nil : relayNotSlower(s) ? L10n.transportRelayFaster() : L10n.transportNotClearlyFaster()
            return DirectView(kind: .relay, mark: .off, word: word, sub: sub, summary: line(word, sub), speeds: shown)
        }
        if s.state == .connecting {
            let word = L10n.transportStatusConnecting()
            return DirectView(kind: .trying, mark: .run, word: word, summary: word, speeds: shown)
        }
        let word = L10n.transportStatusRelay()
        let why = s.reason.flatMap(reason)
        let sub = why.map { L10n.transportUnavailable(reason: $0) }
        return DirectView(kind: .relay, mark: .off, word: word, sub: sub, summary: line(word, sub), speeds: shown)
    }

    /// The state shown for a computer whose direct path was only just turned on and has not
    /// reported anything yet: trying, which is what is happening.
    public static func trying() -> DirectView {
        let word = L10n.transportStatusConnecting()
        return DirectView(kind: .trying, mark: .run, word: word, summary: word, speeds: [])
    }
}
