import Foundation

/// The signing/sealing context of one verified member head. Everything a PeerSignal or
/// DirectSession opens or seals goes through it, so no account scope is global.
struct PeerCodec: Sendable {
    let scope: PeerWire.Scope
    func seal(_ from: PeerWire.Identity, _ to: PeerWire.Public, _ context: PeerWire.Context, _ type: UInt64, _ payload: PeerWire.Bytes) throws -> PeerWire.Bytes {
        try PeerWire.seal(scope, from, to, context, type, payload)
    }
    func open(_ raw: PeerWire.Bytes, _ from: PeerWire.Public, _ own: PeerWire.Identity, _ now: UInt64) throws -> PeerWire.Opened {
        try PeerWire.open(scope, raw, from, own, now)
    }
    func openSent(_ raw: PeerWire.Bytes, _ own: PeerWire.Identity, _ to: PeerWire.Public, _ now: UInt64) throws -> PeerWire.Opened {
        try PeerWire.openSent(scope, raw, own, to, now)
    }
}
