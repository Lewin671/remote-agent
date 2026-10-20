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
    func readSecurityUnlocked() throws -> DeviceSecurity.Record {
        guard let data = try read(.securityState) else { throw DeviceSecurity.Failure.storage }
        return try DeviceSecurity.Record.parse(data)
    }
    func initializeSecurity(_ record: DeviceSecurity.Record) throws {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        _ = try initializeSecurityUnlocked(record)
    }
    func initializeSecurityUnlocked(_ record: DeviceSecurity.Record) throws -> DeviceSecurity.Record {
        if let existing = try read(.securityState) {
            let old = try DeviceSecurity.Record.parse(existing)
            if try old.includesInstallation(record) {
                try refreshNotifyUnlocked(old); return old
            }
            throw DeviceSecurity.Failure.conflict
        }
        try write(.securityState, record.bytes())
        try refreshNotifyUnlocked(record)
        return record
    }
    /// The receipt check and both installations share logout's storage lock.
    func saveRegistrationBootstrap(_ account: Account, record: DeviceSecurity.Record) throws {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        guard let raw = try readNamed("registration-intent") else { throw DeviceSecurity.Failure.stale }
        let pending = try JSONDecoder().decode(RegistrationIntent.self, from: raw)
        guard try pending.matchesBootstrap(account: account, record: record) else { throw DeviceSecurity.Failure.stale }
        // The public key remains after the registration receipt is cleared.
        try saveAccountPublicProfileUnlocked(pending.publicProfile, pin: record.pin)
        let installed: DeviceSecurity.Record
        if let raw = try read(.securityState) {
            let old = try DeviceSecurity.Record.parse(raw)
            if try old.includesInstallation(record) { installed = old }
            else {
                // Re-pairing preserves Ed/pin and installs the fresh X only on a
                // strictly extended, verified chain authorized by this receipt.
                guard old.memberID == record.memberID, old.pin == record.pin,
                    record.headEpoch > old.headEpoch, let ancestor = record.states[old.headEpoch],
                    try DeviceSecurity.stateHash(ancestor) == DeviceSecurity.stateHash(old.state) else {
                    throw DeviceSecurity.Failure.conflict
                }
                var next = record
                next.floor = max(old.floor, record.floor); next.floorSaved = max(old.floorSaved, record.floorSaved)
                next.contentKeys.merge(old.contentKeys) { current, _ in current }
                try write(.securityState, next.bytes())
                installed = next
            }
        } else { installed = try initializeSecurityUnlocked(record) }
        try refreshNotifyUnlocked(installed)
        try save(account.stored, notifyKey: DeviceSecurity.NotifyRecord(record: installed).bytes())
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
        let next = try record.mergingCommitted(old)
        try write(.securityState, next.bytes())
        try refreshNotifyUnlocked(next)
        return next
    }
}


extension Vault {
    /// Shared by creation, pairing bootstrap and recovery/context adoption.
    /// Save before installing an account; a Keychain refusal aborts adoption.
    func saveAccountPublicProfile(_ profile: AccountPublicProfile, pin: DeviceSecurity.Pin) throws {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        try saveAccountPublicProfileUnlocked(profile, pin: pin)
    }
    func saveAccountPublicProfileUnlocked(_ profile: AccountPublicProfile, pin: DeviceSecurity.Pin) throws {
        guard profile.pin == pin else { throw DeviceSecurity.Failure.untrusted }
        // Revalidate even if received from a different bootstrap path.
        let checked = try AccountPublicProfile(publicKey: profile.publicKey, pin: pin)
        try savePairingIdentity(checked.storageKey, checked.bytes())
    }
    func accountPublicProfile(pin: DeviceSecurity.Pin) throws -> AccountPublicProfile? {
        SecurityVaultLock.lock.lock()
        defer { SecurityVaultLock.lock.unlock() }
        let key = "account-public-" + pin.account + "-" + pin.genesis.hex
        guard let raw = try readNamed(key) else { return nil }
        let profile = try AccountPublicProfile.parse(raw)
        guard profile.pin == pin else { throw DeviceSecurity.Failure.untrusted }
        return profile
    }
}

// Independent Keychain item; existing account/security encodings are unchanged.
extension Vault: AuthorityStorage {
    func existingSecurity() throws -> DeviceSecurity.Record? {
        SecurityVaultLock.lock.lock(); defer { SecurityVaultLock.lock.unlock() }
        return try read(.securityState).map { try DeviceSecurity.Record.parse($0) }
    }
    func readOperation() throws -> SecurityOperation? {
        SecurityVaultLock.lock.lock(); defer { SecurityVaultLock.lock.unlock() }
        return try operationUnlocked()
    }
    private func operationUnlocked() throws -> SecurityOperation? {
        try readNamed("authority-operation").map { try JSONDecoder().decode(SecurityOperation.self, from: $0) }
    }
    func prepareOperation(expectedHead: Data?, _ build: (DeviceSecurity.Record?) throws -> SecurityOperation) throws -> SecurityOperation {
        SecurityVaultLock.lock.lock(); defer { SecurityVaultLock.lock.unlock() }
        guard try readNamed("registration-intent") == nil else { throw DeviceSecurity.Failure.conflict }
        guard try operationUnlocked() == nil else { throw DeviceSecurity.Failure.conflict }
        let committed = try read(.securityState).map { try DeviceSecurity.Record.parse($0) }
        if let expectedHead {
            guard let committed, try DeviceSecurity.stateHash(committed.state) == expectedHead else { throw DeviceSecurity.Failure.stale }
        }
        let operation = try build(committed)
        try writeNamed("authority-operation", operation.bytes())
        return operation
    }
    func replaceOperation(expected: SecurityOperation, next: SecurityOperation?) throws {
        SecurityVaultLock.lock.lock(); defer { SecurityVaultLock.lock.unlock() }
        guard try operationUnlocked() == expected else { throw DeviceSecurity.Failure.stale }
        if let next {
            try next.validateProgress(from: expected)
            try writeNamed("authority-operation", next.bytes())
        } else { try deleteNamed("authority-operation") }
    }
    func savePublicProfile(_ profile: AccountPublicProfile) throws {
        try saveAccountPublicProfile(profile, pin: profile.pin)
    }
    func publicProfile(pin: DeviceSecurity.Pin) throws -> AccountPublicProfile? { try accountPublicProfile(pin: pin) }
    func adoptOperation(_ expected: SecurityOperation, account: Account, record: DeviceSecurity.Record,
                        profile: AccountPublicProfile) throws {
        SecurityVaultLock.lock.lock(); defer { SecurityVaultLock.lock.unlock() }
        guard try operationUnlocked() == expected else { throw DeviceSecurity.Failure.stale }
        try saveAccountPublicProfileUnlocked(profile, pin: record.pin)
        let installed = try initializeSecurityUnlocked(record)
        try save(account.stored, notifyKey: DeviceSecurity.NotifyRecord(record: installed).bytes())
    }
}
