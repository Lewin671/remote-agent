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
        state.machines[f.machine] = MachineView(id:f.machine,online:true,lastSeenMs:0,meta:meta)
        let info = PBSession.with {
            $0.id = f.session; $0.title = f.sessionTitle; $0.cwd = "/example/project"
            $0.agent = .claude; $0.mode = .auto; $0.status = .idle
        }
        state.sessions[f.session] = SessionView(id:f.session,machineId:f.machine,info:info,lastSeq:2)
        let events: [PBEvent] = [
            .with { $0.seq = 1; $0.sessionID = f.session; $0.userMessage.text = "检查 PR 的回复。" },
            .with { $0.seq = 2; $0.sessionID = f.session; $0.agentMessage.text = "有变化时会通知你。" }
        ]
        let remote = Demo.remote(), shown = state
        model.showDemo(account:Demo.account,remote:remote,initial:shown) {
            await remote.showForTest(state:shown,events:[f.session:events],live:true)
        }
        guard let context = model.context else { return false }
        context.schedules.configure(.init(cached:{ _ in nil },list:{ _ in f.tasks }))
        context.schedules.show(f.tasks.filter(\.visible).map { .init(machineID:f.machine,task:$0) },state:shown)
        if let task = f.tasks.first(where:{ $0.id == "quiet" }) {
            let draft = context.scheduleDrafts.of("")
            draft.name = task.spec.title; draft.message = task.spec.message; draft.trigger = task.spec.trigger
            draft.config = NewConfig(machineId:f.machine,cwd:task.spec.target.cwd ?? "",agent:.claude,model:"haiku",mode:.auto)
        }
        return true
    }
}
#endif
