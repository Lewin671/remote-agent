import Foundation
import CryptoKit

/// Protocol-only mobile endpoint. It proves its instance and refuses invitations
/// or unknown authenticated types; it never constructs a PeerConnection.
actor PeerResponder {
    typealias W = PeerWire
    private let instance = W.random(16), boot = W.random(32)
    private var binding = ""
    private struct Seen { let hash: W.Bytes; let reply: Data?; let until: UInt64 }
    private struct Session { var peer: W.Public; var seq: UInt64 = 0; var until: UInt64; var seen: [String: Seen] = [:] }
    private var sessions: [String: Session] = [:]
    static func body(_ data: Data) -> Data? {
        let raw = Array(data)
        guard raw.count >= 2, raw.count <= 65540, raw[0] == 90 else { return nil }
        var at = 1
        guard let size = try? PeerSchema.integer(raw, &at), size <= 65536, size == raw.count - at else { return nil }
        return Data(raw[at...])
    }
    /// A PeerFrameResult (field 12) body: the relay's receipt for a directed frame this device sent.
    static func resultBody(_ data: Data) -> Data? {
        let raw = Array(data)
        guard raw.count >= 2, raw.count <= 1024, raw[0] == 98 else { return nil }
        var at = 1
        guard let size = try? PeerSchema.integer(raw, &at), size == raw.count - at else { return nil }
        return Data(raw[at...])
    }
    func receive(_ data: Data, record: DeviceSecurity.Record, seed: Data, now: UInt64) -> Data? {
        do {
            guard record.member, let lease = record.lease else { return nil }
            try lease.verify(record.state)
            guard max(now, record.floor) < lease.notAfter else { return nil }
            let state = try DeviceSecurity.stateHash(record.state)
            let key = record.pin.account + W.hex(Array(record.pin.domain)) + W.hex(Array(state))
            if key != binding { sessions.removeAll(); binding = key }
            sessions = sessions.filter { now <= $0.value.until }
            let scope = W.Scope(account: W.utf8(record.pin.account), accountDomain: Array(record.pin.domain), head: Array(state), epoch: record.headEpoch)
            let sign = try Curve25519.Signing.PrivateKey(rawRepresentation: seed), box = try record.xKey
            let selfPublic = W.Public(id: W.utf8(record.memberID), instance: instance, boot: boot, role: 1, sign: sign.publicKey, box: box.publicKey)
            let own = W.Identity(peer: selfPublic, sign: sign, box: box), raw = Array(data)
            let fields = try W.parse(raw, 5)
            guard let from = fields[1], let id = String(bytes: from.data, encoding: .utf8), let member = record.state.core.member(id), member.kind == 1 else { return nil }
            var peer = W.Public(id: W.utf8(id), instance: [], boot: [], role: 2, sign: try .init(rawRepresentation: member.ed), box: try .init(rawRepresentation: member.x))
            var opened = try? W.open(scope, raw, peer, own, now), sessionKey: String?
            if let o = opened, o.ctx.cls == 1, o.type == 1 {
                _ = try PeerSchema.known(1, 1, o.payload)
                let f = try PeerSchema.fields(o.payload, "Probe")
                try W.check(f[1]!.data == o.ctx.connection && f[2]!.data == o.ctx.senderInstance && f[3]!.data == o.ctx.senderBoot)
                let k = id + W.hex(o.ctx.connection); sessionKey = k
                if sessions[k] == nil {
                    guard sessions.count < 128, sessions.values.filter({$0.peer.id == peer.id}).count < 32 else { return nil }
                    peer.instance = o.ctx.senderInstance; peer.boot = o.ctx.senderBoot
                    sessions[k] = Session(peer: peer, until: o.ctx.deadline + 300000)
                }
            } else {
                opened = nil
                for (k, s) in sessions where s.peer.id == peer.id {
                    if let o = try? W.open(scope, raw, s.peer, own, now), k == id + W.hex(o.ctx.connection) { opened = o; sessionKey = k; break }
                }
            }
            guard let o = opened, let k = sessionKey, var session = sessions[k] else { return nil }
            _ = try PeerSchema.known(o.ctx.cls, o.type, o.payload)
            if o.ctx.cls == 2 && (o.type == 1 || o.type == 14) { try Self.capabilityExpiry(o, now) }
            session.seen = session.seen.filter { now <= $0.value.until }
            let slot = "\(o.ctx.use)/\(o.ctx.generation)/\(o.ctx.cls)/\(o.ctx.seq)"
            if let old = session.seen[slot] { return old.hash == o.hash ? old.reply : nil }
            guard session.seen.count < 2048, sessions.values.reduce(0, {$0 + $1.seen.count}) < 8192 else { return nil }
            var reply: W.Bytes?
            if o.ctx.cls == 1 && o.type == 1 {
                var c = W.Context(selfPublic, session.peer, o.ctx.connection, 0, 0, 1, now, 1, true); c.deadline = min(c.deadline, o.ctx.deadline)
                reply = try W.seal(scope, own, session.peer, c, 2, W.pb([W.b(1,c.connection),W.b(2,instance),W.b(3,boot),W.b(4,session.peer.boot),W.b(6,o.hash)]))
            } else if !(o.ctx.cls == 2 && [4,10,11,12,13].contains(o.type)) && !(o.ctx.cls == 1 && o.type == 2) {
                session.seq += 1
                var c = W.Context(selfPublic, session.peer, o.ctx.connection, 0, 1, session.seq, now, 2); c.deadline = min(c.deadline, o.ctx.deadline)
                reply = try W.seal(scope, own, session.peer, c, 10, W.pb([W.u(1,o.ctx.cls),W.u(2,o.type),W.u(3,o.ctx.seq),W.b(4,o.hash)]))
            }
            var output: Data?
            if let reply { let f = try W.parse(reply,5); output = Data(W.pb([W.b(3,W.pb([W.b(1,f[2]!.data),W.u(2,f[3]!.value),W.b(3,f[4]!.data),W.b(4,f[5]!.data)]))])) }
            session.until = max(session.until,o.ctx.deadline + 300000); session.seen[slot] = Seen(hash:o.hash,reply:output,until:o.ctx.deadline + 300000); sessions[k] = session
            return output
        } catch { return nil }
    }
    static func capabilityExpiry(_ o: W.Opened, _ now: UInt64) throws {
        let f = try PeerSchema.fields(o.payload,o.type == 14 ? "CapabilitySnapshot" : "Capabilities"), expires = f[o.type == 14 ? 3 : 4]!.value
        try W.check(expires > now && expires > o.ctx.issued && expires - o.ctx.issued <= 60000)
    }
}
