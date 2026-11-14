import Foundation
import RAProto
import Testing
@testable import RACore

extension Remote {
    func simulatePendingProbe() { generation += 1; conn = .connecting }
    func runRecoveryReadsWithoutMain() { running = true; conn = .connecting }
}
extension ServerTests {
    @Suite struct RecoveryLeg {
        private func world(_ body:(World) async throws -> Void) async throws {
            try await World.run(setUp:{w in
                try await w.daemon.putMachine("recovery-machine")
                try await w.daemon.putSession("s1",session("/work/example","history"))
                try await w.daemon.uploadBatch("s1",[agent("retained")])
                try await w.daemon.connect()
            },body)
        }
        @Test func historyPageSurvivesAnUnsuccessfulProbe() async throws {
            try await world {w in
                let entered=Signal(),release=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/GetEvents")==true {calls.increment();entered.set();_ = await release.wait()}
                    return(data,response)
                }){$0}
                let remote=try await w.client(http:http);remote.start()
                _ = try await remote.awaitState("ready"){$0.loaded}
                let feed=remote.events("s1");#expect(await entered.wait())
                await remote.simulatePendingProbe();release.set()
                let result=try await feed.await("history after probe",ms:1500){$0.loaded}
                #expect(result.events.count==1);#expect(calls.value==1)
            }
        }
        @Test func readyCatchupSurvivesAProbeOnTheSameActiveLeg() async throws {
            try await world {w in
                let entered=Signal(),release=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/ListRemoteSessions")==true && calls.increment()==1 {entered.set();_ = await release.wait()}
                    return(data,response)
                }){$0}
                let remote=try await w.client(http:http)
                remote.start();#expect(await entered.wait())
                await remote.simulatePendingProbe();release.set()
                try await eventually("Ready preferences after probe",ms:1500){await remote.prefs != nil}
            }
        }
        @Test(arguments:["ListRemoteSessions","GetAccountPrefs"])
        func ownReadRetryDoesNotNeedAnotherMainOnline(_ method:String) async throws {
            try await World.run(setUp:{_ in}) {w in
                let calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/"+method)==true && calls.increment()==1 {
                        return(Data("{\"code\":\"unavailable\"}".utf8),HTTPURLResponse(url:q.url!,statusCode:503,httpVersion:nil,headerFields:["Content-Type":"application/json"])!)
                    }
                    return(data,response)
                }){$0}
                let remote=try await w.client(http:http);await remote.runRecoveryReadsWithoutMain()
                if method=="GetAccountPrefs" {await remote.loadPrefs()} else {_ = await remote.refresh()}
                #expect(calls.value==1)
                try await eventually("independent retry during main recovery",ms:3500){calls.value>=2}
            }
        }
    }
}
