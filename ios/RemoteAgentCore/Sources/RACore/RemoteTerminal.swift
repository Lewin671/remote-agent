// A session's terminal (architecture §11, docs/transport.md §5):
// This shell is a stateless executor: it opens /v1/term sockets, seals/opens
// envelopes with the session key, and routes frames through Core's TerminalStream
// (reordering, duplicate suppression without closing, hot-standby dual-send, and
// AttachPath recovery).
import CryptoKit
import Foundation
import RAProto
import os

public protocol TermListener: Sendable {
    /// Called for each frame in order, one at a time.
    func onFrame(_ frame: PBTerminalFrame)
    /// Called once, when the stream is over, unless this side closed it.
    func onClose(_ reason: String)
}

/// Stateless lookup table mapping active logical terminal stream IDs to their I/O callbacks.
final class ActiveTerminalRegistry: @unchecked Sendable {
    struct Entry: Sendable {
        let sendRelay: @Sendable (Data) -> Void
        let deliver: @Sendable ([Data]) -> Void
        let attachRelay: @Sendable (Data) -> Void
        let onState: @Sendable (UInt32, String, Bool) -> Void
        let onDirectOutput: @Sendable (String, UInt64, PBEnvelope, Data) -> Void
    }

    private let lock = OSAllocatedUnfairLock(initialState: [String: Entry]())

    func register(_ streamId: String, _ entry: Entry) {
        lock.withLock { $0[streamId] = entry }
    }

    func unregister(_ streamId: String) {
        lock.withLock { _ = $0.removeValue(forKey: streamId) }
    }

    func sendRelay(streamId: String, raw: Data) {
        lock.withLock { $0[streamId] }?.sendRelay(raw)
    }

    func deliver(streamId: String, payloads: [Data]) {
        lock.withLock { $0[streamId] }?.deliver(payloads)
    }

    func attachRelay(streamId: String, legId: Data) {
        lock.withLock { $0[streamId] }?.attachRelay(legId)
    }

    func onState(streamId: String, state: UInt32, reason: String, needsConfirmation: Bool) {
        lock.withLock { $0[streamId] }?.onState(state, reason, needsConfirmation)
    }

    func onDirectOutput(streamId: String, machineId: String, pcId: UInt64, env: PBEnvelope, raw: Data) {
        lock.withLock { $0[streamId] }?.onDirectOutput(machineId, pcId, env, raw)
    }
}

/// Input may wait this long, in bytes, to be written before the terminal is given up on.
private let termSendBudget = 16 << 20

public final class TermConn: Sendable {
    private struct Out {
        var stopped = false
        var seq: UInt64 = 0
        var queued = 0
        var tail: Task<Void, Never>?
    }

    private let socketBox: OSAllocatedUnfairLock<any TerminalSocket>
    private let sendBudget: Int
    private let out = OSAllocatedUnfairLock(initialState: Out())
    private let seal: @Sendable (PBTerminalFrame, UInt64, UInt64) async throws -> (Data, Data, Bool)
    private let dispatchInput: (@Sendable (UInt64, Bool, Data, Data) async -> Bool)?
    private let finish: @Sendable (String) -> Void
    private let closeLocal: @Sendable () -> Void

    init(
        socket: any TerminalSocket,
        socketBox: OSAllocatedUnfairLock<any TerminalSocket>? = nil,
        sendBudget: Int = termSendBudget,
        seal: @escaping @Sendable (PBTerminalFrame, UInt64, UInt64) async throws -> (Data, Data, Bool),
        dispatchInput: (@Sendable (UInt64, Bool, Data, Data) async -> Bool)? = nil,
        finish: @escaping @Sendable (String) -> Void,
        closeLocal: @escaping @Sendable () -> Void
    ) {
        self.socketBox = socketBox ?? OSAllocatedUnfairLock(initialState: socket)
        self.sendBudget = sendBudget
        self.seal = seal
        self.dispatchInput = dispatchInput
        self.finish = finish
        self.closeLocal = closeLocal
    }

    /// Sends a frame. Frames go out in the order of the calls, each with the next number.
    public func send(_ frame: PBTerminalFrame) {
        let bytes = (try? frame.serializedData().count) ?? 0
        let deadline = UInt64(Date().timeIntervalSince1970 * 1000) + 60_000
        let failure: String? = out.withLock { o in
            guard !o.stopped else { return nil }
            guard bytes <= sendBudget - o.queued else {
                o.stopped = true
                return L10n.errorsIosTerminalInputWaitingToSendStopped()
            }
            o.seq += 1
            o.queued += bytes
            let seq = o.seq
            let previous = o.tail
            o.tail = Task { [socketBox, seal, dispatchInput, finish, out, sendBudget] in
                await previous?.value
                var reserved = bytes
                defer {
                    let released = reserved
                    out.withLock { $0.queued -= released }
                }
                guard !out.withLock({ $0.stopped }) else { return }
                do {
                    try Task.checkCancellation()
                    let (encoded, frameHash, isInput) = try await seal(frame, seq, deadline)
                    let accepted = out.withLock { o in
                        guard !o.stopped, encoded.count - bytes <= sendBudget - o.queued else { return false }
                        o.queued += encoded.count - bytes
                        return true
                    }
                    guard accepted else { throw SecurityFailure.capacity }
                    reserved = encoded.count
                    if let dispatchInput, await dispatchInput(seq, isInput, encoded, frameHash) {
                        return
                    }
                    let sock = socketBox.withLock { $0 }
                    try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
                        sock.send(encoded) { error in
                            if let error { c.resume(throwing: error) } else { c.resume() }
                        }
                    }
                } catch {
                    let first = out.withLock { o in
                        defer { o.stopped = true }
                        return !o.stopped
                    }
                    if first {
                        finish(L10n.errorsIosTerminalInputFailedToSendStopped())
                        socketBox.withLock { $0 }.cancel()
                    }
                }
            }
            return nil
        }
        if let failure {
            finish(failure)
            socketBox.withLock { $0 }.cancel()
        }
    }

    public func close() {
        out.withLock { $0.stopped = true }
        closeLocal()
        socketBox.withLock { $0 }.close()
    }
}

extension Remote {
    /// Joins a session's terminal stream using Core's Phase 3 logical continuity
    /// and direct + hot-standby executors.
    public func openTerminal(_ sid: String, listener: any TermListener) async throws -> TermConn {
        guard let machineId = machineIdForSessionIfKnown(sid), await hasSessionSeed(sid) else {
            throw CommandError("not_found", L10nMessage.errorsIosUnknownSession())
        }
        let tok = try await currentToken()
        let streamId = newRequestId()
        let closed = OSAllocatedUnfairLock(initialState: false)
        var hello = PBTermHello()
        hello.token = tok
        hello.streamID = streamId
        hello.machineID = machineId
        let initialSocket = deviceLink.relay.openStream("/v1/term", hello: try hello.serializedData())
        let socketBox = OSAllocatedUnfairLock<any TerminalSocket>(initialState: initialSocket)
        let activeLegBox = OSAllocatedUnfairLock<Data>(initialState: Data(streamId.utf8))
        let lastRelayError = OSAllocatedUnfairLock<(any Error)?>(initialState: nil)
        let terminals = activeTerminals

        let stream = deviceLink.openStream(stop: { [weak self] cancelPath in
            closed.withLock { $0 = true }
            terminals.unregister(streamId)
            Task { [weak self] in
                var ev = PBCoreEvent()
                ev.terminalDirectClose = .with { $0.streamID = streamId }
                await self?.emitCoreEvent(ev)
            }
            if cancelPath { socketBox.withLock { $0 }.cancel() }
        }, onClose: listener.onClose)
        let finish: @Sendable (String) -> Void = { reason in stream.end(reason) }

        // The upgrade gets 20 s, the time the server keeps the first side waiting for the other.
        let sleep = self.sleep
        let limit = Task {
            if (try? await sleep(20_000)) != nil { socketBox.withLock { $0 }.cancel() }
        }
        let open = await initialSocket.opened()
        limit.cancel()
        if !open {
            stream.end()
            throw UserError(L10nMessage.errorsCannotConnectToTerminal())
        }

        let handleInbound: @Sendable (PBEnvelope, Bool, UInt64) async -> Void = { [weak self] env, fromDirect, pcId in
            guard let self, !closed.withLock({ $0 }) else { return }
            do {
                let res = try await self.openEnvelopeOp(
                    envelope: env,
                    expectedPurpose: .terminalOutput,
                    machineId: machineId,
                    sessionId: sid,
                    requestId: streamId,
                    history: false
                )
                guard res.purpose == SecurityPurpose.terminalOutput.rawValue,
                      res.machineID == machineId,
                      res.sessionID == sid,
                      res.requestID == streamId
                else { throw SecurityFailure.untrusted }
                let frameHash = Data(SHA256.hash(data: res.plaintext))
                var ev = PBCoreEvent()
                ev.terminalDirectOutput = .with {
                    $0.streamID = streamId
                    $0.seq = res.seq
                    $0.frameHash = frameHash
                    $0.payload = res.plaintext
                    $0.fromDirect = fromDirect
                    $0.pcID = pcId
                }
                await self.emitCoreEvent(ev)
            } catch {
                if !fromDirect {
                    finish(errorText(error))
                    socketBox.withLock { $0 }.cancel()
                }
            }
        }

        let startReadLoop: @Sendable (any FrameStream, Data) -> Void = { [weak self] sock, legData in
            Task.detached {
                while let bytes = try? await sock.receive() {
                    if closed.withLock({ $0 }) { continue }
                    guard bytes.count <= 1 << 20 else {
                        finish(L10n.errorsTerminalConnectionDropped())
                        sock.cancel()
                        break
                    }
                    if let env = try? PBEnvelope(serializedBytes: bytes) {
                        await handleInbound(env, false, 0)
                    } else {
                        finish(L10n.errorsTerminalConnectionDropped())
                        sock.cancel()
                        break
                    }
                }
                let isCurrent = socketBox.withLock { $0 as AnyObject === sock as AnyObject }
                if !isCurrent || closed.withLock({ $0 }) { return }
                let end = await sock.ended()
                if let err = end.error {
                    lastRelayError.withLock { $0 = err }
                }
                if let self {
                    var ev = PBCoreEvent()
                    ev.terminalDirectLeg = .with {
                        $0.streamID = streamId
                        $0.legID = legData
                        $0.direct = false
                        $0.open = false
                    }
                    await self.emitCoreEvent(ev)
                }
            }
        }

        let relayChain = SerialChain()
        terminals.register(streamId, ActiveTerminalRegistry.Entry(
            sendRelay: { raw in
                guard !closed.withLock({ $0 }) else { return }
                let sock = socketBox.withLock { $0 }
                sock.send(raw) { _ in }
            },
            deliver: { payloads in
                for raw in payloads {
                    if closed.withLock({ $0 }) { break }
                    if let frame = try? PBTerminalFrame(serializedBytes: raw) {
                        listener.onFrame(frame)
                    }
                }
            },
            attachRelay: { [weak self] legId in
                guard let self, !closed.withLock({ $0 }) else { return }
                let legStr = String(decoding: legId, as: UTF8.self)
                Task {
                    var nextHello = PBTermHello()
                    nextHello.token = tok
                    nextHello.streamID = legStr
                    nextHello.machineID = machineId
                    guard let helloBytes = try? nextHello.serializedData() else { return }
                    let nextSock = self.deviceLink.relay.openStream("/v1/term", hello: helloBytes)
                    guard await nextSock.opened(), !closed.withLock({ $0 }) else {
                        nextSock.cancel()
                        if !closed.withLock({ $0 }) {
                            var failEv = PBCoreEvent()
                            failEv.terminalDirectLeg = .with {
                                $0.streamID = streamId
                                $0.legID = legId
                                $0.direct = false
                                $0.open = false
                            }
                            await self.emitCoreEvent(failEv)
                        }
                        return
                    }
                    let prev = socketBox.withLock { cur -> any TerminalSocket in
                        let old = cur
                        cur = nextSock
                        return old
                    }
                    activeLegBox.withLock { $0 = legId }
                    prev.close()
                    startReadLoop(nextSock, legId)
                    var ev = PBCoreEvent()
                    ev.terminalDirectLeg = .with {
                        $0.streamID = streamId
                        $0.legID = legId
                        $0.direct = false
                        $0.open = true
                    }
                    await self.emitCoreEvent(ev)
                }
            },
            onState: { st, reason, needsConfirm in
                // M5: unified Core-driven close policy (close iff TermStateClosed or NeedsConfirmation).
                if !closed.withLock({ $0 }) && (st == 5 || needsConfirm) {
                    let msg: String
                    if reason == "conflict" {
                        msg = L10n.errorsTerminalFramesAreOutOfOrder()
                    } else if reason == "replay_overflow" {
                        msg = L10n.terminalReplayOverflow()
                    } else if let err = lastRelayError.withLock({ let e = $0; $0 = nil; return e }) {
                        msg = L10n.errorsIosTerminalConnectionDropped(value: String(describing: errorText(err)))
                    } else {
                        msg = L10n.errorsTerminalConnectionDropped()
                    }
                    finish(msg)
                    socketBox.withLock { $0 }.cancel()
                }
            },
            onDirectOutput: { mId, pcId, env, _ in
                guard mId == machineId, !closed.withLock({ $0 }) else { return }
                relayChain.run {
                    await handleInbound(env, true, pcId)
                }
            }
        ))

        startReadLoop(initialSocket, activeLegBox.withLock { $0 })

        // M6: register in Core before sending openTerminal RPC so early frames are never lost.
        var openEv = PBCoreEvent()
        openEv.terminalDirectOpen = .with {
            $0.streamID = streamId
            $0.machineID = machineId
            $0.sessionID = sid
            $0.relayLegID = activeLegBox.withLock { $0 }
        }
        await emitCoreEvent(openEv)

        do {
            var cmd = PBCommand()
            cmd.openTerminal.sessionID = sid
            cmd.openTerminal.streamID = streamId
            try await forward(try prepare(machineId: machineId, sessionId: sid, cmd))
        } catch {
            stream.end()
            throw error
        }
        // H-B: signal Core that daemon confirmed openTerminal so direct AttachPath fires now.
        if !closed.withLock({ $0 }) {
            var readyEv = PBCoreEvent()
            readyEv.terminalDirectLeg = .with {
                $0.streamID = streamId
                $0.legID = activeLegBox.withLock { $0 }
                $0.direct = false
                $0.open = true
            }
            await emitCoreEvent(readyEv)
        }

        let sealFn: @Sendable (PBTerminalFrame, UInt64, UInt64) async throws -> (Data, Data, Bool) = { [weak self] frame, seq, deadline in
            guard let self, !closed.withLock({ $0 }) else { throw SecurityFailure.storage }
            let purpose: SecurityPurpose
            let isInput: Bool
            switch frame.kind {
            case .input:
                purpose = .terminalInput
                isInput = true
            case .resize:
                purpose = .terminalResize
                isInput = false
            case .historyRequest:
                purpose = .terminalHistory
                isInput = false
            default:
                throw SecurityFailure.invalid
            }
            let plain = try frame.serializedData()
            let sealed = try await self.sealEnvelopeOp(
                purpose: purpose,
                machineId: machineId,
                sessionId: sid,
                requestId: streamId,
                seq: seq,
                deadlineMs: Int64(bitPattern: deadline),
                plaintext: plain,
                useSessionSeed: true
            )
            guard !closed.withLock({ $0 }) else { throw SecurityFailure.storage }
            let hash = Data(SHA256.hash(data: plain))
            return (try sealed.envelope.serializedData(), hash, isInput)
        }

        let dispatchInputFn: @Sendable (UInt64, Bool, Data, Data) async -> Bool = { [weak self] seq, isInput, encoded, frameHash in
            guard let self else { return false }
            var ev = PBCoreEvent()
            ev.terminalDirectInput = .with {
                $0.streamID = streamId
                $0.seq = seq
                $0.isInput = isInput
                $0.sealedEnvelope = encoded
                $0.frameHash = frameHash
            }
            await self.emitCoreEvent(ev)
            return true
        }

        return TermConn(
            socket: initialSocket,
            socketBox: socketBox,
            seal: sealFn,
            dispatchInput: dispatchInputFn,
            finish: finish,
            closeLocal: { stream.end(cancelPath: false) }
        )
    }
}
