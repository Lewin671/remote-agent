import Foundation
import RAProto
import SwiftProtobuf
import Testing

@testable import RACore

@Suite struct TerminalControlVectorsTests {
    private func hexToData(_ hex: String) -> Data {
        var out = Data(capacity: hex.count / 2)
        var i = hex.startIndex
        while i < hex.endIndex {
            let next = hex.index(i, offsetBy: 2)
            out.append(UInt8(hex[i..<next], radix: 16)!)
            i = next
        }
        return out
    }

    @Test(.timeLimit(.minutes(1))) func sharedPhase3ControlAndProbeVectors() throws {
        let json = try loadJSON("testdata/transport/terminal/control-vectors.json")
        let controls = try #require(json["controls"] as? [[String: Any]])
        #expect(controls.count >= 11)

        for v in controls {
            let name = try #require(v["name"] as? String)
            let msgName = try #require(v["message"] as? String)
            let raw = hexToData(try #require(v["wire_hex"] as? String))
            let wantValid = try #require(v["valid"] as? Bool)
            let wantStream = (v["stream"] as? String) ?? ""

            var ok = false
            var stream = ""
            switch msgName {
            case "AttachPath":
                let m = try PBAttachPath(serializedBytes: raw)
                ok = m.unknownFields.data.isEmpty
                    && m.stream.count >= 16
                    && m.session.count >= 16
                    && m.sender.count == 32
                    && m.legID.count == 16
                    && m.pathGeneration >= 1
                    && m.hasState
                stream = String(decoding: m.stream, as: UTF8.self)
            case "InputStatus":
                let m = try PBPeerInputStatus(serializedBytes: raw)
                ok = m.unknownFields.data.isEmpty
                    && m.stream.count >= 16
                    && m.queryID.count == 16
                    && m.fromSeq >= 1
                    && m.limit >= 1 && m.limit <= 128
                stream = String(decoding: m.stream, as: UTF8.self)
            case "InputStatusReply":
                let m = try PBInputStatusReply(serializedBytes: raw)
                ok = m.unknownFields.data.isEmpty
                    && m.stream.count >= 16
                    && m.queryID.count == 16
                    && (!m.available || (m.snapshotToken.count == 32 && m.hasState))
                stream = String(decoding: m.stream, as: UTF8.self)
            case "Ack":
                let m = try PBAck(serializedBytes: raw)
                ok = m.unknownFields.data.isEmpty
                    && m.stream.count >= 16
                    && m.direction != .unspecified
                    && m.hasState
                    && m.acknowledgedSha256.isEmpty
                stream = String(decoding: m.stream, as: UTF8.self)
            case "Replay":
                let m = try PBReplay(serializedBytes: raw)
                ok = m.unknownFields.data.isEmpty
                    && m.stream.count >= 16
                    && m.direction != .unspecified
                    && m.fromSeq >= 1
                    && m.maxBytes >= 1 && m.maxBytes <= (1 << 20)
                    && m.maxFrames >= 1 && m.maxFrames <= 128
                stream = String(decoding: m.stream, as: UTF8.self)
            case "Barrier":
                let m = try PBBarrier(serializedBytes: raw)
                ok = m.unknownFields.data.isEmpty
                    && m.stream.count >= 16
                    && m.barrierID.count == 16
                    && (m.stopAdmission || (m.newLegID.count == 16 && m.pathGeneration >= 1))
                stream = String(decoding: m.stream, as: UTF8.self)
            case "BarrierReply":
                let m = try PBBarrierReply(serializedBytes: raw)
                ok = m.unknownFields.data.isEmpty
                    && m.stream.count >= 16
                    && m.barrierID.count == 16
                    && (!m.final || m.accepted)
                    && (!m.accepted || m.hasState)
                stream = String(decoding: m.stream, as: UTF8.self)
            case "StreamFailed":
                let m = try PBStreamFailed(serializedBytes: raw)
                ok = m.unknownFields.data.isEmpty
                    && m.stream.count >= 16
                    && m.hasInputFailure
                    && m.hasState
                stream = String(decoding: m.stream, as: UTF8.self)
            case "ClosePath":
                let m = try PBClosePath(serializedBytes: raw)
                ok = m.unknownFields.data.isEmpty
                    && m.stream.count >= 16
                    && m.legID.count == 16
                    && m.pathGeneration >= 1
                stream = String(decoding: m.stream, as: UTF8.self)
            default:
                Issue.record("unexpected vector message \(msgName)")
            }
            #expect(ok == wantValid, "vector \(name) validity mismatch")
            if wantValid && !wantStream.isEmpty {
                #expect(stream == wantStream, "vector \(name) stream mismatch")
            }
        }

        let probes = try #require(json["probes"] as? [[String: Any]])
        #expect(probes.count >= 3)
        for p in probes {
            let name = try #require(p["name"] as? String)
            let raw = hexToData(try #require(p["wire_hex"] as? String))
            let wantValid = try #require(p["valid"] as? Bool)
            let kind = try #require(p["kind"] as? Int)
            let ok = raw.count == 17 && (raw[0] == 0 || raw[0] == 1)
            #expect(ok == wantValid, "probe \(name) validity mismatch")
            if wantValid {
                #expect(Int(raw[0]) == kind, "probe \(name) kind mismatch")
            }
        }
    }
}
