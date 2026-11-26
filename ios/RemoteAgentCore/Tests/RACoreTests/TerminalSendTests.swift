import Foundation
import RAProto
import Testing
import os

@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct TerminalPipeTests {
    @Test func terminalBacklogEnforcesInboundAndOutboundBudgetsAndReleasesCredits() {
        let backlog = TerminalBacklog()

        // Inbound: single frame > 1 MiB rejected immediately.
        #expect(!backlog.reserveInbound(bytes: TerminalPipeLimits.maxInboundFrameBytes + 1))

        // Inbound: 128 small frames succeed; 129th fails until consumed credits are released.
        for _ in 0..<TerminalPipeLimits.maxInboundPendingFrames {
            #expect(backlog.reserveInbound(bytes: 1024))
        }
        #expect(!backlog.reserveInbound(bytes: 1024))
        backlog.releaseInbound(frames: 2, bytes: 2048)
        #expect(backlog.reserveInbound(bytes: 1024))
        #expect(backlog.reserveInbound(bytes: 1024))
        #expect(!backlog.reserveInbound(bytes: 1024))
        backlog.releaseInbound(frames: 128, bytes: 128 * 1024)

        // Inbound: byte budget cap (2 MiB) enforced across multi-frame bursts.
        #expect(backlog.reserveInbound(bytes: 1 << 20))
        #expect(backlog.reserveInbound(bytes: 1 << 20))
        #expect(!backlog.reserveInbound(bytes: 1))
        backlog.releaseInbound(frames: 1, bytes: 1 << 20)
        #expect(backlog.reserveInbound(bytes: 1 << 20))

        // Outbound: single frame > 64 KiB rejected; 64 frames succeed; 65th fails until release.
        #expect(!backlog.reserveOutbound(bytes: TerminalPipeLimits.maxOutboundFrameBytes + 1))
        for _ in 0..<TerminalPipeLimits.maxOutboundPendingFrames {
            #expect(backlog.reserveOutbound(bytes: 64))
        }
        #expect(!backlog.reserveOutbound(bytes: 64))
        backlog.releaseOutboundOne()
        #expect(backlog.reserveOutbound(bytes: 64))
        #expect(!backlog.reserveOutbound(bytes: 64))
    }

    @Test func activeTerminalRegistryRoutesPendingOpenDeliverStateAndDirectBytes() {
        let registry = ActiveTerminalRegistry()
        let opId = registry.allocateOpId()
        let openedStream = OSAllocatedUnfairLock<String>(initialState: "")
        let delivered = Mailbox<PBTerminalDeliverOutputEffect>()
        let states = Mailbox<PBTerminalStateChangedEffect>()
        let directRaw = Mailbox<Data>()
        let sentCount = Counter()
        let closedSockets = Counter()

        registry.registerPending(opId, ActiveTerminalRegistry.PendingOpen(
            onOpenSocket: { eff in
                openedStream.withLock { $0 = eff.streamID }
                registry.unregisterPending(opId)
                registry.register(eff.streamID, ActiveTerminalRegistry.Entry(
                    sendRelay: { _ in },
                    onSentOut: { sentCount.increment() },
                    onOpenSocket: { _ in },
                    onCloseSocket: { closedSockets.increment() },
                    onDeliver: { delivered.put($0) },
                    onStateChanged: { states.put($0) },
                    onDirectBytes: { _, _, raw in directRaw.put(raw) }
                ))
            },
            onStateChanged: { states.put($0) }
        ))

        registry.handleOpenSocket(.with {
            $0.opID = opId
            $0.streamID = "term-1"
            $0.isInitial = true
        })
        #expect(openedStream.withLock { $0 } == "term-1")

        registry.onSentOut(streamId: "term-1")
        #expect(sentCount.value == 1)

        let deliverEff = PBTerminalDeliverOutputEffect.with {
            $0.streamID = "term-1"
            $0.consumedFrames = 1
            $0.consumedBytes = 42
            $0.frames = [.with { $0.output = Data("hi".utf8) }]
        }
        registry.handleDeliverOutput(deliverEff)
        #expect(delivered.all == [deliverEff])

        registry.onDirectBytes(streamId: "term-1", machineId: "m1", pcId: 7, raw: Data([9, 8, 7]))
        #expect(directRaw.all == [Data([9, 8, 7])])

        let stateEff = PBTerminalStateChangedEffect.with {
            $0.opID = opId
            $0.streamID = "term-1"
            $0.state = .confirmRestart
        }
        registry.handleStateChanged(stateEff)
        #expect(states.all == [stateEff])

        registry.handleCloseSocket(.with { $0.streamID = "term-1" })
        #expect(closedSockets.value == 1)
    }

    @Test func sessionImageFetchMapsStatusAndWritesEncryptedCacheEnvelope() async throws {
        let cache = MemoryCache()
        let remote = try Remote(
            account: Account(server: "https://example.com", deviceId: "dev-1", accountId: "acc-1"),
            http: HTTP(),
            cache: cache,
            securityStore: MemorySecurityStoreV4()
        )
        defer { remote.close() }

        let nextEffect = OSAllocatedUnfairLock<PBSessionImageLoadedEffect>(initialState: PBSessionImageLoadedEffect())
        let seenCachedEnvelope = OSAllocatedUnfairLock<Data>(initialState: Data())
        await remote.installCoreHooks(Remote.CoreHooks(
            onRefresh: {},
            onRefreshDevices: {},
            onCoreEvent: { [weak remote] ev in
                switch ev.event {
                case .securityAction(let req)?:
                    remote?.securityClient.handleWorkflowResult(.with {
                        $0.actionID = req.actionID
                        $0.ok = true
                    })
                case .sessionFetchImage(let req)?:
                    seenCachedEnvelope.withLock { $0 = req.cachedEnvelope }
                    var eff = nextEffect.withLock { $0 }
                    eff.opID = req.opID
                    eff.sessionID = req.sessionID
                    eff.imageID = req.imageID
                    remote?.handleSessionImageLoaded(eff)
                default:
                    break
                }
            }
        ))

        // 1. Freshly fetched OK image persists opaque cacheEnvelopeBytes.
        let png = Data([0x89, 0x50, 0x4E, 0x47])
        let envBytes = Data([0x0a, 0x0b, 0x0c])
        nextEffect.withLock {
            $0 = .with {
                $0.status = .ok
                $0.format = .png
                $0.width = 4
                $0.height = 3
                $0.data = png
                $0.cacheEnvelopeBytes = envBytes
            }
        }
        let okRes = try await remote.image("s1", imageId: "img1")
        guard case .loaded(let payload) = okRes else {
            Issue.record("expected .loaded, got \(okRes)")
            return
        }
        #expect(payload.data == png && payload.format == .png && payload.width == 4 && payload.height == 3)
        #expect(cache.images["s1/img1"] == envBytes)

        // 2. Subsequent call passes persisted cachedEnvelope bytes back to Core.
        nextEffect.withLock {
            $0 = .with {
                $0.status = .ok
                $0.format = .png
                $0.data = png
            }
        }
        _ = try await remote.image("s1", imageId: "img1")
        #expect(seenCachedEnvelope.withLock { $0 } == envBytes)

        // 3. Missing, expired, retiredHistory, and corrupt statuses map accurately.
        nextEffect.withLock { $0 = .with { $0.status = .missing } }
        #expect(try await remote.image("s1", imageId: "miss") == .missing)

        nextEffect.withLock { $0 = .with { $0.status = .expired } }
        #expect(try await remote.image("s1", imageId: "exp") == .expired)

        nextEffect.withLock { $0 = .with { $0.status = .bad; $0.retiredHistory = true } }
        #expect(try await remote.image("s1", imageId: "ret") == .retiredHistory)

        nextEffect.withLock {
            $0 = .with {
                $0.status = .bad
                $0.errorText = .with { $0.literal = "bad image bytes" }
            }
        }
        #expect(try await remote.image("s1", imageId: "bad") == .corrupt(why: "bad image bytes"))
    }

    /// Full byte-pipe round-trip test using `installCoreHooks` + a fake `FrameStream`:
    /// open -> OpenSocket -> FIFO inbound bytes -> DeliverOutput releases backlog credits ->
    /// FIFO TerminalInputAction with exact streamID -> CloseSocket & close() -> open failure settling.
    @Test func terminalPipeRoundTripPreservesOrderReleasesBacklogAndSettlesFailure() async throws {
        final class FakeFrameStream: FrameStream, @unchecked Sendable {
            let route: String
            let hello: Data
            let sent = Mailbox<Data>()
            let canceled = Counter()
            let closed = Counter()
            private let inboundStream: AsyncThrowingStream<Data, Error>
            private let inboundCont: AsyncThrowingStream<Data, Error>.Continuation
            private let endedLatch = TestLatch()
            private let openResult: Bool

            init(route: String, hello: Data, openResult: Bool = true) {
                self.route = route
                self.hello = hello
                self.openResult = openResult
                let (s, c) = AsyncThrowingStream<Data, Error>.makeStream()
                self.inboundStream = s
                self.inboundCont = c
            }

            func pushInbound(_ raw: Data) {
                inboundCont.yield(raw)
            }

            func finishInbound() {
                inboundCont.finish()
                Task { await endedLatch.open() }
            }

            func opened() async -> Bool { openResult }

            func receive() async throws -> Data {
                var iter = inboundStream.makeAsyncIterator()
                guard let next = try await iter.next() else {
                    throw URLError(.networkConnectionLost)
                }
                return next
            }

            func ended() async -> PathEnd {
                try? await endedLatch.wait(timeoutMs: 5_000, label: "fake stream ended")
                return PathEnd()
            }

            func send(_ data: Data, done: @escaping @Sendable (Error?) -> Void) {
                sent.put(data)
                done(nil)
            }

            func cancel() {
                canceled.increment()
                finishInbound()
            }

            func close() {
                closed.increment()
                finishInbound()
            }
        }

        final class FakePath: Path, @unchecked Sendable {
            private let lock = NSLock()
            private var created: [FakeFrameStream] = []
            private let openResult: Bool

            init(openResult: Bool = true) { self.openResult = openResult }

            var streams: [FakeFrameStream] { lock.withLock { created } }

            func openStream(_ route: String, hello: Data) -> any FrameStream {
                let s = FakeFrameStream(route: route, hello: hello, openResult: openResult)
                lock.withLock { created.append(s) }
                return s
            }
        }

        final class RecordingListener: TermListener, @unchecked Sendable {
            let frames = Mailbox<PBTerminalFrame>()
            let closes = Mailbox<String>()
            func onFrame(_ frame: PBTerminalFrame) { frames.put(frame) }
            func onClose(_ reason: String) { closes.put(reason) }
        }

        let fakePath = FakePath()
        let remote = try Remote(
            account: Account(server: "https://example.com", deviceId: "dev-1", accountId: "acc-1"),
            http: HTTP(),
            cache: MemoryCache(),
            securityStore: MemorySecurityStoreV4(),
            relayPath: fakePath
        )
        defer { remote.close() }

        let openEvents = Mailbox<PBTerminalOpenAction>()
        let socketEvents = Mailbox<PBTerminalSocketEvent>()
        let inboundEvents = Mailbox<PBTerminalInboundBytesEvent>()
        let inputEvents = Mailbox<PBTerminalInputAction>()
        let closeEvents = Mailbox<PBTerminalCloseAction>()
        let socketOpenedLatch = TestLatch()
        let streamID = "term-stream-42"
        let expectedHello = Data("hello-term-wire".utf8)
        let legBytes = Data([0xAA, 0xBB])

        await remote.installCoreHooks(Remote.CoreHooks(
            onRefresh: {},
            onRefreshDevices: {},
            onCoreEvent: { [weak remote] ev in
                switch ev.event {
                case .securityAction(let req)?:
                    remote?.securityClient.handleWorkflowResult(.with {
                        $0.actionID = req.actionID
                        $0.ok = true
                    })
                case .terminalOpen(let req)?:
                    openEvents.put(req)
                    remote?.handleTerminalOpenSocket(.with {
                        $0.opID = req.opID
                        $0.streamID = streamID
                        $0.legID = legBytes
                        $0.helloBytes = expectedHello
                        $0.isInitial = true
                    })
                case .terminalSocket(let s)?:
                    socketEvents.put(s)
                    if s.opened {
                        Task { await socketOpenedLatch.open() }
                        remote?.handleTerminalStateChanged(.with {
                            $0.opID = openEvents.all.first?.opID ?? 0
                            $0.streamID = streamID
                            $0.openSettled = true
                            $0.openOk = true
                        })
                    }
                case .terminalInboundBytes(let b)?:
                    // Yield so any unserialized Task {} scheduling immediately reorders frames.
                    await Task.yield()
                    inboundEvents.put(b)
                case .terminalInput(let inp)?:
                    await Task.yield()
                    inputEvents.put(inp)
                case .terminalClose(let c)?:
                    closeEvents.put(c)
                default:
                    break
                }
            }
        ))

        // 1. Open -> TerminalOpenAction -> OpenSocket -> socket opened -> openTerminal resolves.
        let listener = RecordingListener()
        let conn = try await remote.openTerminal("sess-1", listener: listener, terminalId: "tid-9")
        try await socketOpenedLatch.wait(timeoutMs: 5_000, label: "terminal socket opened")
        #expect(openEvents.all.count == 1)
        #expect(openEvents.all[0].sessionID == "sess-1")
        #expect(openEvents.all[0].terminalID == "tid-9")
        #expect(fakePath.streams.count == 1)
        #expect(fakePath.streams[0].route == "/v1/term")
        #expect(fakePath.streams[0].hello == expectedHello)
        let sock = fakePath.streams[0]

        // 2. Inbound bytes arrive in strict FIFO order and DeliverOutput delivers frames + releases backlog credits.
        // Push 100 frames (within 128 cap), verify strict order, release credits via DeliverOutput, then push 60 more
        // (160 total > 128 cap, proving credit release works without triggering backlog overflow).
        let wave1 = 100
        let wave2 = 60
        for i in 0..<wave1 {
            sock.pushInbound(Data("in-\(i)".utf8))
        }
        var collected1: [PBTerminalInboundBytesEvent] = []
        for _ in 0..<wave1 {
            collected1.append(try await inboundEvents.take())
        }
        #expect(collected1.map { String(decoding: $0.rawEnvelope, as: UTF8.self) } == (0..<wave1).map { "in-\($0)" })
        #expect(collected1.allSatisfy { $0.streamID == streamID && !$0.fromDirect })

        let wave1Bytes = UInt32(collected1.reduce(0) { $0 + $1.rawEnvelope.count })
        remote.handleTerminalDeliverOutput(.with {
            $0.streamID = streamID
            $0.consumedFrames = UInt32(wave1)
            $0.consumedBytes = wave1Bytes
            $0.frames = [
                .with { $0.output = Data("out-a".utf8) },
                .with { $0.output = Data("out-b".utf8) },
            ]
        })
        let d1 = try await listener.frames.take()
        let d2 = try await listener.frames.take()
        #expect(d1.output == Data("out-a".utf8))
        #expect(d2.output == Data("out-b".utf8))

        for i in wave1..<(wave1 + wave2) {
            sock.pushInbound(Data("in-\(i)".utf8))
        }
        var collected2: [PBTerminalInboundBytesEvent] = []
        for _ in 0..<wave2 {
            collected2.append(try await inboundEvents.take())
        }
        #expect(collected2.map { String(decoding: $0.rawEnvelope, as: UTF8.self) } == (wave1..<(wave1 + wave2)).map { "in-\($0)" })
        #expect(listener.closes.all.isEmpty)

        // 3. Rapid outbound inputs emit TerminalInputAction with exact streamID in strict FIFO order.
        let outCount = 30
        for i in 0..<outCount {
            conn.send(.with { $0.input = Data("k-\(i)".utf8) })
        }
        var gotInputs: [PBTerminalInputAction] = []
        for _ in 0..<outCount {
            gotInputs.append(try await inputEvents.take())
        }
        #expect(gotInputs.allSatisfy { $0.streamID == streamID })
        #expect(gotInputs.map { String(decoding: $0.input, as: UTF8.self) } == (0..<outCount).map { "k-\($0)" })

        // 4. CloseSocket cancels underlying socket; conn.close() emits TerminalCloseAction.
        remote.handleTerminalCloseSocket(.with { $0.streamID = streamID })
        #expect(sock.canceled.value >= 1)
        conn.close()
        let closeAct = try await closeEvents.take()
        #expect(closeAct.streamID == streamID)

        // 5. Open failure settles awaiting openTerminal caller with Core's error instead of hanging.
        let failRemote = try Remote(
            account: Account(server: "https://example.com", deviceId: "dev-1", accountId: "acc-1"),
            http: HTTP(),
            cache: MemoryCache(),
            securityStore: MemorySecurityStoreV4(),
            relayPath: FakePath()
        )
        defer { failRemote.close() }
        await failRemote.installCoreHooks(Remote.CoreHooks(
            onRefresh: {},
            onRefreshDevices: {},
            onCoreEvent: { [weak failRemote] ev in
                switch ev.event {
                case .securityAction(let req)?:
                    failRemote?.securityClient.handleWorkflowResult(.with {
                        $0.actionID = req.actionID
                        $0.ok = true
                    })
                case .terminalOpen(let req)?:
                    failRemote?.handleTerminalStateChanged(.with {
                        $0.opID = req.opID
                        $0.openSettled = true
                        $0.openOk = false
                        $0.reasonCode = "not_found"
                        $0.openError = .with { $0.literal = "session missing" }
                    })
                default:
                    break
                }
            }
        ))
        do {
            _ = try await failRemote.openTerminal("missing-sess", listener: RecordingListener())
            Issue.record("expected openTerminal to throw on open failure")
        } catch let err as CommandError {
            #expect(err.code == "not_found")
            #expect(err.message == "session missing")
        }
    }
}
