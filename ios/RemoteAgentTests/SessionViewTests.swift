#if DEBUG
import Foundation
import os
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
        let world = Demo.world(.everyday, now: 1_791_000_000_000)
        model.showDemo(account: Demo.account, remote: remote, world: world, events: [:])
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
        let world = Demo.world(.everyday, now: 1_791_000_000_000)
        let defaults = UserDefaults(suiteName: "session-watch-" + UUID().uuidString)!
        let model = AppModel(settings: SettingsStore(defaults: defaults))
        model.showDemo(account: Demo.account, remote: remote, world: world, events: Demo.events(.everyday, now: 1_791_000_000_000))
        defer { ImageCenter.remove(remote) }
        let context = try #require(model.context)
        let sid = try #require(world.sessions.keys.sorted().first)

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

        // Dispatch semantic actions via CoreHost in demo mode and assert the exact Core events emitted.
        let captured = OSAllocatedUnfairLock(initialState: [PBCoreEvent]())
        await context.coreHost.setDebugEventObserver { ev in
            captured.withLock { $0.append(ev) }
        }
        defer { Task { await context.coreHost.setDebugEventObserver(nil) } }

        await context.coreHost.setDraftText(scope: .session(sid), text: "ping")
        await context.coreHost.sendSessionDraft(sessionID: sid, mode: .steer)
        await context.coreHost.renameSession(sessionID: sid, title: "Renamed in test")
        await context.coreHost.setApprovalMode(sessionID: sid, mode: .auto)
        await context.coreHost.sessionSimpleCommand(sessionID: sid, kind: .interrupt)

        let events = captured.withLock { $0 }
        #expect(events.contains {
            if case .sessionRename(let r)? = $0.event { return r.sessionID == sid && r.title == "Renamed in test" }
            return false
        })
        #expect(events.contains {
            if case .sessionSetApprovalMode(let m)? = $0.event { return m.sessionID == sid && m.mode == .auto }
            return false
        })
        #expect(events.contains {
            if case .sessionSimpleCommand(let s)? = $0.event { return s.sessionID == sid && s.kind == .interrupt }
            return false
        })

        await context.coreHost.releaseSessionWatch(t2)
        await model.logout()
    }

    @Test func semanticActionsDispatchExactCoreEventsAndFields() async throws {
        let remote = Demo.remote()
        let defaults = UserDefaults(suiteName: "session-semantic-dispatch-" + UUID().uuidString)!
        let model = AppModel(settings: SettingsStore(defaults: defaults))
        model.showDemo(account: Demo.account, remote: remote, world: Demo.world(.everyday, now: 1_791_000_000_000), events: [:])
        defer { ImageCenter.remove(remote) }
        let context = try #require(model.context)
        let host = context.coreHost

        let captured = OSAllocatedUnfairLock(initialState: [PBCoreEvent]())
        await host.setDebugEventObserver { ev in
            captured.withLock { $0.append(ev) }
        }

        await host.editApprovalDeny(sessionID: "s1", approvalID: "a1", denyingOpen: true)
        await host.resolveApproval(sessionID: "s1", approvalID: "a1", allow: false, always: false, denyReason: "no thanks")
        await host.toggleQuestionOption(sessionID: "s1", questionID: "q1", optionNum: 2)
        await host.answerQuestion(sessionID: "s1", questionID: "q1", optionNum: 1)
        await host.answerQuestion(sessionID: "s1", questionID: "q2", optionNums: [1, 3])
        await host.answerPlan(sessionID: "s1", choice: .clearAndImplement)
        await host.sessionSimpleCommand(sessionID: "s1", kind: .compact)
        await host.renameSession(sessionID: "s1", title: "New Title")
        await host.setApprovalMode(sessionID: "s1", mode: .fullAccess)
        await host.setSessionModel(sessionID: "s1", model: "claude-sonnet-5-5", effort: "high")
        await host.retrySessionCommand(sessionID: "s1", slotKey: "question:s1:q1#0")
        await host.homeEditApprovalDeny(sessionID: "s2", approvalID: "a2", denyingOpen: true)
        await host.homeResolveApproval(sessionID: "s2", approvalID: "a2", allow: true, always: true, denyReason: "")
        await host.homeAnswerQuestion(sessionID: "s2", questionID: "q3", optionNum: 2)
        await host.homeRetryAttention(sessionID: "s2", itemID: "q3")
        await host.homeAbandonAttention(sessionID: "s2", itemID: "a2")

        // ApprovalTarget routes session vs home actions without duplicating call-site branches.
        let sessionDock = PBSessionDockView.with {
            $0.hasItem_p = true
            $0.kind = .approval
            $0.approval = .with { $0.approvalID = "a-sess" }
            $0.command = .with { $0.slotKey = "approval:s3:a-sess" }
        }
        let sessionTarget = ApprovalTarget(sessionId: "s3", dock: sessionDock)
        await sessionTarget.editDeny(host: host, open: true)
        await sessionTarget.resolve(host: host, allow: true, always: false, denyReason: "")
        await sessionTarget.retry(host: host)
        await sessionTarget.abandon(host: host)

        let homeCard = PBCoreAttentionCardView.with {
            $0.sessionID = "s4"
            $0.approvalID = "a-home"
        }
        let homeTarget = ApprovalTarget(card: homeCard)
        await homeTarget.editDeny(host: host, open: false)
        await homeTarget.resolve(host: host, allow: false, always: false, denyReason: "stop")
        await homeTarget.retry(host: host)
        await homeTarget.abandon(host: host)

        await host.setDebugEventObserver(nil)
        let evs = captured.withLock { $0 }

        #expect(evs.contains {
            if case .sessionEditApprovalDeny(let a)? = $0.event { return a.sessionID == "s1" && a.approvalID == "a1" && a.denyingOpen }
            return false
        })
        #expect(evs.contains {
            if case .sessionResolveApproval(let a)? = $0.event {
                return a.sessionID == "s1" && a.approvalID == "a1" && !a.allow && !a.always && a.denyReason == "no thanks"
            }
            return false
        })
        #expect(evs.contains {
            if case .sessionToggleQuestionOption(let q)? = $0.event { return q.sessionID == "s1" && q.questionID == "q1" && q.optionNum == 2 }
            return false
        })
        #expect(evs.contains {
            if case .sessionAnswerQuestion(let q)? = $0.event { return q.sessionID == "s1" && q.questionID == "q1" && q.optionNum == 1 && q.optionNums.isEmpty }
            return false
        })
        #expect(evs.contains {
            if case .sessionAnswerQuestion(let q)? = $0.event { return q.sessionID == "s1" && q.questionID == "q2" && q.optionNum == 0 && q.optionNums == [1, 3] }
            return false
        })
        #expect(evs.contains {
            if case .sessionAnswerPlan(let p)? = $0.event { return p.sessionID == "s1" && p.choice == .clearAndImplement }
            return false
        })
        #expect(evs.contains {
            if case .sessionSimpleCommand(let s)? = $0.event { return s.sessionID == "s1" && s.kind == .compact }
            return false
        })
        #expect(evs.contains {
            if case .sessionRename(let r)? = $0.event { return r.sessionID == "s1" && r.title == "New Title" }
            return false
        })
        #expect(evs.contains {
            if case .sessionSetApprovalMode(let m)? = $0.event { return m.sessionID == "s1" && m.mode == .fullAccess }
            return false
        })
        #expect(evs.contains {
            if case .sessionSetModel(let m)? = $0.event { return m.sessionID == "s1" && m.model == "claude-sonnet-5-5" && m.effort == "high" }
            return false
        })
        #expect(evs.contains {
            if case .sessionRetryCommand(let r)? = $0.event { return r.sessionID == "s1" && r.slotKey == "question:s1:q1#0" }
            return false
        })
        #expect(evs.contains {
            if case .homeEditApprovalDeny(let h)? = $0.event { return h.sessionID == "s2" && h.approvalID == "a2" && h.denyingOpen }
            return false
        })
        #expect(evs.contains {
            if case .homeResolveApproval(let h)? = $0.event { return h.sessionID == "s2" && h.approvalID == "a2" && h.allow && h.always }
            return false
        })
        #expect(evs.contains {
            if case .homeAnswerQuestion(let h)? = $0.event { return h.sessionID == "s2" && h.questionID == "q3" && h.optionNum == 2 }
            return false
        })
        #expect(evs.contains {
            if case .homeRetryAttention(let h)? = $0.event { return h.sessionID == "s2" && h.itemID == "q3" }
            return false
        })
        #expect(evs.contains {
            if case .homeAbandonAttention(let h)? = $0.event { return h.sessionID == "s2" && h.itemID == "a2" }
            return false
        })
        // ApprovalTarget session vs home routing
        #expect(evs.contains {
            if case .sessionRetryCommand(let r)? = $0.event { return r.sessionID == "s3" && r.slotKey == "approval:s3:a-sess" }
            return false
        })
        #expect(evs.contains {
            if case .sessionAbandonCommand(let a)? = $0.event { return a.sessionID == "s3" && a.slotKey == "approval:s3:a-sess" }
            return false
        })
        #expect(evs.contains {
            if case .homeResolveApproval(let h)? = $0.event { return h.sessionID == "s4" && h.approvalID == "a-home" && !h.allow && h.denyReason == "stop" }
            return false
        })
        #expect(evs.contains {
            if case .homeRetryAttention(let h)? = $0.event { return h.sessionID == "s4" && h.itemID == "a-home" }
            return false
        })
        #expect(evs.contains {
            if case .homeAbandonAttention(let h)? = $0.event { return h.sessionID == "s4" && h.itemID == "a-home" }
            return false
        })

        await model.logout()
    }

    @Test func homeFilteredAttentionRowsExcludesActiveTopCardOrQuestion() {
        let rows = ["s1", "s2", "s3"].map { sid in
            PBCoreAttentionRowView.with { $0.sessionID = sid }
        }
        struct Case {
            let name: String
            let home: PBCoreHomeViewModel
            let expected: [String]
        }
        let cases: [Case] = [
            Case(
                name: "no top card keeps all attention rows",
                home: .with { $0.attention = rows },
                expected: ["s1", "s2", "s3"]
            ),
            Case(
                name: "top approval card excludes its session",
                home: .with {
                    $0.attention = rows
                    $0.topAttentionCard = .with { $0.sessionID = "s1"; $0.approvalID = "a1" }
                },
                expected: ["s2", "s3"]
            ),
            Case(
                name: "top approval card falls back to topAttentionSessionID when card sessionID empty",
                home: .with {
                    $0.attention = rows
                    $0.topAttentionSessionID = "s2"
                    $0.topAttentionCard = .with { $0.approvalID = "a2" }
                },
                expected: ["s1", "s3"]
            ),
            Case(
                name: "top inline question card excludes its session",
                home: .with {
                    $0.attention = rows
                    $0.topAttentionQuestion = .with { $0.sessionID = "s2"; $0.questionID = "q2" }
                },
                expected: ["s1", "s3"]
            ),
            Case(
                name: "top approval takes precedence over top question when both populated",
                home: .with {
                    $0.attention = rows
                    $0.topAttentionCard = .with { $0.sessionID = "s1"; $0.approvalID = "a1" }
                    $0.topAttentionQuestion = .with { $0.sessionID = "s2"; $0.questionID = "q2" }
                },
                expected: ["s2", "s3"]
            ),
            Case(
                name: "non-empty filterQuery keeps all attention rows",
                home: .with {
                    $0.attention = rows
                    $0.filterQuery = "foo"
                    $0.topAttentionCard = .with { $0.sessionID = "s1"; $0.approvalID = "a1" }
                },
                expected: ["s1", "s2", "s3"]
            ),
        ]
        for tc in cases {
            let got = homeFilteredAttentionRows(home: tc.home).map(\.sessionID)
            #expect(got == tc.expected, Comment(rawValue: tc.name))
        }
    }

    @Test func commandCloseDismissAndMultiSelectGuardRules() {
        // Sheet / dialog / plan close rule: strict advancement past baseline.
        #expect(!shouldCloseAfterCommandSuccess(baseline: nil, current: 1))
        #expect(!shouldCloseAfterCommandSuccess(baseline: 0, current: 0))
        #expect(!shouldCloseAfterCommandSuccess(baseline: 5, current: 5))
        #expect(!shouldCloseAfterCommandSuccess(baseline: 5, current: 4))
        #expect(shouldCloseAfterCommandSuccess(baseline: 5, current: 6))

        // QuestionScreen dismiss rule: requires both seq advancement AND isLastStep.
        #expect(!shouldDismissQuestionScreen(baseline: nil, current: 2, isLastStep: true))
        #expect(!shouldDismissQuestionScreen(baseline: 1, current: 1, isLastStep: true))
        #expect(!shouldDismissQuestionScreen(baseline: 1, current: 2, isLastStep: false))
        #expect(shouldDismissQuestionScreen(baseline: 1, current: 2, isLastStep: true))

        // Multi-select submission requires non-empty optionNums.
        #expect(!shouldSubmitMultiSelect([]))
        #expect(shouldSubmitMultiSelect([1]))
        #expect(shouldSubmitMultiSelect([1, 3]))

        // Shared command error text prefers the actual reason (errorText), then Core's generic
        // statusText, then errorCode, matching the web.
        let errorFirst = PBCoreSessionCommandState.with {
            $0.status = .unknown
            $0.frozen = true
            $0.statusText = .with { $0.literal = "expired status copy" }
            $0.errorText = .with { $0.literal = "raw transport error" }
            $0.errorCode = "ERR_NET"
        }
        #expect(errorFirst.renderedErrorText == "raw transport error")

        let statusFallback = PBCoreSessionCommandState.with {
            $0.status = .unknown
            $0.frozen = true
            $0.statusText = .with { $0.literal = "expired status copy" }
            $0.errorCode = "ERR_NET"
        }
        #expect(statusFallback.renderedErrorText == "expired status copy")

        let errorFallback = PBCoreSessionCommandState.with {
            $0.status = .failed
            $0.errorText = .with { $0.literal = "server rejected" }
            $0.errorCode = "ERR_500"
        }
        #expect(errorFallback.renderedErrorText == "server rejected")

        let codeFallback = PBCoreSessionCommandState.with {
            $0.status = .failed
            $0.errorCode = "ERR_OFFLINE"
        }
        #expect(codeFallback.renderedErrorText == "ERR_OFFLINE")
    }

    @Test func homeFocusCardsRenderAllPhasesCleanly() async throws {
        let remote = Demo.remote()
        let defaults = UserDefaults(suiteName: "home-focus-cards-" + UUID().uuidString)!
        let model = AppModel(settings: SettingsStore(defaults: defaults))
        model.showDemo(account: Demo.account, remote: remote, world: Demo.world(.everyday, now: 1_791_000_000_000), events: [:])
        defer { ImageCenter.remove(remote) }
        let context = try #require(model.context)
        let nav = Navigator()

        let baseQuestion = PBCoreHomeQuestionCardView.with {
            $0.sessionID = "s-q"
            $0.questionID = "q-1"
            $0.badgeText = .with { $0.literal = "在问你" }
            $0.timeText = .with { $0.literal = "刚刚" }
            $0.title = .with { $0.literal = "Question Session" }
            $0.where = .with { $0.literal = "Mac · repo" }
            $0.header = "Choice"
            $0.questionText = "Which target should run?"
            $0.canAnswer = true
            $0.options = [
                .with { $0.optionNum = 1; $0.label = "Unit"; $0.description_p = "Fast"; $0.recommended = true },
                .with { $0.optionNum = 2; $0.label = "All"; $0.description_p = "Full suite" },
            ]
        }
        for phase in [PBCoreHomeFocusPhase.idle, .submitting, .failed, .resolvedThisClient, .resolvedElsewhere] {
            var q = baseQuestion
            q.phase = phase
            q.pickedOptionNum = phase == .submitting || phase == .failed ? 1 : 0
            q.resolutionText = .with { $0.literal = "已回答" }
            q.command = .with {
                $0.canRetry = true
                $0.errorText = .with { $0.literal = "network error" }
            }
            render(
                HomeQuestionCard(question: q)
                    .environment(model)
                    .environment(context)
                    .environment(nav)
            )
        }

        let baseCard = PBCoreAttentionCardView.with {
            $0.sessionID = "s-a"
            $0.approvalID = "a-1"
            $0.kindText = .with { $0.literal = "审批" }
            $0.title = .with { $0.literal = "Approval Session" }
            $0.where = .with { $0.literal = "Mac · repo" }
            $0.body = "echo hi"
            $0.description_p = "Run test echo"
        }
        for phase in [PBCoreHomeFocusPhase.idle, .resolvedThisClient, .resolvedElsewhere] {
            var c = baseCard
            c.phase = phase
            c.denyingOpen = phase == .idle
            c.resolutionText = .with { $0.literal = "已允许" }
            render(
                HomeApprovalCard(card: c)
                    .environment(model)
                    .environment(context)
                    .environment(nav)
            )
        }
        await model.logout()
    }
}
#endif
