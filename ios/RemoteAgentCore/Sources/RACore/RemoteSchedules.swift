import Foundation
import RAProto

public func scheduleCommand(operation: String, taskID: String = "", spec: ScheduleSpec? = nil, revision: UInt64 = 0, creationKey: String = "", sessionID: String = "") throws -> PBCommand {
    var c = PBCommand()
    c.schedule.operation = operation; c.schedule.taskID = taskID; c.schedule.expectedRevision = revision
    c.schedule.requestID = creationKey; c.schedule.sessionID = sessionID
    if let spec { c.schedule.specJson = try JSONEncoder().encode(spec) }
    return c
}
public func decodeScheduleList(_ result: PBCommandResult) throws -> [ScheduleTask] {
    guard case .schedule(let r) = result.kind else { throw UserError("电脑没有返回任务列表") }
    struct List: Decodable {
        var tasks: [ScheduleTask]?
        enum CodingKeys: String, CodingKey { case tasks }
        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            guard c.contains(.tasks) else { throw UserError("电脑返回的不是任务列表") }
            tasks = try c.decodeIfPresent([ScheduleTask].self, forKey: .tasks)
        }
    }
    return try JSONDecoder().decode(List.self, from: r.resultJson).tasks ?? []
}
extension Remote {
    /// Revalidates the cached signed ciphertext against its machine before opening it.
    public func cachedSchedules(_ machineID: String) async -> [ScheduleTask]? {
        guard currentScheduleMember(machineID) else { await cache?.removeBlob(name: "schedules/" + machineID); return nil }
        guard let data = await cache?.blob(name: "schedules/" + machineID),
              let env = try? PBEnvelope(serializedBytes: data),
              let h = try? PBHeader(serializedBytes: env.header), h.kind == .commandResult,
              let (_, result) = try? await openSecureMessage(envelope: env, want: Place(.commandResult, machineId: machineID, requestId: h.requestID), as: PBCommandResult.self, history: true)
        else { return nil }
        guard currentScheduleMember(machineID) else { await cache?.removeBlob(name: "schedules/" + machineID); return nil }
        return try? decodeScheduleList(result)
    }
    func currentScheduleMember(_ id: String) -> Bool {
        !closed && (try? securityIdentity().1.core.member(id)?.kind) == 1
    }
    func keepScheduleSnapshot(_ machine: String, data: Data) async {
        guard currentScheduleMember(machine) else { return }
        await cache?.putBlob(name: "schedules/" + machine, data: data)
        // A directory removal can overtake the suspended cache write.
        if !currentScheduleMember(machine) { await cache?.removeBlob(name: "schedules/" + machine) }
    }
    public func listSchedules(_ machineID: String) async throws -> [ScheduleTask] {
        try decodeScheduleList(await machineCommand(machineID, scheduleCommand(operation: "list")))
    }
}
