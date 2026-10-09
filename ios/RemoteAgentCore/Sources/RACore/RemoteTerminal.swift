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
        var seq: UInt64 = 0
        var queued = 0
    }

    private let socket: Socket
    private let out = OSAllocatedUnfairLock(initialState: Out())
    private let seal: @Sendable (PBTerminalFrame, UInt64) throws -> Data
    private let finish: @Sendable (String) -> Void
    private let closeLocal: @Sendable () -> Void

    init(
        socket: Socket, seal: @escaping @Sendable (PBTerminalFrame, UInt64) throws -> Data,
        finish: @escaping @Sendable (String) -> Void, closeLocal: @escaping @Sendable () -> Void
    ) {
        self.socket = socket
        self.seal = seal
        self.finish = finish
        self.closeLocal = closeLocal
    }

    /// Sends a frame. Frames go out in the order of the calls, each with the next number.
    public func send(_ frame: PBTerminalFrame) {
        // Numbered, sealed and queued under one lock: the daemon ends the
        // stream on a number that does not go up.
        let failure: String? = out.withLock { o in
            o.seq += 1
            guard let bytes = try? seal(frame, o.seq) else { return "终端连接断开：加密失败" }
            // Written too slowly to keep up: ending the stream is said out
            // loud, where dropping input would not be.
            if o.queued + bytes.count > termSendBudget { return "终端连接断开" }
            o.queued += bytes.count
            let n = bytes.count
            socket.send(bytes) { [out] _ in out.withLock { $0.queued -= n } }
            return nil
        }
        if let failure {
            finish(failure)
            socket.cancel()
        }
    }

    public func close() {
        closeLocal()
        socket.close()
    }
}

extension Remote {
    /// Joins a session's terminal stream. Frames are sealed with the session key
    /// and numbered; each side refuses frames out of order.
    public func openTerminal(_ sid: String, listener: any TermListener) async throws -> TermConn {
        guard let s = sessions[sid], let key = sessionKeys[sid] else { throw CommandError("not_found", "未知会话") }
        let tok = try await currentToken()
        let streamId = newRequestId()
        let machineId = s.machineId
        let closed = OSAllocatedUnfairLock(initialState: false)
        let finish: @Sendable (String) -> Void = { reason in
            let first = closed.withLock { c -> Bool in
                defer { c = true }
                return !c
            }
            if first { listener.onClose(reason) }
        }
        var hello = PBTermHello()
        hello.token = tok
        hello.streamID = streamId
        hello.machineID = machineId
        let socket = Socket(http: http, url: api.socketURL("/v1/term"), hello: try hello.serializedData(), sleep: sleep)
        // The upgrade gets 20 s, the time the server keeps the first side waiting for the other.
        let sleep = self.sleep
        let limit = Task {
            if (try? await sleep(20_000)) != nil { socket.cancel() }
        }
        let open = await socket.opened()
        limit.cancel()
        if !open {
            closed.withLock { $0 = true }
            socket.cancel()
            throw UserError("连不上终端")
        }
        Task.detached {
            var last: UInt64 = 0
            while let bytes = try? await socket.receive() {
                if closed.withLock({ $0 }) { continue }
                do {
                    let (h, frame) = try Envelopes.openMessage(
                        key: key, envelope: try PBEnvelope(serializedBytes: bytes),
                        want: Place(.terminalOutput, machineId: machineId, sessionId: sid, requestId: streamId),
                        as: PBTerminalFrame.self)
                    if h.seq <= last { throw OpenError("终端帧顺序不对") }
                    last = h.seq
                    listener.onFrame(frame)
                } catch {
                    finish(errorText(error))
                    socket.cancel()
                }
            }
            let end = await socket.ended()
            finish(end.error.map { "终端连接断开：" + errorText($0) } ?? "终端连接断开")
        }
        do {
            var cmd = PBCommand()
            cmd.openTerminal.sessionID = sid
            cmd.openTerminal.streamID = streamId
            try await command(sid, cmd)
        } catch {
            closed.withLock { $0 = true }
            socket.cancel()
            throw error
        }
        let sender = account.deviceId
        return TermConn(
            socket: socket,
            seal: { frame, seq in
                var h = PBHeader()
                h.kind = .terminalInput
                h.machineID = machineId
                h.sessionID = sid
                h.requestID = streamId
                h.seq = seq
                h.sender = sender
                return try Envelopes.seal(key: key, header: h, message: frame).serializedData()
            },
            finish: finish,
            closeLocal: { closed.withLock { $0 = true } })
    }
}
