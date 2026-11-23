import RACore
import RAProto
import SwiftUI
import UIKit

enum UsageTickAlign {
    case start, center, end
}

struct UsageAxisTick: Identifiable {
    let id: Int
    let pct: CGFloat
    let label: String
    let align: UsageTickAlign
}

enum UsageAxisHelper {
    private static func effectiveGranularity(
        _ granularity: PBUsageBucketGranularity,
        preset: PBUsageRangePreset
    ) -> PBUsageBucketGranularity {
        if granularity != .unspecified { return granularity }
        switch preset {
        case .today: return .hour
        case .sevenDays: return .sixHours
        default: return .day
        }
    }

    static func buildTicks(
        granularity: PBUsageBucketGranularity,
        buckets: [PBCoreUsageBarBucketView],
        startMs: Int64,
        endMs: Int64,
        preset: PBUsageRangePreset
    ) -> [UsageAxisTick] {
        let gran = effectiveGranularity(granularity, preset: preset)
        if gran == .hour {
            return [
                UsageAxisTick(id: 0, pct: 0, label: "0", align: .start),
                UsageAxisTick(id: 1, pct: 25, label: "6", align: .center),
                UsageAxisTick(id: 2, pct: 50, label: "12", align: .center),
                UsageAxisTick(id: 3, pct: 75, label: "18", align: .center),
                UsageAxisTick(id: 4, pct: 100, label: L10n.usageAxis24H(), align: .end),
            ]
        }

        let cal = Calendar.current
        let todayStart = cal.startOfDay(for: Date())
        let fallbackDays = preset == .thirtyDays ? 30 : 7
        let effStartMs: Int64 = {
            if startMs > 0 { return startMs }
            if let first = buckets.first, first.startMs > 0 { return first.startMs }
            let d = cal.date(byAdding: .day, value: -(fallbackDays - 1), to: todayStart) ?? todayStart
            return Int64(d.timeIntervalSince1970 * 1000)
        }()
        let startDate = Date(timeIntervalSince1970: Double(effStartMs) / 1000.0)

        if gran == .sixHours {
            let n = buckets.isEmpty ? 28 : buckets.count
            let days = max(1, Int((Double(n) / 4.0).rounded()))
            var out: [UsageAxisTick] = []
            var prevMonth = -1
            for d in 0..<days {
                let idx = d * 4
                let dt: Date = {
                    if idx < buckets.count, buckets[idx].startMs > 0 {
                        return Date(timeIntervalSince1970: Double(buckets[idx].startMs) / 1000.0)
                    }
                    return cal.date(byAdding: .day, value: d, to: startDate) ?? startDate
                }()
                let m = cal.component(.month, from: dt)
                let day = cal.component(.day, from: dt)
                let tickStr = (d == 0 || m != prevMonth) ? mdLabel(m, day) : String(day)
                prevMonth = m
                out.append(UsageAxisTick(
                    id: d,
                    pct: CGFloat(Double(d * 4 + 2) / Double(n)) * 100,
                    label: tickStr,
                    align: .center
                ))
            }
            return out
        }

        let derivedDays: Int = {
            if endMs > startMs, startMs > 0 {
                return max(1, Int((Double(endMs - startMs) / 86_400_000.0).rounded()))
            }
            return fallbackDays
        }()
        let n = buckets.isEmpty ? derivedDays : buckets.count
        if n <= 1 {
            let m = cal.component(.month, from: startDate)
            let day = cal.component(.day, from: startDate)
            return [UsageAxisTick(id: 0, pct: 50, label: mdLabel(m, day), align: .center)]
        }

        let lastDate: Date = {
            if let last = buckets.last, last.startMs > 0 {
                return Date(timeIntervalSince1970: Double(last.startMs) / 1000.0)
            }
            if endMs > 0 {
                return Date(timeIntervalSince1970: Double(endMs - 1) / 1000.0)
            }
            return cal.date(byAdding: .day, value: n - 1, to: startDate) ?? startDate
        }()
        let tickCount = n <= 4 ? n : (n <= 18 ? 4 : 5)
        var out: [UsageAxisTick] = []
        for k in 0..<tickCount {
            let isLast = k == tickCount - 1
            let posIdx = isLast ? n : Int((Double(k * n) / Double(tickCount - 1)).rounded())
            let dt: Date = {
                if isLast { return lastDate }
                if posIdx < buckets.count, buckets[posIdx].startMs > 0 {
                    return Date(timeIntervalSince1970: Double(buckets[posIdx].startMs) / 1000.0)
                }
                return cal.date(byAdding: .day, value: posIdx, to: startDate) ?? startDate
            }()
            let align: UsageTickAlign = k == 0 ? .start : (isLast ? .end : .center)
            let m = cal.component(.month, from: dt)
            let day = cal.component(.day, from: dt)
            out.append(UsageAxisTick(
                id: k,
                pct: CGFloat(Double(posIdx) / Double(n)) * 100,
                label: mdLabel(m, day),
                align: align
            ))
        }
        return out
    }

    private static func mdLabel(_ m: Int, _ d: Int) -> String {
        [String(m), String(d)].joined(separator: "/")
    }

    static func formatBucketLabel(startMs: Int64, endMs: Int64, granularity: PBUsageBucketGranularity) -> String {
        let cal = Calendar.current
        let s = Date(timeIntervalSince1970: Double(startMs) / 1000.0)
        let effEndMs = endMs > startMs ? endMs : startMs + 3_600_000
        let e = Date(timeIntervalSince1970: Double(effEndMs) / 1000.0)
        let startH = cal.component(.hour, from: s)
        var endH = cal.component(.hour, from: e)
        if endH <= startH && endMs > startMs { endH = 24 }
        switch granularity {
        case .hour:
            return L10n.usageScrubHourRange(end: endH, start: startH)
        case .sixHours:
            return L10n.usageScrubDayHourRange(
                day: cal.component(.day, from: s),
                end: endH,
                month: cal.component(.month, from: s),
                start: startH
            )
        default:
            return L10n.usageScrubDay(
                day: cal.component(.day, from: s),
                month: cal.component(.month, from: s)
            )
        }
    }

    static func formatCustomRange(startMs: Int64, inclusiveEndMs: Int64) -> String {
        let cal = Calendar.current
        let s = Date(timeIntervalSince1970: Double(startMs) / 1000.0)
        let e = Date(timeIntervalSince1970: Double(inclusiveEndMs) / 1000.0)
        let sy = cal.component(.year, from: s)
        let sm = cal.component(.month, from: s)
        let sd = cal.component(.day, from: s)
        let ey = cal.component(.year, from: e)
        let em = cal.component(.month, from: e)
        let ed = cal.component(.day, from: e)
        if sy == ey && sm == em && sd == ed {
            return L10n.usageCustomRangeSingleDay(day: sd, month: sm, year: sy)
        }
        if sy == ey {
            return L10n.usageCustomRangeSameYear(endDay: ed, endMonth: em, startDay: sd, startMonth: sm, year: sy)
        }
        return L10n.usageCustomRangeCrossYear(endDay: ed, endMonth: em, endYear: ey, startDay: sd, startMonth: sm, startYear: sy)
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
            onSelectRange: { preset in
                Task { await context.coreHost.usageSelectRange(preset) }
            },
            onSelectCustomDates: { startMs, endMs in
                Task { await context.coreHost.usageSelectCustomDates(startMs: startMs, endMs: endMs) }
            },
            onSelectSort: { sortBy in
                Task { await context.coreHost.usageSelectSort(machineID: machineID, sortBy: sortBy) }
            },
            onSelectSeries: { key, toggle in
                Task { await context.coreHost.usageSelectSeries(machineID: machineID, seriesKey: key, toggle: toggle) }
            },
            onScrub: { idx, clear in
                Task { await context.coreHost.usageScrub(machineID: machineID, bucketIndex: idx, clear: clear) }
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
            $0.emptyKind = .loading
        }
    }
}

struct UsageScreenContent: View {
    let vm: PBCoreUsageViewModel
    var onRefresh: (@MainActor @Sendable () async -> Void)? = nil
    var onSelectRange: (PBUsageRangePreset) -> Void = { _ in }
    var onSelectCustomDates: (Int64, Int64) -> Void = { _, _ in }
    var onSelectSort: (PBCoreUsageSortColumn) -> Void = { _ in }
    var onSelectSeries: (String, Bool) -> Void = { _, _ in }
    var onScrub: (Int32, Bool) -> Void = { _, _ in }
    var onRetry: () -> Void = {}

    @State private var showSkeleton = false
    @State private var customSheetOpen = false
    @State private var draftStartDate = Date()
    @State private var draftEndDate = Date()
    @Namespace private var rangeNamespace

    private var preset: PBUsageRangePreset {
        vm.preset == .unspecified ? .sevenDays : vm.preset
    }

    private var machineName: String {
        vm.machineName.or(vm.machineID)
    }

    private var clampedRange: (startMs: Int64, endMs: Int64) {
        let rawEnd = (vm.endMs > vm.startMs && vm.endMs > 0) ? vm.endMs - 1 : vm.endMs
        return CoreHost.clampUsageCustomRange(startMs: vm.startMs, endMs: rawEnd)
    }

    var body: some View {
        let subtitle: String? = {
            guard vm.found, vm.hasSubtitle else { return nil }
            let text = vm.subtitle.rendered()
            return text.isEmpty ? nil : text
        }()

        Page(gray: false) {
            GeometryReader { geo in
                let isWide = geo.size.width >= 900
                Group {
                    if isWide && vm.found && vm.supported && vm.hasAnyUsage_p {
                        wideContent
                    } else {
                        narrowContent(isWide: isWide)
                    }
                }
                .toolbar {
                    if isWide {
                        ToolbarItem(placement: .topBarTrailing) {
                            HStack(spacing: 14) {
                                if preset == .custom {
                                    customRangeTrigger
                                }
                                rangeSegmented(height: 30)
                                    .frame(width: 290)
                            }
                        }
                    }
                }
            }
        }
        .navBar(L10n.usageTitle(), subtitle: subtitle)
        .task(id: vm.loading) {
            if !vm.loading || vm.hasAnyUsage_p {
                showSkeleton = false
                return
            }
            try? await Task.sleep(for: .milliseconds(200))
            if !Task.isCancelled && vm.loading && !vm.hasAnyUsage_p {
                showSkeleton = true
            }
        }
        .kitSheet(isPresented: $customSheetOpen) {
            customRangeSheet
        }
    }

    // MARK: Narrow / Single-Column Layout

    @ViewBuilder private func narrowContent(isWide: Bool) -> some View {
        ScrollViewReader { proxy in
            let scroll = ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    Color.clear.frame(height: 0).id("usage-top")
                    ConnectionBar()

                    VStack(alignment: .leading, spacing: 0) {
                        if !isWide {
                            rangeSegmented(height: 32)
                                .padding(.horizontal, 16)
                                .padding(.top, 10)
                            if preset == .custom {
                                customRangeTrigger
                                    .frame(maxWidth: .infinity)
                                    .padding(.top, 8)
                            }
                        }

                        bannersSection
                            .padding(.horizontal, 16)

                        stateOrContent(
                            isWide: false,
                            onRowTap: { row in
                                let selecting = !row.selected
                                onSelectSeries(row.seriesKey, true)
                                if selecting {
                                    withAnimation(.easeOut(duration: 0.28)) {
                                        proxy.scrollTo("usage-top", anchor: .top)
                                    }
                                }
                            }
                        )
                    }
                }
                .frame(maxWidth: 560, alignment: .leading)
                .frame(maxWidth: .infinity, alignment: .center)
                .padding(.bottom, 24)
            }
            if let onRefresh {
                scroll.refreshable { await onRefresh() }
            } else {
                scroll
            }
        }
    }

    // MARK: Wide Two-Column Layout (≥900pt)

    @ViewBuilder private var wideContent: some View {
        VStack(alignment: .leading, spacing: 0) {
            ConnectionBar()
            bannersSection
                .padding(.horizontal, 28)

            HStack(alignment: .top, spacing: 48) {
                ScrollView {
                    VStack(alignment: .leading, spacing: 16) {
                        UsageHeroBlock(
                            vm: vm,
                            isWide: true,
                            onClearSeries: { onSelectSeries("", false) }
                        )
                        UsageChartsStack(
                            vm: vm,
                            isWide: true,
                            onScrub: onScrub
                        )
                    }
                    .padding(.bottom, 22)
                }
                .frame(maxWidth: 600)
                .layoutPriority(1)

                ScrollView {
                    UsageModelTable(
                        vm: vm,
                        isWide: true,
                        onSelectSort: onSelectSort,
                        onSelectRow: { row in
                            onSelectSeries(row.seriesKey, true)
                        }
                    )
                    .padding(.top, 20)
                    .padding(.bottom, 22)
                }
                .frame(minWidth: 260, maxWidth: .infinity)
            }
            .padding(.horizontal, 28)
            .padding(.top, 6)
            .frame(maxWidth: 1120, alignment: .leading)
            .frame(maxWidth: .infinity, alignment: .center)
        }
    }

    // MARK: State Machine

    @ViewBuilder private func stateOrContent(
        isWide: Bool,
        onRowTap: @escaping (PBCoreUsageModelRowView) -> Void
    ) -> some View {
        switch vm.emptyKind {
        case .loading:
            if showSkeleton { skeletonCard.padding(.horizontal, 16) }
        case .notFound:
            PageNote(vm.hasEmptyBody ? vm.emptyBody.rendered() : L10n.settingsIosThisComputerIsNotInTheCurrentTrustedDirectory())
                .padding(.horizontal, 16)
        case .unsupported, .offlineEmpty, .loadError, .allSourcesUnavailable:
            edgeCard(
                title: vm.emptyTitle.rendered(),
                body: vm.emptyBody.rendered(),
                actionTitle: vm.emptyCanRetry ? L10n.commonRetry() : nil,
                action: vm.emptyCanRetry ? onRetry : nil
            )
            .padding(.horizontal, 16)
        case .noRecords:
            UsageEmptyRecordsBlock(vm: vm, preset: preset, isWide: isWide)
                .padding(.horizontal, 16)
        case .none, .unspecified, .UNRECOGNIZED:
            if vm.hasAnyUsage_p {
                UsageHeroBlock(
                    vm: vm,
                    isWide: isWide,
                    onClearSeries: { onSelectSeries("", false) }
                )
                .padding(.horizontal, 16)

                UsageChartsStack(
                    vm: vm,
                    isWide: isWide,
                    onScrub: onScrub
                )
                .padding(.top, 16)
                .padding(.horizontal, 16)

                UsageModelTable(
                    vm: vm,
                    isWide: isWide,
                    onSelectSort: onSelectSort,
                    onSelectRow: onRowTap
                )
                .padding(.top, 22)
            }
        }
    }

    // MARK: Controls & Banners

    private func rangeSegmented(height: CGFloat) -> some View {
        let ranges: [(PBUsageRangePreset, String)] = [
            (.today, L10n.usageRangeToday()),
            (.sevenDays, L10n.usageRange7D()),
            (.thirtyDays, L10n.usageRange30D()),
            (.custom, L10n.usageRangeCustom()),
        ]
        return HStack(spacing: 0) {
            ForEach(ranges, id: \.0.rawValue) { item in
                let active = preset == item.0
                Button {
                    if item.0 == .custom {
                        syncDraftCustomDates()
                        if preset != .custom {
                            onSelectRange(.custom)
                        }
                        customSheetOpen = true
                    } else {
                        onSelectRange(item.0)
                    }
                } label: {
                    Text(item.1)
                        .font(.system(size: 13, weight: active ? .semibold : .medium))
                        .foregroundStyle(active ? DS.ink : DS.ink2)
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .background {
                            if active {
                                RoundedRectangle(cornerRadius: 7, style: .continuous)
                                    .fill(DS.raised)
                                    .overlay(
                                        RoundedRectangle(cornerRadius: 7, style: .continuous)
                                            .strokeBorder(DS.sep, lineWidth: 0.5)
                                    )
                                    .shadow(color: .black.opacity(0.14), radius: 1.5, x: 0, y: 1)
                                    .matchedGeometryEffect(id: "usage-seg", in: rangeNamespace)
                            }
                        }
                }
                .buttonStyle(.plain)
            }
        }
        .padding(2)
        .frame(height: height)
        .background(DS.fill, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
    }

    @ViewBuilder private var customRangeTrigger: some View {
        Button {
            syncDraftCustomDates()
            customSheetOpen = true
        } label: {
            HStack(spacing: 4) {
                Text(UsageAxisHelper.formatCustomRange(startMs: clampedRange.startMs, inclusiveEndMs: clampedRange.endMs))
                    .font(.system(size: 13, weight: .medium))
                    .monospacedDigit()
                Image(systemName: "chevron.down")
                    .font(.system(size: 11, weight: .semibold))
            }
            .foregroundStyle(DS.blue)
            .frame(height: 32)
            .padding(.horizontal, 8)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder private var bannersSection: some View {
        if !vm.banners.isEmpty {
            VStack(spacing: 8) {
                ForEach(Array(vm.banners.enumerated()), id: \.offset) { _, b in
                    let tone: BannerTone = b.isError ? .error : (b.kind == .offlineCached ? .warn : .plain)
                    GroupBanner(
                        b.text.rendered(),
                        tone: tone,
                        action: b.canRetry ? L10n.commonRetry() : nil,
                        onAction: b.canRetry ? onRetry : nil
                    )
                }
            }
            .padding(.top, 8)
        }
    }

    @ViewBuilder private var skeletonCard: some View {
        RoundedRectangle(cornerRadius: 18, style: .continuous)
            .fill(DS.gbg)
            .frame(height: 260)
            .padding(.top, 16)
            .accessibilityLabel(L10n.commonIosLoading())
    }

    @ViewBuilder private func edgeCard(
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
        .padding(.top, 14)
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

    private func customPickerDateBounds() -> (min: Date, max: Date) {
        let cal = Calendar.current
        let today = cal.startOfDay(for: Date())
        let minDay = cal.date(byAdding: .day, value: -(CoreHost.usageRetentionDays - 1), to: today) ?? today
        return (minDay, today)
    }

    private func syncDraftCustomDates() {
        let clamped = clampedRange
        draftStartDate = Date(timeIntervalSince1970: Double(clamped.startMs) / 1000.0)
        draftEndDate = Date(timeIntervalSince1970: Double(clamped.endMs) / 1000.0)
    }
}

// MARK: - Hero + 5-Row Composition

private struct UsageHeroBlock: View {
    let vm: PBCoreUsageViewModel
    let isWide: Bool
    let onClearSeries: () -> Void

    private var hero: PBCoreUsageHeroView {
        vm.hasHero ? vm.hero : PBCoreUsageHeroView()
    }

    private var subline: String {
        if hero.scrubbing || !hero.hasSublineText { return "" }
        return hero.sublineText.rendered()
    }

    var body: some View {
        let compRowH: CGFloat = isWide ? 18 : 17
        let minCompH = CGFloat(max(0, Int(hero.periodCompositionRows))) * compRowH + 5
        let topGap: CGFloat = isWide ? 14 : 18
        let compW: CGFloat = isWide ? 168 : 132
        let bigSize: CGFloat = isWide ? 52 : 44

        VStack(alignment: .leading, spacing: 2) {
            scopeLine
                .frame(minHeight: 18)
                .padding(.top, topGap)

            HStack(alignment: .top, spacing: 12) {
                VStack(alignment: .leading, spacing: 3) {
                    HStack(alignment: .firstTextBaseline, spacing: 5) {
                        Text(hero.hasTotalTokensText ? hero.totalTokensText.rendered() : "0")
                            .font(.system(size: bigSize, weight: .bold, design: .rounded))
                            .tracking(-0.8)
                            .monospacedDigit()
                            .foregroundStyle(DS.ink)
                            .lineLimit(1)
                            .minimumScaleFactor(0.75)
                        Text(L10n.usageKpiUnitTokens())
                            .font(.system(size: 13, weight: .medium))
                            .foregroundStyle(DS.ink2)
                    }

                    Text(subline)
                        .font(.system(size: 12))
                        .monospacedDigit()
                        .foregroundStyle(DS.ink2)
                        .lineLimit(1)
                        .frame(height: 17, alignment: .leading)
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                compositionColumn(lineHeight: compRowH, minHeight: minCompH, width: compW)
            }
        }
    }

    @ViewBuilder private var scopeLine: some View {
        HStack(spacing: 5) {
            if hero.scrubbing {
                Text(UsageAxisHelper.formatBucketLabel(
                    startMs: hero.scrubStartMs,
                    endMs: hero.scrubEndMs,
                    granularity: vm.granularity
                ))
                .font(.system(size: 12, weight: .medium))
                .monospacedDigit()
                .foregroundStyle(DS.ink)
                .lineLimit(1)

                if hero.hasSelectedModel_p {
                    let dotModel = "· " + hero.selectedModel
                    Text(dotModel)
                        .font(.system(size: 12))
                        .foregroundStyle(DS.ink2)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
            } else if hero.hasSelectedModel_p {
                if let glyph = AgentGlyph(hero.selectedAgent) {
                    AgentMarkShape(glyph: glyph)
                        .fill(DS.ink2)
                        .frame(width: 13, height: 13)
                        .accessibilityLabel(agentName(hero.selectedAgent))
                }
                Text(hero.hasScopeText ? hero.scopeText.rendered() : hero.selectedModel)
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(DS.ink)
                    .lineLimit(1)
                    .truncationMode(.middle)

                Button(action: onClearSeries) {
                    ZStack {
                        Circle()
                            .fill(DS.fill2)
                            .frame(width: 18, height: 18)
                        Image(systemName: "xmark")
                            .font(.system(size: 9, weight: .bold))
                            .foregroundStyle(DS.ink)
                    }
                    .frame(minWidth: 32, minHeight: 28)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(L10n.usageScopeClearA11Y())
            } else {
                Text(hero.hasScopeText ? hero.scopeText.rendered() : L10n.usageScopeAllModels(count: Int(hero.totalModelCount)))
                    .font(.system(size: 12))
                    .foregroundStyle(DS.ink2)
                    .lineLimit(1)
            }
            Spacer(minLength: 0)
        }
    }

    @ViewBuilder private func compositionColumn(lineHeight: CGFloat, minHeight: CGFloat, width: CGFloat) -> some View {
        let comp = hero.hasComposition ? hero.composition : PBCoreUsageCompositionView()
        var items: [(String, String, Bool)] = []
        if comp.hasUncachedInputText { items.append((L10n.usageCompUncached(), comp.uncachedInputText.rendered(), false)) }
        if comp.hasCacheReadText { items.append((L10n.usageCompCacheRead(), comp.cacheReadText.rendered(), false)) }
        if comp.hasCacheWriteText { items.append((L10n.usageCompCacheWrite(), comp.cacheWriteText.rendered(), false)) }
        if comp.hasOutputText { items.append((L10n.usageCompOutput(), comp.outputText.rendered(), false)) }
        if comp.hasReasoningOutputText { items.append((L10n.usageCompReasoningLabel(), comp.reasoningOutputText.rendered(), true)) }
        let fontSize: CGFloat = isWide ? 12.5 : 12

        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(items.enumerated()), id: \.offset) { _, it in
                HStack(spacing: 4) {
                    Text(it.0)
                        .font(.system(size: fontSize))
                        .foregroundStyle(DS.ink2)
                        .padding(.leading, it.2 ? 10 : 0)
                        .lineLimit(1)
                    Spacer(minLength: 4)
                    Text(it.1)
                        .font(.system(size: fontSize, weight: it.2 ? .regular : .medium))
                        .monospacedDigit()
                        .foregroundStyle(it.2 ? DS.ink2 : DS.ink)
                        .lineLimit(1)
                }
                .frame(height: lineHeight)
            }
        }
        .padding(.top, 5)
        .frame(width: width, alignment: .topLeading)
        .frame(minHeight: minHeight, alignment: .topLeading)
    }
}

// MARK: - Minimal Empty State (s7)

private struct UsageEmptyRecordsBlock: View {
    let vm: PBCoreUsageViewModel
    let preset: PBUsageRangePreset
    let isWide: Bool

    var body: some View {
        let barH: CGFloat = isWide ? 150 : 108
        let bigSize: CGFloat = isWide ? 52 : 44
        let ticks = UsageAxisHelper.buildTicks(
            granularity: vm.granularity,
            buckets: [],
            startMs: vm.startMs,
            endMs: vm.endMs,
            preset: preset
        )

        var zeroAttr = AttributedString("0")
        zeroAttr.font = .system(size: bigSize, weight: .bold, design: .rounded)
        zeroAttr.foregroundColor = DS.ink3
        var unitAttr = AttributedString(" \(L10n.usageKpiUnitTokens())")
        unitAttr.font = .system(size: 13, weight: .medium)
        unitAttr.foregroundColor = DS.ink2
        zeroAttr.append(unitAttr)

        return VStack(alignment: .leading, spacing: 0) {
            Text(L10n.usageScopeAllEmpty())
                .font(.system(size: 12))
                .foregroundStyle(DS.ink2)
                .frame(height: 18, alignment: .leading)
                .padding(.top, isWide ? 14 : 18)

            Text(zeroAttr)
                .padding(.top, 2)

            Color.clear.frame(height: 20)

            VStack(spacing: 4) {
                ZStack(alignment: .bottom) {
                    Text(L10n.usageEmptyNoRecords())
                        .font(.system(size: 13))
                        .foregroundStyle(DS.ink2)
                        .frame(maxWidth: .infinity)
                        .frame(height: barH)
                    Rectangle()
                        .fill(DS.faint)
                        .frame(height: 1)
                }
                UsageAxisView(ticks: ticks)
            }
            .padding(.top, 16)
        }
    }
}

// MARK: - 4 Stacked Charts + Shared X-Axis + Hold/Drag Scrub

private struct UsageChartsStack: View {
    let vm: PBCoreUsageViewModel
    let isWide: Bool
    let onScrub: (Int32, Bool) -> Void

    @GestureState private var isScrubActive = false
    @State private var lastBucket: Int?
    @State private var chartWidth: CGFloat = 358
    private let haptic = UISelectionFeedbackGenerator()

    var body: some View {
        let barH: CGFloat = isWide ? 150 : 108
        let stripH: CGFloat = isWide ? 34 : 26
        let barChart = vm.hasBarChart ? vm.barChart : PBCoreUsageBarChartView()
        let buckets = barChart.buckets
        let n = buckets.count
        let hero = vm.hasHero ? vm.hero : PBCoreUsageHeroView()
        let scrubbing = hero.scrubbing && n > 0
        let scrubIdx = Int(hero.scrubBucketIndex)
        let hasSelected = hero.hasSelectedModel_p

        let ticks = UsageAxisHelper.buildTicks(
            granularity: vm.granularity,
            buckets: buckets,
            startMs: vm.startMs,
            endMs: vm.endMs,
            preset: vm.preset
        )

        VStack(alignment: .leading, spacing: 4) {
            ZStack(alignment: .topLeading) {
                VStack(alignment: .leading, spacing: 12) {
                    UsageBarChartCanvas(
                        barChart: barChart,
                        hasSelected: hasSelected,
                        scrubbing: scrubbing,
                        scrubIdx: scrubIdx,
                        height: barH
                    )

                    UsageStripBlock(
                        label: L10n.usageStripCacheHit(),
                        unit: nil,
                        strip: vm.hasCacheHitStrip ? vm.cacheHitStrip : PBCoreUsageMetricStripView(),
                        scrubbing: scrubbing,
                        scrubIdx: scrubIdx,
                        height: stripH
                    )

                    UsageStripBlock(
                        label: L10n.usageStripSpeed(),
                        unit: L10n.usageKpiUnitToks(),
                        strip: vm.hasOutputSpeedStrip ? vm.outputSpeedStrip : PBCoreUsageMetricStripView(),
                        scrubbing: scrubbing,
                        scrubIdx: scrubIdx,
                        height: stripH
                    )

                    UsageStripBlock(
                        label: L10n.usageStripTtft(),
                        unit: nil,
                        strip: vm.hasTtftStrip ? vm.ttftStrip : PBCoreUsageMetricStripView(),
                        scrubbing: scrubbing,
                        scrubIdx: scrubIdx,
                        height: stripH
                    )
                }

                if scrubbing && scrubIdx >= 0 && scrubIdx < n {
                    Canvas { ctx, size in
                        let cx = (CGFloat(scrubIdx) + 0.5) / CGFloat(n) * size.width
                        var path = Path()
                        path.move(to: CGPoint(x: cx, y: 0))
                        path.addLine(to: CGPoint(x: cx, y: size.height))
                        ctx.stroke(path, with: .color(DS.ink2), lineWidth: 1)
                    }
                    .allowsHitTesting(false)
                }
            }
            .background {
                GeometryReader { g in
                    Color.clear
                        .onAppear { chartWidth = g.size.width }
                        .onChange(of: g.size.width) { _, w in chartWidth = w }
                }
            }
            .contentShape(Rectangle())
            .gesture(scrubGesture(bucketCount: n))
            .onChange(of: isScrubActive) { _, active in
                if !active {
                    lastBucket = nil
                    onScrub(0, true)
                }
            }

            UsageAxisView(ticks: ticks)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(vm.hasChartsA11YLabel ? vm.chartsA11YLabel.rendered() : "")
    }

    private func scrubGesture(bucketCount: Int) -> some Gesture {
        LongPressGesture(minimumDuration: 0.15, maximumDistance: 10)
            .sequenced(before: DragGesture(minimumDistance: 0, coordinateSpace: .local))
            .updating($isScrubActive) { value, state, _ in
                switch value {
                case .second(true, let drag):
                    if !state {
                        haptic.prepare()
                        haptic.selectionChanged()
                    }
                    state = true
                    if let drag, bucketCount > 0, chartWidth > 0 {
                        let raw = Int(floor(drag.location.x / chartWidth * CGFloat(bucketCount)))
                        let idx = max(0, min(bucketCount - 1, raw))
                        if lastBucket != idx {
                            if lastBucket != nil {
                                haptic.selectionChanged()
                            }
                            lastBucket = idx
                            onScrub(Int32(idx), false)
                        }
                    }
                default:
                    break
                }
            }
            .onEnded { _ in
                lastBucket = nil
                onScrub(0, true)
            }
    }
}

private struct UsageBarChartCanvas: View {
    let barChart: PBCoreUsageBarChartView
    let hasSelected: Bool
    let scrubbing: Bool
    let scrubIdx: Int
    let height: CGFloat

    var body: some View {
        let buckets = barChart.buckets
        let n = buckets.count
        let topPad: CGFloat = 12
        let midY = topPad + (height - topPad) / 2

        Canvas { ctx, size in
            let w = size.width
            let innerH = max(1, size.height - topPad)

            var gTop = Path()
            gTop.move(to: CGPoint(x: 0, y: topPad))
            gTop.addLine(to: CGPoint(x: w, y: topPad))
            ctx.stroke(gTop, with: .color(DS.sep), lineWidth: 1)

            var gMid = Path()
            gMid.move(to: CGPoint(x: 0, y: midY))
            gMid.addLine(to: CGPoint(x: w, y: midY))
            ctx.stroke(gMid, with: .color(DS.sep), lineWidth: 1)

            let yMax = Double(barChart.yMax)
            if yMax > 0 && n > 0 {
                let bw = w / CGFloat(n)
                let gap: CGFloat = n > 26 ? 2 : 3
                let barW = max(1, bw - gap)

                for (i, b) in buckets.enumerated() {
                    let allTok = Double(b.allTokens)
                    guard allTok > 0 else { continue }
                    let x = CGFloat(i) * bw + gap / 2
                    let allH = min(innerH, max(1.5, CGFloat(allTok / yMax) * innerH))
                    let alpha: Double = (scrubbing && scrubIdx != i) ? 0.28 : 1.0

                    if hasSelected {
                        let ghostRect = CGRect(x: x, y: size.height - allH, width: barW, height: allH)
                        ctx.fill(Path(roundedRect: ghostRect, cornerRadius: 1.5), with: .color(DS.fill2.opacity(alpha)))
                        let selTok = Double(b.selectedTokens)
                        if selTok > 0 {
                            let selH = min(innerH, max(1.5, CGFloat(selTok / yMax) * innerH))
                            let selRect = CGRect(x: x, y: size.height - selH, width: barW, height: selH)
                            ctx.fill(Path(roundedRect: selRect, cornerRadius: 1.5), with: .color(DS.ink.opacity(alpha)))
                        }
                    } else {
                        let rect = CGRect(x: x, y: size.height - allH, width: barW, height: allH)
                        ctx.fill(Path(roundedRect: rect, cornerRadius: 1.5), with: .color(DS.ink.opacity(alpha)))
                    }
                }

                if barChart.hasYMaxText {
                    drawHaloLabel(ctx, text: barChart.yMaxText.rendered(), x: w, bottomY: topPad - 2)
                }
                if barChart.hasYMidText {
                    drawHaloLabel(ctx, text: barChart.yMidText.rendered(), x: w, bottomY: midY - 2)
                }
            }

            var base = Path()
            base.move(to: CGPoint(x: 0, y: size.height - 0.5))
            base.addLine(to: CGPoint(x: w, y: size.height - 0.5))
            ctx.stroke(base, with: .color(DS.faint), lineWidth: 1)
        }
        .frame(height: height)
    }

    private func drawHaloLabel(_ ctx: GraphicsContext, text: String, x: CGFloat, bottomY: CGFloat) {
        let halo = ctx.resolve(Text(text).font(.system(size: 10.5)).monospacedDigit().foregroundStyle(DS.paper))
        let ink = ctx.resolve(Text(text).font(.system(size: 10.5)).monospacedDigit().foregroundStyle(DS.ink2))
        let anchor = CGPoint(x: x, y: bottomY)
        for r in [1.3, 2.5] as [CGFloat] {
            for k in 0..<12 {
                let angle = Double(k) * (.pi / 6.0)
                let dx = CGFloat(cos(angle)) * r
                let dy = CGFloat(sin(angle)) * r
                ctx.draw(halo, at: CGPoint(x: anchor.x + dx, y: anchor.y + dy), anchor: .bottomTrailing)
            }
        }
        ctx.draw(ink, at: anchor, anchor: .bottomTrailing)
    }
}

private struct UsageStripBlock: View {
    let label: String
    let unit: String?
    let strip: PBCoreUsageMetricStripView
    let scrubbing: Bool
    let scrubIdx: Int
    let height: CGFloat

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if !strip.hasRange_p {
                HStack(spacing: 6) {
                    Text(label).font(.system(size: 12)).foregroundStyle(DS.ink2)
                    Text("–")
                        .font(.system(size: 13.5, weight: .semibold))
                        .foregroundStyle(DS.ink3)
                        .accessibilityLabel(L10n.usageNoSampleA11Y())
                }
                .frame(height: 17)
                Color.clear.frame(height: height)
            } else {
                let curStr = strip.hasCurrentValueText ? strip.currentValueText.rendered() : "–"
                let minStr = strip.hasMinValueText ? strip.minValueText.rendered() : ""
                let maxStr = strip.hasMaxValueText ? strip.maxValueText.rendered() : ""

                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(label).font(.system(size: 12)).foregroundStyle(DS.ink2)
                    Text(curStr)
                        .font(.system(size: 13.5, weight: .semibold))
                        .monospacedDigit()
                        .foregroundStyle(strip.hasValue_p ? DS.ink : DS.ink3)
                    if strip.hasValue_p, let unit {
                        Text(unit).font(.system(size: 12)).foregroundStyle(DS.ink2)
                    }
                    Spacer()
                    if strip.showRange {
                        let rangeText = [minStr, maxStr].joined(separator: " – ")
                        Text(rangeText)
                            .font(.system(size: 11))
                            .monospacedDigit()
                            .foregroundStyle(DS.ink2)
                    }
                }
                .frame(height: 17)

                Canvas { ctx, size in
                    let w = size.width
                    let h = size.height
                    let pad: CGFloat = 3
                    let innerH = max(1, h - pad * 2)
                    let span = max(strip.scaleMax - strip.scaleMin, 1e-9)

                    func coord(_ pts: [PBCoreUsageStripPointView], _ i: Int) -> CGPoint? {
                        guard i >= 0, i < pts.count, pts[i].hasValue_p else { return nil }
                        let n = pts.count
                        let x = (CGFloat(i) + 0.5) / CGFloat(n) * w
                        let y = h - pad - CGFloat((pts[i].value - strip.scaleMin) / span) * innerH
                        return CGPoint(x: x, y: y)
                    }

                    func drawSeries(_ pts: [PBCoreUsageStripPointView], color: Color, width: CGFloat, drawDots: Bool) {
                        var path = Path()
                        var pen = false
                        for i in 0..<pts.count {
                            guard let pt = coord(pts, i) else {
                                pen = false
                                continue
                            }
                            let prev = coord(pts, i - 1)
                            let next = coord(pts, i + 1)
                            if prev == nil && next == nil {
                                if drawDots {
                                    ctx.fill(
                                        Path(ellipseIn: CGRect(x: pt.x - 1.8, y: pt.y - 1.8, width: 3.6, height: 3.6)),
                                        with: .color(color)
                                    )
                                }
                                pen = false
                                continue
                            }
                            if !pen { path.move(to: pt) } else { path.addLine(to: pt) }
                            pen = true
                        }
                        if !path.isEmpty {
                            ctx.stroke(
                                path,
                                with: .color(color),
                                style: StrokeStyle(lineWidth: width, lineCap: .round, lineJoin: .round)
                            )
                        }
                    }

                    let hasSel = !strip.selectedPoints.isEmpty
                    if hasSel {
                        drawSeries(strip.allPoints, color: DS.faint, width: 1.2, drawDots: false)
                    }
                    let frontPts = hasSel ? strip.selectedPoints : strip.allPoints
                    drawSeries(frontPts, color: DS.ink, width: 1.6, drawDots: true)

                    let mark = scrubbing
                        ? coord(frontPts, scrubIdx)
                        : coord(frontPts, Int(strip.lastValidIndex))
                    if let mark {
                        if scrubbing {
                            let ringRect = CGRect(x: mark.x - 3, y: mark.y - 3, width: 6, height: 6)
                            ctx.fill(Path(ellipseIn: ringRect), with: .color(DS.paper))
                            ctx.stroke(Path(ellipseIn: ringRect), with: .color(DS.ink), lineWidth: 1.6)
                        } else {
                            let dotRect = CGRect(x: mark.x - 2.2, y: mark.y - 2.2, width: 4.4, height: 4.4)
                            ctx.fill(Path(ellipseIn: dotRect), with: .color(DS.ink))
                        }
                    }
                }
                .frame(height: height)
            }
        }
    }
}

private struct UsageAxisView: View {
    let ticks: [UsageAxisTick]

    var body: some View {
        GeometryReader { geo in
            let w = geo.size.width
            ZStack(alignment: .topLeading) {
                ForEach(ticks) { tick in
                    let anchorX = tick.pct / 100 * w
                    let alignment: Alignment = {
                        switch tick.align {
                        case .start: return .topLeading
                        case .center: return .top
                        case .end: return .topTrailing
                        }
                    }()
                    Text(tick.label)
                        .font(.system(size: 10.5))
                        .monospacedDigit()
                        .foregroundStyle(DS.ink2)
                        .lineLimit(1)
                        .fixedSize()
                        .position(
                            x: {
                                switch tick.align {
                                case .start: return 0
                                case .center: return anchorX
                                case .end: return w
                                }
                            }(),
                            y: 11
                        )
                        .frame(width: 0, alignment: alignment)
                }
            }
        }
        .frame(height: 20)
    }
}

// MARK: - 2-Line Sortable Model Table

private struct UsageModelTable: View {
    let vm: PBCoreUsageViewModel
    let isWide: Bool
    let onSelectSort: (PBCoreUsageSortColumn) -> Void
    let onSelectRow: (PBCoreUsageModelRowView) -> Void

    private var sortBy: PBCoreUsageSortColumn {
        vm.sortBy == .unspecified ? .tokens : vm.sortBy
    }

    var body: some View {
        let hPad: CGFloat = isWide ? 12 : 16
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Text(L10n.usageColModel())
                    .font(.system(size: 11.5))
                    .foregroundStyle(DS.ink2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                sortHeader(L10n.usageColTokens(), column: .tokens, width: 62, asc: false)
                sortHeader(L10n.usageColCache(), column: .cacheHit, width: 42, asc: false)
                sortHeader(L10n.usageColSpeed(), column: .speed, width: 46, asc: false)
                sortHeader(L10n.usageColTtft(), column: .ttft, width: 44, asc: true)
            }
            .padding(.horizontal, hPad)
            .padding(.bottom, 7)

            Rectangle().fill(DS.sep).frame(height: 0.5)

            ForEach(Array(vm.rows.enumerated()), id: \.element.seriesKey) { idx, row in
                let prevSel = idx > 0 && vm.rows[idx - 1].selected
                if idx > 0 && !row.selected && !prevSel {
                    Rectangle()
                        .fill(DS.sep)
                        .frame(height: 0.5)
                        .padding(.horizontal, hPad)
                }
                modelRow(row, hPad: hPad)
            }

            Rectangle().fill(DS.sep).frame(height: 0.5)

            VStack(alignment: .leading, spacing: 6) {
                Text(L10n.usageFooterNote())
                    .font(.system(size: 11.5))
                    .lineSpacing(4)
                    .foregroundStyle(DS.ink2)
                if vm.hasUnsupportedFootnote {
                    let extra = vm.unsupportedFootnote.rendered()
                    if !extra.isEmpty {
                        Text(extra)
                            .font(.system(size: 11.5))
                            .lineSpacing(4)
                            .foregroundStyle(DS.ink2)
                    }
                }
            }
            .padding(.horizontal, hPad)
            .padding(.top, 14)
            .padding(.bottom, 6)
        }
    }

    @ViewBuilder private func sortHeader(
        _ title: String,
        column: PBCoreUsageSortColumn,
        width: CGFloat,
        asc: Bool
    ) -> some View {
        let active = sortBy == column
        let arrow = active ? (asc ? " ↑" : " ↓") : ""
        let headerText = title + arrow
        Button {
            onSelectSort(column)
        } label: {
            Text(headerText)
                .font(.system(size: 11.5, weight: active ? .semibold : .regular))
                .foregroundStyle(active ? DS.ink : DS.ink2)
                .lineLimit(1)
                .frame(width: width, alignment: .trailing)
                .frame(minHeight: 24, alignment: .trailing)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder private func modelRow(_ row: PBCoreUsageModelRowView, hPad: CGFloat) -> some View {
        let deltaStr = row.hasDeltaText ? row.deltaText.rendered() : ""
        let shape = RoundedRectangle(cornerRadius: isWide ? 10 : 0, style: .continuous)

        Button {
            onSelectRow(row)
        } label: {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 6) {
                    if let glyph = AgentGlyph(row.agent) {
                        AgentMarkShape(glyph: glyph)
                            .fill(DS.ink2)
                            .frame(width: 13, height: 13)
                            .accessibilityLabel(row.agentName)
                    }
                    Text(row.model)
                        .font(.system(size: 13.5, weight: .medium))
                        .foregroundStyle(DS.ink)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if !deltaStr.isEmpty {
                        Text(deltaStr)
                            .font(.system(size: 11.5))
                            .monospacedDigit()
                            .foregroundStyle(DS.ink2)
                    }
                }
                .frame(minHeight: 18)

                HStack(spacing: 8) {
                    GeometryReader { g in
                        let w = max(2, g.size.width * CGFloat(max(0, min(1, row.shareRatio))))
                        ZStack(alignment: .leading) {
                            Capsule().fill(DS.fill).frame(height: 3)
                            Capsule()
                                .fill(row.selected ? DS.ink : DS.ink2)
                                .frame(width: w, height: 3)
                        }
                    }
                    .frame(height: 3)
                    .frame(maxWidth: .infinity)

                    Text(row.hasTotalTokensText ? row.totalTokensText.rendered() : "0")
                        .font(.system(size: 13, weight: .semibold))
                        .monospacedDigit()
                        .foregroundStyle(DS.ink)
                        .lineLimit(1)
                        .frame(width: 62, alignment: .trailing)

                    metricCell(row.hasCacheHitText ? row.cacheHitText.rendered() : nil, width: 42)
                    metricCell(row.hasSpeedText ? row.speedText.rendered() : nil, width: 46)
                    metricCell(row.hasTtftText ? row.ttftText.rendered() : nil, width: 44)
                }
                .frame(minHeight: 17)
            }
            .padding(.horizontal, hPad)
            .padding(.top, 9)
            .padding(.bottom, 10)
            .background(row.selected ? DS.fill : Color.clear, in: shape)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder private func metricCell(_ text: String?, width: CGFloat) -> some View {
        Text(text ?? "–")
            .font(.system(size: 13))
            .monospacedDigit()
            .foregroundStyle(text == nil ? DS.ink3 : DS.ink)
            .lineLimit(1)
            .frame(width: width, alignment: .trailing)
            .accessibilityLabel(text ?? L10n.usageNoSampleA11Y())
    }
}
