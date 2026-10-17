import Foundation
import RAProto
import SwiftProtobuf
import Testing
@testable import RACore

@Suite struct ProjectionVectorsTests {
    private func normalize(_ events: [PBEvent], _ d: Derived) -> NSDictionary {
        func seqs(_ xs: some Sequence<UInt64>) -> [String] { xs.sorted().map(String.init) }
        let decisions = approvalDecisions(events, d: d)
        let result: [String: Any] = [
            "running": d.running,
            "approvals": d.approvals.entries.map(\.key),
            "resolved": d.resolved.mapValues { $0.decision.rawValue },
            "questions": d.questions.entries.map(\.key), "answered": d.answered,
            "inputs": d.inputs.entries.map { id, status -> [String: Any] in
                let st = d.inputDetails[id]!
                return ["id": id, "status": status.rawValue, "stage": (d.accepted[id]?.stage ?? st.stage).rawValue,
                        "mode": st.mode.rawValue, "paused": st.paused]
            },
            "accepted": d.accepted.mapValues { a -> [String: Any] in
                ["text": a.text, "images": a.imageIds, "origin": a.origin?.kind.rawValue ?? 0]
            },
            "failures": d.failures, "queue": pendingInputs(d).map(\.id), "plan": d.plan?.text as Any? ?? NSNull(),
            "unsent": d.unsent.entries.map(\.key).sorted(), "local": seqs(d.typedLocally),
            "runningTools": seqs(d.tools.map(\.seq)), "unknownTools": seqs(d.unfinished),
            "interruptedTools": seqs(d.unfinished.subtracting(d.noResult)),
            "completed": Dictionary(uniqueKeysWithValues: d.completedAt.map { (String($0.key), String($0.value)) }),
            "background": seqs(d.background.map { $0.tool.seq }),
            "compactions": Dictionary(uniqueKeysWithValues: d.compactions.map { seq, c in
                (String(seq), [c.before.map { $0 as Any } ?? NSNull(), c.after.map { $0 as Any } ?? NSNull()])
            }),
            "terminalChanges": seqs(d.terminalChanges.keys), "merged": decisions.merged.sorted(),
            "callDecisions": seqs(decisions.onCall.keys),
        ]
        return result as NSDictionary
    }

    @Test func sharedProjectionVectors() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let data = try Data(contentsOf: root.appendingPathComponent("testdata/projection/vectors.json"))
        let corpus = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(corpus["projectionVersion"] as? Int == 1)
        for v in try #require(corpus["vectors"] as? [[String: Any]]) {
            let name = try #require(v["name"] as? String)
            let agent = try #require(PBAgent(rawValue: v["agent"] as! Int))
            let events = try (v["events"] as! [[String: Any]]).map {
                try PBEvent(jsonUTF8Data: JSONSerialization.data(withJSONObject: $0))
            }
            for o in v["observations"] as! [[String: Any]] {
                let prefix = Array(events.prefix(Int(o["through"] as! String)!))
                let expected = o["state"] as! NSDictionary
                if let history = o["history"] as? NSDictionary {
                    let rows = buildChat(prefix, d: derive(prefix, agent: agent), machine: "test")
                    let asyncQuestions = rows.compactMap { row -> String? in
                        if case .asyncQuestion(let q) = row { return String(q.key.dropFirst()) }
                        return nil
                    }
                    #expect(["asyncQuestions": asyncQuestions] as NSDictionary == history, "\(name) async history")
                }
                if name == "background-uses-explicit-result-fact" {
                    let rows = buildChat(prefix, d: derive(prefix, agent: agent), machine: "test")
                    #expect(rows.contains { if case .tool(let t) = $0 { return !t.background }; return false })
                }
                #expect(normalize(prefix, derive(prefix, agent: agent)) == expected, "\(name) full through \(prefix.count)")
                for cut in (v["cuts"] as! [Int]).filter({ $0 <= prefix.count }) {
                    var r = EventReducer(agent: agent)
                    r.append(prefix.prefix(cut))
                    let held = r.snapshot()
                    var projection = EventProjection()
                    _ = projection.update(Array(prefix.prefix(cut)), agent: agent)
                    r.append(prefix.dropFirst(cut))
                    #expect(normalize(prefix, r.snapshot()) == expected, "\(name) cut \(cut)")
                    #expect(normalize(prefix, projection.update(prefix, agent: agent)) == expected, "\(name) projection \(cut)")
                    #expect(normalize(Array(prefix.prefix(cut)), held) == normalize(Array(prefix.prefix(cut)), derive(prefix.prefix(cut), agent: agent)))
                }
            }
        }
    }
}
