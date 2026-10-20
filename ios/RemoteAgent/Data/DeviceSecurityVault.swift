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
        _ = try initializeSecurityUnlocked(record)
    }
    private func initializeSecurityUnlocked(_ record: DeviceSecurity.Record) throws -> DeviceSecurity.Record {
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
        let installed = try initializeSecurityUnlocked(record)
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
    private func saveAccountPublicProfileUnlocked(_ profile: AccountPublicProfile, pin: DeviceSecurity.Pin) throws {
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
