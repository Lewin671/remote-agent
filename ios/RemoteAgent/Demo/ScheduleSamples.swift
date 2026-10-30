#if DEBUG
import Foundation
import RACore
import RAProto
import SwiftProtobuf

/// The same synthetic fixture as the web demo and Android instrumentation gallery.
/// Copy testdata/schedules/clients.json into the simulator app's Documents directory.
@MainActor enum ScheduleSamples {
    private struct Fixture: Decodable {
        let machine: String, machineName: String, session: String, sessionTitle: String
        let tasks: [ScheduleTask]
    }
    static func prepare(_ model: AppModel) -> Bool {
        guard let data = try? Data(contentsOf: URL.documentsDirectory.appendingPathComponent("schedule-clients.json")),
              let f = try? JSONDecoder().decode(Fixture.self, from: data) else { return false }
        var state = RemoteState(); state.conn = .online; state.link = .online; state.synced = true
        let meta = PBMachineMeta.with {
            $0.hostname = f.machineName; $0.protocolVersion = protocolVersion; $0.scheduleRevision = 1
            $0.capabilities.scheduledTasks = true
            $0.agents = [PBAgent.codex, .claude].map { agent in .with { $0.agent = agent; $0.defaultMode = .auto } }
        }
        state.currentMachines = [f.machine]
        state.machines[f.machine] = MachineView(id:f.machine,online:true,lastSeenMs:0,meta:meta)
        let info = PBSession.with {
            $0.id = f.session; $0.title = f.sessionTitle; $0.cwd = "/example/project"
            $0.agent = .claude; $0.mode = .auto; $0.status = .idle
        }
        state.sessions[f.session] = SessionView(id:f.session,machineId:f.machine,info:info,lastSeq:2)
        var events: [PBEvent] = [
            .with { $0.seq = 1; $0.sessionID = f.session; $0.userMessage.text = "检查 PR 的回复。" },
            .with { $0.seq = 2; $0.sessionID = f.session; $0.agentMessage.text = "有变化时会通知你。" }
        ]
        let scene = HomeLaunch.value("-scheduleScene")
        let longMessage = f.tasks.first(where: { $0.id == "quiet" })?.spec.message ?? "检查回复"
        if scene == "chat-long" {
            events[0].userMessage.text = longMessage; events[0].origin.scheduledRunID = "sample-run"
            events[0].time.seconds = Int64(Date().timeIntervalSince1970)
        } else if scene == "chat-queue" {
            state.sessions[f.session]?.info?.status = .running
            events.append(.with {
                $0.seq = 3; $0.sessionID = f.session; $0.time.seconds = Int64(Date().timeIntervalSince1970)
                $0.origin.scheduledRunID = "sample-queued-run"
                $0.inputState.requestID = "sample-queued-request"; $0.inputState.text = longMessage
                $0.inputState.status = .queued; $0.inputState.mode = .queue
            })
        } else if scene == "chat-folded" {
            events = (0..<2).flatMap { i -> [PBEvent] in
                let seq = UInt64(i * 3 + 1), run = "sample-run-\(i)"
                return [
                    .with { $0.seq = seq; $0.sessionID = f.session; $0.time.seconds = Int64(Date().timeIntervalSince1970) - Int64((2 - i) * 3600); $0.userMessage.text = "检查回复"; $0.origin.scheduledRunID = run },
                    .with { $0.seq = seq + 1; $0.sessionID = f.session; $0.agentMessage.text = "没有新回复" },
                    .with { $0.seq = seq + 2; $0.sessionID = f.session; $0.turnEnded.outcome = .completed; $0.turnEnded.scheduledRunID = run }
                ]
            }
        }
        state.sessions[f.session]?.lastSeq = events.last?.seq ?? 0
        if HomeLaunch.value("-scheduleOffline") == "yes" { state.machines[f.machine]?.online = false }
        let remote = Demo.remote(), shown = state, sampleEvents = events
        model.showDemo(account:Demo.account,remote:remote,initial:shown) {
            await remote.showForTest(state:shown,events:[f.session:sampleEvents],live:true)
        }
        guard let context = model.context else { return false }
        context.schedules.configure(.init(cached:{ _ in f.tasks },list:{ _ in f.tasks }))
        context.schedules.show(f.tasks.filter(\.visible).map { .init(machineID:f.machine,task:$0) },state:shown)
        if let task = f.tasks.first(where:{ $0.id == "quiet" }) {
            let draft = context.scheduleDrafts.of("")
            draft.name = ""; draft.message = task.spec.message; draft.trigger = task.spec.trigger
            draft.config = NewConfig(machineId:f.machine,cwd:task.spec.target.cwd ?? "",agent:.claude,model:"haiku",mode:.auto)
        }
        if let task = f.tasks.first(where: { $0.id == "conversation" }) {
            let draft = context.scheduleDrafts.of(f.session)
            draft.message = task.spec.message; draft.trigger = task.spec.trigger
        }
        return true
    }
}
#endif
