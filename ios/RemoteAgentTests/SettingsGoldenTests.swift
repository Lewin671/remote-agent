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
                ForEach(vm.machines, id: \.machineID) { m in
                    MachineCard(machine: m)
                }
                GroupCard {
                    ForEach(vm.recentClients, id: \.deviceID) { c in
                        ClientRow(client: c)
                    }
                }
                HomeDiskReminders(reminders: vm.diskReminders)
            })
        }
        for d in devicesVMs {
            render(SettingsPage {
                GroupCard {
                    ForEach(d.computers, id: \.machineID) { m in
                        MachineRow(machine: m)
                    }
                    ForEach(d.clients, id: \.deviceID) { c in
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
                onToggleDirect: { _ in },
                onRetryDirect: {},
                onRetrySettings: {},
                onRefresh: nil,
                onSelectAgent: { _ in },
                onUpdateAgentDefaults: { _, _, _, _ in },
                onUpdateDaemonSettings: { _, _, _, _ in },
                onOpenProject: { _ in },
                managementSection: { EmptyView() }
            ))
        }
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
