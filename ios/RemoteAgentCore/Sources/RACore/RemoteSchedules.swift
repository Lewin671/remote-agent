import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    /// Revalidates the cached signed ciphertext against its machine before opening it.
    public func cachedScheduleResultJson(_ machineID: String) async -> Data? {
        guard currentScheduleMember(machineID) else {
            await cache?.removeBlob(name: "schedules/" + machineID)
            return nil
        }
        guard let data = await cache?.blob(name: "schedules/" + machineID),
              let env = try? PBEnvelope(serializedBytes: data),
              let opened = try? await openEnvelopeOp(
                  envelope: env,
                  expectedPurpose: .result,
                  machineId: machineID,
                  requestId: "",
                  history: true
              ),
              let result = try? PBCommandResult(serializedBytes: opened.plaintext)
        else { return nil }
        guard currentScheduleMember(machineID) else {
            await cache?.removeBlob(name: "schedules/" + machineID)
            return nil
        }
        guard case .schedule(let r) = result.kind, result.errorCode.isEmpty else { return nil }
        return r.resultJson
    }

    public func deleteScheduleCache(_ machineID: String) async {
        await cache?.removeBlob(name: "schedules/" + machineID)
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
}
