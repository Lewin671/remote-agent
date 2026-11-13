import Foundation
import RAProto
import SwiftProtobuf
import Testing

@testable import RACore

struct OutcomeTests {
    @Test func sharedVectors() throws {
        let fixture = try loadJSON("testdata/transport/outcomes.json")
        let vectors = fixture["vectors"] as! [[String: Any]]
        #expect(fixture["protocol"] as? Int == 14)
        #expect(vectors.count == 552)
        for v in vectors {
            let source = v.s("source"), code = v.s("code")
            let signed = v["signed"] as! Bool, prior = v["prior"] as! Bool
            let readOnly = v["readOnly"] as! Bool
            let unknown = v.s("outcome") == "unknown"
            let result = classifyOutcome(source: source, code: code, signed: signed, prior: prior, readOnly: readOnly)
            #expect(result.rawValue == v.s("outcome"))
            if source == "local" || (source == "daemon" && signed) {
                let error = CommandError(code, "fixture", fromDaemon: source == "daemon", readOnly: readOnly)
                // These wrappers and exact retry identity used to be checked
                // again in hand-written matrices. Keep them on shared vectors.
                if !prior { #expect(uncertain(error) == unknown) }
                let attempt = Attempt()
                let first = try attempt.next {
                    PreparedCommand(machineId: "machine", sessionId: "", requestId: "fixture-request", deadlineMs: 1000, command: PBCommand())
                }
                if prior { attempt.settle(CommandError(CommandError.unknownOutcome, "fixture")) }
                attempt.settle(error)
                #expect(attempt.frozen == unknown)
                if attempt.frozen && !attempt.expired {
                    #expect(try attempt.next { throw TestFailure("an unknown retry was rebuilt") } === first)
                }
            }
        }
    }

    @Test func commandEvidence() throws {
        let fixture = try loadJSON("testdata/transport/outcomes.json")
        for v in fixture["commands"] as! [[String: Any]] {
            let kind = v.s("kind")
            let body: [String: Any] = kind == "schedule" ? ["operation": v.s("operation")] : [:]
            let object: [String: Any] = kind.isEmpty ? [:] : [kind: body]
            let cmd = try PBCommand(jsonUTF8Data: JSONSerialization.data(withJSONObject: object))
            let readOnly = cmd.isReadOnlyCommand
            #expect(readOnly == v["readOnly"] as! Bool)
            for code in ["canceled", "deadline_exceeded", "aborted"] {
                #expect(CommandError(code, "fixture", fromDaemon: true, readOnly: readOnly).unknown == !readOnly)
            }
        }
    }
}
