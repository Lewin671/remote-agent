#if DEBUG
import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// What the computer-status screens keep apart from drawing: the feed that follows the page's visibility,
/// 让 Agent 帮我清理's draft, and the sample answers the screenshots use.
@MainActor
@Suite struct MachineStatusScreenTests {
    private actor Reads {
        var count = 0
        func next() -> PBMachineStatus {
            count += 1
            var s = PBMachineStatus(); s.observedMs = Int64(count) * 2000; s.cpuPercent = 10
            return s
        }
    }

    private func settle(_ test: () -> Bool) async -> Bool {
        for _ in 0..<5_000 { if test() { return true }; try? await Task.sleep(for: .milliseconds(1)) }
        return false
    }

    @Test func theFeedAsksOnlyWhileVisibleOnlineAndSupported() async {
        let reads = Reads()
        let feed = MachineStatusFeed(interval: .seconds(3600))
        let fetch: MachineStatusFetch = { await reads.next() }
        // Hidden, offline or unsupported: no request.
        feed.update(fetch: fetch, visible: false, online: true, supported: true)
        feed.update(fetch: fetch, visible: true, online: false, supported: true)
        feed.update(fetch: fetch, visible: true, online: true, supported: false)
        try? await Task.sleep(for: .milliseconds(30))
        #expect(await reads.count == 0)
        #expect(feed.view != .stale)
        feed.update(fetch: fetch, visible: true, online: true, supported: true)
        #expect(await settle { feed.view == .live })
        #expect(feed.last?.observedMs == 2000 && feed.receivedAtMs > 0)
        // Hiding stops it for good: a retry while hidden asks nothing.
        feed.update(fetch: fetch, visible: false, online: true, supported: true)
        feed.retry()
        try? await Task.sleep(for: .milliseconds(30))
        #expect(await reads.count == 1)
        feed.stop()
    }

    @Test func anOfflineComputerKeepsTheLastFigures() async {
        let reads = Reads()
        let feed = MachineStatusFeed(interval: .seconds(3600))
        let fetch: MachineStatusFetch = { await reads.next() }
        feed.update(fetch: fetch, visible: true, online: true, supported: true)
        #expect(await settle { feed.view == .live })
        feed.update(fetch: fetch, visible: true, online: false, supported: true)
        #expect(feed.view == .stale && feed.last != nil)
        feed.stop()
    }

    @Test func cleanupPrefillsTheDraftOnThisComputerAndNeverTwice() {
        let account = Demo.account
        _ = account
        var state = Demo.state(.everyday)
        state.machines["mac"]?.meta?.projects = [.with { $0.path = Demo.foo; $0.latestCreatedMs = 5 }]
        let drafts = NewSessionDrafts()
        let prompt = L10n.machineStatusCleanupPrompt(locale: "zh-CN")
        let key = recentMachineProject(state.machines["mac"]!.meta!)!
        #expect(key == Demo.foo)
        drafts.prepareCleanup(state, key: key, machine: "mac", prompt: prompt)
        let draft = drafts.of(key, initial: nil, settled: true)
        #expect(draft.text == prompt && draft.config.value?.machineId == "mac")
        // The user's own words stay, the prompt follows after an empty line, and a second tap changes nothing.
        draft.text = "先看 build 目录"
        drafts.prepareCleanup(state, key: key, machine: "mac", prompt: prompt)
        #expect(draft.text == "先看 build 目录\n\n" + prompt)
        drafts.prepareCleanup(state, key: key, machine: "mac", prompt: prompt)
        #expect(draft.text == "先看 build 目录\n\n" + prompt)
    }

    @Test func aComputerWithoutAProjectHasNoRecentOne() {
        #expect(recentMachineProject(PBMachineMeta()) == nil)
    }

    @Test func theSampleAnswersMatchTheSpecsExamples() {
        let normal = MachineStatusDemo.status(mode: "normal", now: 1_000_000)
        let rings = machineRings(status: normal, online: true, text: StatusText(locale: "zh-CN"))
        #expect(rings.map(\.value) == ["23%", "70%", "42 GB"])
        #expect(normal.samples.count == 60 && normal.samples.last?.timeMs == 1_000_000 && normal.samples.first?.timeMs == 1_000_000 - 59 * 2000)
        let tight = MachineStatusDemo.status(mode: "tight", now: 1_000_000)
        #expect(machineRings(status: tight, online: true, text: StatusText(locale: "zh-CN"))[2].tone == .critical)
        #expect(showDiskActions(tight) && !showDiskActions(normal))
        #expect(MachineStatusDemo.status(mode: "few", now: 1).samples.count == 1)
    }

    @Test func theSampleComputersCanSayTheirStatusAndOneIsLow() {
        let s = Demo.state(.everyday)
        #expect(s.machines.values.allSatisfy { $0.meta?.capabilities.machineStatus == true })
        #expect(diskReminders(s).isEmpty)
    }
}
#endif
