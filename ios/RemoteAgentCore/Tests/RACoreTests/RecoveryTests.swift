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

    @Test func adaptiveLivenessThresholdsAndTwoHeartbeatGuard() {
        let now = OSAllocatedUnfairLock(initialState: Int64(0))
        let liveness = LegLiveness(initialNow: 0, now: { now.withLock { $0 } })
        #expect(liveness.thresholdMs() == 60_000)
        liveness.onFrame(5_000, isHeartbeat: false)
        #expect(liveness.thresholdMs() == 60_000)
        liveness.onFrame(10_000, isHeartbeat: true)
        #expect(liveness.thresholdMs() == 60_000)
        now.withLock { $0 = 14_000 }
        #expect(!liveness.suspect())
        now.withLock { $0 = 70_000 }
        #expect(liveness.suspect())

        let fast = LegLiveness(initialNow: 0)
        fast.onFrame(1_000, isHeartbeat: true)
        fast.onFrame(2_000, isHeartbeat: true)
        #expect(fast.thresholdMs() == 3_000)
        #expect(!fast.suspect(4_999))
        #expect(fast.suspect(5_000))

        let legacy = LegLiveness(initialNow: 0)
        legacy.onFrame(1_000, isHeartbeat: true)
        legacy.onFrame(21_000, isHeartbeat: true)
        #expect(legacy.thresholdMs() == 60_000)
        #expect(!legacy.suspect(80_999))
        #expect(legacy.suspect(81_000))
    }

    @Test func terminalRecoveryBackoffAndSharedGlobalBudget() {
        let time = OSAllocatedUnfairLock(initialState: Int64(0))
        let budget = RecoveryBudget()
        let t1 = TerminalRecovery(now: { time.withLock { $0 } }, random: { 0.5 }, budget: budget)
        let t2 = TerminalRecovery(now: { time.withLock { $0 } }, random: { 0.5 }, budget: budget)
        let t3 = TerminalRecovery(now: { time.withLock { $0 } }, random: { 0.5 }, budget: budget)

        #expect(t1.takeOrDelayMs() == nil)
        t1.recordFailureIfUnstabilized()
        let d1 = t1.takeOrDelayMs()
        #expect(d1 != nil && d1! >= 500)

        t1.onStabilized()
        time.withLock { $0 = 1_000 }
        #expect(t1.takeOrDelayMs() == nil)

        #expect(t2.takeOrDelayMs() == nil)
        #expect(t2.takeOrDelayMs() == nil)
        #expect(t3.takeOrDelayMs() == nil)
        // t1(1000)=3, t2(1000)=2,1, t3(1000)=0! A fresh 4th terminal still hits global wait >= 500ms:
        let t4 = TerminalRecovery(now: { time.withLock { $0 } }, random: { 0.5 }, budget: budget)
        let globalDelay = t4.takeOrDelayMs()
        #expect(globalDelay != nil && globalDelay! >= 500)
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
                let hole=try StreamBlackhole(firstReady:true,firstHeartbeats:2,firstHeartbeatIntervalMs:500)
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
        @Test func silentMainStreamWithoutTwoHeartbeatsKeepsLegacy60sThreshold() async throws {
            try await World.run(setUp:{w in try await w.daemon.connect()}) {w in
                let account=try await w.join()
                let hole=try StreamBlackhole(firstReady:true,firstHeartbeats:1)
                w.onClose{hole.close()}
                let real=w.server.url
                let http=Intercept.http(for:hole.url){$0.moved(to:real)}
                let remote=try await w.remote(Account(server:hole.url,root:account.root,deviceSeed:account.deviceSeed,deviceId:account.deviceId),http:http)
                remote.start();_ = try await remote.awaitState("first ready"){$0.conn == .online}
                let old=await remote.ws
                await sleepMs(4500)
                #expect(await remote.ws === old)
                #expect(hole.attempts.value==1)
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

        @Test func oversizedMainStreamFrameIsRejectedBeforeParsingProtobuf() async throws {
            let listener = try Listener()
            defer { listener.close() }
            let attempts = Counter()
            let dropped = Signal()
            let sendOversized = DispatchSemaphore(value: 0)
            let payloadLen = (2 << 20) + 1
            try await World.run(setUp: { w in try await w.daemon.connect() }) { w in
                let upstreamPort = w.server.port
                listener.serve { fd in
                    guard let head = readHead(fd) else { return }
                    if head.first?.hasPrefix("GET /v1/stream ") == true {
                        guard acceptUpgrade(fd, head) else { return }
                        let attempt = attempts.increment()
                        if attempt == 1 {
                            var ready = PBServerFrame()
                            ready.ready = PBReady.with { $0.serverTimeMs = Int64(Date().timeIntervalSince1970 * 1000) }
                            let readyBytes: Data = (try? ready.serializedData()) ?? Data()
                            _ = writeAll(fd, Data([0x82, UInt8(readyBytes.count)]) + readyBytes)
                            _ = sendOversized.wait(timeout: .now() + 5)
                            var header = Data([0x82, 0x7f])
                            var bigLen = UInt64(payloadLen).bigEndian
                            withUnsafeBytes(of: &bigLen) { header.append(contentsOf: $0) }
                            _ = writeAll(fd, header + Data(count: payloadLen))
                            var b: UInt8 = 0
                            while read(fd, &b, 1) == 1 {}
                            dropped.set()
                        } else {
                            var b: UInt8 = 0
                            while read(fd, &b, 1) == 1 {}
                        }
                        return
                    }
                    let up = socket(AF_INET, SOCK_STREAM, 0)
                    guard up >= 0 else { return }
                    defer { Darwin.close(up) }
                    var yes: Int32 = 1
                    setsockopt(up, SOL_SOCKET, SO_NOSIGPIPE, &yes, socklen_t(MemoryLayout<Int32>.size))
                    var addr = sockaddr_in()
                    addr.sin_family = sa_family_t(AF_INET)
                    addr.sin_addr.s_addr = inet_addr("127.0.0.1")
                    addr.sin_port = UInt16(upstreamPort).bigEndian
                    let ok = withUnsafePointer(to: &addr) {
                        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                            connect(up, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
                        }
                    }
                    guard ok == 0 else { return }
                    let headData = Data((head.joined(separator: "\r\n") + "\r\n\r\n").utf8)
                    guard writeAll(up, headData) else { return }
                    let done = DispatchGroup()
                    done.enter()
                    Thread.detachNewThread {
                        var buf = [UInt8](repeating: 0, count: 16384)
                        while true {
                            let n = read(up, &buf, buf.count)
                            if n <= 0 || !writeAll(fd, Data(buf[..<n])) { break }
                        }
                        shutdown(fd, SHUT_RDWR)
                        done.leave()
                    }
                    var buf = [UInt8](repeating: 0, count: 16384)
                    while true {
                        let n = read(fd, &buf, buf.count)
                        if n <= 0 || !writeAll(up, Data(buf[..<n])) { break }
                    }
                    shutdown(up, SHUT_RDWR)
                    done.wait()
                }
                let acct = try await w.join()
                let proxied = Account(
                    server: "http://127.0.0.1:\(listener.port)",
                    root: acct.root,
                    deviceSeed: acct.deviceSeed,
                    deviceId: acct.deviceId,
                    accountId: acct.accountId
                )
                let remote = try await w.remote(proxied)
                let parseCalls = Counter()
                remote.frameParser.withLock {
                    $0 = { data in
                        parseCalls.increment()
                        return try PBServerFrame(serializedBytes: data)
                    }
                }
                remote.start()
                _ = try await remote.awaitState("online") { $0.conn == .online }
                let prior = parseCalls.value
                sendOversized.signal()
                #expect(await dropped.wait(5_000))
                try await eventually("socket replaced after oversized frame") { attempts.value >= 2 }
                #expect(parseCalls.value == prior)
            }
        }
    }
}

extension Remote {
    func expireRecoveryTestLogin() {token="";tokenExpires=0}
}
