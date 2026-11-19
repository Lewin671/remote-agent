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
            #expect(UsageFormat.formatCacheHit(0.624) == "62%")
            #expect(UsageFormat.formatSpeed(74.2) == "74")
            #expect(UsageFormat.formatTtft(820) == "820 ms")
        }
        MessageLocale.$overrideForTesting.withValue("en") {
            #expect(UsageFormat.formatTokens(1_500) == "1.5k")
            #expect(UsageFormat.formatTokens(2_400_000) == "2.4M")
        }

        let nowMs: Int64 = 1_760_000_000_000
        let today = CoreHost.buildUsageOpenParams(preset: .today, nowMs: nowMs)
        #expect(today.bucketBoundsMs.count == 25)
        let seven = CoreHost.buildUsageOpenParams(preset: .sevenDays, nowMs: nowMs)
        #expect(seven.bucketBoundsMs.count == 8)
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
        let params = CoreHost.buildUsageOpenParams(preset: .sevenDays, nowMs: now)
        let req = PBGetUsageStatsRequest.with {
            $0.preset = .sevenDays
            $0.bucketBoundsMs = params.bucketBoundsMs
        }
        let stats = SettingsDemo.usageStats(request: req, machineID: "mac", now: now)
        for metric: PBCoreUsageMetricTab in [.tokens, .cacheHit, .outputSpeed, .ttft] {
            let vm = PBCoreUsageViewModel.with {
                $0.machineID = "mac"
                $0.machineName = "Workstation"
                $0.found = true
                $0.supported = true
                $0.online = true
                $0.preset = .sevenDays
                $0.granularity = stats.granularity
                $0.startMs = stats.startMs
                $0.endMs = stats.endMs
                $0.metric = metric
                $0.sources = stats.sources
                $0.series = stats.series.enumerated().map { idx, s in
                    PBCoreUsageSeriesView.with {
                        $0.agent = s.agent
                        $0.agentName = agentName(s.agent)
                        $0.model = s.model
                        $0.seriesKey = "\(s.agent.rawValue):\(s.model)"
                        $0.patternIndex = Int32(idx)
                        $0.cacheWriteSupported = s.cacheWriteSupported
                        $0.current = s.current
                        $0.previous = s.previous
                        $0.hasTokensDelta_p = true
                        $0.tokensDeltaPercent = 14
                        $0.hasCacheHitDelta_p = true
                        $0.cacheHitDeltaPoints = 4
                        $0.hasSpeedDelta_p = true
                        $0.speedDeltaPercent = 6
                        $0.hasTtftDelta_p = true
                        $0.ttftDeltaPercent = -7
                        $0.buckets = s.buckets
                    }
                }
            }
            render(UsageScreenContent(vm: vm))
        }
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
