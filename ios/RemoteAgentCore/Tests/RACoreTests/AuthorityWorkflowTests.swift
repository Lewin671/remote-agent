import Connect
import CryptoKit
import Foundation
import RAProto
import Testing
import os

@testable import RACore

/// Serializes every receipt/state CAS, with power-loss injection on either side
/// of every durable mutation. No oracle or synthetic signing authority is involved.
final class AuthorityTestVault: AuthorityStorage, @unchecked Sendable {
    let lock = NSRecursiveLock()
    var record: DeviceSecurity.Record?
    var op: SecurityOperation?
    var profile: AccountPublicProfile?
    var account: Account?
    var writes = 0
    var crashAt: Int?
    var crashAfter = false
    var committedReceipts: [SecurityOperation] = []
    func mutate<T>(_ body: () throws -> T) throws -> T {
        writes += 1
        if writes == crashAt && !crashAfter { throw TestFailure("injected before storage") }
        let value = try body()
        if writes == crashAt && crashAfter { throw TestFailure("injected after storage") }
        return value
    }
    func inject(_ write: Int?, after: Bool = false) { lock.withLock { crashAt = write; crashAfter = after; writes = 0 } }
    func existingSecurity() throws -> DeviceSecurity.Record? { lock.withLock { record } }
    func readSecurity() throws -> DeviceSecurity.Record { try lock.withLock { try record.unwrap() } }
    func initializeSecurity(_ record: DeviceSecurity.Record) throws {
        try lock.withLock {
            try mutate {
                if let old = self.record { try DeviceSecurity.check(old.includesInstallation(record), .conflict) }
                else { self.record = record }
            }
        }
    }
    func writeSecurity(expectedHead: Data, _ record: DeviceSecurity.Record) throws -> DeviceSecurity.Record {
        try lock.withLock {
            try mutate {
                let old = try self.record.unwrap()
                try DeviceSecurity.check(DeviceSecurity.stateHash(old.state) == expectedHead, .stale)
                let merged = try record.mergingCommitted(old)
                self.record = merged; return merged
            }
        }
    }
    func readOperation() throws -> SecurityOperation? { lock.withLock { op } }
    func prepareOperation(expectedHead: Data?, _ build: (DeviceSecurity.Record?) throws -> SecurityOperation) throws -> SecurityOperation {
        try lock.withLock {
            try DeviceSecurity.check(op == nil, .conflict)
            if let expectedHead { try DeviceSecurity.check(DeviceSecurity.stateHash(record.unwrap().state) == expectedHead, .stale) }
            return try mutate {
                let next = try build(record); op = next; committedReceipts.append(next); return next
            }
        }
    }
    func replaceOperation(expected: SecurityOperation, next: SecurityOperation?) throws {
        try lock.withLock {
            try DeviceSecurity.check(op == expected, .stale)
            try next?.validateProgress(from: expected)
            try mutate { op = next; if let next { committedReceipts.append(next) } }
        }
    }
    func savePublicProfile(_ profile: AccountPublicProfile) throws { try lock.withLock { try mutate { self.profile = profile } } }
    func publicProfile(pin: DeviceSecurity.Pin) throws -> AccountPublicProfile? {
        try lock.withLock { if let profile { try DeviceSecurity.check(profile.pin == pin, .untrusted) }; return profile }
    }
    func adoptOperation(_ expected: SecurityOperation, account: Account, record: DeviceSecurity.Record, profile: AccountPublicProfile) throws {
        try lock.withLock {
            try DeviceSecurity.check(op == expected, .stale)
            try mutate { self.profile = profile }
            try mutate {
                if let old = self.record {
                    if expected.kind == .recovery {
                        try DeviceSecurity.check(old.pin == record.pin && record.states[old.headEpoch].map { try DeviceSecurity.stateHash($0) } == DeviceSecurity.stateHash(old.state), .conflict)
                        if try old.memberID != record.memberID || !old.includesInstallation(record) { self.record = record }
                    } else { try DeviceSecurity.check(old.includesInstallation(record), .conflict) }
                } else { self.record = record }
            }
            try mutate { self.account = account }
        }
    }
}

private func enabledGate() -> AuthorityGate {
    let gate = AuthorityGate(); gate.set(active: true, protectedDataAvailable: true); return gate
}
