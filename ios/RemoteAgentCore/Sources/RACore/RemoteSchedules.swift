import Foundation
import RAProto
import SwiftProtobuf

extension Remote {
    /// Revalidates the cached signed ciphertext against trusted membership before opening it.
    public func cachedScheduleResultJson(_ machineID: String) async -> Data? {
        try? await ensureSecurityBootstrapped()
        guard await currentScheduleMember(machineID) else {
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
        guard await currentScheduleMember(machineID) else {
            await cache?.removeBlob(name: "schedules/" + machineID)
            return nil
        }
        guard case .schedule(let r) = result.kind, result.errorCode.isEmpty else { return nil }
        return r.resultJson
    }

    public func deleteScheduleCache(_ machineID: String) async {
        await cache?.removeBlob(name: "schedules/" + machineID)
    }

    func currentScheduleMember(_ id: String) async -> Bool {
        guard !closed else { return false }
        if let st = securityClient.currentStatus, (!st.isMember || st.removed) {
            return false
        }
        if let v = await currentCoreSyncStateView(), v.securityLoaded {
            if v.removed || !v.isMember { return false }
            return v.trustedMachineIds.contains(id)
        }
        return true
    }

    func keepScheduleSnapshot(_ machine: String, data: Data) async {
        guard await currentScheduleMember(machine) else { return }
        await cache?.putBlob(name: "schedules/" + machine, data: data)
        if !(await currentScheduleMember(machine)) {
            await cache?.removeBlob(name: "schedules/" + machine)
        }
    }
}
