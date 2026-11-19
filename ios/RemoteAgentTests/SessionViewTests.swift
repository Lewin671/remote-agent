#if DEBUG
import Foundation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit

@testable import RemoteAgent

private struct ComposerWrapper: View {
    let sessionID: String
    let vm: PBSessionViewModel
    @FocusState private var focused: Bool

    var body: some View {
        SessionComposer(
            sessionID: sessionID,
            header: vm.header,
            composer: vm.composer,
            actions: vm.actions,
            focus: $focused
        )
    }
}

@MainActor
@Suite struct SessionViewTests {
    nonisolated private static let goldenNames = [
        "session_long_window",
        "session_approval_flow",
        "session_question_plan",
        "session_queue_retry",
        "session_scheduled_fold",
        "session_cli_error_turn",
    ]

    private func render<V: View>(_ view: V, width: CGFloat = 390) {
        let host = UIHostingController(rootView: view)
        let size = host.sizeThatFits(in: CGSize(width: width, height: 1200))
        #expect(size.width <= width + 0.5)
        host.view.frame = CGRect(x: 0, y: 0, width: width, height: 844)
        host.view.setNeedsLayout()
        host.view.layoutIfNeeded()
    }

    @Test(arguments: SessionViewTests.goldenNames)
    func goldenSessionViewModelsRenderAllPanelsCleanly(name: String) async {
        let steps = ScreenGoldens.sessions(name)
        #expect(!steps.isEmpty)

        let remote = Demo.remote()
        let settings = SettingsStore(defaults: UserDefaults(suiteName: "session-golden-" + UUID().uuidString)!)
        let model = AppModel(settings: settings)
        let state = Demo.state(.everyday, now: 1_791_000_000_000)
        model.showDemo(account: Demo.account, remote: remote, initial: state, events: [:]) {}
        defer { ImageCenter.remove(remote) }
        guard let context = model.context else { Issue.record("missing context"); return }
        let nav = Navigator()

        for vm in steps {
            context.core.sessions[vm.sessionID] = vm

            render(
                ChatPane(
                    sessionID: vm.sessionID,
                    vm: vm,
                    onTerminal: {},
                    onLoadEarlier: {},
                    onToggleGroup: { _ in }
                )
                .environment(model)
                .environment(context)
                .environment(nav)
            )

            render(
                ComposerWrapper(sessionID: vm.sessionID, vm: vm)
                    .environment(model)
                    .environment(context)
                    .environment(nav)
            )

            if vm.hasDock, vm.dock.hasItem_p {
                switch vm.dock.kind {
                case .approval:
                    render(
                        ApprovalDock(
                            sessionID: vm.sessionID,
                            machineID: vm.header.machineID,
                            dock: vm.dock,
                            open: .constant(true),
                            onOpen: {}
                        )
                        .environment(model)
                        .environment(context)
                        .environment(nav)
                    )
                    render(
                        ApprovalScreen(session: vm.sessionID, approval: vm.dock.approval.approvalID)
                            .environment(model)
                            .environment(context)
                            .environment(nav)
                    )
                case .question:
                    render(
                        QuestionDock(
                            sessionID: vm.sessionID,
                            machineID: vm.header.machineID,
                            dock: vm.dock,
                            open: .constant(true),
                            onOpen: {},
                            onTerminal: {},
                            onLong: {}
                        )
                        .environment(model)
                        .environment(context)
                        .environment(nav)
                    )
                    render(
                        QuestionScreen(session: vm.sessionID, question: vm.dock.question.questionID)
                            .environment(model)
                            .environment(context)
                            .environment(nav)
                    )
                case .plan:
                    render(
                        PlanDock(
                            sessionID: vm.sessionID,
                            machineID: vm.header.machineID,
                            dock: vm.dock,
                            open: .constant(true),
                            onOpen: {},
                            onFull: {}
                        )
                        .environment(model)
                        .environment(context)
                        .environment(nav)
                    )
                    render(
                        PlanScreen(session: vm.sessionID)
                            .environment(model)
                            .environment(context)
                            .environment(nav)
                    )
                case .terminalPopup:
                    render(
                        PopupDock(
                            dock: vm.dock,
                            open: .constant(true),
                            onOpen: {},
                            onTerminal: {}
                        )
                        .environment(model)
                        .environment(context)
                        .environment(nav)
                    )
                case .unspecified, .UNRECOGNIZED:
                    break
                }
            }

            if !vm.actions.modeChoices.isEmpty {
                render(
                    SessionModeSheet(sessionID: vm.sessionID, onClose: {})
                        .environment(model)
                        .environment(context)
                        .environment(nav)
                )
            }
            if !vm.actions.modelChoices.isEmpty {
                render(
                    SessionModelSheet(sessionID: vm.sessionID, onClose: {})
                        .environment(model)
                        .environment(context)
                        .environment(nav)
                )
            }
        }
        await model.logout()
    }

    @Test func goldenSessionContentsCaptureWindowDockRetryFoldAndErrorStates() {
        let longWin = ScreenGoldens.session("session_long_window")
        #expect(longWin.timeline.totalItems > Int32(longWin.timeline.items.count))
        #expect(longWin.timeline.canLoadEarlier || longWin.timeline.hasEarlier_p)

        let approvals = ScreenGoldens.sessions("session_approval_flow")
        #expect(approvals.contains { $0.dock.kind == .approval })

        let qp = ScreenGoldens.sessions("session_question_plan")
        #expect(qp.contains { $0.dock.kind == .question })
        #expect(qp.contains { $0.dock.kind == .plan })

        let queueRetry = ScreenGoldens.sessions("session_queue_retry")
        #expect(queueRetry.contains { $0.composer.pendingTray.totalCount > 0 })

        let folded = ScreenGoldens.sessions("session_scheduled_fold")
        #expect(folded.contains { vm in
            vm.timeline.items.contains {
                if case .quietScheduledGroup = $0.kind { return true }
                return false
            }
        })

        let err = ScreenGoldens.sessions("session_cli_error_turn")
        #expect(err.contains { vm in
            vm.timeline.items.contains {
                if case .errorLine = $0.kind { return true }
                return false
            }
        })
    }

    @Test func refCountedSessionWatchesMergeWindowsAndCoreSessionCommandsDispatch() async throws {
        let remote = Demo.remote()
        let state = Demo.state(.everyday, now: 1_791_000_000_000)
        let defaults = UserDefaults(suiteName: "session-watch-" + UUID().uuidString)!
        let model = AppModel(settings: SettingsStore(defaults: defaults))
        model.showDemo(account: Demo.account, remote: remote, initial: state, events: Demo.events(.everyday, now: 1_791_000_000_000)) {}
        defer { ImageCenter.remove(remote) }
        let context = try #require(model.context)
        let sid = try #require(state.sessions.keys.sorted().first)

        // Two simultaneous watchers (e.g. SessionScreen + ScheduleResultCard) share one Core watch
        // and merge limit / expanded groups / revealRunID cleanly.
        let t1 = await context.coreHost.acquireSessionWatch(sid, initialWindow: .with {
            $0.limit = 80
            $0.expandedGroupIds = ["g1"]
        })
        let t2 = await context.coreHost.acquireSessionWatch(sid, initialWindow: .with {
            $0.limit = 160
            $0.revealRunID = "run-9"
            $0.expandedGroupIds = ["g2"]
        })
        await eventually("core renders initial watched session") {
            context.core.sessions[sid] != nil
        }

        await context.coreHost.openSession(sid, refreshModels: false)
        await context.coreHost.loadEarlierSessionHistory(sid)
        await context.coreHost.setSessionQuestionIndex(sessionID: sid, questionID: "q1", questionIndex: 1)

        // Release first watcher: second still keeps the session live.
        await context.coreHost.releaseSessionWatch(t1)
        #expect(context.core.sessions[sid] != nil)

        // Dispatch a real message via CoreHost in demo mode and verify it settles without crashing.
        await context.coreHost.submitSessionCommand(
            sessionID: sid,
            command: .with {
                $0.send = .with {
                    $0.sessionID = sid
                    $0.requestID = "req-1"
                    $0.text = "ping"
                    $0.mode = .steer
                }
            },
            machineID: state.sessions[sid]?.machineId ?? ""
        )

        await context.coreHost.releaseSessionWatch(t2)
        await model.logout()
    }
}
#endif
