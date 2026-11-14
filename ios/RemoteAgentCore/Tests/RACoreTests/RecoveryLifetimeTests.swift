import Foundation
import RAProto
import Testing
@testable import RACore

extension ServerTests {
    @Suite struct RecoveryLifetime {
        @Test func rejectedSavedTokenIsRenewedBeforeTheResumeSecurityGate() async throws {
            try await World.run(setUp:{w in try await w.daemon.connect()}) {w in
                let armed=Signal(),logins=Counter()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if armed.isSet {
                        if q.url?.path.hasSuffix("/GetSecurityStates")==true {return (Data("{\"code\":\"unauthenticated\"}".utf8),HTTPURLResponse(url:q.url!,statusCode:401,httpVersion:nil,headerFields:["Content-Type":"application/json"])!)}
                        if q.url?.path.hasSuffix("/Login")==true {logins.increment();return (Data("{\"code\":\"permission_denied\"}".utf8),HTTPURLResponse(url:q.url!,statusCode:403,httpVersion:nil,headerFields:["Content-Type":"application/json"])!)}
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                remote.start();_ = try await remote.awaitState("ready"){$0.conn == .online && $0.loaded}
                remote.stop();try await eventually("stopped"){await !remote.running}
                armed.set();remote.start()
                _ = try await remote.awaitState("removed after renew",ms:5000){$0.removed}
                #expect(logins.value==2)
            }
        }
        @Test func aLateSecurityRefusalCannotDiscardANewerLogin() async throws {
            try await World.run(setUp:{_ in}) {w in
                let entered=Signal(),release=Signal(),states=Counter(),logins=Counter()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/Login")==true {logins.increment()}
                    if q.url?.path.hasSuffix("/GetSecurityStates")==true && states.increment()==1 {
                        entered.set();_ = await release.wait()
                        return (Data("{\"code\":\"unauthenticated\"}".utf8),HTTPURLResponse(url:q.url!,statusCode:401,httpVersion:nil,headerFields:["Content-Type":"application/json"])!)
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http),security=try await remote.securitySession()
                let pending=Task {try await security.sync()};#expect(await entered.wait())
                _ = try await remote.signIn();release.set()
                #expect(try await pending.value.member);#expect(logins.value==2)
            }
        }
        @Test func sharedPullExpiresAndTheNextPullCanSucceed() async throws {
            try await World.run(setUp:{_ in}) {w in
                let entered=Signal(),stopped=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/GetSecurityStates")==true && calls.increment()==1 {entered.set();defer{stopped.set()};try await Task.sleep(for:.seconds(30))}
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http),security=try await remote.securitySession()
                let first=Task {do{_ = try await security.sync();return false}catch{return true}}
                #expect(await entered.wait());let expired=await stopped.wait(3800);#expect(expired,"shared pull retained past owner deadline")
                if !expired {await security.networkChanged()}
                #expect(await first.value);#expect(try await security.sync().member);#expect(calls.value==2)
            }
        }
        @Test func memberReadExpiresAndReleasesItsSharedSlot() async throws {
            try await World.run(setUp:{_ in}) {w in
                let entered=Signal(),stopped=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/ListDevices")==true && calls.increment()==1 {entered.set();defer{stopped.set()};try await Task.sleep(for:.seconds(30))}
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http);await remote.setRecoveryLifetimeMode()
                let first=Task {do{try await remote.refreshTrustedDirectory();return false}catch{return true}}
                #expect(await entered.wait());let expired=await stopped.wait(3800);#expect(expired,"member read retained past owner deadline")
                if !expired {await remote.retireDirectory()}
                #expect(await first.value);try await remote.refreshTrustedDirectory();#expect(calls.value>=2)
            }
        }
        @Test func networkRetirementReleasesAnIndependentDirectoryCaller() async throws {
            try await World.run(setUp:{_ in}) {w in
                let entered=Signal(),stopped=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/ListDevices")==true && calls.increment()==1 {entered.set();defer{stopped.set()};try await Task.sleep(for:.seconds(30))}
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                remote.networkChanged("initial",available:true)
                try await eventually("initial signature"){await remote.networkSignature == "initial"}
                let caller=Task {try await remote.refreshTrustedDirectory()};#expect(await entered.wait())
                remote.networkChanged("replacement",available:true)
                #expect(await stopped.wait(1000),"old directory was kept until its deadline")
                try await caller.value;#expect(calls.value==2);#expect(await remote.directoryLoad == nil)
            }
        }
        @Test func loginDeadlineIncludesBothPermissionDeniedAttempts() async throws {
            try await World.run(setUp:{_ in}) {w in
                let calls=Counter(),stopped=Signal()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/Login")==true && calls.increment()<=2 {
                        defer{if calls.value==2{stopped.set()}}
                        try await Task.sleep(for:.seconds(2))
                        return (Data("{\"code\":\"permission_denied\"}".utf8),HTTPURLResponse(url:q.url!,statusCode:403,httpVersion:nil,headerFields:["Content-Type":"application/json"])!)
                    }
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http);await remote.setRecoveryLifetimeMode()
                let caller=Task {do{_ = try await remote.currentToken();return false}catch is TimedOut{return true}catch{return false}}
                #expect(await stopped.wait(3800),"login retained beyond its complete round deadline")
                #expect(await caller.value);#expect(await remote.loginFlight == nil);#expect(await !remote.removed)
                #expect(!(try await remote.currentToken()).isEmpty);#expect(calls.value==3)
            }
        }
        @Test func resumingAnInterruptedColdStartRetiresItsNetworkRound() async throws {
            try await World.run(setUp:{_ in}) {w in
                let entered=Signal(),stopped=Signal(),calls=Counter()
                let http=Intercept.http(for:w.server.url,transform:{q,data,response in
                    if q.url?.path.hasSuffix("/GetChallenge")==true && calls.increment()==1 {entered.set();defer{stopped.set()};try await Task.sleep(for:.seconds(30))}
                    return (data,response)
                }) {$0}
                let remote=try await w.remote(try await w.join(),http:http)
                remote.start();#expect(await entered.wait());remote.stop();try await eventually("stopped"){await !remote.running}
                let before=await remote.networkGeneration;remote.start()
                try await eventually("resume generation"){await remote.networkGeneration>before}
                #expect(await stopped.wait(1000));_ = try await remote.awaitState("ready"){$0.conn == .online}
            }
        }
    }
}
extension Remote {func setRecoveryLifetimeMode(){everReady=true}}
