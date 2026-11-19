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
        #expect(settingsVMs.count == 2)
        #expect(devicesVMs.count == 1)
        #expect(clientVMs.count == 2)

        for vm in settingsVMs {
            render(SettingsPage {
                GroupCard(separatorLeading: 46) {
                    ForEach(vm.machines, id: \.machineID) { m in
                        MachineCard(machine: m)
                    }
                }
                HomeDiskReminders(reminders: vm.diskReminders)
            })
        }
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

    @Test func usageFormatAndOpenParamsWorkAcrossLocalesAndPresets() {
        MessageLocale.$overrideForTesting.withValue("zh-CN") {
            #expect(UsageFormat.formatTokens(0) == "0")
            #expect(UsageFormat.formatTokens(9_850) == "9,850")
            #expect(UsageFormat.formatTokens(34_500_000) == "3,450万")
            #expect(UsageFormat.formatTokens(120_000_000) == "1.2亿")
            #expect(UsageFormat.formatTokens(12_860_000_000) == "128.6亿")
            #expect(UsageFormat.formatCacheHit(0.624) == "62%")
            #expect(UsageFormat.formatSpeed(74.2) == "74")
            #expect(UsageFormat.formatTtftSeconds(820) == "0.8s")
            #expect(UsageFormat.formatTtftSeconds(14_200) == "14s")
            #expect(UsageFormat.formatDeltaTriangle(1_240) == "▲1,240%")
            #expect(UsageFormat.formatDeltaTriangle(-72) == "▼72%")
            #expect(UsageFormat.formatDeltaTriangle(0.2) == L10n.usageFlat())
        }
        MessageLocale.$overrideForTesting.withValue("en") {
            #expect(UsageFormat.formatTokens(1_500) == "1.5k")
            #expect(UsageFormat.formatTokens(2_400_000) == "2.4M")
        }

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
            $0.hasRange_p = true
            $0.minValue = 0.90
            $0.maxValue = 0.97
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
            $0.found = true
            $0.supported = true
            $0.online = true
            $0.preset = .today
            $0.granularity = .hour
            $0.rangeDays = 1
            $0.sortBy = .tokens
            $0.hasAnyUsage_p = true
            $0.hero = .with {
                $0.totalModelCount = 2
                $0.totalTokens = 720_000_000
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
                }
            }
            $0.barChart = .with {
                $0.yMax = 100_000_000
                $0.yMid = 50_000_000
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
                    $0.hasCacheHit_p = true
                    $0.cacheHitRate = 0.96
                    $0.hasSpeed_p = true
                    $0.speedP50 = 118
                    $0.hasTtft_p = true
                    $0.ttftP50Ms = 400
                },
                .with {
                    $0.agent = .claude
                    $0.agentName = "Claude Code"
                    $0.model = "claude-sonnet-4-6"
                    $0.seriesKey = "2:claude-sonnet-4-6"
                    $0.shareRatio = 0.05
                    $0.totalTokens = 18_000_000
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
        }))
        render(UsageScreenContent(vm: PBCoreUsageViewModel.with {
            $0.machineID = "mac"
            $0.machineName = "Workstation"
            $0.found = true
            $0.supported = false
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
