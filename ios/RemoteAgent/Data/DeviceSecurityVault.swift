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
        guard record.member else {
            try delete(.notifyKey)
            return
        }
        try write(.notifyKey, DeviceSecurity.NotifyRecord(record: record).bytes())
    }
    func writeSecurity(expectedHead: Data, _ record: DeviceSecurity.Record) throws
        -> DeviceSecurity.Record
    {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        let old = try readSecurityUnlocked()
        guard try DeviceSecurity.stateHash(old.state) == expectedHead else {
            throw DeviceSecurity.Failure.stale
        }
        try write(.securityState, record.bytes())
        try refreshNotifyUnlocked(record)
        return record
    }
}
