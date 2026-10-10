import Foundation
import RAProto

/// Where envelopes are cached between runs. Only ciphertext is stored.
public protocol CipherCache: Sendable {
    func machines() async -> [PBMachine]
    func sessions() async -> [PBRemoteSession]
    func events(sessionId: String) async -> [PBStoredEvent]
    func putMachine(_ m: PBMachine) async
    func putSession(_ s: PBRemoteSession) async
    func putEvents(sessionId: String, events: [PBStoredEvent]) async

    /// Forgets a session's events, e.g. when one of them does not open; the session stays listed.
    func dropEvents(sessionId: String) async
    func removeMachine(id: String) async

    /// A session that no longer exists: its entry and its events.
    func removeSession(id: String) async

    /// Takes a session off the cached list, which is the listing's first page; its events stay.
    func unlistSession(id: String) async

    /// Notes whose a session outside the cached list is (one of a later page,
    /// or fetched by its ID), so that events kept for it go with its machine.
    /// A session already noted, listed or not, is left as it is.
    func keepOwner(_ s: PBRemoteSession) async

    /// An image's display copy as fetched: its sealed envelope, never decrypted bytes.
    func image(key: String) async -> Data?
    func putImage(key: String, envelope: Data) async

    /// Other sealed objects by name, such as the account's project preferences.
    func blob(name: String) async -> Data?
    func putBlob(name: String, data: Data) async
}

extension CipherCache {
    public func keepOwner(_ s: PBRemoteSession) async {}
    public func image(key: String) async -> Data? { nil }
    public func putImage(key: String, envelope: Data) async {}
    public func blob(name: String) async -> Data? { nil }
    public func putBlob(name: String, data: Data) async {}
}
