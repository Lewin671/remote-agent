import Foundation

extension Remote {
    /// Reads the opaque schedule cache blob persisted for `machineID` (decrypted and verified in Go clientcore).
    public func readScheduleCacheBlob(_ machineID: String) async -> Data? {
        await cache?.blob(name: "schedules/" + machineID)
    }

    /// Persists the opaque schedule cache blob produced by Go clientcore for `machineID`.
    public func writeScheduleCacheBlob(_ machineID: String, blob: Data) async {
        await cache?.putBlob(name: "schedules/" + machineID, data: blob)
    }

    /// Deletes the persisted schedule cache blob for `machineID`.
    public func deleteScheduleCache(_ machineID: String) async {
        await cache?.removeBlob(name: "schedules/" + machineID)
    }
}
