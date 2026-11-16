import Foundation

/// The Candidate/EndCandidates/SDP bodies a session sends and reads (web/src/transport/peer/signal.js).
enum PeerCandidates {
    struct Candidate { var original: PeerWire.Bytes; var ufrag: PeerWire.Bytes; var mid: PeerWire.Bytes; var index: UInt64; var generation: UInt64 }
    typealias W = PeerWire
    static func sdpBody(_ raw: W.Bytes) -> W.Bytes { W.pb([W.b(1, raw), W.b(2, W.hash(raw))]) }
    static func candidateBody(_ list: [Candidate], _ index: Int) -> W.Bytes {
        var out = W.Bytes()
        for c in list {
            let raw = W.pb([W.b(1, c.original), W.b(2, c.ufrag), W.b(3, c.mid), W.u(4, c.index), W.u(5, c.generation)])
            out += [10] + W.vint(UInt64(raw.count)) + raw
        }
        return out + W.pb([W.u(2, UInt64(index))])
    }
    static func endBody(_ ufrag: W.Bytes, _ count: Int, _ batches: Int) -> W.Bytes { W.pb([W.b(1, ufrag), W.u(2, UInt64(count)), W.u(3, UInt64(batches))]) }
    static func read(_ payload: W.Bytes) throws -> [Candidate] {
        let bytes = try PeerSchema.normalize(payload, "Candidates"); var at = 0, list: [Candidate] = []
        while at < bytes.count && bytes[at] == 10 {
            at += 1
            let size = Int(try PeerSchema.integer(bytes, &at))
            try W.check(size <= 1024 && size <= bytes.count - at && list.count < 16)
            let f = try PeerSchema.fields(Array(bytes[at..<at + size]), "Candidate"); at += size
            try W.check(f[1]?.bytes == true && f[2]?.bytes == true && f[3]?.bytes == true && f[4]?.bytes == false && f[5]?.bytes == false)
            guard let original = String(bytes: f[1]!.data, encoding: .utf8), original.hasPrefix("candidate:"), !original.contains("\0"), !original.contains("\r"), !original.contains("\n") else { throw W.Invalid.packet }
            try W.check(f[1]!.data.allSatisfy { $0 >= 32 && $0 <= 126 })
            list.append(Candidate(original: f[1]!.data, ufrag: f[2]!.data, mid: f[3]!.data, index: f[4]!.value, generation: f[5]!.value))
        }
        return list
    }
}
