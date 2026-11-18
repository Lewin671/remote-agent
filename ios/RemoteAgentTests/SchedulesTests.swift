import Observation
import Foundation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit
@testable import RemoteAgent

@MainActor @Suite struct SchedulesTests {
    @Test func goldenViewModelsRenderSchedulesInboxDetailFormAndStrip() throws {
        let inbox = ScreenGoldens.schedules("schedules_inbox_detail", index: 0)
        #expect(!inbox.headline.title.rendered().isEmpty)
        render(
            SchedulesContent(vm: inbox, connectionBar: { EmptyView() })
                .environment(Navigator())
        )

        let details = ScreenGoldens.scheduleDetails("schedules_inbox_detail")
        #expect(!details.isEmpty)
        for vm in details {
            render(ScheduleDetailContent(vm: vm, connectionBar: { EmptyView() }))
        }

        let forms = ScreenGoldens.scheduleForms("schedules_form_lifecycle")
        #expect(!forms.isEmpty)
        for form in forms {
            render(NewScheduleContent(vm: form))
            if form.hasTiming {
                render(
                    ScheduleTimingContent(
                        draft: form.timing,
                        mutation: form.mutation,
                        enabled: form.editable,
                        onUpdate: { _ in },
                        onApply: { _ in },
                        onCancel: {}
                    )
                )
            }
        }

        let strips = ScreenGoldens.scheduleStrips("schedules_form_lifecycle")
        for strip in strips {
            render(SessionScheduleStripContent(vm: strip) { _ in })
        }
    }

    @Test func coreHostSchedulesFlowsWithDemoSeedAndSeenScoping() async throws {
        let defaults = UserDefaults(suiteName: "schedules-host-" + UUID().uuidString)!
        let model = AppModel(settings: SettingsStore(defaults: defaults))
        let remote = Demo.remote()
        let now: Int64 = 1_791_000_000_000
        let state = Demo.state(.everyday, now: now)
        let samples = Demo.schedules(.everyday, now: now)

        model.showDemo(
            account: Demo.account,
            remote: remote,
            initial: state,
            events: Demo.events(.everyday, now: now),
            schedules: samples.schedules,
            scheduleSeen: samples.seen
        ) {
            await remote.showForTest(state: state, modelLists: Demo.models)
        }
        defer { ImageCenter.remove(remote) }
        let context = try #require(model.context)

        await eventually("schedules list renders from demo seed") {
            !context.core.schedules.fresh.isEmpty && !context.core.schedules.attention.isEmpty
        }

        await context.coreHost.openScheduleDetail(machineID: "mac", taskID: "ci")
        await eventually("detail view renders selected task") {
            context.core.scheduleDetail.found && context.core.scheduleDetail.taskID == "ci"
        }

        await context.coreHost.openScheduleForm(mode: .createNew)
        await eventually("create schedule form opens") {
            context.core.scheduleForm.open && !context.core.scheduleForm.formKey.isEmpty
        }

        await context.coreHost.closeScheduleDetail(machineID: "mac", taskID: "ci")
    }

    @Test func seenStoreScopesKeysAcrossAccountScopeAndMachine() {
        let defaults = UserDefaults(suiteName: "schedules-seen-" + UUID().uuidString)!
        let a = ScopeDefaults(defaults, scope: ["https://s1.example", "dev-1"])
        let b = ScopeDefaults(defaults, scope: ["https://s2.example", "dev-1"])
        let tok: [PBScheduleSeenToken] = [.with { $0.taskID = "t1"; $0.runID = "r1" }]
        a.saveScheduleSeen(machineID: "m1", tokens: tok)

        #expect(a.loadScheduleSeen(machineID: "m1") == tok)
        #expect(b.loadScheduleSeen(machineID: "m1") == nil)
        #expect(a.loadScheduleSeen(machineID: "m2") == nil)

        a.deleteScheduleSeen(machineID: "m1")
        #expect(a.loadScheduleSeen(machineID: "m1") == nil)
    }

    @Test func tabBadgeCapsCountsAndWholeBlocksFitWithoutClipping() {
        // Both tabs write 99+ past 99 (design-system §6 「底部」); VoiceOver hears the whole count.
        let conversation = TabBadge(count: 100)
        let schedules = TabBadge(count: 100, unit: .tasks)
        let newResult = TabBadge(dot: true, unit: .tasks)
        #expect(conversation.text == "99+" && conversation.spoken == L10n.homeIosBadgeSessions(count: 100))
        #expect(schedules.text == "99+" && schedules.spoken == L10n.homeIosBadgeTasks(count: 100))
        #expect(newResult.spoken == L10n.homeIosNewResults() && TabBadge().spoken.isEmpty)
        #expect(TabBadge(count: 99).text == "99")
        #expect(scheduleResultBlockCount([24, 48, 96], budget: 192) == 2)
        #expect(scheduleResultBlockCount([240], budget: 192) == 1)
        #expect(scheduleResultBlockCount([193, 193], budget: 192) == 1) // Still show the first block before measurement.
        #expect(scheduleResultBlockCount([24, 48], budget: 192) == 2)
        #expect(scheduleResultBlockCount([], budget: 192) == 0)
    }

    @Test func aLongDescriptionKeepsHistoryWithinTheFirstScreen() async throws {
        await WindowTestGate.shared.acquire(); defer { WindowTestGate.shared.release() }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let text = (1...40).map { "\($0). 检查构建并保留原文换行。" }.joined(separator: "\n")
        let host = UIHostingController(rootView: ScheduleMessageText(text: text).environment(\.dynamicTypeSize, .large))
        host.safeAreaRegions = []
        let window = UIWindow(windowScene: scene); window.frame = CGRect(x: 0, y: 0, width: 320, height: 600)
        window.rootViewController = host; window.isHidden = false; defer { window.isHidden = true }
        for _ in 0..<30 { host.view.layoutIfNeeded(); try await Task.sleep(for: .milliseconds(10)) }
        let size = host.sizeThatFits(in: CGSize(width: 288, height: 4000))
        #expect(size.height < 230, "six lines and the disclosure must not lay out all 40 lines")
        #expect(size.height > 100, "the preview must keep the text visible")
    }

    @Test func cliIsANativeInlineSegmentAndDisappearsWithOnlyOneInstalledAgent() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        for agents in [[PBAgent.codex, .claude, .antigravity], [PBAgent.codex, .claude], [.claude]] {
            var pickedAgent: PBAgent = .claude
            let options = agents.map { ag in
                PBCoreAgentOptionView.with {
                    $0.agent = ag
                    $0.displayName = agentName(ag)
                    $0.selected = ag == .claude
                }
            }
            let row = ScheduleAgentRow(availableAgents: options, selected: .claude) { pickedAgent = $0 }
            let host = UIHostingController(rootView: row)
            let window = UIWindow(windowScene: scene); window.frame = CGRect(x: 0, y: 0, width: 320, height: 200)
            window.rootViewController = host; window.isHidden = false
            func segments(_ view: UIView) -> [UISegmentedControl] { (view as? UISegmentedControl).map { [$0] } ?? view.subviews.flatMap(segments) }
            for _ in 0..<30 where agents.count > 1 && segments(host.view).isEmpty { try await Task.sleep(for: .milliseconds(10)) }
            host.view.layoutIfNeeded()
            let controls = segments(host.view)
            #expect(controls.count == (agents.count > 1 ? 1 : 0))
            if let control = controls.first {
                #expect(control.numberOfSegments == agents.count)
                #expect(control.titleForSegment(at: 0) == "Codex" && control.titleForSegment(at: 1) == "Claude Code")
                if agents.count > 2 { #expect(control.titleForSegment(at: 2) == "Antigravity") }
                #expect(control.selectedSegmentIndex == 1)
                #expect(control.selectedSegmentTintColor?.resolvedColor(with: control.traitCollection) == UIColor(DS.raised).resolvedColor(with: control.traitCollection))
                let normal = control.titleTextAttributes(for: .normal)?[.foregroundColor] as? UIColor
                let selected = control.titleTextAttributes(for: .selected)?[.foregroundColor] as? UIColor
                #expect(normal?.resolvedColor(with: control.traitCollection) == UIColor(DS.ink).resolvedColor(with: control.traitCollection))
                #expect(selected?.resolvedColor(with: control.traitCollection) == UIColor(DS.ink).resolvedColor(with: control.traitCollection))
                control.selectedSegmentIndex = 0; control.sendActions(for: .valueChanged)
                for _ in 0..<20 where pickedAgent != .codex { try await Task.sleep(for: .milliseconds(10)) }
                #expect(pickedAgent == .codex)
                if agents.count == 2 {
                    let swapped = [PBAgent.codex, .antigravity].map { ag in
                        PBCoreAgentOptionView.with {
                            $0.agent = ag
                            $0.displayName = agentName(ag)
                        }
                    }
                    host.rootView = ScheduleAgentRow(availableAgents: swapped, selected: .codex) { pickedAgent = $0 }
                    await eventually("updated installed CLI names") { control.titleForSegment(at: 1) == "Antigravity" }
                    control.selectedSegmentIndex = 1; control.sendActions(for: .valueChanged)
                    await eventually("selects the replacement CLI") { pickedAgent == .antigravity }
                }
            }
            window.isHidden = true
        }
    }

    private func render<V: View>(_ view: V) {
        let host = UIHostingController(rootView: view)
        host.view.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
    }
}

@MainActor @Observable private final class ScheduledJumpProbe {
    var loaded = false
    var jump = SessionStore.ScheduledJump(runID: "run", seq: 5)
    var attempts = 0
}

@MainActor @Suite struct ScheduleJumpObserverTests {
    @Test func retriesWhenLoadingFinishesWithoutChangingAnyRowID() async throws {
        await WindowTestGate.shared.acquire(); defer { WindowTestGate.shared.release() }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let probe = ScheduledJumpProbe()
        struct Host: View {
            let probe: ScheduledJumpProbe
            var body: some View {
                Text("history").modifier(
                    ScheduledJumpObserver(rowIDs: ["e5"], loaded: probe.loaded, jump: probe.jump, anchorID: "") {
                        probe.attempts += 1
                    }
                )
            }
        }
        let host = UIHostingController(rootView: Host(probe: probe))
        let window = UIWindow(windowScene: scene); window.frame = CGRect(x: 0, y: 0, width: 320, height: 200); window.rootViewController = host; window.isHidden = false
        defer { window.isHidden = true }
        await eventually("initial jump observation") { probe.attempts == 1 }
        probe.loaded = true
        await eventually("loaded history retries jump") { probe.attempts == 2 }
        probe.jump = .init(runID: "another", seq: 8)
        await eventually("another requested run") { probe.attempts == 3 }
        #expect(probe.attempts == 3)
    }
}
