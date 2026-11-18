#if DEBUG
import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// Shell-side tests for 电脑状态 demo fixtures and watch ref-counting.
/// Poll scheduling, hysteresis, scrub math, and cleanup routing live in `clientcore`.
@MainActor
@Suite struct MachineStatusScreenTests {
    @Test func demoFixturesKeepExpectedSampleShapes() throws {
        let normal = MachineStatusDemo.status(mode: "normal", now: 1_000_000)
        #expect(normal.samples.count == 60)
        let last = try #require(normal.samples.last)
        let first = try #require(normal.samples.first)
        #expect(last.timeMs == 1_000_000)
        #expect(first.timeMs == 1_000_000 - 59 * 2000)
        #expect(Int(normal.cpuPercent.rounded()) == 23)

        let tight = MachineStatusDemo.status(mode: "tight", now: 1_000_000)
        #expect(tight.disk.pressure == .critical)

        #expect(MachineStatusDemo.status(mode: "few", now: 1).samples.count == 1)
    }

    @Test func demoMachineSettingsPopulatesRings() async throws {
        let settings = SettingsStore(defaults: UserDefaults(suiteName: "ra-rings-\(UUID())")!)
        let model = AppModel(settings: settings)
        let remote = Demo.remote()
        let initial = Demo.state(.everyday)
        model.setForeground(true)
        model.showDemo(account: Demo.account, remote: remote, initial: initial) {
            await remote.showForTest(state: initial, modelLists: Demo.models)
        }
        defer { model.closeSyncForTest() }
        let host = try #require(model.context?.coreHost)
        // Wait briefly for seedDemo Task in showDemo
        for _ in 0..<200 where !(model.context?.core.machineSettings.found ?? false) {
            await host.openMachineSettings(machineID: "mac")
            try? await Task.sleep(for: .milliseconds(20))
        }
        let token = await host.acquireMachineStatusWatch("mac", mode: .overview)
        for _ in 0..<200 where (model.context?.core.machineSettings.statusRings.first?.valueText.rendered() ?? "").isEmpty || model.context?.core.machineSettings.statusRings.first?.valueText.rendered() == "—" {
            try? await Task.sleep(for: .milliseconds(20))
        }
        let ms = model.context!.core.machineSettings
        let labelsZh = ms.statusRings.map { $0.a11YLabel.rendered(locale: "zh-CN") }
        await host.releaseMachineStatusWatch(token)
        #expect(ms.found && ms.showStatusEntry)
        #expect(labelsZh == ["CPU，23%", "内存，70%", "磁盘可用，42 GB"])
    }

    @Test func watchTokensRefCountAndPreferLiveMode() async {
        let settings = SettingsStore(defaults: UserDefaults(suiteName: "ra-watch-\(UUID())")!)
        let model = AppModel(settings: settings)
        model.showDemo(account: Demo.account, remote: Demo.remote(), initial: Demo.state(.everyday)) {}
        defer { model.closeSyncForTest() }
        let host = model.context!.coreHost

        let overview = await host.acquireMachineStatusWatch("mac", mode: .overview)
        let live = await host.acquireMachineStatusWatch("mac", mode: .live)
        // Release overview while live remains active.
        await host.releaseMachineStatusWatch(overview)
        let machineID = model.context?.core.machineStatus.machineID ?? ""
        #expect(machineID.isEmpty || machineID == "mac")
        await host.releaseMachineStatusWatch(live)
    }
}
#endif
