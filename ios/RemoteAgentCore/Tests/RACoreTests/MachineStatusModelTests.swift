import Foundation
import RAProto
import Testing
@testable import RACore

private func status(cpu: Double = 23, total: UInt64 = 16 << 30, used: UInt64 = UInt64(11.2 * Double(1 << 30)), pressure: PBResourcePressure = .normal,
                    availableGB: Double = 42, disk: PBResourcePressure = .normal) -> PBMachineStatus {
    var s = PBMachineStatus()
    s.cpuPercent = cpu; s.cores = 10; s.loadOne = 3.2
    s.memoryTotalBytes = total; s.memoryUsedBytes = used; s.memoryPressure = pressure
    s.disk.totalBytes = 460_000_000_000; s.disk.availableBytes = UInt64(availableGB * 1e9); s.disk.pressure = disk
    s.agentBytes = 3_100_000_000
    return s
}

@Suite struct MachineStatusModelTests {
    let zh = StatusText(locale: "zh-CN")
    let en = StatusText(locale: "en")

    @Test func ringsReadFromValuesAndTonesNotFromWords() {
        var s = status(pressure: .warning, availableGB: 1.2, disk: .critical)
        s.cpuPercent = 99
        let r = machineRings(status: s, online: true, text: zh)
        #expect(r.map(\.value) == ["99%", "70%", "1.2 GB"])
        #expect(r.map(\.tone) == [.normal, .warn, .critical])
        // The disk ring draws the share used and says the amount free.
        #expect(abs((r[2].fraction ?? 0) - (1 - 1.2 / 460)) < 1e-6)
        #expect(r.map(\.spoken) == ["CPU，99%", "内存，70%", "磁盘可用，1.2 GB"])
        #expect(machineRings(status: status(), online: true, text: en).map(\.spoken) == ["CPU, 23%", "Memory, 70%", "Disk free, 42 GB"])
    }

    @Test func offlineOrUnreadDrawsTracksAndDashes() {
        for r in [machineRings(status: status(), online: false, text: zh), machineRings(status: nil, online: true, text: zh)] {
            #expect(r.map(\.value) == ["—", "—", "—"])
            #expect(r.allSatisfy { $0.fraction == nil && $0.tone == .normal })
        }
    }

    @Test func unitsMemoryBinaryDiskDecimal() {
        // ui-rulings 1: a 16 GiB computer reads 16 GB, a 460 GB volume 460 GB.
        #expect(zh.size(16 << 30, binary: true) == "16 GB")
        #expect(zh.size(460_000_000_000) == "460 GB")
        #expect(zh.size(380_000_000) == "380 MB")
        #expect(zh.size(380 << 20, binary: true) == "380 MB")
        #expect(zh.gbNumber(Int64(11.2 * Double(1 << 30)), oneDecimal: true, binary: true) == "11.2")
        #expect(zh.gbNumber(42_000_000_000) == "42")
        #expect(zh.gbNumber(1_200_000_000) == "1.2")
        #expect(en.number(1234.5, decimals: 1) == "1234.5")
        let de = StatusText(locale: "en")
        #expect(de.percent(23.4) == "23%")
    }

    @Test func memoryCardLines() {
        var s = status(); s.swapUsedBytes = UInt64(1.3 * Double(1 << 30)); s.memoryPressurePosition = 0.34
        let c = memoryCard(s, text: zh)
        #expect(c.used == "11.2" && c.pressureWord == "压力正常" && c.position == 0.34)
        #expect(c.detail == "共 16 GB，交换 1.3 GB")
        s.swapUsedBytes = 0
        #expect(memoryCard(s, text: zh).detail == "共 16 GB")
        #expect(memoryCard(s, text: en).detail == "16 GB total")
    }

    @Test func pressureWithoutPositionDrawsNoDot() {
        // ui-rulings 18: a level and no position says the word only; no level, no scale at all.
        var s = status(pressure: .warning)
        #expect(pressurePosition(s) == nil && memoryCard(s, text: zh).pressureWord == "压力偏高")
        s.memoryPressurePosition = 1.4
        #expect(pressurePosition(s) == 1)
        s.memoryPressure = .unspecified
        #expect(pressurePosition(s) == nil && memoryCard(s, text: zh).pressureWord == nil)
    }

    @Test func diskCardStatesAndLines() {
        let ok = diskCard(status(), text: zh)
        #expect(ok.word == "空间充足" && ok.free == "42" && ok.detail == "共 460 GB" && ok.detail2 == "remote-agent 占 3.1 GB")
        let tight = diskCard(status(availableGB: 1.2, disk: .critical), text: zh)
        #expect(tight.word == "快满了" && tight.detail == "新会话可能启动不了" && tight.detail2 == nil && tight.free == "1.2")
        #expect(diskCard(status(availableGB: 8, disk: .warning), text: zh).word == "空间不多了")
        #expect(abs(ok.agentShare - 3.1 / 460) < 1e-9 && abs(ok.agentShare + ok.otherShare - (1 - 42.0 / 460)) < 1e-9)
        #expect(diskCard(status(), text: en).detail2 == "remote-agent uses 3.1 GB")
        #expect(!showDiskActions(status()) && showDiskActions(status(disk: .warning)) && showDiskActions(status(disk: .critical)))
    }

    @Test func absentItemsAreNotMadeUp() {
        var s = PBMachineStatus(); s.cpuPercent = 5
        #expect(cpuFigures(s, text: zh) == CpuFigures(percent: 5, load: nil, cores: nil))
        #expect(footerParts(s, text: zh).isEmpty)
        s.uptimeSeconds = 3 * 86_400 + 4 * 3600
        s.battery.percent = 81; s.battery.charging = true
        #expect(footerParts(s, text: zh) == ["已开机 3 天 4 小时", "电池 81%，充电中"])
        s.battery.charging = false
        #expect(footerParts(s, text: en) == ["Up 3 d 4 hr", "Battery 81%"])
        s.uptimeSeconds = 90
        #expect(zh.duration(seconds: 90) == "1 分钟")
        #expect(zh.duration(seconds: 2 * 3600 + 120) == "2 小时 2 分")
    }

    @Test func staleAndLookbackTexts() {
        #expect(zh.stale(ageMs: 30_000) == "30 秒前的数据" && zh.stale(ageMs: 120_000) == "2 分钟前的数据" && zh.stale(ageMs: 0) == "1 秒前的数据")
        #expect(zh.lookback(ageMs: 2_900) == "2 秒前" && zh.lookback(ageMs: 3_100) == "4 秒前" && zh.lookback(ageMs: 4_900) == "4 秒前" && zh.lookback(ageMs: 5_100) == "6 秒前")
        #expect(en.stale(ageMs: 1_000) == "Data from 1 sec ago")
    }

    // MARK: Chart

    private func points(_ times: [Int64], _ v: Double = 50) -> [ChartPoint] { times.map { ChartPoint(timeMs: $0, value: v + Double($0 % 7)) } }

    @Test func curveIsPlacedByTimeAndBrokenByGaps() {
        let end: Int64 = 200_000
        // A hole of 12 s ends a run; 10 s does not (ui-rulings 16).
        let broken = chartRuns(points([150_000, 152_000, 164_000, 166_000]), endMs: end, ceiling: 100)
        #expect(broken.count == 2 && broken[0].count == 2 && broken[1].count == 2)
        let joined = chartRuns(points([150_000, 152_000, 162_000, 164_000]), endMs: end, ceiling: 100)
        #expect(joined.count == 1 && joined[0].count == 4)
        // Placement is by time: the newest sample at the right edge, 116 s window.
        let run = chartRuns(points([198_000, 200_000]), endMs: end, ceiling: 100)[0]
        #expect(abs(run[1].x - 1) < 1e-9 && abs(run[0].x - (1 - 2_000.0 / 116_000)) < 1e-9)
        #expect(chartWindowMs == 116_000 && chartGapMs == 10_000)
    }

    @Test func curveIsCutAtTheWindowEdgesAndNeverPadded() {
        let end: Int64 = 300_000, start = end - chartWindowMs
        let cut = chartRuns(points([start - 1_000, start + 1_000, start + 3_000]), endMs: end, ceiling: 100)
        #expect(cut.count == 1 && abs(cut[0][0].x) < 1e-9 && cut[0].count == 3)
        // A point after the right edge is cut by interpolation, not drawn past it.
        let right = chartRuns(points([end - 2_000, end + 1_000]), endMs: end, ceiling: 100)
        #expect(right[0].count == 2 && abs(right[0][1].x - 1) < 1e-9)
        // One sample: nothing to join (the page draws its dot); none: nothing.
        #expect(chartRuns(points([end]), endMs: end, ceiling: 100).first?.count == 1)
        #expect(chartRuns([], endMs: end, ceiling: 100).isEmpty)
        #expect(chartValue(at: end - 1_000, in: points([end - 2_000, end]), gapMs: 10_000) != nil)
        #expect(chartValue(at: end - 20_000, in: points([end - 40_000, end]), gapMs: 10_000) == nil)
    }

    @Test func chartSamplesSinceTheRecoveryPointOnly() {
        var s = PBMachineStatus()
        s.samples = [10, 30, 20].map { t in .with { $0.timeMs = Int64(t) * 1000; $0.cpuPercent = Double(t); $0.memoryUsedBytes = UInt64(t) } }
        #expect(chartPoints(s, metric: .cpu).map(\.timeMs) == [10_000, 20_000, 30_000])
        #expect(chartPoints(s, metric: .memory, sinceMs: 20_000).map(\.value) == [20, 30])
        s.memoryTotalBytes = 0
        #expect(chartCeiling(s, metric: .cpu) == 100 && chartCeiling(s, metric: .memory) == 1)
    }

    @Test func rightEdgeSlidesAtThePaceOfTimeAndGrowsFromTheRight() {
        // First sample: the edge is on it. Then it moves with time, never past the newest, never 2 intervals behind.
        #expect(nextChartEnd(previous: nil, elapsedMs: 0, firstMs: 100_000, latestMs: 100_000) == 100_000)
        #expect(nextChartEnd(previous: 100_000, elapsedMs: 16, firstMs: 90_000, latestMs: 102_000) == 100_016)
        #expect(nextChartEnd(previous: 101_990, elapsedMs: 50, firstMs: 90_000, latestMs: 102_000) == 102_000)
        #expect(nextChartEnd(previous: 90_000, elapsedMs: 1, firstMs: 90_000, latestMs: 110_000) == 106_000)
    }

    @Test func nearestSampleAndSummary() {
        let pts = [ChartPoint(timeMs: 0, value: 10), ChartPoint(timeMs: 2_000, value: 41), ChartPoint(timeMs: 4_000, value: 23)]
        #expect(nearestSample(pts, at: 2_900) == 1 && nearestSample(pts, at: -50) == 0 && nearestSample([], at: 1) == nil)
        #expect(chartSummary(pts, metric: .cpu, text: zh) == "最近 2 分钟，最高 41%，现在 23%")
        #expect(chartSummary([], metric: .cpu, text: zh) == "")
        let mem = [ChartPoint(timeMs: 0, value: Double(8 << 30)), ChartPoint(timeMs: 2_000, value: Double(11 << 30))]
        #expect(chartSummary(mem, metric: .memory, text: en) == "Last 2 minutes, peak 11 GB, now 11 GB")
    }

    // MARK: Ranking

    private func usage(_ id: String, cpu: Double, mb: UInt64, exe: String = "", session: String = "") -> PBResourceUsage {
        .with { $0.id = id; $0.executable = exe; $0.sessionID = session; $0.cpuPercent = cpu; $0.memoryBytes = mb << 20 }
    }

    @Test func rankingRowsAreTheTopEightWithBarsRelativeToTheHead() {
        var s = PBMachineStatus()
        s.sessions = (0..<10).map { usage("s\($0)", cpu: Double(100 - $0 * 10), mb: UInt64(1000 + $0)) }
        let rows = usageRows(s, showSessions: true, metric: .cpu, order: UsageOrder(), text: zh, titleOf: { "T\($0)" }, agentOf: { _ in .claude })
        #expect(rows.count == 8 && rows[0].top && !rows[1].top)
        #expect(rows[0].bar == 1 && abs(rows[1].bar - 0.9) < 1e-9)
        #expect(rows[0].primary == "100%" && rows[0].secondary == "1000 MB")
        #expect(rows[0].title == "Ts0" && rows[0].agent == .claude && rows[0].known)
    }

    @Test func memoryMetricSwapsPrimaryAndSecondary() {
        var s = PBMachineStatus()
        s.sessions = [usage("a", cpu: 141, mb: 2458), usage("b", cpu: 62, mb: 4096)]
        let rows = usageRows(s, showSessions: true, metric: .memory, order: UsageOrder(), text: zh, titleOf: { $0 }, agentOf: { _ in nil })
        #expect(rows.map(\.id) == ["b", "a"] && rows[0].primary == "4.0 GB" && rows[0].secondary == "62%" && rows[1].primary == "2.4 GB")
    }

    @Test func processesNameTheirSessionAndExecutableOnly() {
        var s = PBMachineStatus()
        s.processes = [usage("p1", cpu: 312, mb: 2150, exe: "java", session: "run"), usage("p2", cpu: 41, mb: 1640, exe: "Google Chrome")]
        let rows = usageRows(s, showSessions: false, metric: .cpu, order: UsageOrder(), text: zh, titleOf: { $0 == "run" ? "三端措辞统一" : nil }, agentOf: { _ in .codex })
        #expect(rows[0].title == "java" && rows[0].secondary == "2.1 GB · 三端措辞统一" && rows[0].agent == nil)
        #expect(rows[1].secondary == "1.6 GB" && !rows[1].isSession)
    }

    @Test func aSessionThisClientDoesNotListFallsBackToItsExecutable() {
        // ui-rulings 15: the title is the executable, the row cannot be opened.
        var s = PBMachineStatus()
        s.sessions = [usage("gone", cpu: 5, mb: 10, exe: "claude")]
        let row = usageRows(s, showSessions: true, metric: .cpu, order: UsageOrder(), text: zh, titleOf: { _ in nil }, agentOf: { _ in .claude })[0]
        #expect(row.title == "claude" && !row.known && row.sessionId == "" && row.agent == nil)
    }

    @Test func rowsDoNotTradePlacesWithinTheThreshold() {
        let order = UsageOrder()
        var s = PBMachineStatus()
        s.sessions = [usage("a", cpu: 50, mb: 1), usage("b", cpu: 49, mb: 1)]
        #expect(usageRows(s, showSessions: true, metric: .cpu, order: order, text: zh, titleOf: { $0 }, agentOf: { _ in nil }).map(\.id) == ["a", "b"])
        s.sessions = [usage("a", cpu: 50, mb: 1), usage("b", cpu: 51.5, mb: 1)]
        #expect(usageRows(s, showSessions: true, metric: .cpu, order: order, text: zh, titleOf: { $0 }, agentOf: { _ in nil }).map(\.id) == ["a", "b"])
        s.sessions = [usage("a", cpu: 50, mb: 1), usage("b", cpu: 52.5, mb: 1)]
        #expect(usageRows(s, showSessions: true, metric: .cpu, order: order, text: zh, titleOf: { $0 }, agentOf: { _ in nil }).map(\.id) == ["b", "a"])
    }

    // MARK: Home reminder, page state, cleanup, swap

    private func state(_ machines: [(String, String, Double, Bool)]) -> RemoteState {
        var s = RemoteState()
        for (id, name, gb, online) in machines {
            s.machines[id] = MachineView(id: id, online: online, lastSeenMs: 0, meta: .with {
                $0.hostname = name
                $0.capabilities.machineStatus = true
                $0.diskStatus.availableBytes = UInt64(gb * 1e9)
                $0.diskStatus.totalBytes = 460_000_000_000
            })
        }
        return s
    }

    @Test func reminderTiersAndOrder() {
        let s = state([("a", "MacBook", 8.2, true), ("b", "VPS", 1.2, true), ("c", "Quiet", 30, true), ("d", "Away", 0.5, false)])
        let r = diskReminders(s, text: zh)
        #expect(r.map(\.machineId) == ["b", "a"])
        #expect(r[1].title == "磁盘空间不多了" && r[1].detail == "MacBook 还剩 8.2 GB" && !r[1].critical)
        #expect(r[0].title == "磁盘快满了" && r[0].detail == "VPS 只剩 1.2 GB，会话可能启动不了" && r[0].critical)
        #expect(diskReminders(s, text: en)[1].detail == "MacBook has 8.2 GB left")
        // 10 GB exactly is not under 10 GB; an unsupported daemon makes no reminder.
        #expect(diskReminders(state([("a", "X", 10, true)]), text: zh).isEmpty)
        var off = state([("a", "X", 1, true)]); off.machines["a"]?.meta?.capabilities.machineStatus = false
        #expect(diskReminders(off, text: zh).isEmpty)
    }

    @Test func pageFaceFollowsTheControllerState() {
        let some = PBMachineStatus()
        #expect(statusView(state: .idle, last: nil) == .loading && statusView(state: .loading, last: nil) == .loading)
        #expect(statusView(state: .unavailable, last: nil) == .unavailable)
        #expect(statusView(state: .live, last: some) == .live && statusView(state: .stale, last: some) == .stale)
        #expect(statusView(state: .unavailable, last: some) == .stale)
    }

    @Test func cleanupPromptIsAppendedOnceAfterTheDraft() {
        let prompt = L10n.machineStatusCleanupPrompt(locale: "zh-CN")
        #expect(prompt.hasPrefix("这台电脑磁盘空间不够了。") && prompt.hasSuffix("等我确认。"))
        #expect(cleanupDraft(existing: "", prompt: prompt) == prompt && cleanupDraft(existing: " \n", prompt: prompt) == prompt)
        let drafted = cleanupDraft(existing: "先看 logs\n", prompt: prompt)
        #expect(drafted == "先看 logs\n\n" + prompt)
        #expect(cleanupDraft(existing: drafted, prompt: prompt) == drafted)
    }

    @Test func swapShowsOneSetOfFiguresAtATime() {
        var t = 0.0
        while t <= swapMs {
            #expect(min(swapOldAlpha(t), swapNewAlpha(t)) == 0, "t=\(t)")
            t += 1
        }
        #expect(swapOldAlpha(0) == 1 && swapNewAlpha(0) == 0 && swapOldAlpha(swapMs) == 0 && swapNewAlpha(swapMs) == 1)
        #expect(swapOldAlpha(100) == 0 && swapNewAlpha(230) == 0 && swapNewAlpha(350) == 1)
    }

    @Test func aStartFailedOnAFullDiskHasItsOwnCard() {
        var s = RemoteState()
        s.machines["m"] = MachineView(id: "m", online: true, lastSeenMs: 0, meta: .with { $0.protocolVersion = protocolVersion })
        let c = NewConfig(machineId: "m", cwd: "/p")
        func notices(_ p: NewSessionProgress) -> [NewSessionNotice] {
            newSessionNotices(s, config: c, name: "p", pick: nil, drafting: true, settled: true, start: p)
        }
        #expect(notices(NewSessionProgress(error: "这台电脑磁盘满了", diskFull: true)).contains(.diskFull(machine: "m")))
        // Any other failure keeps its sentence, with the computer's own words after the prefix.
        let failed = notices(NewSessionProgress(error: "boom"))
        #expect(failed.contains(.failed(L10n.newSessionStartFailed(reason: "boom", locale: "zh-CN"))))
        let err = CommandError("disk_full", "mkdir /Users/x: no space left on device")
        #expect(isDiskFull(err) && errorText(err) == "这台电脑磁盘满了")
        #expect(L10n.newSessionStartFailed(reason: errorText(err), locale: "zh-CN") == "启动失败：这台电脑磁盘满了")
    }
}
