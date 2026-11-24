// A session's terminal (architecture §11): this client and the daemon each
// connect to /v1/term with the same stream ID, and the server passes their
// frames along unread. Every frame is sealed with the session key and
// numbered; each side refuses frames out of order.
import Foundation
import RAProto
import os

public protocol TermListener: Sendable {
    /// Called for each frame in order, one at a time. The next frame is not
    /// read before this returns, so a listener that falls behind slows the
    /// stream down instead of piling frames up; none is skipped.
    func onFrame(_ frame: PBTerminalFrame)
    /// Called once, when the stream is over, unless this side closed it.
    func onClose(_ reason: String)
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

    private let socket: any TerminalSocket
    private let sendBudget: Int
    private let out = OSAllocatedUnfairLock(initialState: Out())
    private let seal: @Sendable (PBTerminalFrame, UInt64, UInt64) async throws -> Data
    private let finish: @Sendable (String) -> Void
    private let closeLocal: @Sendable () -> Void

    init(
        socket: any TerminalSocket, sendBudget: Int = termSendBudget,
        seal: @escaping @Sendable (PBTerminalFrame, UInt64, UInt64) async throws -> Data,
        finish: @escaping @Sendable (String) -> Void, closeLocal: @escaping @Sendable () -> Void
    ) {
        self.socket = socket
        self.sendBudget = sendBudget
        self.seal = seal
        self.finish = finish
        self.closeLocal = closeLocal
    }

    /// Sends a frame. Frames go out in the order of the calls, each with the next number.
    public func send(_ frame: PBTerminalFrame) {
        // Numbered, sealed and queued under one lock: the daemon ends the
        // stream on a number that does not go up.
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
            o.tail = Task { [socket, seal, finish, out, sendBudget] in
                await previous?.value
                var reserved = bytes
                defer {
                    let released = reserved
                    out.withLock { $0.queued -= released }
                }
                guard !out.withLock({ $0.stopped }) else { return }
                do {
                    try Task.checkCancellation()
                    let encoded = try await seal(frame, seq, deadline)
                    let accepted = out.withLock { o in
                        guard !o.stopped, encoded.count - bytes <= sendBudget - o.queued else { return false }
                        o.queued += encoded.count - bytes
                        return true
                    }
                    guard accepted else { throw SecurityFailure.capacity }
                    reserved = encoded.count
                    try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
                        socket.send(encoded) { error in
                            if let error { c.resume(throwing: error) } else { c.resume() }
                        }
                    }
                } catch {
                    let first = out.withLock { o in
                        defer { o.stopped = true }
                        return !o.stopped
                    }
                    if first { finish(L10n.errorsIosTerminalInputFailedToSendStopped()); socket.cancel() }
                }
            }
            return nil
        }
        if let failure {
            finish(failure)
            socket.cancel()
        }
    }

    public func close() {
        out.withLock { $0.stopped = true }
        closeLocal()
        socket.close()
    }
}

extension Remote {
    /// Joins a session's terminal stream. Frames are sealed with the session key
    /// and numbered; each side refuses frames out of order.
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
        let socket = deviceLink.relay.openStream("/v1/term", hello: try hello.serializedData())
        let stream = deviceLink.openStream(stop: { cancelPath in
            closed.withLock { $0 = true }
            if cancelPath { socket.cancel() }
        }, onClose: listener.onClose)
        let finish: @Sendable (String) -> Void = { reason in stream.end(reason) }
        // The upgrade gets 20 s, the time the server keeps the first side waiting for the other.
        let sleep = self.sleep
        let limit = Task {
            if (try? await sleep(20_000)) != nil { socket.cancel() }
        }
        let open = await socket.opened()
        limit.cancel()
        if !open {
            stream.end()
            throw UserError(L10nMessage.errorsCannotConnectToTerminal())
        }
        Task.detached {
            var last: UInt64 = 0
            while let bytes = try? await socket.receive() {
                if closed.withLock({ $0 }) { continue }
                guard bytes.count <= 1 << 20 else {
                    finish(L10n.errorsTerminalConnectionDropped())
                    socket.cancel()
                    break
                }
                do {
                    let res = try await self.openEnvelopeOp(
                        envelope: PBEnvelope(serializedBytes: bytes),
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
                    if res.seq <= last { throw OpenError(L10nMessage.errorsTerminalFramesAreOutOfOrder()) }
                    last = res.seq
                    let frame = try PBTerminalFrame(serializedBytes: res.plaintext)
                    listener.onFrame(frame)
                } catch {
                    finish(errorText(error))
                    socket.cancel()
                }
            }
            let end = await socket.ended()
            finish(end.error.map { L10n.errorsIosTerminalConnectionDropped(value: String(describing: errorText($0))) } ?? L10n.errorsTerminalConnectionDropped())
        }
        do {
            var cmd = PBCommand()
            cmd.openTerminal.sessionID = sid
            cmd.openTerminal.streamID = streamId
            _ = try await runMachineCommand(machineId: machineId, sessionId: sid, cmd, skipConnectivityPreflight: true)
        } catch {
            stream.end()
            throw error
        }
        return TermConn(
            socket: socket,
            seal: { frame, seq, deadline in
                guard !closed.withLock({ $0 }) else { throw SecurityFailure.storage }
                let purpose: SecurityPurpose
                switch frame.kind {
                case .input: purpose = .terminalInput
                case .resize: purpose = .terminalResize
                case .historyRequest: purpose = .terminalHistory
                default: throw SecurityFailure.invalid
                }
                let sealed = try await self.sealEnvelopeOp(
                    purpose: purpose,
                    machineId: machineId,
                    sessionId: sid,
                    requestId: streamId,
                    seq: seq,
                    deadlineMs: Int64(bitPattern: deadline),
                    plaintext: try frame.serializedData(),
                    useSessionSeed: true
                )
                guard !closed.withLock({ $0 }) else { throw SecurityFailure.storage }
                return try sealed.envelope.serializedData()
            },
            finish: finish,
            closeLocal: { stream.end(cancelPath: false) })
    }
}
