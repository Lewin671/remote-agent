import Charts
import RACore
import RAProto
import SwiftUI
import UIKit

/// Shared token / rate / latency formatting matching Core / Web / Android specs.
enum UsageFormat {
    static var isChinese: Bool { MessageLocale.current.lowercased().hasPrefix("zh") }

    static func commaUint(_ n: Double) -> String {
        guard n.isFinite else { return "0" }
        let value = max(0, n.rounded())
        let fmt = NumberFormatter()
        fmt.locale = Locale(identifier: "en_US")
        fmt.numberStyle = .decimal
        fmt.usesGroupingSeparator = true
        fmt.maximumFractionDigits = 0
        return fmt.string(from: NSNumber(value: value)) ?? String(Int64(value))
    }

    private static func scaled(_ val: Double, unit: String) -> String {
        if val < 100 {
            let rounded = (val * 10).rounded() / 10
            if rounded == rounded.rounded() {
                return "\(Int(rounded))\(unit)"
            }
            return String(format: "%.1f%@", rounded, unit)
        }
        return commaUint(val.rounded()) + unit
    }

    static func formatTokens(_ val: Double) -> String {
        guard val.isFinite, val > 0 else { return "0" }
        if !isChinese {
            if val < 1000 { return commaUint(val) }
            if val < 999_500 { return scaled(val / 1000, unit: "k") }
            if val < 999_500_000 { return scaled(val / 1_000_000, unit: "M") }
            return scaled(val / 1_000_000_000, unit: "B")
        }
        let wan = L10n.usageUnitWan()
        let yi = L10n.usageUnitYi()
        if val < 10_000 { return commaUint(val) }
        if val < 99_950_000 { return scaled(val / 10_000, unit: wan) }
        if val < 100_000_000 { return "1\(yi)" }
        return scaled(val / 100_000_000, unit: yi)
    }

    static func formatCacheHit(_ rate: Double) -> String {
        guard rate.isFinite, rate > 0 else { return "0%" }
        if rate >= 1 { return "100%" }
        let pct = rate * 100
        if pct < 1 || pct > 99 {
            let rounded = (pct * 10).rounded() / 10
            if rounded == rounded.rounded() { return "\(Int(rounded))%" }
            return String(format: "%.1f%%", rounded)
        }
        return "\(Int(pct.rounded()))%"
    }

    static func formatSpeed(_ tokPerSec: Double) -> String {
        guard tokPerSec.isFinite, tokPerSec > 0 else { return "0" }
        if tokPerSec < 10 {
            let rounded = (tokPerSec * 10).rounded() / 10
            if rounded == rounded.rounded() { return "\(Int(rounded))" }
            return String(format: "%.1f", rounded)
        }
        return commaUint(tokPerSec.rounded())
    }

    static func formatTtft(_ ms: Double) -> String {
        guard ms.isFinite, ms > 0 else { return "0 ms" }
        if ms < 1000 { return "\(Int(ms.rounded())) ms" }
        if ms < 10_000 { return String(format: "%.1f s", ((ms / 100).rounded() / 10)) }
        return "\(Int((ms / 1000).rounded())) s"
    }

    static func formatTtftSummary(_ ms: Double) -> String {
        guard ms.isFinite, ms > 0 else { return "—" }
        if ms >= 60_000 {
            let totalSec = Int((ms / 1000).rounded())
            return L10n.usageDurationMinSec(min: totalSec / 60, sec: totalSec % 60)
        }
        return formatTtft(ms)
    }

    static func formatSignedPercent(_ pct: Double) -> String {
        guard pct.isFinite else { return "—" }
        if abs(pct) < 0.5 { return L10n.usageFlat() }
        let r = Int(pct.rounded())
        return r > 0 ? "+\(r)%" : "−\(abs(r))%"
    }

    static func formatSignedPoints(_ pts: Double) -> String {
        guard pts.isFinite else { return "—" }
        if abs(pts) < 0.5 { return L10n.usageFlat() }
        let r = Int(pts.rounded())
        return r > 0 ? "+\(r) pt" : "−\(abs(r)) pt"
    }

    static func formatTtftDelta(curMs: Double, prevMs: Double) -> String {
        guard curMs.isFinite, prevMs.isFinite, prevMs > 0 else { return "—" }
        let diff = curMs - prevMs
        if abs(diff) < 50 { return L10n.usageFlat() }
        let sign = diff > 0 ? "+" : "−"
        let absMs = abs(diff)
        if absMs < 1000 { return "\(sign)\(Int(absMs.rounded())) ms" }
        if absMs < 10_000 { return String(format: "%@%.1f s", sign, (absMs / 100).rounded() / 10) }
        return "\(sign)\(Int((absMs / 1000).rounded())) s"
    }
}

/// 用量与速度 screen driven by Core's `PBCoreUsageViewModel`.
struct UsageScreen: View {
    let machineID: String
    var focusAgent: PBAgent = .unspecified
    var focusModel: String = ""
    @Environment(AccountContext.self) private var context

    var body: some View {
        let vm = context.core.usageView
        let effective = vm.machineID == machineID ? vm : loadingFallback(machineID)
        UsageScreenContent(
            vm: effective,
            onRefresh: {
                await context.coreHost.openUsageScreen(
                    machineID: machineID,
                    focusAgent: focusAgent,
                    focusModel: focusModel
                )
            },
            onSelectMetric: { metric in
                Task { await context.coreHost.usageSelectMetric(metric) }
            },
            onSelectRange: { preset in
                Task { await context.coreHost.usageSelectRange(preset) }
            },
            onSelectCustomDates: { startMs, endMs in
                Task { await context.coreHost.usageSelectCustomDates(startMs: startMs, endMs: endMs) }
            },
            onRetry: {
                Task { await context.coreHost.retryUsageScreen(machineID: machineID) }
            }
        )
        .task(id: "\(machineID):\(focusAgent.rawValue):\(focusModel)") {
            await context.coreHost.openUsageScreen(
                machineID: machineID,
                focusAgent: focusAgent,
                focusModel: focusModel
            )
        }
    }

    private func loadingFallback(_ id: String) -> PBCoreUsageViewModel {
        PBCoreUsageViewModel.with {
            $0.machineID = id
            $0.loading = true
            $0.supported = true
        }
    }
}

struct UsageScreenContent: View {
    let vm: PBCoreUsageViewModel
    var onRefresh: (@MainActor @Sendable () async -> Void)? = nil
    var onSelectMetric: (PBCoreUsageMetricTab) -> Void = { _ in }
    var onSelectRange: (PBUsageRangePreset) -> Void = { _ in }
    var onSelectCustomDates: (Int64, Int64) -> Void = { _, _ in }
    var onRetry: () -> Void = {}

    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    @Environment(\.colorScheme) private var colorScheme

    @State private var selectedKeys: [String] = []
    @State private var scrubIdx: Int?
    @State private var showSkeleton = false
    @State private var customSheetOpen = false
    @State private var draftStartDate = Date()
    @State private var draftEndDate = Date()

    private static let defaultOverlaySeries = 3
    private static let maxOverlaySeries = 4
    private static let fewSamplesMin: UInt64 = 3

    private var metric: PBCoreUsageMetricTab {
        vm.metric == .unspecified ? .tokens : vm.metric
    }

    private var preset: PBUsageRangePreset {
        vm.preset == .unspecified ? .sevenDays : vm.preset
    }

    private var machineName: String {
        vm.machineName.or(vm.machineID)
    }

    private var seriesSignature: String {
        vm.series.map { "\($0.seriesKey):\($0.focused ? 1 : 0)" }.joined(separator: "|")
    }

    private var selectedSeries: [PBCoreUsageSeriesView] {
        let byKey = Dictionary(uniqueKeysWithValues: vm.series.map { ($0.seriesKey, $0) })
        let picked = selectedKeys.compactMap { byKey[$0] }
        if !picked.isEmpty { return picked }
        return Array(vm.series.prefix(1))
    }

    var body: some View {
        let subtitle = vm.found
            ? (vm.online ? L10n.usageSubtitleLive(name: machineName) : L10n.usageSubtitleOffline(name: machineName))
            : nil

        SettingsPage(refresh: onRefresh) {
            controlsSection
            bannersSection
            bodySection
        }
        .navBar(L10n.usageTitle(), subtitle: subtitle)
        .onChange(of: seriesSignature, initial: true) { _, _ in
            selectedKeys = Array(vm.series.prefix(Self.defaultOverlaySeries).map(\.seriesKey))
        }
        .onChange(of: metric) { _, _ in scrubIdx = nil }
        .onChange(of: preset) { _, _ in scrubIdx = nil }
        .onChange(of: vm.startMs) { _, _ in scrubIdx = nil }
        .onChange(of: vm.endMs) { _, _ in scrubIdx = nil }
        .task(id: vm.loading) {
            if !vm.loading {
                showSkeleton = false
                return
            }
            try? await Task.sleep(for: .milliseconds(200))
            if !Task.isCancelled && vm.loading {
                showSkeleton = true
            }
        }
        .kitSheet(isPresented: $customSheetOpen) {
            customRangeSheet
        }
    }

    // MARK: Controls

    @ViewBuilder private var controlsSection: some View {
        let metrics: [(PBCoreUsageMetricTab, String)] = [
            (.tokens, L10n.usageTabTokens()),
            (.cacheHit, L10n.usageTabCacheHit()),
            (.outputSpeed, L10n.usageTabSpeed()),
            (.ttft, L10n.usageTabTtft()),
        ]
        let ranges: [(PBUsageRangePreset, String)] = [
            (.today, L10n.usageRangeToday()),
            (.sevenDays, L10n.usageRange7D()),
            (.thirtyDays, L10n.usageRange30D()),
            (.custom, L10n.usageRangeCustom()),
        ]

        VStack(alignment: .leading, spacing: 8) {
            let metricBinding = Binding<PBCoreUsageMetricTab>(
                get: { metric },
                set: { onSelectMetric($0) }
            )
            if typeSize.isAccessibilitySize {
                ScrollView(.horizontal, showsIndicators: false) {
                    SegPick(metrics.map(\.1), selected: Binding(
                        get: { metrics.firstIndex { $0.0 == metric } ?? 0 },
                        set: { onSelectMetric(metrics[$0].0) }
                    ))
                }
            } else {
                Picker(L10n.usageTitle(), selection: metricBinding) {
                    ForEach(metrics, id: \.0.rawValue) { item in
                        Text(item.1).tag(item.0)
                    }
                }
                .pickerStyle(.segmented)
            }

            let rangeBinding = Binding<PBUsageRangePreset>(
                get: { preset },
                set: { next in
                    if next == .custom {
                        syncDraftCustomDates()
                        customSheetOpen = true
                    }
                    onSelectRange(next)
                }
            )
            if typeSize.isAccessibilitySize {
                ScrollView(.horizontal, showsIndicators: false) {
                    SegPick(ranges.map(\.1), selected: Binding(
                        get: { ranges.firstIndex { $0.0 == preset } ?? 1 },
                        set: { idx in
                            let next = ranges[idx].0
                            if next == .custom {
                                syncDraftCustomDates()
                                customSheetOpen = true
                            }
                            onSelectRange(next)
                        }
                    ))
                }
            } else {
                Picker(L10n.usageRange7D(), selection: rangeBinding) {
                    ForEach(ranges, id: \.0.rawValue) { item in
                        Text(item.1).tag(item.0)
                    }
                }
                .pickerStyle(.segmented)
            }
        }
        .padding(.horizontal, DS.Space.l)
        .padding(.bottom, 10)

        if preset == .custom {
            let clamped = CoreHost.clampUsageCustomRange(
                startMs: vm.startMs,
                endMs: vm.endMs > 0 ? vm.endMs - 86_400_000 : 0
            )
            let startMs = vm.startMs > 0 ? vm.startMs : clamped.startMs
            let endIncMs = vm.endMs > 0 ? vm.endMs - 1 : clamped.endMs
            let days = max(1, Int(((vm.endMs > vm.startMs ? Double(vm.endMs - vm.startMs) : Double(endIncMs - startMs + 86_400_000)) / 86_400_000.0).rounded()))
            GroupCard {
                GRow(
                    L10n.usageCustomRangeSummary(
                        days: days,
                        end: formatFullDay(endIncMs),
                        start: formatFullDay(startMs)
                    ),
                    value: L10n.usageCustomRangeChange(),
                    chevron: true
                ) {
                    syncDraftCustomDates()
                    customSheetOpen = true
                }
            }
        }
    }

    @ViewBuilder private var customRangeSheet: some View {
        let bounds = customPickerDateBounds()
        SheetHead(L10n.usageRangeCustom(), action: L10n.commonDone()) {
            customSheetOpen = false
            let sMs = Int64(Calendar.current.startOfDay(for: draftStartDate).timeIntervalSince1970 * 1000)
            let eMs = Int64(Calendar.current.startOfDay(for: draftEndDate).timeIntervalSince1970 * 1000)
            let clamped = CoreHost.clampUsageCustomRange(startMs: sMs, endMs: eMs)
            onSelectCustomDates(clamped.startMs, clamped.endMs)
        }
        GroupCard {
            DatePicker(
                L10n.usageCustomStartLabel(),
                selection: $draftStartDate,
                in: bounds.min...bounds.max,
                displayedComponents: .date
            )
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            DatePicker(
                L10n.usageCustomEndLabel(),
                selection: $draftEndDate,
                in: draftStartDate...bounds.max,
                displayedComponents: .date
            )
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
        }
        GroupNote(L10n.usageCustomRangeLimitHint())
    }

    // MARK: Banners

    @ViewBuilder private var bannersSection: some View {
        let errStr = vm.errorText.rendered()
        if vm.cachedOffline {
            GroupBanner(
                L10n.usageOfflineBanner(name: machineName, time: formatOfflineTime(vm.cachedAtMs)),
                tone: .warn
            )
        }
        if vm.backfilling {
            GroupBanner(
                L10n.usageBackfillingBanner(
                    scanned: Int(vm.backfillScannedDays),
                    total: vm.backfillTotalDays > 0 ? Int(vm.backfillTotalDays) : 90
                ),
                tone: .plain
            )
        }
        if !errStr.isEmpty && !vm.series.isEmpty {
            let errMessage = [L10n.usageLoadErrorTitle(), errStr].joined(separator: L10n.commonIosNoteSeparator())
            GroupBanner(
                errMessage,
                tone: .error,
                action: L10n.commonRetry(),
                onAction: onRetry
            )
        }
    }

    // MARK: States & Main Body

    @ViewBuilder private var bodySection: some View {
        let errStr = vm.errorText.rendered()
        let allSourcesUnavailable = vm.found && vm.supported && (
            (!vm.sources.isEmpty && vm.sources.allSatisfy { $0.state == .unavailable })
                || (vm.series.isEmpty && vm.unsupportedFootnote.key == "usage.only_unavailable_title")
        )
        let unavailNames = vm.sources
            .filter { $0.state == .unavailable }
            .map { agentName($0.agent) }
            .joined(separator: ", ")
            .or("Antigravity")
        let showOfflineEmpty = vm.found && vm.supported && !vm.online && !vm.cachedOffline && vm.series.isEmpty && !vm.loading
        let showGenericEmpty = vm.found && vm.supported && vm.online && vm.series.isEmpty && !vm.loading && errStr.isEmpty && !allSourcesUnavailable

        if !vm.found && vm.loading {
            if showSkeleton { skeletonCard }
        } else if !vm.found {
            PageNote(L10n.settingsIosThisComputerIsNotInTheCurrentTrustedDirectory())
        } else if !vm.supported {
            emptyStateCard(
                title: L10n.usageUnsupportedTitle(),
                body: L10n.usageUnsupportedBody(name: machineName)
            )
        } else if showOfflineEmpty {
            emptyStateCard(
                title: L10n.usageOfflineEmptyTitle(name: machineName),
                body: L10n.usageOfflineEmptyBody(name: machineName)
            )
        } else if !errStr.isEmpty && vm.series.isEmpty {
            emptyStateCard(
                title: L10n.usageLoadErrorTitle(),
                body: errStr,
                actionTitle: L10n.commonRetry(),
                action: onRetry
            )
        } else if vm.loading && vm.series.isEmpty {
            if showSkeleton { skeletonCard }
        } else if allSourcesUnavailable {
            emptyStateCard(
                title: L10n.usageOnlyUnavailableTitle(agents: unavailNames),
                body: L10n.usageOnlyUnavailableBody(agents: unavailNames)
            )
        } else if showGenericEmpty {
            emptyStateCard(
                title: L10n.usageEmptyTitle(),
                body: L10n.usageEmptyBody()
            )
        } else if !vm.series.isEmpty {
            activeUsageContent
        }
    }

    @ViewBuilder private var skeletonCard: some View {
        GroupCard {
            VStack(alignment: .leading, spacing: 12) {
                RoundedRectangle(cornerRadius: 6).fill(DS.fill).frame(width: 140, height: 14)
                RoundedRectangle(cornerRadius: 8).fill(DS.fill).frame(width: 180, height: 28)
                RoundedRectangle(cornerRadius: 10).fill(DS.fill).frame(height: 150)
            }
            .padding(14)
            .accessibilityLabel(L10n.commonIosLoading())
        }
    }

    @ViewBuilder private func emptyStateCard(
        title: String,
        body: String,
        actionTitle: String? = nil,
        action: (() -> Void)? = nil
    ) -> some View {
        GroupCard {
            VStack(alignment: .leading, spacing: 6) {
                Text(title).font(.body.weight(.semibold)).foregroundStyle(DS.ink)
                Text(body).font(.subheadline).foregroundStyle(DS.ink2)
                if let actionTitle, let action {
                    Btn(actionTitle, kind: .primary, action: action)
                        .padding(.top, 8)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(14)
        }
    }

    // MARK: Active Chart + Breakdown + Footnotes

    @ViewBuilder private var activeUsageContent: some View {
        let summary = computeSummary(selectedSeries)
        let bucketCount = selectedSeries.map(\.buckets.count).max() ?? 0
        let fewSamples = metric != .tokens && !selectedSeries.isEmpty
            && selectedSeries.allSatisfy { isFewSamples($0, metric: metric) }
        let pairHeadline = makePairHeadline(selectedSeries)

        GroupCard {
            VStack(alignment: .leading, spacing: 12) {
                cardHeader(pairHeadline: pairHeadline, summary: summary, anyFewSamples: fewSamples)
                if vm.backfilling && vm.previousPeriodBackfilling {
                    let backfillNote = L10n.usageBackfilledSummary(name: machineName, scanned: Int(vm.backfillScannedDays))
                        + " · "
                        + L10n.usageBackfillingZone()
                    Text(backfillNote)
                        .font(.footnote)
                        .foregroundStyle(DS.ink2)
                }
                if bucketCount > 0 {
                    chartView(selectedSeries: selectedSeries, bucketCount: bucketCount)
                }
                if let scrubIdx {
                    scrubReadoutView(bi: scrubIdx, selectedSeries: selectedSeries)
                }
                if fewSamples {
                    Text(L10n.usageFewSamplesHint())
                        .font(.footnote)
                        .foregroundStyle(DS.ink2)
                }
                if metric == .tokens {
                    tokenCompositionView(summary: summary)
                }
            }
            .padding(14)
        }

        GroupCard {
            ForEach(Array(vm.series.enumerated()), id: \.element.seriesKey) { _, s in
                let slot = selectedSeries.firstIndex { $0.seriesKey == s.seriesKey }
                seriesRow(s, slot: slot)
            }
        }

        footnotesView(hasOutlier: summary.hasOutlier)
    }

    // MARK: Card Header & Summary

    private struct ActiveSummary {
        let uncached: Double
        let read: Double
        let write: Double
        let out: Double
        let reas: Double
        let total: Double
        let usageReqs: UInt64
        let speedReqs: UInt64
        let hitRate: Double
        let inDenom: Double
        let tokDeltaPct: Double?
        let prevLabel: String
        let hasOutlier: Bool
        let primary: PBCoreUsageSeriesView?
    }

    private func computeSummary(_ picked: [PBCoreUsageSeriesView]) -> ActiveSummary {
        var uncached = 0.0
        var read = 0.0
        var write = 0.0
        var out = 0.0
        var reas = 0.0
        var prevTok = 0.0
        var usageReqs: UInt64 = 0
        var speedReqs: UInt64 = 0
        var hasOutlier = false

        for s in picked where s.hasCurrent {
            let c = s.current
            uncached += Double(c.uncachedInputTokens)
            read += Double(c.cacheReadTokens)
            write += Double(c.cacheWriteTokens)
            out += Double(c.outputTokens)
            reas += Double(c.reasoningOutputTokens)
            usageReqs += c.usageRequestCount
            speedReqs += c.speedRequestCount
            if s.hasPrevious {
                let p = s.previous
                prevTok += Double(p.uncachedInputTokens + p.cacheReadTokens + p.cacheWriteTokens + p.outputTokens)
            }
            if c.speedP50 > 0 && c.speedP90 > c.speedP50 * 4 { hasOutlier = true }
            if c.ttftP50Ms > 0 && c.ttftP90Ms > c.ttftP50Ms * 4 { hasOutlier = true }
        }
        let total = uncached + read + write + out
        let inDenom = uncached + read + write
        let hitRate = inDenom > 0 ? read / inDenom : 0
        let prevLabel: String = switch preset {
        case .sevenDays: L10n.usageSubVsPrev7D()
        case .thirtyDays: L10n.usageSubVsPrev30D()
        default: L10n.usageSubVsPrevGeneric()
        }
        let anyPrevBf = picked.contains(where: \.previousBackfilling)
        let deltaPct: Double? = !anyPrevBf && usageReqs >= 3 && prevTok > 0
            ? ((total - prevTok) / prevTok) * 100
            : nil

        return ActiveSummary(
            uncached: uncached, read: read, write: write, out: out, reas: reas,
            total: total, usageReqs: usageReqs, speedReqs: speedReqs,
            hitRate: hitRate, inDenom: inDenom, tokDeltaPct: deltaPct,
            prevLabel: prevLabel, hasOutlier: hasOutlier, primary: picked.first
        )
    }

    private func makePairHeadline(_ picked: [PBCoreUsageSeriesView]) -> String {
        if picked.count == 1, let s = picked.first {
            return "\(s.agentName) · \(s.model)"
        }
        if selectedKeys.count >= Self.maxOverlaySeries && vm.series.count > Self.maxOverlaySeries {
            return L10n.usagePairsCappedSummary(count: vm.series.count)
        }
        if picked.count < vm.series.count {
            return L10n.usagePairsSummary(count: vm.series.count, name: machineName)
        }
        return "\(machineName) · \(L10n.usageFilterAll())"
    }

    @ViewBuilder private func cardHeader(
        pairHeadline: String,
        summary: ActiveSummary,
        anyFewSamples: Bool
    ) -> some View {
        let prim = summary.primary
        let (kpiMain, kpiUnit, sub1, sub2): (String, String?, String, String) = {
            switch metric {
            case .tokens:
                let deltaStr = summary.tokDeltaPct.map(UsageFormat.formatSignedPercent) ?? "—"
                let hitStr = summary.inDenom > 0 ? UsageFormat.formatCacheHit(summary.hitRate) : "—"
                return (
                    UsageFormat.formatTokens(summary.total),
                    L10n.usageKpiUnitTokens(),
                    "\(summary.prevLabel) \(deltaStr)",
                    "\(L10n.usageTabCacheHit()) \(hitStr)"
                )
            case .cacheHit:
                let dStr = (prim?.hasCacheHitDelta_p == true)
                    ? UsageFormat.formatSignedPoints(prim!.cacheHitDeltaPoints)
                    : "—"
                let main = (anyFewSamples || summary.inDenom <= 0) ? "—" : UsageFormat.formatCacheHit(summary.hitRate)
                let second = anyFewSamples
                    ? L10n.usageFewSamplesCount(count: Int(summary.usageReqs))
                    : "\(summary.prevLabel) \(dStr)"
                return (main, nil, L10n.usageSubWeightedCache(), second)
            case .outputSpeed:
                let c = prim?.hasCurrent == true ? prim!.current : nil
                let p10 = (c?.hasSpeed_p == true) ? UsageFormat.formatSpeed(c!.speedP10) : "—"
                let p90 = (c?.hasSpeed_p == true) ? UsageFormat.formatSpeed(c!.speedP90) : "—"
                let subA = (c?.hasSpeed_p == true)
                    ? L10n.usageSubMedianP10P90(high: p90, low: p10)
                    : L10n.usageSubP10P90(high: p90, low: p10)
                let subB: String = if anyFewSamples {
                    L10n.usageFewSamplesCount(count: Int(summary.speedReqs))
                } else if let c, c.recent10SpeedCount > 0 && c.recent10SpeedP50 > 0 {
                    L10n.usageSubRecent10(speed: UsageFormat.formatSpeed(c.recent10SpeedP50))
                } else if let prim {
                    formatSeriesDeltaCell(prim, metric: .outputSpeed)
                } else {
                    "—"
                }
                if let c, c.hasSpeed_p {
                    return (UsageFormat.formatSpeed(c.speedP50), L10n.usageKpiUnitToks(), subA, subB)
                }
                return ("—", nil, subA, subB)
            case .ttft, .unspecified, .UNRECOGNIZED:
                let c = prim?.hasCurrent == true ? prim!.current : nil
                let p10 = (c?.hasTtft_p == true) ? UsageFormat.formatTtftSummary(c!.ttftP10Ms) : "—"
                let p90 = (c?.hasTtft_p == true) ? UsageFormat.formatTtftSummary(c!.ttftP90Ms) : "—"
                let subB = anyFewSamples
                    ? L10n.usageFewSamplesCount(count: Int(summary.speedReqs))
                    : (prim.map { "\(summary.prevLabel) \(formatSeriesDeltaCell($0, metric: .ttft))" } ?? "—")
                let main = (c?.hasTtft_p == true) ? UsageFormat.formatTtftSummary(c!.ttftP50Ms) : "—"
                return (main, nil, L10n.usageSubP10P90(high: p90, low: p10), subB)
            }
        }()

        let left = VStack(alignment: .leading, spacing: 2) {
            Text(pairHeadline)
                .font(.caption)
                .foregroundStyle(DS.ink2)
                .lineLimit(1)
            HStack(alignment: .firstTextBaseline, spacing: 4) {
                Text(kpiMain)
                    .font(.system(size: 26, weight: .bold, design: .rounded))
                    .monospacedDigit()
                    .foregroundStyle(DS.ink)
                if let kpiUnit {
                    Text(kpiUnit)
                        .font(.footnote)
                        .foregroundStyle(DS.ink2)
                }
            }
        }
        let right = VStack(alignment: typeSize.isAccessibilitySize ? .leading : .trailing, spacing: 2) {
            Text(sub1).font(.caption).foregroundStyle(DS.ink2)
            Text(sub2).font(.caption).foregroundStyle(DS.ink2)
        }

        if typeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 6) {
                left
                right
            }
        } else {
            HStack(alignment: .bottom, spacing: 8) {
                left
                Spacer(minLength: 0)
                right
            }
        }
    }

    // MARK: Charts

    @ViewBuilder private func chartView(selectedSeries: [PBCoreUsageSeriesView], bucketCount: Int) -> some View {
        let firstBuckets = selectedSeries.first?.buckets ?? []
        let a11yTotal = UsageFormat.formatTokens(computeSummary(selectedSeries).total)
        let metricTitle: String = switch metric {
        case .tokens: L10n.usageTabTokens()
        case .cacheHit: L10n.usageTabCacheHit()
        case .outputSpeed: L10n.usageTabSpeed()
        default: L10n.usageTabTtft()
        }
        let a11yLabel = metric == .tokens
            ? L10n.usageChartA11YTokens(count: selectedSeries.count, name: machineName, total: a11yTotal)
            : L10n.usageChartA11YLine(count: selectedSeries.count, metric: metricTitle, name: machineName)

        Chart {
            if metric == .tokens {
                ForEach(0..<bucketCount, id: \.self) { bi in
                    let segments = stackedBarSegments(selectedSeries: selectedSeries, bucketIndex: bi)
                    ForEach(segments) { seg in
                        BarMark(
                            x: .value("Bucket", bi),
                            yStart: .value("Start", seg.y0),
                            yEnd: .value("End", seg.y1)
                        )
                        .foregroundStyle(slotBarStyle(slot: seg.slot))
                        .opacity(scrubIdx == nil || scrubIdx == bi ? 1 : 0.35)
                    }
                }
            } else {
                ForEach(Array(selectedSeries.enumerated()), id: \.offset) { slot, s in
                    let few = isFewSamples(s, metric: metric)
                    let runs = lineRuns(series: s, metric: metric, few: few)
                    ForEach(runs) { run in
                        if !few && slot < 3 && metric != .cacheHit {
                            ForEach(run.points) { pt in
                                if let low = pt.low, let high = pt.high {
                                    AreaMark(
                                        x: .value("Bucket", pt.bi),
                                        yStart: .value("P10", low),
                                        yEnd: .value("P90", high),
                                        series: .value("Band", "\(s.seriesKey)#band#\(run.id)")
                                    )
                                    .foregroundStyle(lineColor(slot: slot).opacity(reduceTransparency ? 0 : 0.14))
                                }
                            }
                        }
                        if !few && run.points.count >= 2 {
                            ForEach(run.points) { pt in
                                LineMark(
                                    x: .value("Bucket", pt.bi),
                                    y: .value("Value", pt.val),
                                    series: .value("Series", "\(s.seriesKey)#\(run.id)")
                                )
                                .foregroundStyle(lineColor(slot: slot))
                                .lineStyle(lineStrokeStyle(slot: slot))
                            }
                        }
                        ForEach(run.points) { pt in
                            PointMark(
                                x: .value("Bucket", pt.bi),
                                y: .value("Value", pt.val)
                            )
                            .foregroundStyle(slot == 2 ? DS.gbg : lineColor(slot: slot))
                            .symbol {
                                seriesMarkerSymbol(slot: slot, highlighted: few || scrubIdx == pt.bi)
                            }
                        }
                    }
                }
            }
            if let scrubIdx, scrubIdx >= 0, scrubIdx < bucketCount {
                RuleMark(x: .value("Scrub", scrubIdx))
                    .foregroundStyle(DS.ink2.opacity(0.6))
                    .lineStyle(StrokeStyle(lineWidth: 1, dash: [3, 3]))
            }
        }
        .chartXScale(domain: -0.5...(Double(max(1, bucketCount)) - 0.5))
        .chartYScale(domain: 0...chartUpperBound(selectedSeries: selectedSeries))
        .chartXAxis {
            let indices = axisTickIndices(count: firstBuckets.count)
            AxisMarks(values: indices) { value in
                AxisGridLine().foregroundStyle(Color.clear)
                AxisValueLabel {
                    if let idx = value.as(Int.self), firstBuckets.indices.contains(idx) {
                        Text(formatDateLabel(firstBuckets[idx].startMs, granularity: vm.granularity))
                            .font(.caption2)
                            .foregroundStyle(DS.ink2)
                    }
                }
            }
        }
        .chartYAxis {
            AxisMarks(position: .trailing, values: .automatic(desiredCount: 3)) { value in
                AxisGridLine(stroke: StrokeStyle(lineWidth: 0.5)).foregroundStyle(DS.sep)
                AxisValueLabel {
                    if let y = value.as(Double.self) {
                        Text(formatYAxisTick(y))
                            .font(.caption2)
                            .foregroundStyle(DS.ink2)
                            .monospacedDigit()
                    }
                }
            }
        }
        .chartOverlay { proxy in
            GeometryReader { geo in
                Rectangle()
                    .fill(Color.clear)
                    .contentShape(Rectangle())
                    .gesture(
                        DragGesture(minimumDistance: 0)
                            .onChanged { value in
                                guard let plotFrame = proxy.plotFrame else { return }
                                let origin = geo[plotFrame].origin
                                let relX = value.location.x - origin.x
                                if let raw: Double = proxy.value(atX: relX) {
                                    let clamped = max(0, min(bucketCount - 1, Int(raw.rounded())))
                                    if scrubIdx != clamped {
                                        scrubIdx = clamped
                                        Haptics.selection()
                                    }
                                }
                            }
                    )
            }
        }
        .frame(height: 164)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(a11yLabel)
    }

    @ViewBuilder private func scrubReadoutView(bi: Int, selectedSeries: [PBCoreUsageSeriesView]) -> some View {
        let timeLabel = selectedSeries.first.flatMap { bi < $0.buckets.count ? formatDateLabel($0.buckets[bi].startMs, granularity: vm.granularity) : nil } ?? ""
        let totalTok = metric == .tokens
            ? selectedSeries.reduce(0.0) { acc, s in acc + (bi < s.buckets.count ? bucketTokens(s.buckets[bi]) : 0) }
            : 0

        let closeSymbol = "xmark.circle.fill"
        HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 8) {
                    Text(timeLabel).font(.caption.weight(.semibold)).foregroundStyle(DS.ink)
                    if metric == .tokens {
                        Text(L10n.usageScrubTotal(total: UsageFormat.formatTokens(totalTok)))
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(DS.ink)
                    }
                }
                FlowWords(items: selectedSeries.map { s in
                    let name = s.model.isEmpty ? s.agentName : s.model
                    let valStr: String = {
                        guard bi < s.buckets.count else { return "—" }
                        let b = s.buckets[bi]
                        switch metric {
                        case .tokens:
                            let tok = bucketTokens(b)
                            return tok > 0 ? UsageFormat.formatTokens(tok) : "0"
                        case .cacheHit:
                            return bucketCacheHit(b).map(UsageFormat.formatCacheHit) ?? "—"
                        case .outputSpeed:
                            return bucketHasSpeed(b) ? "\(UsageFormat.formatSpeed(b.speedP50)) \(L10n.usageKpiUnitToks())" : "—"
                        default:
                            return bucketHasTtft(b) ? UsageFormat.formatTtftSummary(b.ttftP50Ms) : "—"
                        }
                    }()
                    return "\(name): \(valStr)"
                })
            }
            Spacer(minLength: 0)
            Button { scrubIdx = nil } label: {
                SymbolImage(closeSymbol, style: .caption, color: DS.ink3)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(L10n.commonClose())
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .background(DS.fill.opacity(0.6), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
    }

    // MARK: Token Composition Bar

    private enum CompBucket: Hashable { case cacheRead, uncached, cacheWrite, output }

    @ViewBuilder private func tokenCompositionView(summary: ActiveSummary) -> some View {
        let compTotal = max(1.0, summary.total)
        let writeUnreported = !selectedSeries.contains(where: \.cacheWriteSupported) && summary.write == 0
        let items: [(key: CompBucket, color: Color, label: String, val: Double, unreported: Bool)] = [
            (.cacheRead, DS.ink, L10n.usageCompCacheRead(), summary.read, false),
            (.uncached, DS.ink2, L10n.usageCompUncached(), summary.uncached, false),
            (.cacheWrite, DS.ink3.opacity(0.55), L10n.usageCompCacheWrite(), summary.write, writeUnreported),
            (.output, DS.ink.opacity(0.32), L10n.usageCompOutput(), summary.out, false),
        ]

        VStack(alignment: .leading, spacing: 8) {
            GeometryReader { geo in
                HStack(spacing: 1) {
                    ForEach(items, id: \.key) { item in
                        if item.val > 0 {
                            Rectangle()
                                .fill(item.color)
                                .frame(width: max(3, geo.size.width * CGFloat(item.val / compTotal)))
                        }
                    }
                    Spacer(minLength: 0)
                }
                .background(DS.fill)
                .clipShape(Capsule())
            }
            .frame(height: 8)
            .accessibilityHidden(true)

            LazyVGrid(
                columns: [GridItem(.adaptive(minimum: 115), spacing: 8, alignment: .leading)],
                alignment: .leading,
                spacing: 4
            ) {
                ForEach(items, id: \.key) { item in
                    HStack(spacing: 5) {
                        RoundedRectangle(cornerRadius: 2).fill(item.color).frame(width: 8, height: 8)
                        Text(item.label).font(.caption).foregroundStyle(DS.ink2)
                        Text(item.unreported ? "—" : UsageFormat.formatTokens(item.val))
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(DS.ink)
                            .accessibilityLabel(item.unreported ? L10n.usageUnreportedAria() : "")
                    }
                }
            }
            if summary.reas > 0 {
                Text(L10n.usageCompReasoning(value: UsageFormat.formatTokens(summary.reas)))
                    .font(.caption)
                    .foregroundStyle(DS.ink2)
            }
        }
    }

    // MARK: Series Breakdown List

    @ViewBuilder private func seriesRow(_ s: PBCoreUsageSeriesView, slot: Int?) -> some View {
        let isSelected = slot != nil
        let c = s.hasCurrent ? s.current : nil
        let tot = c.map { Double($0.uncachedInputTokens + $0.cacheReadTokens + $0.cacheWriteTokens + $0.outputTokens) } ?? 0
        let writeText = s.cacheWriteSupported ? UsageFormat.formatTokens(Double(c?.cacheWriteTokens ?? 0)) : "—"

        let valText: String = {
            switch metric {
            case .tokens:
                return UsageFormat.formatTokens(tot)
            case .cacheHit:
                return (c?.hasCacheHitRate_p == true) ? UsageFormat.formatCacheHit(c!.cacheHitRate) : "—"
            case .outputSpeed:
                return (c?.hasSpeed_p == true) ? "\(UsageFormat.formatSpeed(c!.speedP50)) \(L10n.usageKpiUnitToks())" : "—"
            default:
                return (c?.hasTtft_p == true) ? UsageFormat.formatTtftSummary(c!.ttftP50Ms) : "—"
            }
        }()

        let reqs = metric == .tokens || metric == .cacheHit
            ? (c?.usageRequestCount ?? 0)
            : (c?.speedRequestCount ?? 0)
        let deltaText = formatSeriesDeltaCell(s, metric: metric)
        let subText: String = {
            guard isSelected else { return L10n.usageRowSubTapOverlay() }
            guard let c else { return "" }
            switch metric {
            case .tokens:
                let inp = UsageFormat.formatTokens(Double(c.uncachedInputTokens))
                let read = UsageFormat.formatTokens(Double(c.cacheReadTokens))
                let out = UsageFormat.formatTokens(Double(c.outputTokens))
                if c.reasoningOutputTokens > 0 {
                    return L10n.usageRowSubTokensReasoning(
                        inp: inp, out: out, read: read,
                        reas: UsageFormat.formatTokens(Double(c.reasoningOutputTokens)),
                        write: writeText
                    )
                }
                return L10n.usageRowSubTokens(inp: inp, out: out, read: read, write: writeText)
            case .cacheHit:
                return L10n.usageRowSubCache(
                    inp: UsageFormat.formatTokens(Double(c.uncachedInputTokens)),
                    read: UsageFormat.formatTokens(Double(c.cacheReadTokens)),
                    write: writeText
                )
            default:
                let low = c.hasSpeed_p ? UsageFormat.formatSpeed(c.speedP10) : "—"
                let high = c.hasSpeed_p ? UsageFormat.formatSpeed(c.speedP90) : "—"
                let ttft = c.hasTtft_p ? UsageFormat.formatTtftSummary(c.ttftP50Ms) : "—"
                return L10n.usageRowSubSpeed(high: high, low: low, ttft: ttft)
            }
        }()

        let reqCountText = L10n.usageEntryReqs(count: Int(reqs))
        let metaLine = reqCountText + " · " + deltaText
        let pairTitle = s.agentName + " · " + s.model

        Button {
            Haptics.selection()
            toggleSeriesOverlay(s.seriesKey)
        } label: {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 8) {
                    if let slot {
                        seriesSwatch(slot: slot, isBar: metric == .tokens)
                    }
                    HStack(alignment: .firstTextBaseline, spacing: 0) {
                        AgentMark(s.agent, style: .body, named: true)
                        Text(pairTitle)
                            .font(.body)
                            .foregroundStyle(isSelected ? DS.ink : DS.ink2)
                            .lineLimit(typeSize.isAccessibilitySize ? 2 : 1)
                    }
                    Spacer(minLength: 6)
                    if !typeSize.isAccessibilitySize {
                        VStack(alignment: .trailing, spacing: 1) {
                            Text(valText)
                                .font(.subheadline.weight(.semibold))
                                .foregroundStyle(isSelected ? DS.ink : DS.ink2)
                                .monospacedDigit()
                            Text(metaLine)
                                .font(.caption)
                                .foregroundStyle(DS.ink2)
                                .monospacedDigit()
                        }
                    }
                }
                if typeSize.isAccessibilitySize {
                    HStack(spacing: 8) {
                        Text(valText).font(.subheadline.weight(.semibold)).foregroundStyle(isSelected ? DS.ink : DS.ink2)
                        Spacer(minLength: 0)
                        Text(metaLine).font(.caption).foregroundStyle(DS.ink2)
                    }
                }
                if !subText.isEmpty {
                    Text(subText)
                        .font(.caption)
                        .foregroundStyle(DS.ink2)
                        .lineLimit(typeSize.isAccessibilitySize ? nil : 2)
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 11)
            .frame(maxWidth: .infinity, minHeight: DS.minHit, alignment: .leading)
            .opacity(isSelected ? 1 : 0.72)
            .contentShape(Rectangle())
        }
        .buttonStyle(RowPressStyle())
        .accessibilityAddTraits(isSelected ? .isSelected : [])
    }

    @ViewBuilder private func footnotesView(hasOutlier: Bool) -> some View {
        let unsupported = vm.unsupportedFootnote.rendered()
        VStack(alignment: .leading, spacing: 4) {
            if vm.showCacheWriteFootnote {
                Text(L10n.usageCacheWriteFootnote())
            }
            Text(L10n.usageCacheFormulaFootnote())
            Text(L10n.usageSpeedThresholdFootnote())
            if hasOutlier {
                Text(L10n.usageOutlierNote())
            }
            if !unsupported.isEmpty {
                Text(unsupported)
            }
            Text(L10n.usagePassiveFootnote())
        }
        .font(.footnote)
        .foregroundStyle(DS.ink2)
        .padding(.horizontal, 30)
        .padding(.bottom, 12)
    }

    // MARK: Series Selection & Chart Data Helpers

    private func toggleSeriesOverlay(_ key: String) {
        if let idx = selectedKeys.firstIndex(of: key) {
            guard selectedKeys.count > 1 else { return }
            selectedKeys.remove(at: idx)
        } else if selectedKeys.count < Self.maxOverlaySeries {
            selectedKeys.append(key)
        } else {
            selectedKeys[Self.maxOverlaySeries - 1] = key
        }
    }

    private func isFewSamples(_ s: PBCoreUsageSeriesView, metric: PBCoreUsageMetricTab) -> Bool {
        if metric == .tokens { return false }
        if metric == .cacheHit {
            let n = s.hasCurrent ? s.current.usageRequestCount : 0
            return n > 0 && n < Self.fewSamplesMin
        }
        let n = s.hasCurrent ? s.current.speedRequestCount : 0
        return s.fewSpeedSamples || (n > 0 && n < Self.fewSamplesMin)
    }

    private func bucketTokens(_ b: PBUsageBucketPoint) -> Double {
        Double(b.uncachedInputTokens + b.cacheReadTokens + b.cacheWriteTokens + b.outputTokens)
    }

    private func bucketCacheHit(_ b: PBUsageBucketPoint) -> Double? {
        let denom = Double(b.uncachedInputTokens + b.cacheReadTokens + b.cacheWriteTokens)
        guard denom > 0 else { return nil }
        return Double(b.cacheReadTokens) / denom
    }

    private func bucketHasSpeed(_ b: PBUsageBucketPoint) -> Bool {
        b.speedRequestCount > 0 && b.speedP50 > 0
    }

    private func bucketHasTtft(_ b: PBUsageBucketPoint) -> Bool {
        b.speedRequestCount > 0 && b.ttftP50Ms > 0
    }

    private struct BarSeg: Identifiable {
        let slot: Int
        let y0: Double
        let y1: Double
        var id: Int { slot }
    }

    private func stackedBarSegments(selectedSeries: [PBCoreUsageSeriesView], bucketIndex bi: Int) -> [BarSeg] {
        var acc = 0.0
        var out: [BarSeg] = []
        for (slot, s) in selectedSeries.enumerated() where bi < s.buckets.count {
            let val = bucketTokens(s.buckets[bi])
            guard val > 0 else { continue }
            out.append(BarSeg(slot: slot, y0: acc, y1: acc + val))
            acc += val
        }
        return out
    }

    private struct LinePt: Identifiable {
        let bi: Int
        let val: Double
        let low: Double?
        let high: Double?
        var id: Int { bi }
    }

    private struct LineRun: Identifiable {
        let id: Int
        let points: [LinePt]
    }

    private func lineRuns(series s: PBCoreUsageSeriesView, metric: PBCoreUsageMetricTab, few: Bool) -> [LineRun] {
        var runs: [LineRun] = []
        var cur: [LinePt] = []
        var nextID = 0
        for (bi, b) in s.buckets.enumerated() {
            var val: Double?
            var low: Double?
            var high: Double?
            switch metric {
            case .cacheHit:
                if let r = bucketCacheHit(b) { val = r * 100 }
            case .outputSpeed:
                if bucketHasSpeed(b) {
                    val = b.speedP50
                    if !few && b.speedP10 > 0 && b.speedP90 >= b.speedP10 {
                        low = b.speedP10
                        high = b.speedP90
                    }
                }
            case .ttft:
                if bucketHasTtft(b) {
                    val = b.ttftP50Ms
                    if !few && b.ttftP10Ms > 0 && b.ttftP90Ms >= b.ttftP10Ms {
                        low = b.ttftP10Ms
                        high = b.ttftP90Ms
                    }
                }
            default:
                break
            }
            if let val {
                cur.append(LinePt(bi: bi, val: val, low: low, high: high))
            } else if !cur.isEmpty {
                runs.append(LineRun(id: nextID, points: cur))
                nextID += 1
                cur = []
            }
        }
        if !cur.isEmpty {
            runs.append(LineRun(id: nextID, points: cur))
        }
        return runs
    }

    private func chartUpperBound(selectedSeries: [PBCoreUsageSeriesView]) -> Double {
        switch metric {
        case .tokens:
            let bucketCount = selectedSeries.map(\.buckets.count).max() ?? 0
            let peak = (0..<bucketCount).map { bi in
                selectedSeries.reduce(0.0) { acc, s in acc + (bi < s.buckets.count ? bucketTokens(s.buckets[bi]) : 0) }
            }.max() ?? 0
            return max(10, (peak * 1.12).rounded(.up))
        case .cacheHit:
            return 100
        case .outputSpeed, .ttft, .unspecified, .UNRECOGNIZED:
            var peak = 0.0
            for s in selectedSeries {
                let few = isFewSamples(s, metric: metric)
                for b in s.buckets {
                    if metric == .outputSpeed && bucketHasSpeed(b) {
                        peak = max(peak, !few && b.speedP90 > 0 ? b.speedP90 : b.speedP50)
                    } else if metric == .ttft && bucketHasTtft(b) {
                        peak = max(peak, !few && b.ttftP90Ms > 0 ? b.ttftP90Ms : b.ttftP50Ms)
                    }
                }
            }
            let minBound: Double = metric == .ttft ? 200 : 10
            return max(minBound, (peak * 1.15).rounded(.up))
        }
    }

    private func formatYAxisTick(_ v: Double) -> String {
        if v <= 0 { return "0" }
        switch metric {
        case .tokens: return UsageFormat.formatTokens(v)
        case .cacheHit: return "\(Int(v.rounded()))%"
        case .ttft: return UsageFormat.formatTtft(v)
        default: return UsageFormat.formatSpeed(v)
        }
    }

    private func axisTickIndices(count: Int) -> [Int] {
        guard count > 0 else { return [] }
        if count == 1 { return [0] }
        if count == 2 { return [0, 1] }
        return [0, (count - 1) / 2, count - 1]
    }

    private func formatDateLabel(_ ms: Int64, granularity: PBUsageBucketGranularity) -> String {
        let d = Date(timeIntervalSince1970: Double(ms) / 1000.0)
        let cal = Calendar.current
        if granularity == .hour {
            return String(format: "%02d:00", cal.component(.hour, from: d))
        }
        return "\(cal.component(.month, from: d))/\(cal.component(.day, from: d))"
    }

    private func formatFullDay(_ ms: Int64) -> String {
        let d = Date(timeIntervalSince1970: Double(ms) / 1000.0)
        let cal = Calendar.current
        return "\(cal.component(.month, from: d))/\(cal.component(.day, from: d))"
    }

    private func formatOfflineTime(_ ms: Int64) -> String {
        let d = Date(timeIntervalSince1970: Double(ms) / 1000.0)
        let cal = Calendar.current
        return String(format: "%02d:%02d", cal.component(.hour, from: d), cal.component(.minute, from: d))
    }

    private func formatSeriesDeltaCell(_ s: PBCoreUsageSeriesView, metric: PBCoreUsageMetricTab) -> String {
        if s.previousBackfilling { return L10n.usageBackfillingCell() }
        switch metric {
        case .tokens:
            return s.hasTokensDelta_p ? UsageFormat.formatSignedPercent(s.tokensDeltaPercent) : "—"
        case .cacheHit:
            return s.hasCacheHitDelta_p ? UsageFormat.formatSignedPoints(s.cacheHitDeltaPoints) : "—"
        case .outputSpeed:
            return s.hasSpeedDelta_p ? UsageFormat.formatSignedPercent(s.speedDeltaPercent) : "—"
        default:
            return s.hasTtftDelta_p ? UsageFormat.formatTtftDelta(
                curMs: s.hasCurrent ? s.current.ttftP50Ms : 0,
                prevMs: s.hasPrevious ? s.previous.ttftP50Ms : 0
            ) : "—"
        }
    }

    private func customPickerDateBounds() -> (min: Date, max: Date) {
        let cal = Calendar.current
        let today = cal.startOfDay(for: Date())
        let minDay = cal.date(byAdding: .day, value: -(CoreHost.usageRetentionDays - 1), to: today) ?? today
        return (minDay, today)
    }

    private func syncDraftCustomDates() {
        let clamped = CoreHost.clampUsageCustomRange(
            startMs: vm.startMs,
            endMs: vm.endMs > 0 ? vm.endMs - 86_400_000 : 0
        )
        draftStartDate = Date(timeIntervalSince1970: Double(clamped.startMs) / 1000.0)
        draftEndDate = Date(timeIntervalSince1970: Double(clamped.endMs) / 1000.0)
    }

    // MARK: Monochrome Bar & Line Visual Differentiation

    private func slotBarStyle(slot: Int) -> AnyShapeStyle {
        switch slot {
        case 0: return AnyShapeStyle(DS.ink)
        case 1: return AnyShapeStyle(DS.ink2)
        case 2: return AnyShapeStyle(hatchPaint)
        default: return AnyShapeStyle(outlinePatternPaint)
        }
    }

    private var hatchPaint: ImagePaint {
        let size = CGSize(width: 6, height: 6)
        let img = UIGraphicsImageRenderer(size: size).image { ctx in
            let bg = colorScheme == .dark ? UIColor(white: 0.16, alpha: 1) : UIColor(white: 0.96, alpha: 1)
            let fg = colorScheme == .dark ? UIColor.white : UIColor.black
            bg.setFill()
            ctx.fill(CGRect(origin: .zero, size: size))
            ctx.cgContext.setStrokeColor(fg.cgColor)
            ctx.cgContext.setLineWidth(1.5)
            ctx.cgContext.move(to: CGPoint(x: -1, y: 7))
            ctx.cgContext.addLine(to: CGPoint(x: 7, y: -1))
            ctx.cgContext.move(to: CGPoint(x: -1, y: 1))
            ctx.cgContext.addLine(to: CGPoint(x: 1, y: -1))
            ctx.cgContext.move(to: CGPoint(x: 5, y: 7))
            ctx.cgContext.addLine(to: CGPoint(x: 7, y: 5))
            ctx.cgContext.strokePath()
        }
        return ImagePaint(image: Image(uiImage: img))
    }

    private var outlinePatternPaint: ImagePaint {
        let size = CGSize(width: 6, height: 6)
        let img = UIGraphicsImageRenderer(size: size).image { ctx in
            let bg = colorScheme == .dark ? UIColor(white: 0.12, alpha: 1) : UIColor(white: 0.99, alpha: 1)
            let stroke = UIColor.secondaryLabel.withAlphaComponent(0.6)
            bg.setFill()
            ctx.fill(CGRect(origin: .zero, size: size))
            stroke.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 6, height: 1))
            ctx.fill(CGRect(x: 0, y: 0, width: 1, height: 6))
        }
        return ImagePaint(image: Image(uiImage: img))
    }

    private func lineColor(slot: Int) -> Color {
        switch slot {
        case 0, 2: return DS.ink
        default: return DS.ink2
        }
    }

    private func lineStrokeStyle(slot: Int) -> StrokeStyle {
        switch slot {
        case 0: return StrokeStyle(lineWidth: 2, lineCap: .round, lineJoin: .round)
        case 1: return StrokeStyle(lineWidth: 1.8, lineCap: .round, dash: [5, 3])
        case 2: return StrokeStyle(lineWidth: 1.8, lineCap: .round, dash: [2, 3])
        default: return StrokeStyle(lineWidth: 1.8, lineCap: .round, dash: [7, 3, 2, 3])
        }
    }

    @ViewBuilder private func seriesMarkerSymbol(slot: Int, highlighted: Bool) -> some View {
        let d: CGFloat = highlighted ? 7 : 5
        switch slot {
        case 0:
            Circle().fill(DS.ink).frame(width: d, height: d)
        case 1:
            RoundedRectangle(cornerRadius: 1).fill(DS.ink2).frame(width: d, height: d)
        case 2:
            Circle()
                .fill(DS.gbg)
                .overlay(Circle().strokeBorder(DS.ink, lineWidth: 1.5))
                .frame(width: d + 1, height: d + 1)
        default:
            Rectangle()
                .fill(DS.ink2)
                .rotationEffect(.degrees(45))
                .frame(width: d, height: d)
        }
    }

    @ViewBuilder private func seriesSwatch(slot: Int, isBar: Bool) -> some View {
        if isBar {
            RoundedRectangle(cornerRadius: 2.5, style: .continuous)
                .fill(slotBarStyle(slot: slot))
                .overlay {
                    if slot >= 2 {
                        RoundedRectangle(cornerRadius: 2.5, style: .continuous)
                            .strokeBorder(DS.ink3, lineWidth: 0.6)
                    }
                }
                .frame(width: 11, height: 11)
        } else {
            ZStack {
                Rectangle()
                    .fill(lineColor(slot: slot))
                    .frame(width: 18, height: 1.6)
                seriesMarkerSymbol(slot: slot, highlighted: false)
            }
            .frame(width: 20, height: 12)
        }
    }
}

private struct FlowWords: View {
    let items: [String]

    var body: some View {
        Text(items.joined(separator: "  ·  "))
            .font(.caption)
            .foregroundStyle(DS.ink2)
            .monospacedDigit()
    }
}
