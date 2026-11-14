import Foundation
import Testing
@testable import RACore

struct DispatchStateTests {
    @Test func ownershipVectors() throws {
        let data=try Data(contentsOf:repoRoot.appendingPathComponent("testdata/transport/dispatch.json"))
        let rows=try JSONSerialization.jsonObject(with:data) as! [[String:Any]]
        for row in rows {
            let name=row["name"] as! String
            if let only=ProcessInfo.processInfo.environment["RA_DISPATCH_CASE"],!only.isEmpty,only != name {continue}
            let state=DispatchState(prior:row["prior"] as! Bool)
            var calls:[String:DispatchState.Call]=[:]
            var deliveries:[String:DispatchState.Call.Delivery]=[:]
            for step in row["steps"] as! [[String:Any]] {
                let call=step["call"] as? String ?? "",delivery=step["delivery"] as? String ?? ""
                switch step["op"] as! String {
                case "open":calls[call]=state.open(prior:false)
                case "delivery":deliveries[delivery]=calls[call]!.delivery()
                case "dispatch":try deliveries[delivery]!.dispatch()
                case "unsent":deliveries[delivery]!.notForwarded()
                case "close":calls[call]!.close()
                case "check":
                    #expect(calls[call]!.uncertain == step["uncertain"] as! Bool, "\(name)")
                    #expect(calls[call]!.hasPrior == step["hasPrior"] as! Bool, "\(name)")
                default:Issue.record("unknown step")
                }
            }
        }
    }
}
