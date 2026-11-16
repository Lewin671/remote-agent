import CryptoKit
import Foundation
import Testing
@testable import RACore

/// The shared frozen vectors (testdata/transport) against the Swift signalling state machine,
/// sender and SDP validator: the same files the Go, web and Android implementations consume.
@Suite struct PeerSignalVectorsTests {
 typealias W = PeerWire
 static let base: UInt64 = 1700000000000
 static func identity(_ name: String, _ role: UInt64) throws -> W.Identity {
  let sign=try Curve25519.Signing.PrivateKey(rawRepresentation:W.hash(W.utf8("lab/sign/"+name))),box=try Curve25519.KeyAgreement.PrivateKey(rawRepresentation:W.hash(W.utf8("lab/box/"+name)))
  return W.Identity(peer:W.Public(id:W.utf8(W.hex(Array(W.hash(Array(sign.publicKey.rawRepresentation)).prefix(16)))),instance:Array(W.hash(W.utf8("lab/instance/"+name)).prefix(16)),boot:W.hash(W.utf8("lab/boot/"+name)),role:role,sign:sign.publicKey,box:box.publicKey),sign:sign,box:box)
 }
 static var scope: W.Scope { W.Scope(account:W.utf8("0123456789abcdef0123456789abcdef"),accountDomain:W.hash(W.utf8("lab/account-domain")),head:W.hash(W.utf8("lab/member-head/1")),epoch:1) }
 static let codec = PeerCodec(scope: scope)

 @Test func sdpVectors() throws {
  let root = repoRoot.appendingPathComponent("testdata/transport/sdp")
  var count = 0
  for row in try String(contentsOf: root.appendingPathComponent("vectors.tsv"), encoding: .utf8).split(separator: "\n") {
   let parts = row.split(separator: "\t"), data = try Data(contentsOf: root.appendingPathComponent(String(parts[0])))
   var valid = true; do { try PeerSdp.validate(data) } catch { valid = false }
   #expect(valid == (parts[1] == "1"), "\(parts[0])"); count += 1
  }
  #expect(count == 45)
 }

 @Test func signalScenarios() throws {
  let rows = try String(contentsOf: repoRoot.appendingPathComponent("testdata/transport/signal/scenarios.tsv"), encoding: .utf8).split(separator: "\n")
  var gates: [PeerSignal] = [], cases = 0, assertions = 0, batch: PeerCandidateBatcher?, sender: PeerSender?, queued: W.Bytes = []
  var ids: [String: W.Identity] = [:]
  func ident(_ name: String, _ role: String) throws -> W.Identity { let k = name + role; if let v = ids[k] { return v }; let v = try Self.identity(name, UInt64(role)!); ids[k] = v; return v }
  func check(_ ok: Bool, _ what: @autoclosure () -> String, line: Int = #line) { assertions += 1; #expect(ok, Comment(rawValue: what()), sourceLocation: SourceLocation(fileID: #fileID, filePath: #filePath, line: line, column: 1)) }
  let t0 = Self.base, codec = Self.codec, scope = Self.scope
  for row in rows {
   let p = row.split(separator: "\t", omittingEmptySubsequences: false).map(String.init)
   switch p[0] {
   case "BODY": var ok = true; do { _ = try PeerSchema.known(UInt64(p[1])!, UInt64(p[2])!, W.unhex(p[3])) } catch { ok = false }; check(ok == (p[4] == "1"), "BODY \(cases)")
   case "SCHEMA": var got: String; do { got = W.hex(try PeerSchema.normalize(W.unhex(p[2]), p[1])) } catch { got = "!" }; check(got == p[3], p[1])
   case "AUTH": gates[Int(p[1])!].setAuthorization(t0 + UInt64(p[2])!, p[3] == "1", UInt64(p[4])!)
   case "SEND_AUTH": sender!.setAuthorization(UInt64(p[1])!, p[2] == "1", UInt64(p[3])!)
   case "REPLY": check(gates[Int(p[1])!].replyAllowed(W.unhex(p[4]), t0 + UInt64(p[2])!) == (p[3] == "1"), "REPLY \(cases)")
   case "CASE":
    let a = try ident(p[2], p[3]), b = try ident(p[4], p[5]), conn = Array(W.hash(W.utf8("signal/" + p[1])).prefix(16))
    gates = [PeerSignal(codec, a, b.peer, conn, UInt64(p[6])!, t0), PeerSignal(codec, b, a.peer, conn, UInt64(p[6])!, t0)]; cases += 1
   case "BATCH_CASE": batch = PeerCandidateBatcher(UInt64(p[2])!, UInt64(p[3])!, UInt64(p[4])!); cases += 1
   case "BATCH_ADD": check(batch!.add(W.unhex(p[3]), UInt64(p[2])!, UInt64(p[1])!, 1) == (p[4] == "1"), "BATCH_ADD")
   case "BATCH_FLUSH": let g = batch!.flush(UInt64(p[1])!, p[2] == "1", 2, 1); check(g.map { String($0.count) }.joined(separator: ",") == p[3] && W.hex(g.flatMap { $0 }.flatMap { $0 }) == p[4], "BATCH_FLUSH")
   case "DISPATCH": check(gates[Int(p[1])!].dispatch(W.unhex(p[4]), t0 + UInt64(p[2])!) == (p[3] == "1"), "DISPATCH \(cases)")
   case "FORGET": gates[Int(p[1])!].forgetPeer()
   case "QUEUE": queued = W.unhex(p[2]); _ = try codec.open(queued, gates[Int(p[1])!].peer, gates[Int(p[1])!].selfID, t0)
   case "DELIVER": var ok = true; do { _ = try gates[Int(p[1])!].receive(queued, t0 + UInt64(p[2])!) } catch { ok = false }; check(ok == (p[3] == "1"), "DELIVER \(cases)")
   case "SENDER_CASE": sender = PeerSender(UInt64(p[2])!); cases += 1
   case "SEND_ADD": check(sender!.enqueue(W.unhex(p[4]), UInt64(p[1])!, UInt64(p[2])!, UInt64(p[3])!) == (p[5] == "1"), "SEND_ADD")
   case "SEND_TICK": check(sender!.tick(UInt64(p[1])!).count == Int(p[2])!, "SEND_TICK")
   case "SEND_REBIND": sender!.rebind(UInt64(p[1])!)
   case "SEND_LIMIT": sender!.limited(W.hash(W.unhex(p[3])), UInt64(p[1])!, UInt64(p[2])!)
   case "SEND_ADMIT": check(sender!.admit(W.unhex(p[2]), UInt64(p[1])!) == (p[3] == "1"), "SEND_ADMIT")
   case "SEND_PENDING": check(sender!.pending.count == Int(p[1])!, "SEND_PENDING")
   case "BATCH_BOUND_FLUSH": check(batch!.flush(UInt64(p[1])!, p[2] == "1", UInt64(p[3])!, UInt64(p[4])!).isEmpty && batch!.pending.isEmpty, "BATCH_BOUND_FLUSH")
   case "POLICY":
    let request = try codec.open(W.unhex(p[3]), gates[0].selfID.peer, gates[1].selfID, t0), reply = try codec.open(W.unhex(p[4]), gates[1].selfID.peer, gates[0].selfID, t0)
    var ok = true; do { try W.validatePolicyReply(request, reply, t0 + UInt64(p[5])!) } catch { ok = false }; check(ok == (p[2] == "1"), p[1])
   case "RECORD":
    let who = Int(p[2])!; _ = gates[who].tick(t0 + UInt64(p[3])!)
    let record = try codec.open(W.unhex(p[4]), gates[who].selfID.peer, gates[1 - who].selfID, t0); var ok = true
    do { if p[1] == "probe" { try gates[who].rememberProbe(record) } else { try gates[who].rememberChallenge(record) } } catch { ok = false }
    check(ok == (p[5] == "1"), "RECORD \(cases)")
   case "PROBE", "CHALLENGE":
    let which = Int(p[1])!, sent = try codec.open(W.unhex(p[2]), gates[which].selfID.peer, gates[1 - which].selfID, t0)
    if p[0] == "PROBE" { try gates[which].rememberProbe(sent) } else { try gates[which].rememberChallenge(sent) }
   case "RX": var ok = false; do { _ = try gates[Int(p[1])!].receive(W.unhex(p[4]), t0 + UInt64(p[2])!); ok = true } catch {}; check(ok == (p[3] == "1"), "RX \(cases)")
   case "LOCAL": var ok = false; do { try gates[Int(p[1])!].setLocal(W.unhex(p[5]), p[3] == "1", UInt64(p[2])!); ok = true } catch {}; check(ok == (p[4] == "1"), "LOCAL \(cases)")
   case "DTLS": gates[Int(p[1])!].dtls = true
   case "NEXT": try gates[Int(p[1])!].nextGeneration(UInt64(p[2])!)
   case "TICK": _ = gates[Int(p[1])!].tick(t0 + UInt64(p[2])!)
   case "EXPECT":
    let s = gates[Int(p[1])!]
    let values: [String: UInt64] = ["seen": UInt64(s.seen.count), "dispatches": UInt64(s.dispatches), "peer_known": s.peer.boot.count == 32 ? 1 : 0, "stale": UInt64(s.staleChallenges), "boot": s.bootReady ? 1 : 0, "gap_ms": s.gapDeadline == 0 ? 0 : s.gapDeadline - t0, "sdk": UInt64(s.sdkActions), "duplicates": UInt64(s.duplicates), "conflicts": UInt64(s.conflicts), "unsupported": UInt64(s.unsupported), "pending": UInt64(s.pending.count), "ready": s.ready ? 1 : 0, "closed": s.closed ? 1 : 0, "generation": s.generation, "next": s.next]
    check(values[p[2]] == UInt64(p[3])!, "\(p[2]) \(cases)")
   default: Issue.record("unknown row \(p[0])")
   }
  }
  _ = scope
  #expect(cases > 100); #expect(assertions > 1500)
 }

 @Test func cryptoVectors() throws {
  let corpus = try loadJSON("testdata/transport/wire/vectors.json"), a = try Self.identity(corpus.s("sender"), 1), b = try Self.identity(corpus.s("receiver"), 2)
  var accepted = 0
  for row in corpus["rows"] as! [[String: Any]] {
   let result = try? W.open(Self.scope, W.unhex(row.s("packet_hex")), a.peer, b, Self.base)
   #expect((result != nil) == (row["accept"] as! Bool), "\(row.s("name"))")
   if let result { #expect(W.hex(result.payload) == row.s("payload_hex")); accepted += 1 }
  }
  #expect(accepted == 5)
 }
}
