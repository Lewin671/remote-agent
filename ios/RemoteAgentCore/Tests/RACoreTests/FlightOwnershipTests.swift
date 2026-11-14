import Foundation
import RAProto
import Testing
@testable import RACore

@Suite struct SharedResultOwnershipTests {
    @Test func waiterCancellationDoesNotSettleTheSharedOwner() async throws {
        let result=SharedResult<Int>(),entered=Signal(),canceled=Signal()
        let first=Task {entered.set();do{_ = try await result.value}catch is CancellationError{canceled.set()}catch{}}
        #expect(await entered.wait());first.cancel()
        #expect(await canceled.wait(500))
        let second=Task {try await result.value}
        result.resolve(.success(42))
        #expect(try await second.value==42)
        await first.value
    }
}

extension ServerTests {
    @Suite struct FlightOwnershipIntegration {
        @Test func secondNetworkRetiresSecurityOwnedByTheSuspendedCandidate() async throws {
            try await World.run(setUp:{w in try await w.daemon.connect()}) { w in
                let armed=Signal(),entered=Signal(),stopped=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    if armed.isSet && request.url?.path.hasSuffix("/GetSecurityStates")==true && calls.increment()==1 {
                        entered.set();defer{stopped.set()};try await Task.sleep(for:.seconds(30))
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                remote.networkChanged("initial",available:true);remote.start()
                _ = try await remote.awaitState("initial ready"){$0.conn == .online && $0.loaded}
                armed.set();remote.networkChanged("first replacement",available:true)
                #expect(await entered.wait())
                await remote.blockOwnershipTestRecovery()
                remote.networkChanged("second replacement",available:true)
                #expect(await stopped.wait(500),"retiring a candidate left its old security HTTP alive")
                await remote.resumeOwnershipTestRecovery()
                remote.reconnectNow()
                try await eventually("replacement pull",ms:2000){calls.value>=2}
                _ = try await remote.awaitState("replacement ready"){$0.conn == .online}
            }
        }

        @Test func retiredMainLegCancelsItsSuspendedFrameHandler() async throws {
            try await World.run(setUp:{w in try await w.daemon.connect()}) {w in
                let entered=Signal(),stopped=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    // The first list belongs to the shared directory owner;
                    // the second is the Ready handler's own refresh request.
                    if request.url?.path.hasSuffix("/ListMachines")==true && calls.increment()==2 {
                        entered.set();defer{stopped.set()};try await Task.sleep(for:.seconds(30))
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                remote.start();#expect(await entered.wait())
                let old=await remote.ws;remote.reconnectNow()
                try await eventually("new main leg"){await remote.ws !== old}
                #expect(await stopped.wait(500),"retired frame processing still owns an HTTP request")
                _ = try await remote.awaitState("loaded"){$0.conn == .online && $0.loaded}
            }
        }

        @Test func mainRefusalCannotDiscardANewerCredential() async throws {
            try await World.run(setUp:{_ in}) {w in
                let gate=DispatchSemaphore(value:0);defer{gate.signal()}
                let hole=try StreamBlackhole(firstClose:1008,firstReady:true,firstCloseAfter:gate)
                w.onClose{hole.close()}
                let account=try await w.join(), real=w.server.url
                let http=Intercept.http(for:hole.url){$0.moved(to:real)}
                let remote=try await w.remote(Account(server:hole.url,root:account.root,deviceSeed:account.deviceSeed,deviceId:account.deviceId),http:http)
                remote.start();_ = try await remote.awaitState("ready"){$0.conn == .online}
                let original=await remote.token,newer=try await remote.signIn()
                let changed=original != newer;#expect(changed)
                await remote.blockOwnershipTestRecovery();gate.signal()
                _ = try await remote.awaitState("old stream refused"){$0.conn == .offline}
                let kept=await remote.token==newer
                #expect(kept,"old stream refusal discarded the new credential")
            }
        }

        @Test func loginWaiterCanCancelWhileTheSameOwnerServesOthers() async throws {
            try await World.run(setUp: {_ in}) {w in
                let entered=Signal(),release=Signal(),canceled=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    if request.url?.path.hasSuffix("/GetChallenge")==true {
                        calls.increment();entered.set();_ = await release.wait()
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                let first=Task {do{_ = try await remote.currentToken()}catch is CancellationError{canceled.set()}catch{}}
                #expect(await entered.wait())
                first.cancel();#expect(await canceled.wait(500))
                let owner=await remote.loginFlight?.id
                #expect(owner != nil)
                let second=Task {try await remote.currentToken()}
                await Task.yield();#expect(await remote.loginFlight?.id==owner)
                release.set();#expect(!(try await second.value).isEmpty)
                await first.value;#expect(calls.value==1)
            }
        }

        @Test func aLoginWithNoRemainingWaitersStillPublishesItsCredential() async throws {
            try await World.run(setUp: {_ in}) {w in
                let entered=Signal(),release=Signal(),canceled=Signal(),settled=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    if request.url?.path.hasSuffix("/GetChallenge")==true {
                        calls.increment();entered.set();_ = await release.wait()
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                remote.keepLogin(nil,onChange:{if $0 != nil {settled.set()}})
                let first=Task {do{_ = try await remote.currentToken()}catch is CancellationError{canceled.set()}catch{}}
                #expect(await entered.wait());first.cancel();#expect(await canceled.wait(500))
                release.set();#expect(await settled.wait())
                #expect(await !remote.token.isEmpty,"credential installation belonged to the canceled waiter")
                #expect(!(try await remote.currentToken()).isEmpty);#expect(calls.value==1)
                await first.value
            }
        }

        @Test func candidateDoesNotRetireAnIndependentLogin() async throws {
            try await World.run(setUp: {_ in}) {w in
                let entered=Signal(),release=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    if request.url?.path.hasSuffix("/GetChallenge")==true {
                        calls.increment();entered.set();_ = await release.wait()
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                let caller=Task {try await remote.currentToken()}
                #expect(await entered.wait());let owner=await remote.loginFlight?.id
                remote.start()
                try await eventually("candidate started"){await remote.dialing}
                #expect(await remote.loginFlight?.id==owner,"candidate retired someone else's login")
                release.set();#expect(!(try await caller.value).isEmpty)
                _ = try await remote.awaitState("ready"){$0.conn == .online}
                #expect(calls.value==1)
            }
        }

        @Test func directoryWaiterCancellationPreservesTheSharedRead() async throws {
            try await World.run(setUp: {_ in}) {w in
                let entered=Signal(),release=Signal(),canceled=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    if request.url?.path.hasSuffix("/ListDevices")==true {
                        calls.increment();entered.set();_ = await release.wait()
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                let first=Task {do{try await remote.refreshTrustedDirectory()}catch is CancellationError{canceled.set()}catch{}}
                #expect(await entered.wait());let owner=await remote.directoryLoad?.id
                first.cancel();#expect(await canceled.wait(500))
                #expect(await remote.directoryLoad?.id==owner)
                let second=Task {try await remote.refreshTrustedDirectory()}
                release.set();try await second.value;await first.value
                #expect(calls.value==1)
            }
        }

        @Test func securityWaiterCanCancelWithoutClearingThePull() async throws {
            try await World.run(setUp: {_ in}) {w in
                let entered=Signal(),release=Signal(),canceled=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{request,data,response in
                    if request.url?.path.hasSuffix("/GetSecurityStates")==true {
                        calls.increment();entered.set();_ = await release.wait()
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                let security=try await remote.securitySession()
                let first=Task {do{_ = try await security.sync()}catch is CancellationError{canceled.set()}catch{}}
                #expect(await entered.wait());first.cancel();#expect(await canceled.wait(500))
                let second=Task {try await security.sync()};await Task.yield()
                release.set();#expect(try await second.value.member);await first.value
                #expect(calls.value==1)
            }
        }
    }
}

extension Remote {
    func blockOwnershipTestRecovery() {
        recovery=Recovery(now:{0},random:{0.5},budget:RecoveryBudget())
        _ = recovery.take();_ = recovery.take()
    }
    func resumeOwnershipTestRecovery() {
        recovery=Recovery(now:monotonic,random:{0.5},budget:RecoveryBudget())
        reconnect?.task.cancel();reconnect=nil
    }
}
