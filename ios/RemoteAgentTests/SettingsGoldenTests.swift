#if DEBUG
import Foundation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

@MainActor
@Suite struct SettingsGoldenTests {
    private func render<V: View>(_ view: V, width: CGFloat = 390) {
        let suite = "golden-\(UUID())"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let settings = SettingsStore(defaults: defaults)
        let model = AppModel(settings: settings)
        let host = UIHostingController(
            rootView: view
                .environment(model)
                .environment(settings)
                .environment(Navigator())
        )
        let size = host.sizeThatFits(in: CGSize(width: width, height: 2400))
        #expect(size.width <= width + 0.5 && size.height > 0)
        host.view.frame = CGRect(x: 0, y: 0, width: width, height: 844)
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
    }

    @Test func settingsOverviewGoldenCardsAndRowsRenderWithoutOverflow() {
        let settingsVMs = ScreenGoldens.settingsAll("settings_overview")
        let devicesVMs = ScreenGoldens.devicesAll("settings_overview")
        let clientVMs = ScreenGoldens.clientDetails("settings_overview")
        #expect(settingsVMs.count == 3)
        #expect(settingsVMs[2].connCheck.lastFailureText.key.isEmpty)
        #expect(settingsVMs[2].connCheck.lastFailureText.literal == "dial tcp: timeout")
        #expect(devicesVMs.count == 1)
        #expect(clientVMs.count == 2)

        for vm in settingsVMs {
            #expect(vm.machines.allSatisfy { !$0.shortID.isEmpty })
            render(SettingsPage {
                GroupCard(separatorLeading: 46) {
                    ForEach(vm.machines, id: \.machineID) { m in
                        MachineCard(machine: m)
                    }
                }
                HomeDiskReminders(reminders: vm.diskReminders)
            })
        }
        // Archived machine card with empty subtitleParts still renders shortID cleanly.
        var archived = PBMachineListCardView()
        archived.machineID = "11223344556677889900aabbccddeeff"
        archived.name = "Old Studio"
        archived.shortID = "…ccddeeff"
        archived.member = false
        render(SettingsPage { GroupCard(separatorLeading: 46) { MachineCard(machine: archived) } })
        for d in devicesVMs {
            render(SettingsPage {
                GroupCard {
                    ForEach(d.clients, id: \.deviceID) { c in
                        ClientRow(client: c)
                    }
                    ForEach(d.unfinishedPairings, id: \.deviceID) { c in
                        ClientRow(client: c)
                    }
                }
            })
        }
    }

    @Test func machineSettingsGoldenViewsRenderEveryStep() {
        let vms = ScreenGoldens.machineSettingsAll("machine_settings_edit")
        #expect(vms.count == 4)
        for vm in vms {
            render(MachineScreenContent(
                vm: vm,
                onOpenStatus: {},
                onOpenQuota: {},
                onOpenUsage: {},
                onOpenStorage: {},
                onOpenProjects: {},
                onToggleDirect: { _ in },
                onRetryDirect: {},
                onRetrySettings: {},
                onRefresh: nil,
                onSelectAgent: { _ in },
                onToggleAgent: { _, _ in },
                onUpdateAgentDefaults: { _, _, _, _ in },
                onUpdateDaemonSettings: { _, _, _, _ in },
                managementSection: { EmptyView() }
            ))
            if vm.hasAttachmentStorage && vm.attachmentStorage.loaded {
                render(MachineStorageContent(
                    vm: vm,
                    onRefresh: nil,
                    onRetry: {},
                    onSelectQuota: { _ in },
                    onClear: {}
                ))
            }
        }
    }

    @Test func demoUsageStatsSurvivesEmptyBuckets() {
        // 空 bounds 时演示数据按最近 7 天补齐区间；上一周期的请求数不能因无符号减法下溢而崩溃
        let stats = SettingsDemo.usageStats(request: PBGetUsageStatsRequest(), machineID: "m", now: 1_760_000_000_000)
        #expect(!stats.series.isEmpty)
        #expect(stats.series.allSatisfy { $0.previous.usageRequestCount >= 3 })
    }

    @Test func usageOpenParamsWorkAcrossPresets() {
        let nowMs: Int64 = 1_760_000_000_000
        let today = CoreHost.buildUsageOpenParams(preset: .today, nowMs: nowMs)
        #expect(today.bucketBoundsMs.count == 25)
        let seven = CoreHost.buildUsageOpenParams(preset: .sevenDays, nowMs: nowMs)
        #expect(seven.bucketBoundsMs.count == 29)
        let thirty = CoreHost.buildUsageOpenParams(preset: .thirtyDays, nowMs: nowMs)
        #expect(thirty.bucketBoundsMs.count == 31)

        let tooOldStart = nowMs - Int64(180) * 86_400_000
        let clamped = CoreHost.clampUsageCustomRange(startMs: tooOldStart, endMs: nowMs + 5 * 86_400_000, nowMs: nowMs)
        #expect(clamped.startMs >= tooOldStart)
        #expect(clamped.endMs <= nowMs)

        #expect(MachineScreenContent<EmptyView>.parseAgentSlug("claude") == .claude)
        #expect(MachineScreenContent<EmptyView>.parseAgentSlug("codex") == .codex)
        #expect(MachineScreenContent<EmptyView>.agentSlug(.claude) == "claude")
    }

    @Test func usageScreenRendersAllFourMetricsAndEmptyStates() {
        let now: Int64 = 1_760_000_000_000
        let strip = PBCoreUsageMetricStripView.with {
            $0.hasValue_p = true
            $0.currentValue = 0.94
            $0.currentValueText = .with { $0.literal = "94%" }
            $0.hasRange_p = true
            $0.showRange = true
            $0.minValue = 0.90
            $0.maxValue = 0.97
            $0.minValueText = .with { $0.literal = "90%" }
            $0.maxValueText = .with { $0.literal = "97%" }
            $0.scaleMin = 0.88
            $0.scaleMax = 0.99
            $0.lastValidIndex = 1
            $0.allPoints = [
                .with { $0.hasValue_p = true; $0.value = 0.91 },
                .with { $0.hasValue_p = true; $0.value = 0.96 },
            ]
        }
        let vm = PBCoreUsageViewModel.with {
            $0.machineID = "mac"
            $0.machineName = "Workstation"
            $0.subtitle = .with { $0.literal = "Workstation" }
            $0.found = true
            $0.supported = true
            $0.online = true
            $0.preset = .today
            $0.granularity = .hour
            $0.rangeDays = 1
            $0.sortBy = .tokens
            $0.hasAnyUsage_p = true
            $0.emptyKind = .none
            $0.chartsA11YLabel = .with { $0.literal = "Workstation usage" }
            $0.hero = .with {
                $0.totalModelCount = 2
                $0.totalTokens = 720_000_000
                $0.totalTokensText = .with { $0.literal = "7.2亿" }
                $0.scopeText = .with { $0.literal = "全部 2 个模型" }
                $0.sublineText = .with { $0.literal = "862 次请求 · ▼72%" }
                $0.requestCount = 862
                $0.hasTokensDelta_p = true
                $0.tokensDeltaPercent = -72
                $0.periodCompositionRows = 5
                $0.composition = .with {
                    $0.uncachedInputTokens = 42_260_000
                    $0.cacheReadTokens = 670_000_000
                    $0.cacheWriteTokens = 4_400_000
                    $0.outputTokens = 3_240_000
                    $0.reasoningOutputTokens = 1_360_000
                    $0.uncachedInputText = .with { $0.literal = "4,226万" }
                    $0.cacheReadText = .with { $0.literal = "6.7亿" }
                    $0.cacheWriteText = .with { $0.literal = "440万" }
                    $0.outputText = .with { $0.literal = "324万" }
                    $0.reasoningOutputText = .with { $0.literal = "136万" }
                }
            }
            $0.barChart = .with {
                $0.yMax = 100_000_000
                $0.yMid = 50_000_000
                $0.yMaxText = .with { $0.literal = "1亿" }
                $0.yMidText = .with { $0.literal = "5,000万" }
                $0.buckets = [
                    .with {
                        $0.startMs = now - 7_200_000
                        $0.endMs = now - 3_600_000
                        $0.allTokens = 45_000_000
                        $0.hasData_p = true
                    },
                    .with {
                        $0.startMs = now - 3_600_000
                        $0.endMs = now
                        $0.allTokens = 85_000_000
                        $0.hasData_p = true
                    },
                ]
            }
            $0.cacheHitStrip = strip
            $0.outputSpeedStrip = strip
            $0.ttftStrip = strip
            $0.rows = [
                .with {
                    $0.agent = .codex
                    $0.agentName = "Codex"
                    $0.model = "gpt-6-astra"
                    $0.seriesKey = "1:gpt-6-astra"
                    $0.shareRatio = 1
                    $0.totalTokens = 380_000_000
                    $0.totalTokensText = .with { $0.literal = "3.8亿" }
                    $0.hasCacheHit_p = true
                    $0.cacheHitRate = 0.96
                    $0.cacheHitText = .with { $0.literal = "96%" }
                    $0.hasSpeed_p = true
                    $0.speedP50 = 118
                    $0.speedText = .with { $0.literal = "118" }
                    $0.hasTtft_p = true
                    $0.ttftP50Ms = 400
                    $0.ttftText = .with { $0.literal = "0.4s" }
                },
                .with {
                    $0.agent = .claude
                    $0.agentName = "Claude Code"
                    $0.model = "claude-sonnet-4-6"
                    $0.seriesKey = "2:claude-sonnet-4-6"
                    $0.shareRatio = 0.05
                    $0.totalTokens = 18_000_000
                    $0.totalTokensText = .with { $0.literal = "1,800万" }
                },
            ]
        }
        render(UsageScreenContent(vm: vm))
        render(UsageScreenContent(vm: PBCoreUsageViewModel.with {
            $0.machineID = "mac"
            $0.machineName = "Workstation"
            $0.found = true
            $0.supported = true
            $0.online = true
            $0.preset = .today
            $0.granularity = .hour
            $0.hasAnyUsage_p = false
            $0.emptyKind = .noRecords
        }))
        render(UsageScreenContent(vm: PBCoreUsageViewModel.with {
            $0.machineID = "mac"
            $0.machineName = "Workstation"
            $0.found = true
            $0.supported = false
            $0.emptyKind = .unsupported
            $0.emptyTitle = .with { $0.literal = "Unsupported" }
            $0.emptyBody = .with { $0.literal = "Update daemon" }
        }))
    }

    @Test func machineStatusGoldenViewsRenderEveryStep() {
        let vms = ScreenGoldens.machineStatuses("machine_status_lifecycle")
        #expect(vms.count == 5)
        for vm in vms {
            render(MachineStatusScreenContent(
                vm: vm,
                main: .constant(.cpu),
                onRetry: {},
                onScrub: { _, _, _ in },
                onSelectTab: { _ in },
                onOpenStorage: {},
                onCleanup: {},
                onOpenSession: { _ in }
            ))
        }
    }

    @Test func machineQuotaGoldenViewsRenderEveryStep() {
        let vms = ScreenGoldens.quotas("machine_quota")
        #expect(vms.count == 2)
        for vm in vms {
            render(QuotaScreenContent(vm: vm))
        }
    }
}
#endif
