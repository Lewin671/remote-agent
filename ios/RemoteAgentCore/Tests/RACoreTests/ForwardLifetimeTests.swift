import Foundation
import RAProto
import Testing
import os
@testable import RACore

extension ServerTests {
    @Suite struct ForwardLifetime {
        @Test func repeatedReadyCannotExtendTheOriginalForwardLifetime() async throws {
            try await World.run(setUp:{w in
                try await w.daemon.connect()
                await w.daemon.setAnswer {_,_ in .with{$0.getSettings=PBGetSettingsResponse()}}
            }) {w in
                let now=OSAllocatedUnfairLock(initialState:Int64(Date().timeIntervalSince1970*1000))
                let entered=(0..<8).map{_ in Signal()},requests=OSAllocatedUnfairLock(initialState:[PBForwardRequest]()),completed=Signal()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/Forward")==true,let bytes=q.httpBody {
                        let request=try PBForwardRequest(serializedBytes:bytes)
                        let index=requests.withLock{list in list.append(request);return list.count-1}
                        if index<entered.count {entered[index].set()}
                        try await Task.sleep(for:.seconds(30))
                    }
                    return(data,response)
                }){$0}
                let remote=try fixtureRemote(account:try await w.join(),http:http,clock:{now.withLock{$0}})
                w.onClose{remote.close()}
                let p=try remote.prepare(machineId:w.daemon.id,sessionId:"",PBCommand.with{$0.getSettings=PBGetSettingsRequest()})
                let task=Task {defer{completed.set()};do{_ = try await remote.forward(p);return false}catch let e as CommandError{return e.outcome == .unknown}catch{return false}}
                for index in 0..<7 {
                    #expect(await entered[index].wait())
                    now.withLock{$0+=100_000};await remote.recovery.ready()
                }
                let ended=await completed.wait(1500);#expect(ended,"Forward exceeded original deadline plus retention")
                task.cancel();#expect(await task.value)
                let sent=requests.withLock{$0};#expect(sent.count==7)
                #expect(sent.allSatisfy{$0.command == sent.first?.command})
                #expect(Set(sent.map(\.attemptID)).count==sent.count)
            }
        }

        @Test func aBlackholedDeliveryExpiresWithoutAnotherReady() async throws {
            try await World.run(setUp:{w in try await w.daemon.connect()}) {w in
                let entered=Signal(),completed=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/Forward")==true {calls.increment();entered.set();try await Task.sleep(for:.seconds(30))}
                    return(data,response)
                }){$0}
                let remote=try await w.client(http:http)
                _ = try await remote.currentToken()
                let p=PreparedCommand(machineId:w.daemon.id,sessionId:"",requestId:newRequestId(),
                    deadlineMs:Int64(Date().timeIntervalSince1970*1000)-resultRetentionMs+1000,
                    command:PBCommand.with{$0.protocolVersion=protocolVersion;$0.getSettings=PBGetSettingsRequest()})
                let task=Task {defer{completed.set()};do{_ = try await remote.forward(p,retryOfUnknown:true);return false}catch let e as CommandError{return e.outcome == .unknown}catch{return false}}
                #expect(await entered.wait());#expect(await completed.wait(2500),"blackholed delivery ignored its owner cutoff")
                task.cancel();#expect(await task.value);#expect(calls.value==1)
            }
        }

        @Test func aLoginTimeoutIsNotMistakenForTheForwardCutoff() async throws {
            try await World.run(setUp:{_ in}) {w in
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/GetChallenge")==true {try await Task.sleep(for:.seconds(30))}
                    return(data,response)
                }){$0}
                let remote=try await w.client(http:http);await remote.setRecoveryLifetimeMode()
                let p=try remote.prepare(machineId:w.daemon.id,sessionId:"",PBCommand.with{$0.getSettings=PBGetSettingsRequest()})
                do {_ = try await remote.forward(p);Issue.record("login unexpectedly succeeded")}
                catch {#expect(error is TimedOut,"an unsent login error was relabeled as the Forward cutoff")}
                #expect(!p.mayHaveRun)
            }
        }

        @Test func recoveryCutoffCancelsOnlyTheAuthorizationWaiter() async throws {
            try await World.run(setUp:{_ in}) {w in
                let remote=try await w.client();_ = try await remote.currentToken()
                let hash=try await remote.authorizationStateHash(),release=Signal(),entered=Signal(),completed=Signal(),calls=Counter()
                let p=PreparedCommand(machineId:w.daemon.id,sessionId:"",requestId:newRequestId(),
                    deadlineMs:Int64(Date().timeIntervalSince1970*1000)-resultRetentionMs+300,
                    command:PBCommand.with{$0.protocolVersion=protocolVersion;$0.getSettings=PBGetSettingsRequest()})
                let owner=p.authorizationResult(stateHash:hash){_ in Task{calls.increment();entered.set();_ = await release.wait();return PBEnvelope()}}
                #expect(await entered.wait())
                let task=Task {defer{completed.set()};do{_ = try await remote.forward(p,retryOfUnknown:true);return false}catch let e as CommandError{return e.outcome == .unknown}catch{return false}}
                #expect(await completed.wait(1500),"shared authorization prevented the caller deadline from settling")
                release.set();#expect(await task.value)
                _ = try await owner.value
                _ = try await p.authorizationResult(stateHash:hash){_ in Task{calls.increment();return PBEnvelope()}}.value
                #expect(calls.value==1,"one expired waiter cleared shared authorization")
            }
        }
    }
}
