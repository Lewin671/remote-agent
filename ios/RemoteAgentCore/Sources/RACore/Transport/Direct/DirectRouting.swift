import Foundation

enum RouteName: String, Sendable { case relay, direct }
enum UploadKind: Sendable { case image, file }
enum RouteHint: Sendable, Equatable {
    case read
    case bulk(file: String, upload: UploadKind? = nil, bytes: Int? = nil)
    var isRead: Bool { if case .read = self { return true }; return false }
}

/// docs/transport.md 「只读选路」: every number is that document's (the web's READ_RULE), kept in
/// one place.
enum ReadRule {
    /// Warm-up after a connection, a network change or a fallback: authorized background read
    /// samples, alternating paths, bounded in time, rate and count.
    static let warmupWindowMs = 5000.0, warmupPerPath = 10, warmupMinGapMs = 250.0
    /// Fast upgrade: a clear win needs few samples.
    static let fastWindowMs = 60_000.0, fastMinSamples = 8, fastMinSpanMs = 2000.0, fastMedianRatio = 0.7
    /// Fallback: 0.7 vs 1.2 is the gap that stops flapping.
    static let backWindowMs = 30_000.0, backDirectSamples = 8, backRelaySamples = 4, backMedianRatio = 1.2, backSlackMs = 10.0
    /// Stability wait after any fallback before direct may be chosen again.
    static let stableMs = 5000.0
    static let keep = 512, keepMs = 60_000.0
}

/// Uses verified application completions. Trial transfers carry disjoint file chunks; a hot path
/// keeps one relay sample in eight, never duplicate bodies. All methods are called under the
/// engine's lock (it owns one per computer), so this type has no lock of its own.
final class DirectRouting: @unchecked Sendable {
    struct Sample { var at: Double; var ms: Double; var queued: Int }
    private struct Window { var bytes = 0.0, ms = 0.0; var rate: Double { ms > 0 ? bytes / ms : 0 } }
    private struct Pair { var relay = Window(), direct = Window()
        subscript(p: RouteName) -> Window { get { p == .relay ? relay : direct } set { if p == .relay { relay = newValue } else { direct = newValue } } } }
    private struct Counts { var relay = 0, direct = 0
        subscript(p: RouteName) -> Int { get { p == .relay ? relay : direct } set { if p == .relay { relay = newValue } else { direct = newValue } } } }
    private struct Flags { var relay = false, direct = false
        subscript(p: RouteName) -> Bool { get { p == .relay ? relay : direct } set { if p == .relay { relay = newValue } else { direct = newValue } } } }
    private struct Samples { var relay: [Sample] = [], direct: [Sample] = []
        subscript(p: RouteName) -> [Sample] { get { p == .relay ? relay : direct } set { if p == .relay { relay = newValue } else { direct = newValue } } } }
    private struct Bulk { var measured = Pair(), recent = Pair(); var comparisons = 0; var preferred = RouteName.relay, measuring = RouteName.relay; var bad = 0; var stableAfter = 0.0; var window: Int }
    private struct Warm { var start = Double.infinity, end = -Double.infinity; var issued = Counts(); var inflight = Flags(); var last = RouteName.relay }

    private var reads = Samples(), recentReads = Samples()
    private var readPreferred = RouteName.relay
    private var readStableAfter = 0.0
    private var readBad = 0, readWindow = 0, sample = 0
    private var warm = Warm()
    private var lastProbeAt = -Double.infinity
    /// Display only: each path's last window median that had at least three samples.
    private var lastReadMs: [RouteName: Double] = [:]
    private var files: [String: Bulk] = [:], fileOrder: [String] = []
    private(set) var filePath = RouteName.relay
    private var fileRate: [RouteName: Double] = [:]
    private var fileTally = Pair()
    private let now: () -> Double
    private let armed: (() -> Void)?
    init(now: @escaping () -> Double, armed: (() -> Void)? = nil) { self.now = now; self.armed = armed; readWindow = Int(now() / 5000) }

    private static func sorted(_ s: [Sample]) -> [Double] { s.map(\.ms).sorted() }
    private static func quantile(_ s: [Sample], _ q: Double) -> Double { let a = sorted(s), i = Int((Double(s.count) * q).rounded(.up)) - 1; return i >= 0 && i < a.count ? a[i] : .infinity }
    private static func median(_ s: [Sample]) -> Double { let a = sorted(s); return a.isEmpty ? .infinity : a[a.count / 2] }
    private static func span(_ s: [Sample]) -> Double { s.isEmpty ? 0 : s[s.count - 1].at - s[0].at }
    private func live(_ p: RouteName, _ ms: Double, _ at: Double? = nil) -> [Sample] { let t = at ?? now(); return reads[p].filter { t - $0.at <= ms } }

    /// What has really been measured: a path with no samples is absent, never zero. Throughput in
    /// MiB/s; latency the median of the last minute (at least three samples), else the last such.
    func measured() -> (fileMiBps: [RouteName: Double], readMs: [RouteName: Double]) {
        var file: [RouteName: Double] = [:], read: [RouteName: Double] = [:]
        for p in [RouteName.relay, .direct] {
            if let r = fileRate[p] { file[p] = r * 1000 / Double(1 << 20) }
            let samples = live(p, ReadRule.fastWindowMs)
            if samples.count >= 3 { lastReadMs[p] = Self.median(samples) }
            if let shown = lastReadMs[p] { read[p] = shown }
        }
        return (file, read)
    }
    /// Path that read requests use now; the same gate as choose() for reads.
    func readPath() -> RouteName { now() >= readStableAfter ? readPreferred : .relay }
    /// Whether the live window lacks the samples a decision needs on either path.
    func sparse() -> Bool { live(.relay, ReadRule.fastWindowMs).count < ReadRule.fastMinSamples || live(.direct, ReadRule.fastWindowMs).count < ReadRule.fastMinSamples }
    /// Whether the window holds what a decision needs on both paths; only then can the page say how direct compares.
    func compared() -> Bool { !sparse() }

    /// Starts a warm-up: background samples of both paths, right away when the read path is
    /// ready, after the stability wait when a fallback just happened. A trigger while one runs
    /// is absorbed. `fresh` is the trigger "a new read PC became ready": a window that started
    /// before there was a path to measure would leave too few samples, so it is replaced.
    func arm(fresh: Bool = false) {
        let t = now()
        if !fresh && t >= warm.start && t < warm.end { return }
        let start = max(t, readStableAfter)
        warm = Warm(start: start, end: start + ReadRule.warmupWindowMs, issued: Counts(), inflight: warm.inflight, last: .relay)
        armed?()
    }
    /// A warm-up window is open or still waiting to open.
    func pending() -> Bool { now() < warm.end }
    /// Running, or waiting out the stability period before it starts.
    func measuring() -> Bool { warming() || (now() < warm.start && now() < warm.end) }
    func warming() -> Bool {
        let t = now()
        return t >= warm.start && t < warm.end && !(warm.issued.relay >= ReadRule.warmupPerPath && warm.issued.direct >= ReadRule.warmupPerPath && !warm.inflight.relay && !warm.inflight.direct)
    }
    /// The path to take the next warm-up sample on: alternating (the less sampled first, direct
    /// before relay), one in flight per path, at most ten per path, never closer than 250 ms.
    func nextProbe() -> RouteName? {
        let t = now()
        if t < warm.start || t >= warm.end || t - lastProbeAt < ReadRule.warmupMinGapMs { return nil }
        var open = [RouteName.direct, .relay].filter { warm.issued[$0] < ReadRule.warmupPerPath && !warm.inflight[$0] }
        open.sort { a, b in warm.issued[a] != warm.issued[b] ? warm.issued[a] < warm.issued[b] : (a == warm.last ? false : true) }
        guard let path = open.first else { return nil }
        warm.issued[path] += 1; warm.inflight[path] = true; warm.last = path; lastProbeAt = t
        return path
    }
    func probeDone(_ path: RouteName) { warm.inflight[path] = false }

    func choose(_ hint: RouteHint, ready: Bool) -> RouteName {
        guard ready else { return .relay }
        if hint.isRead {
            advanceReads(); decideReads(now())
            if now() < readStableAfter { return .relay }
            if readPreferred == .direct { sample += 1; return sample % 8 == 0 ? .relay : .direct }
            return .relay
        }
        guard case .bulk(let key, _, _) = hint else { return .relay }
        advanceBulk(hint, key)
        let file = file(key)
        if now() < file.stableAfter { return .relay }
        if file.preferred == .direct { sample += 1; return sample % 8 == 0 ? .relay : .direct }
        return file.measuring
    }

    func observe(_ hint: RouteHint, _ path: RouteName, bytes: Int, ms: Double, queued: Int = 0) {
        let t = now()
        guard ms.isFinite, ms > 0 else { return }
        if hint.isRead {
            advanceReads()
            let s = Sample(at: t, ms: ms, queued: queued)
            reads[path].append(s); recentReads[path].append(s)
            for p in [RouteName.relay, .direct] {
                while reads[p].count > ReadRule.keep || (!reads[p].isEmpty && t - reads[p][0].at > ReadRule.keepMs) { reads[p].removeFirst() }
            }
            let live = self.live(path, ReadRule.fastWindowMs, t)
            if live.count >= 3 { lastReadMs[path] = Self.median(live) }
            decideReads(t)
            return
        }
        guard case .bulk(let key, _, _) = hint else { return }
        advanceBulk(hint, key); filePath = path
        guard bytes > 0 else { return }
        fileTally[path].bytes += Double(bytes); fileTally[path].ms += ms
        if fileTally[path].bytes >= Double(4 << 20) { fileRate[path] = fileTally[path].rate; fileTally[path] = Window() }
        var f = file(key)
        f.measured[path].bytes += Double(bytes); f.measured[path].ms += ms; f.recent[path].bytes += Double(bytes); f.recent[path].ms += ms
        if f.preferred == .relay {
            if f.measuring == .relay && f.measured.relay.bytes >= Double(4 << 20) { f.measuring = .direct }
            if f.measured.relay.bytes >= Double(4 << 20) && f.measured.direct.bytes >= Double(4 << 20) {
                f.comparisons = f.measured.direct.rate >= f.measured.relay.rate * 0.95 ? f.comparisons + 1 : 0
                if f.comparisons >= 2 && t >= f.stableAfter { f.preferred = .direct; f.bad = 0; f.recent = Pair() }
                f.measured = Pair(); f.measuring = .relay
            }
        }
        files[key] = f
    }

    /// Upgrades when direct is clearly or steadily better, falls back when it is clearly worse.
    private func decideReads(_ t: Double) {
        if readPreferred == .relay {
            if t < readStableAfter { return }
            let relay = live(.relay, ReadRule.fastWindowMs, t), direct = live(.direct, ReadRule.fastWindowMs, t)
            let queueStable = !direct.isEmpty && direct[direct.count - 1].queued <= direct[0].queued
            let clear = relay.count >= ReadRule.fastMinSamples && direct.count >= ReadRule.fastMinSamples && Self.span(relay) >= ReadRule.fastMinSpanMs && Self.span(direct) >= ReadRule.fastMinSpanMs && queueStable
                && Self.median(direct) <= ReadRule.fastMedianRatio * Self.median(relay) && Self.quantile(direct, 0.9) <= Self.median(relay)
            let steady = relay.count >= 100 && direct.count >= 100 && t - max(relay[0].at, direct[0].at) >= 5000 && queueStable && Self.quantile(direct, 0.95) <= Self.quantile(relay, 0.95) + max(5, Self.quantile(relay, 0.95) * 0.10)
            if clear || steady { readPreferred = .direct }
            return
        }
        let relay = live(.relay, ReadRule.backWindowMs, t), direct = live(.direct, ReadRule.backWindowMs, t)
        if direct.count >= ReadRule.backDirectSamples && relay.count >= ReadRule.backRelaySamples && Self.median(direct) > ReadRule.backMedianRatio * Self.median(relay) + ReadRule.backSlackMs { fail(.read) }
    }
    private func advanceReads() {
        let window = Int(now() / 5000)
        if window <= readWindow { return }
        let bad = !recentReads.relay.isEmpty && !recentReads.direct.isEmpty && Self.quantile(recentReads.direct, 0.95) > Self.quantile(recentReads.relay, 0.95) + max(10, Self.quantile(recentReads.relay, 0.95) * 0.20)
        readBad = bad ? (window == readWindow + 1 ? readBad + 1 : 1) : 0
        recentReads = Samples(); readWindow = window
        if readBad >= 2 { fail(.read) }
    }
    private func advanceBulk(_ hint: RouteHint, _ key: String) {
        var f = file(key)
        let window = Int(now() / 5000)
        if window <= f.window { return }
        let bad = f.recent.relay.bytes > 0 && f.recent.direct.bytes > 0 && f.recent.direct.rate < f.recent.relay.rate * 0.90
        f.bad = bad ? (window == f.window + 1 ? f.bad + 1 : 1) : 0
        f.recent = Pair(); f.window = window
        files[key] = f
        if f.bad >= 2 { fail(hint) }
    }
    /// A hard failure or soft deadline: back to the relay at once, a stability wait before direct
    /// may be chosen again, samples cleared (the last measured numbers with them).
    func fail(_ hint: RouteHint? = nil) {
        let until = now() + ReadRule.stableMs
        if hint == nil || hint!.isRead {
            readPreferred = .relay; readStableAfter = until; reads = Samples(); recentReads = Samples(); readBad = 0; lastReadMs = [:]
            // A fallback re-measures after the stability wait; a window still open is cancelled.
            warm.end = -.infinity; arm()
        }
        var keys: [String] = []
        if hint == nil { keys = Array(files.keys) } else if case .bulk(let key, _, _) = hint! { _ = file(key); keys = [key] }
        for key in keys {
            var f = files[key]!
            f.preferred = .relay; f.stableAfter = until; f.comparisons = 0; f.measured = Pair(); f.recent = Pair(); f.measuring = .relay; f.bad = 0
            files[key] = f
        }
        if hint == nil || !hint!.isRead { filePath = .relay; fileRate = [:]; fileTally = Pair() }
    }
    private func file(_ key: String) -> Bulk {
        if let f = files[key] { return f }
        if files.count >= 32, let oldest = fileOrder.first { files.removeValue(forKey: oldest); fileOrder.removeFirst() }
        let f = Bulk(window: Int(now() / 5000)); files[key] = f; fileOrder.append(key)
        return f
    }
}
