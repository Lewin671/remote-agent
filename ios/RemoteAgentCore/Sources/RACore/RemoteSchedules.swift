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
        guard let data = await cache?.blob(name: "schedules/" + machineID),
              let env = try? PBEnvelope(serializedBytes: data),
              let h = try? PBHeader(serializedBytes: env.header), h.kind == .commandResult,
              let (_, result) = try? await openSecureMessage(envelope: env, want: Place(.commandResult, machineId: machineID, requestId: h.requestID), as: PBCommandResult.self, history: true)
        else { return nil }
        return try? decodeScheduleList(result)
    }
    public func listSchedules(_ machineID: String) async throws -> [ScheduleTask] {
        try decodeScheduleList(await machineCommand(machineID, scheduleCommand(operation: "list")))
    }
}
