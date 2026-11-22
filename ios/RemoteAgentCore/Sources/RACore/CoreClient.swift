import Corebind
import Foundation
import RAProto
import SwiftProtobuf

public enum CoreClientError: Error, Sendable {
    case unavailable
    case bind(String)
}

/// Thin SwiftProtobuf wrapper around the gomobile `CorebindCore` handle.
public final class CoreClient: @unchecked Sendable {
    private let raw: CorebindCore
    private var closed = false

    public init() throws {
        guard let core = CorebindNew() else {
            throw CoreClientError.unavailable
        }
        self.raw = core
    }

    public func dispatch(_ event: PBCoreEvent) throws -> PBCoreEffectBatch {
        guard !closed else { throw CoreClientError.unavailable }
        let input = try event.serializedData()
        do {
            let output = try raw.dispatch(input)
            return try PBCoreEffectBatch(serializedBytes: output)
        } catch let error as NSError where error.domain == "Foundation._GenericObjCError" && error.code == 0 {
            // Go returns an empty []byte(nil) for an empty batch, which gomobile maps to (nil, nil).
            return PBCoreEffectBatch()
        } catch {
            throw CoreClientError.bind(error.localizedDescription)
        }
    }

    public func view(_ screen: PBCoreScreen) throws -> PBCoreViewModel {
        guard !closed else { throw CoreClientError.unavailable }
        let input = try screen.serializedData()
        do {
            let output = try raw.view(input)
            return try PBCoreViewModel(serializedBytes: output)
        } catch let error as NSError where error.domain == "Foundation._GenericObjCError" && error.code == 0 {
            return PBCoreViewModel()
        } catch {
            throw CoreClientError.bind(error.localizedDescription)
        }
    }

    public func close() {
        guard !closed else { return }
        closed = true
        raw.close()
    }

    deinit {
        if !closed { raw.close() }
    }
}

/// Derives the account-specific HMAC key used to obfuscate remembered home scopes on disk.
public func scopeHmacKey(localKey: Data) -> Data {
    deriveScopeKey(key: localKey, purpose: "home-scope")
}

/// Encodes a core scope key (`""` for all projects, otherwise a project path) as an opaque token.
public func encodeScopeToken(scopeKey: String, hmacKey: Data) -> String {
    if scopeKey.isEmpty { return "all" }
    return "project:" + hmacSha256(key: hmacKey, Data(scopeKey.utf8)).hex
}

/// Resolves a stored scope token against candidate project keys.
public func decodeScopeToken(_ token: String?, hmacKey: Data, keys: [String]) -> String? {
    guard let token, !token.isEmpty else { return nil }
    if token == "all" { return "all" }
    for key in keys where !key.isEmpty {
        if encodeScopeToken(scopeKey: key, hmacKey: hmacKey) == token {
            return key
        }
    }
    return nil
}
