import Foundation
import RAProto
import Testing
import os
@testable import RACore

@Suite struct RecoveryTests {
    @Test func creditAndFullJitterSurviveNotifications() {
        let time=OSAllocatedUnfairLock(initialState:Int64(0))
        let budget=RecoveryBudget()
        let a=Recovery(now:{time.withLock{$0}},random:{0.5},budget:budget)
        let b=Recovery(now:{time.withLock{$0}},random:{0.5},budget:budget)
        #expect(a.take());#expect(a.take());#expect(!a.take())
        a.ready();#expect(!a.take())
        #expect(b.take());#expect(b.take());#expect(!b.take())
        #expect(a.delay()==1000)
        time.withLock{$0=2000}
        for cap in [Int64(1000),2000,4000,8000,16000,30000,30000] {
            a.failed()
            for _ in 0..<1000 {#expect(a.delay()==cap/2)}
            time.withLock{$0+=cap/2}
        }
    }
    @Test func readyRetiresOnlyTheHTTPWaiter() async throws {
        let r=Recovery(budget:RecoveryBudget()), entered=Signal(), stopped=Signal()
        let pending=Task {
            do {
                _ = try await r.follow {
                    entered.set();defer{stopped.set()}
                    try await Task.sleep(for:.seconds(30));return 1
                }
                return false
            } catch is PathChanged {return true} catch {return false}
        }
        #expect(await entered.wait())
        r.ready()
        #expect(await pending.value);#expect(stopped.isSet)
    }
}

extension ServerTests {
    @Suite struct RecoveryIntegration {
        @Test func temporaryDaemonGapKeepsTheOriginalLiveCallPending() async throws {
            try await World.run(setUp:{w in
                try await w.daemon.connect()
                await w.daemon.setAnswer{_,_ in .with{$0.getSettings=PBGetSettingsResponse()}}
            }) {w in
                let bodies=OSAllocatedUnfairLock(initialState:[PBForwardRequest]())
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    if request.url?.path.hasSuffix("/Forward")==true,let raw=request.httpBody {
                        let command=try PBForwardRequest(serializedBytes:raw)
                        let first=bodies.withLock{list in list.append(command);return list.count==1}
                        if first {return (Data("{\"code\":\"unavailable\"}".utf8),HTTPURLResponse(url:request.url!,statusCode:503,httpVersion:"HTTP/1.1",headerFields:["Content-Type":"application/json"])!)}
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                remote.start();_ = try await remote.awaitState("ready"){$0.conn == .online && $0.loaded}
                _ = try await remote.machineCommand(w.daemon.id,PBCommand.with{$0.getSettings=PBGetSettingsRequest()})
                let received=bodies.withLock{$0}
                #expect(received.count==2)
                if received.count==2 {#expect(received[0].command==received[1].command)}
            }
        }
        @Test func supersededSecurityHTTPDoesNotBlockTheNewRound() async throws {
            try await World.run(setUp:{w in try await w.daemon.connect()}) {w in
                let held=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    if request.url?.path.hasSuffix("/GetSecurityStates")==true && calls.increment()==1 {
                        held.set();try await Task.sleep(for:.seconds(30))
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                let security=try await remote.securitySession()
                let first=Task {try await security.sync()}
                #expect(await held.wait())
                await security.networkChanged()
                let record=try await withTimeout(2000,sleep:realSleep){try await first.value}
                #expect(record.member)
                #expect(calls.value==2)
            }
        }
        @Test func supersededLoginKeepsItsCallerAndRefreshesTheNewPath() async throws {
            try await World.run(setUp:{w in try await w.daemon.connect()}) {w in
                let hold=Signal(),entered=Signal(),challenges=Counter()
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    if request.url?.path.hasSuffix("/GetChallenge")==true && hold.isSet && challenges.increment()==1 {
                        entered.set();try await Task.sleep(for:.seconds(30))
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                remote.networkChanged("initial",available:true)
                remote.start();_ = try await remote.awaitState("ready"){$0.conn == .online && $0.loaded}
                await remote.expireRecoveryTestLogin()
                hold.set()
                let pending=Task {try await remote.currentToken()}
                #expect(await entered.wait())
                remote.networkChanged("new-route",available:true)
                let token=try await withTimeout(2000,sleep:realSleep){try await pending.value}
                #expect(!token.isEmpty && challenges.value==2)
                #expect(!remote.current.removed)
            }
        }
        @Test func retainedHTTPIsWokenWithTheOriginalAuthorization() async throws {
            try await World.run(setUp:{w in
                try await w.daemon.connect()
                await w.daemon.setAnswer {_,_ in .with{$0.getSettings=PBGetSettingsResponse()}}
            }) {w in
                let requests=OSAllocatedUnfairLock(initialState:[PBForwardRequest]())
                let held=Signal()
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    if request.url?.path.hasSuffix("/Forward")==true,
                       let body=request.httpBody {
                        let command=try PBForwardRequest(serializedBytes:body)
                        let first=requests.withLock {list in list.append(command);return list.count==1}
                        if first {held.set();try await Task.sleep(for:.seconds(30))}
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                remote.start();_ = try await remote.awaitState("ready"){$0.conn == .online && $0.loaded}
                let pending=Task {try await remote.machineCommand(w.daemon.id,PBCommand.with{$0.getSettings=PBGetSettingsRequest()})}
                #expect(await held.wait())
                let started=ContinuousClock.now
                remote.reconnectNow()
                _ = try await withTimeout(5000,sleep:realSleep){try await pending.value}
                let recorded=requests.withLock{$0}
                #expect(recorded.count==2)
                if recorded.count==2 {
                    #expect(recorded[0].command==recorded[1].command)
                    #expect(recorded[0].attemptID != recorded[1].attemptID)
                }
                #expect(started.duration(to:.now) < .seconds(5))
            }
        }
        @Test func silentMainStreamIsReplacedWithoutAnEOF() async throws {
            try await World.run(setUp:{w in
                try await w.daemon.connect()
                await w.daemon.setAnswer {_,_ in .with{$0.getSettings=PBGetSettingsResponse()}}
            }) {w in
                let account=try await w.join()
                let hole=try StreamBlackhole(firstReady:true)
                w.onClose{hole.close()}
                let real=w.server.url
                let http=Intercept.http(for:hole.url){$0.moved(to:real)}
                let remote=try await w.remote(Account(server:hole.url,root:account.root,deviceSeed:account.deviceSeed,deviceId:account.deviceId),http:http)
                remote.start();_ = try await remote.awaitState("first ready"){$0.conn == .online}
                let old=await remote.ws
                let started=ContinuousClock.now
                try await eventually("silent recovery",ms:6000){await remote.ws !== old && remote.current.conn == .online}
                _ = try await remote.machineCommand(w.daemon.id,PBCommand.with{$0.getSettings=PBGetSettingsRequest()})
                #expect(hole.attempts.value==2)
                #expect(started.duration(to:.now) < .seconds(6))
            }
        }
        @Test func candidateCannotRetireTheActivePathBeforeReady() async throws {
            try await World.run(setUp:{w in try await w.daemon.connect()}) {w in
                let gate=DispatchSemaphore(value:0)
                defer {gate.signal()}
                let proxy=try CountingProxy(to:w.server.port,holdReplacement:gate)
                w.onClose{proxy.close()}
                let account=try await w.join()
                let remote=try await w.remote(Account(server:proxy.url,root:account.root,deviceSeed:account.deviceSeed,deviceId:account.deviceId))
                remote.start();_ = try await remote.awaitState("ready"){$0.conn == .online}
                let old=await remote.ws
                remote.reconnectNow()
                try await eventually("candidate handshake"){proxy.streams.value==2}
                #expect(await remote.ws === old)
                #expect(remote.current.conn == .online)
                gate.signal()
                try await eventually("replacement Ready"){await remote.ws !== old}
                #expect(remote.current.conn == .online)
            }
        }

        @Test func wakeAndDuplicateHintsKeepOnlyTheLatestGeneration() async throws {
            try await World.run(setUp:{w in try await w.daemon.connect()}) {w in
                let remote=try await w.client()
                remote.networkChanged("initial-route",available:true)
                remote.start();_ = try await remote.awaitState("ready"){$0.conn == .online}
                let old=await remote.ws
                for _ in 0..<1000 {remote.networkChanged("same-interface-new-address",available:true)}
                try await eventually("network replacement"){await remote.ws !== old}
                let generation=await remote.networkGeneration
                #expect(generation==1)
                for _ in 0..<10 {remote.networkChanged("same-interface-new-address",available:true);await Task.yield()}
                await sleepMs(100)
                #expect(await remote.networkGeneration==generation)
                let before=await remote.ws
                let now=await remote.livenessTime()
                _ = await remote.livenessTick((now.0-10000,now.1-10000))
                try await eventually("wake replacement"){await remote.ws !== before}
                #expect(await remote.networkGeneration==generation+1)
            }
        }
    }
}

extension Remote {
    func expireRecoveryTestLogin() {token="";tokenExpires=0}
}
