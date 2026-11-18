#if DEBUG
import Foundation
import RACore
import RAProto

/// The same synthetic fixture as the web demo and Android instrumentation gallery.
/// Copy testdata/schedules/clients.json into the simulator app's Documents directory.
@MainActor enum ScheduleSamples {
    static func prepare(_ model: AppModel) -> Bool {
        let url = URL.documentsDirectory.appendingPathComponent("schedule-clients.json")
        guard let data = try? Data(contentsOf: url),
              let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let machine = root["machine"] as? String,
              let machineName = root["machineName"] as? String,
              let session = root["session"] as? String,
              let sessionTitle = root["sessionTitle"] as? String,
              let tasks = root["tasks"] as? [[String: Any]],
              let listData = try? JSONSerialization.data(withJSONObject: ["tasks": tasks])
        else { return false }

        var state = RemoteState(); state.conn = .online; state.link = .online; state.synced = true
        let meta = PBMachineMeta.with {
            $0.hostname = machineName; $0.protocolVersion = protocolVersion; $0.scheduleRevision = 1
            $0.capabilities.scheduledTasks = true
            $0.agents = [PBAgent.codex, .claude, .antigravity].map { agent in
                .with { $0.agent = agent; $0.defaultMode = .auto }
            }
        }
        state.currentMachines = [machine]
        state.machines[machine] = MachineView(id: machine, online: true, lastSeenMs: 0, meta: meta)
        let info = PBSession.with {
            $0.id = session; $0.title = sessionTitle; $0.cwd = "/example/project"
            $0.agent = .claude; $0.mode = .auto; $0.status = .idle
        }
        state.sessions[session] = SessionView(id: session, machineId: machine, info: info, lastSeq: 2)
        var events: [PBEvent] = [
            .with { $0.seq = 1; $0.sessionID = session; $0.userMessage.text = "检查 PR 的回复。" },
            .with { $0.seq = 2; $0.sessionID = session; $0.agentMessage.text = "有变化时会通知你。" }
        ]
        let scene = HomeLaunch.value("-scheduleScene")
        let quietMessage = (tasks.first { ($0["id"] as? String) == "quiet" }?["spec"] as? [String: Any])?["message"] as? String
        let longMessage = quietMessage ?? "检查回复"
        if scene == "chat-long" {
            events[0].userMessage.text = longMessage; events[0].origin.scheduledRunID = "sample-run"
            events[0].time.seconds = Int64(Date().timeIntervalSince1970)
        } else if scene == "chat-queue" {
            state.sessions[session]?.info?.status = .running
            events.append(.with {
                $0.seq = 3; $0.sessionID = session; $0.time.seconds = Int64(Date().timeIntervalSince1970)
                $0.origin.scheduledRunID = "sample-queued-run"
                $0.inputState.requestID = "sample-queued-request"; $0.inputState.text = longMessage
                $0.inputState.status = .queued; $0.inputState.mode = .queue
            })
        } else if scene == "chat-folded" {
            events = (0..<2).flatMap { i -> [PBEvent] in
                let seq = UInt64(i * 3 + 1), run = "sample-run-\(i)"
                return [
                    .with { $0.seq = seq; $0.sessionID = session; $0.time.seconds = Int64(Date().timeIntervalSince1970) - Int64((2 - i) * 3600); $0.userMessage.text = "检查回复"; $0.origin.scheduledRunID = run },
                    .with { $0.seq = seq + 1; $0.sessionID = session; $0.agentMessage.text = "没有新回复" },
                    .with { $0.seq = seq + 2; $0.sessionID = session; $0.turnEnded.outcome = .completed; $0.turnEnded.scheduledRunID = run }
                ]
            }
        }
        state.sessions[session]?.lastSeq = events.last?.seq ?? 0
        if HomeLaunch.value("-scheduleOffline") == "yes" { state.machines[machine]?.online = false }
        let remote = Demo.remote(), shown = state, sampleEvents = events
        model.showDemo(
            account: Demo.account,
            remote: remote,
            initial: shown,
            events: [session: sampleEvents],
            schedules: [machine: listData]
        ) {
            await remote.showForTest(state: shown)
        }
        return true
    }
}
#endif
