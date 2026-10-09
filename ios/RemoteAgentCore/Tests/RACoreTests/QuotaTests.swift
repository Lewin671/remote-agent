import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's core QuotaTest.
@Suite struct QuotaTests {
    private let now: Int64 = 1_790_000_000_000

    private func machine(_ online: Bool, _ q: PBAgentQuota...) -> MachineView {
        MachineView(
            id: "m", online: online, lastSeenMs: 0,
            meta: .with {
                $0.agents = [.with { $0.agent = .codex }, .with { $0.agent = .claude }]
                $0.quotas = q
            })
    }

    private func w(_ name: String, _ pct: Double?, resetIn: Int64 = 3_600_000, minutes: Int64 = 0) -> PBQuotaWindow {
        .with {
            $0.name = name
            $0.resetsAtMs = now + resetIn
            $0.windowMinutes = minutes
            if let pct { $0.usedPercent = pct }
        }
    }

    private func quota(_ agent: PBAgent = .claude, _ state: PBQuotaState = .ok, _ windows: [PBQuotaWindow] = [], _ more: (inout PBAgentQuota) -> Void = { _ in }) -> PBAgentQuota {
        .with {
            $0.agent = agent
            $0.state = state
            $0.windows = windows
            more(&$0)
        }
    }

    private func claude(_ m: MachineView, at: Int64? = nil) throws -> QuotaGroup {
        let gs = quotaGroups(m, now: at ?? now).filter { $0.agent == .claude }
        try #require(gs.count == 1)
        return gs[0]
    }

    @Test func timesUseBeijingWhereverTheClientIs() throws {
        let now = Mk.instant("2026-10-02T15:30:00Z")
        let window = PBQuotaWindow.with { $0.name = "seven_day"; $0.usedPercent = 95.0; $0.resetsAtMs = Mk.instant("2026-10-02T16:10:00Z") }
        let g = try claude(machine(true, quota(.claude, .ok, [window]) { $0.observedMs = now - 60_000 }), at: now)
        // Still Friday in Beijing, so the reset after midnight is not 今天.
        #expect(g.rows.count == 1 && g.rows[0].reset == "40 分钟后重置 · 10月3日 周六 0:10")
        #expect(g.received == "最近收到 23:29")
    }

    @Test func resetSaysHowLongIsLeftThenTheMoment() {
        // 9:41 in Beijing on Friday 2 October 2026.
        let now = Mk.instant("2026-10-02T01:41:00Z")
        func at(_ utc: String) -> String { resetText(resetMs: Mk.instant(utc), now: now) }
        #expect(at("2026-10-02T05:20:00Z") == "3 小时 39 分后重置 · 今天 13:20")
        #expect(at("2026-10-08T06:10:00Z") == "6 天 4 小时后重置 · 10月8日 周四 14:10")
        // Rounded down, and a zero part is still written.
        #expect(at("2026-10-08T01:41:59Z") == "6 天 0 小时后重置 · 10月8日 周四 9:41")
        #expect(at("2026-10-02T04:41:30Z") == "3 小时 0 分后重置 · 今天 12:41")
        #expect(at("2026-10-03T01:40:00Z") == "23 小时 59 分后重置 · 10月3日 周六 9:40")
        #expect(at("2026-10-03T01:41:00Z") == "1 天 0 小时后重置 · 10月3日 周六 9:41")
        #expect(at("2026-10-02T02:41:00Z") == "1 小时 0 分后重置 · 今天 10:41")
        #expect(at("2026-10-02T02:20:00Z") == "39 分钟后重置 · 今天 10:20")
        #expect(at("2026-10-02T01:42:00Z") == "1 分钟后重置 · 今天 9:42")
        #expect(at("2026-10-02T01:41:59Z") == "即将重置 · 今天 9:41")
        // The hour is not padded; the minute is.
        #expect(at("2026-10-02T17:05:00Z") == "15 小时 24 分后重置 · 10月3日 周六 1:05")
    }

    @Test func claudeCodesQuotaIsShownWhenTheDaemonReadsIt() throws {
        let m = machine(true, quota(.claude, .ok, [w("five_hour", 92.0), w("seven_day", nil)]) { $0.plan = "claude_max"; $0.observedMs = now - 60_000 })
        let g = try claude(m)
        #expect(g.title == "Claude Code · Max")
        #expect(quotaBanner(g) == "5 小时额度即将用尽")
        #expect(g.status == .ok)
        #expect(g.rows[0].used == "已用 92%")
        #expect(g.rows[0].tone == .warn)
        // A window without a number is unknown, never 0%.
        #expect(g.rows[1].used == "未获取")
        #expect(g.rows[1].percent == nil)
        // Codex has no entry: this daemon does not read it.
        #expect(quotaGroups(m, now: now).filter { $0.agent == .codex }.map(\.status) == [.unsupported])
    }

    @Test func waitingFailedAndOfflineAreSaidAsSuch() throws {
        #expect(quotaLine(try claude(machine(true, quota(.claude, .waiting)))) == "暂未获取额度")
        let failed = quota(.claude, .failed, [w("five_hour", 28.0)]) { $0.detail = "状态栏输入读不出来"; $0.observedMs = now - 3_600_000 }
        let g = try claude(machine(true, failed))
        #expect(g.lastRecord)
        #expect(g.rows[0].used == "已用 28%")
        #expect(g.rows[0].tone == .stale)
        #expect(quotaLine(g) == "5 小时 已用 28%（上次记录）")
        #expect(g.received.hasPrefix("最近成功收到"))
        #expect(try claude(machine(false, quota(.claude, .ok, [w("five_hour", 28.0)]))).rows[0].tone == .stale)
    }

    @Test func aPassedResetOnlyWaitsForAnUpdate() throws {
        let r = try claude(machine(true, quota(.claude, .ok, [w("five_hour", 100.0, resetIn: -60_000)]))).rows[0]
        #expect(r.used == "等待更新")
        #expect(r.note.contains("不能据此确认额度已经恢复"))
    }

    @Test func accountsThatCannotBeToldApartStayApart() {
        let a = quota(.claude, .ok, [w("five_hour", 10.0)]) { $0.sourceSessionID = "aaaa1111bbbb" }
        let b = quota(.claude, .ok, [w("five_hour", 50.0)]) { $0.account = "f00dbeef99" }
        let gs = quotaGroups(machine(true, a, b), now: now).filter { $0.agent == .claude }
        #expect(gs.map(\.title) == ["Claude Code · 会话 bbbb", "Claude Code · 账户 f00dbeef99"])
    }

    @Test func absentWindowsAreLeftOutAndReportedDurationsAreKept() throws {
        for (agent, name) in [(PBAgent.claude, "five_hour"), (.codex, "primary")] {
            let gs = quotaGroups(machine(true, quota(agent, .ok, [w(name, 99.6, minutes: 120)])), now: now).filter { $0.agent == agent }
            let g = try #require(gs.first)
            #expect(g.rows[0].label == "2 小时")
            #expect(g.rows[0].used == "已用 99%")
            #expect(g.rows[0].tone == .warn)
            #expect(quotaBanner(g) == "2 小时额度即将用尽")
            // A window the account does not have is not shown as missing.
            #expect(g.rows.count == 1)
        }
        #expect(planName("prolite") == "Pro Lite")
        #expect(planName("Team Plus") == "Team Plus")
    }

    // --- not in Android's tests ---

    @Test func labelsAndPlanNamesFollowTheJvm() throws {
        #expect(windowLabel(minutes: 30) == "30 分钟")
        // Math.round: a half goes up.
        #expect(windowLabel(minutes: 90) == "2 小时")
        #expect(windowLabel(minutes: 89) == "1 小时")
        #expect(windowLabel(minutes: 2880) == "2 天")
        #expect(windowLabel(minutes: 1500) == "25 小时")
        #expect(planName("CLAUDE_team_plus") == "Team Plus")
        #expect(planName("claude_MAX") == "Max")
        #expect(planName("business  plan_x") == "Business Plan X")
        // A number that is not one is no reason to stop: Kotlin's toInt gives 0.
        let odd = PBQuotaWindow.with { $0.name = "primary"; $0.usedPercent = .nan }
        #expect(quotaRow(odd, now: now, lastRecord: false).used == "已用 0%")
        // The usual windows first, whatever order they came in; others after.
        let g = try claude(machine(true, quota(.claude, .ok, [w("opus", 1), w("seven_day", 2), w("five_hour", 3)])))
        #expect(g.rows.map(\.label) == ["5 小时", "7 天", "opus"])
        #expect(quotaLine(g) == "5 小时 已用 3% · 7 天 已用 2% · opus 已用 1%")
    }
}
