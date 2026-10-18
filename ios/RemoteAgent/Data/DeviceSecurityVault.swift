import Foundation
import RACore

// Keychain updates are atomic items. All SDK instances in this app serialize
// their compare-and-update with the same lock; the notification extension does
// not receive this item or any member signing/recipient key.
enum SecurityVaultLock { static let lock = NSLock() }
extension Vault: DeviceSecurity.Storage {
    func readSecurity() throws -> DeviceSecurity.Record {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        let record = try readSecurityUnlocked()
        try refreshNotifyUnlocked(record)
        return record
    }
    private func readSecurityUnlocked() throws -> DeviceSecurity.Record {
        guard let data = try read(.securityState) else { throw DeviceSecurity.Failure.storage }
        return try DeviceSecurity.Record.parse(data)
    }
    func initializeSecurity(_ record: DeviceSecurity.Record) throws {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        guard try read(.securityState) == nil else { throw DeviceSecurity.Failure.conflict }
        try write(.securityState, record.bytes())
        try refreshNotifyUnlocked(record)
    }
    private func refreshNotifyUnlocked(_ record: DeviceSecurity.Record) throws {
        if let bytes = try read(.notifyKey),
            let old = try? DeviceSecurity.NotifyRecord.parse(bytes),
            try DeviceSecurity.stateHash(old.state) == DeviceSecurity.stateHash(record.state)
        {
            return
        }
        try write(.notifyKey, DeviceSecurity.NotifyRecord(record: record).bytes())
    }
    func reserveSecurity(expectedState: Data) throws -> DeviceSecurity.Record {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        var r = try readSecurityUnlocked()
        guard try DeviceSecurity.stateHash(r.state) == expectedState else {
            throw DeviceSecurity.Failure.stale
        }
        guard r.counter < UInt64.max else { throw DeviceSecurity.Failure.capacity }
        r.counter += 1
        try write(.securityState, r.bytes())
        return r
    }
    func advanceSecurity(expectedState: Data, next: DeviceSecurity.State) throws
        -> DeviceSecurity.Record
    {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        let old = try readSecurityUnlocked()
        guard try DeviceSecurity.stateHash(old.state) == expectedState else {
            throw DeviceSecurity.Failure.stale
        }
        try writeNamed(
            "history-v2.\(old.state.core.epoch)",
            DeviceSecurity.Historical(state: old.state, secret: old.secret()).bytes())
        let next = try old.advanced(next)
        try write(.securityState, next.bytes())
        try refreshNotifyUnlocked(next)
        return next
    }
    func readSecurityHistory(epoch: UInt64) throws -> DeviceSecurity.Historical? {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        let current = try readSecurityUnlocked()
        guard epoch > 0 && epoch <= current.state.core.epoch else {
            throw DeviceSecurity.Failure.untrusted
        }
        guard let bytes = try readNamed("history-v2.\(epoch)") else { return nil }
        let h = try DeviceSecurity.Historical.parse(bytes)
        guard h.state.core.epoch == epoch else { throw DeviceSecurity.Failure.storage }
        try DeviceSecurity.verifyState(current.pin, h.state)
        return h
    }
    func cacheSecurityHistory(_ history: DeviceSecurity.Historical) throws {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        let current = try readSecurityUnlocked()
        guard history.state.core.epoch <= current.state.core.epoch else {
            throw DeviceSecurity.Failure.untrusted
        }
        try DeviceSecurity.verifyState(current.pin, history.state)
        try writeNamed("history-v2.\(history.state.core.epoch)", history.bytes())
    }

    func readRemovalOperations() throws -> Data? {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        _ = try readSecurityUnlocked()
        return try readNamed("security-removals-v2")
    }
    func writeRemovalOperations(_ data: Data) throws {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        _ = try readSecurityUnlocked()
        guard data.count <= 16 << 20 else { throw DeviceSecurity.Failure.capacity }
        try writeNamed("security-removals-v2", data)
    }

}
