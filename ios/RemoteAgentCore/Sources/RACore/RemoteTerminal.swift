// A session's terminal (architecture §11, docs/transport.md §5):
// This shell is a stateless byte-pipe executor: it opens /v1/term sockets using
// Core's pre-built TermHello bytes, forwards raw inbound envelope bytes to Core
// (TerminalInboundBytesEvent), sends user input/resize/history actions to Core
// (TerminalInputAction), and delivers Core-decrypted output frames and lifecycle
// state transitions directly to the terminal UI.
import Foundation
import RAProto
import os

public enum TermConfirmKind: Equatable, Sendable {
    case input
    case restart
}

public protocol TermListener: Sendable {
    /// Called for each frame in order, one at a time.
    func onFrame(_ frame: PBTerminalFrame)
    /// Called once, when the stream is over, unless this side closed it.
    func onClose(_ reason: String)
    /// Called when Core requires explicit user confirmation before rebuilding the stream.
    func onConfirmRebuild(_ kind: TermConfirmKind)
    /// Called after the user confirmed rebuild and Core closed the old stream.
    func onRebuildConfirmed()
}

public extension TermListener {
    func onConfirmRebuild(_ kind: TermConfirmKind) {}
    func onRebuildConfirmed() {}
}

/// Bounded shell-side terminal pipe budgets aligned with Android (Remote.kt).
enum TerminalPipeLimits {
    static let maxInboundFrameBytes = 1 << 20        // 1 MiB single frame
    static let maxInboundPendingFrames = 128         // 128 unacknowledged frames
    static let maxInboundPendingBytes = 2 << 20      // 2 MiB unacknowledged bytes
    static let maxOutboundFrameBytes = 64 * 1024     // 64 KiB single outbound frame
    static let maxOutboundPendingFrames = 64         // 64 unacknowledged outbound frames
    static let socketFailureCodes: Set<String> = ["socket_error", "closed"]
}

/// Stateless lookup table mapping pending open op_ids and active logical terminal
/// stream IDs to their I/O callbacks.
final class ActiveTerminalRegistry: @unchecked Sendable {
    struct PendingOpen: Sendable {
        let onOpenSocket: @Sendable (PBTerminalOpenSocketEffect) -> Void
        let onStateChanged: @Sendable (PBTerminalStateChangedEffect) -> Void
    }

    struct Entry: Sendable {
        let sendRelay: @Sendable (Data) -> Void
        let onSentOut: @Sendable () -> Void
        let onOpenSocket: @Sendable (PBTerminalOpenSocketEffect) -> Void
        let onCloseSocket: @Sendable () -> Void
        let onDeliver: @Sendable (PBTerminalDeliverOutputEffect) -> Void
        let onStateChanged: @Sendable (PBTerminalStateChangedEffect) -> Void
        let onDirectBytes: @Sendable (String, UInt64, Data) -> Void
    }

    private struct State {
        var pending: [UInt64: PendingOpen] = [:]
        var active: [String: Entry] = [:]
        var nextOpId: UInt64 = 1
    }

    private let state = OSAllocatedUnfairLock(initialState: State())

    func allocateOpId() -> UInt64 {
        state.withLock { s in
            let id = s.nextOpId
            s.nextOpId &+= 1
            return id
        }
    }

    func registerPending(_ opId: UInt64, _ pending: PendingOpen) {
        state.withLock { $0.pending[opId] = pending }
    }

    func unregisterPending(_ opId: UInt64) {
        state.withLock { _ = $0.pending.removeValue(forKey: opId) }
    }

    func register(_ streamId: String, _ entry: Entry) {
        state.withLock { $0.active[streamId] = entry }
    }

    func unregister(_ streamId: String) {
        state.withLock { _ = $0.active.removeValue(forKey: streamId) }
    }

    func handleOpenSocket(_ eff: PBTerminalOpenSocketEffect) {
        let pending = state.withLock { $0.pending[eff.opID] }
        if let pending {
            pending.onOpenSocket(eff)
            return
        }
        let active = state.withLock { $0.active[eff.streamID] }
        active?.onOpenSocket(eff)
    }

    func handleCloseSocket(_ eff: PBTerminalCloseSocketEffect) {
        let active = state.withLock { $0.active[eff.streamID] }
        active?.onCloseSocket()
    }

    func handleDeliverOutput(_ eff: PBTerminalDeliverOutputEffect) {
        let active = state.withLock { $0.active[eff.streamID] }
        active?.onDeliver(eff)
    }

    func handleStateChanged(_ eff: PBTerminalStateChangedEffect) {
        let target: (@Sendable (PBTerminalStateChangedEffect) -> Void)? = state.withLock { s in
            if !eff.streamID.isEmpty, let active = s.active[eff.streamID] {
                return active.onStateChanged
            }
            if eff.opID != 0, let pending = s.pending[eff.opID] {
                return pending.onStateChanged
            }
            return nil
        }
        target?(eff)
    }

    func sendRelay(streamId: String, raw: Data) {
        let active = state.withLock { $0.active[streamId] }
        active?.sendRelay(raw)
    }

    func onSentOut(streamId: String) {
        let active = state.withLock { $0.active[streamId] }
        active?.onSentOut()
    }

    func onDirectBytes(streamId: String, machineId: String, pcId: UInt64, raw: Data) {
        let active = state.withLock { $0.active[streamId] }
        active?.onDirectBytes(machineId, pcId, raw)
    }
}

public final class TermConn: Sendable {
    private let sendFrame: @Sendable (PBTerminalFrame) -> Void
    private let closeLocal: @Sendable () -> Void
    private let confirmRebuildAction: @Sendable () -> Void

    init(
        sendFrame: @escaping @Sendable (PBTerminalFrame) -> Void,
        closeLocal: @escaping @Sendable () -> Void,
        confirmRebuild: @escaping @Sendable () -> Void = {}
    ) {
        self.sendFrame = sendFrame
        self.closeLocal = closeLocal
        self.confirmRebuildAction = confirmRebuild
    }

    public func send(_ frame: PBTerminalFrame) {
        sendFrame(frame)
    }

    public func close() {
        closeLocal()
    }

    public func confirmRebuild() {
        confirmRebuildAction()
    }
}

/// Thread-safe inbound/outbound terminal backlog tracker enforcing bounded memory
/// before frames cross into Go clientcore.
final class TerminalBacklog: Sendable {
    private struct Counts {
        var inFrames = 0
        var inBytes = 0
        var outFrames = 0
    }

    private let counts = OSAllocatedUnfairLock(initialState: Counts())

    func reserveInbound(bytes: Int) -> Bool {
        guard bytes <= TerminalPipeLimits.maxInboundFrameBytes else { return false }
        return counts.withLock { c in
            if c.inFrames + 1 > TerminalPipeLimits.maxInboundPendingFrames
                || c.inBytes + bytes > TerminalPipeLimits.maxInboundPendingBytes
            {
                return false
            }
            c.inFrames += 1
            c.inBytes += bytes
            return true
        }
    }

    func releaseInbound(frames: UInt32, bytes: UInt32) {
        counts.withLock { c in
            c.inFrames = max(0, c.inFrames - Int(frames))
            c.inBytes = max(0, c.inBytes - Int(bytes))
        }
    }

    func reserveOutbound(bytes: Int) -> Bool {
        guard bytes <= TerminalPipeLimits.maxOutboundFrameBytes else { return false }
        return counts.withLock { c in
            if c.outFrames + 1 > TerminalPipeLimits.maxOutboundPendingFrames {
                return false
            }
            c.outFrames += 1
            return true
        }
    }

    func releaseOutboundOne() {
        counts.withLock { c in
            c.outFrames = max(0, c.outFrames - 1)
        }
    }
}

extension Remote {
    /// Joins a session's terminal stream using Core's unified byte-pipe
    /// (`TerminalOpenAction` / `TerminalOpenSocketEffect` / `TerminalInboundBytesEvent` /
    /// `TerminalInputAction` / `TerminalDeliverOutputEffect` / `TerminalStateChangedEffect`).
    public func openTerminal(_ sid: String, listener: any TermListener, terminalId: String = "") async throws -> TermConn {
        try await ensureSecurityBootstrapped()
        let sessionMachineId = machineIdForSessionIfKnown(sid) ?? ""
        let opId = activeTerminals.allocateOpId()
        let openedWaiter = ContinuationWaiter<Void>()
        let openedSettled = OSAllocatedUnfairLock(initialState: false)
        let closed = OSAllocatedUnfairLock(initialState: false)
        let streamIdBox = OSAllocatedUnfairLock<String>(initialState: "")
        let socketsState = OSAllocatedUnfairLock<(active: (any FrameStream)?, all: [any FrameStream])>(initialState: (nil, []))
        let backlog = TerminalBacklog()
        let inboundChain = SerialChain()
        let outboundChain = SerialChain()
        let terminals = activeTerminals

        let settleOpen: @Sendable (Result<Void, Error>) -> Void = { result in
            let first = openedSettled.withLock { s -> Bool in
                guard !s else { return false }
                s = true
                return true
            }
            if first { openedWaiter.resolve(result) }
        }

        let cancelAllSockets: @Sendable () -> Void = {
            let list = socketsState.withLock { s -> [any FrameStream] in
                let cur = s.all
                s.all.removeAll()
                s.active = nil
                return cur
            }
            for ws in list { ws.cancel() }
        }

        let stream = deviceLink.openStream(stop: { [weak self] _ in
            let first = closed.withLock { c -> Bool in
                guard !c else { return false }
                c = true
                return true
            }
            guard first else { return }
            terminals.unregisterPending(opId)
            let sidStr = streamIdBox.withLock { $0 }
            if !sidStr.isEmpty {
                terminals.unregister(sidStr)
            }
            cancelAllSockets()
            settleOpen(.failure(UserError(L10nMessage.errorsIosTerminalClosed())))
            Task { [weak self] in
                await self?.emitCoreEvent(PBCoreEvent.with {
                    $0.terminalClose = .with {
                        $0.streamID = sidStr
                        $0.opID = opId
                    }
                })
            }
        }, onClose: listener.onClose)

        let finish: @Sendable (String) -> Void = { reason in stream.end(reason) }

        let sendInbound: @Sendable (String, Data, Bool, UInt64) -> Void = { [weak self] sidStr, raw, fromDirect, pcId in
            guard let self, !closed.withLock({ $0 }) else { return }
            guard backlog.reserveInbound(bytes: raw.count) else {
                finish(L10n.errorsTerminalOutputBacklogReconnectForTheLatestDisplay())
                return
            }
            inboundChain.run { [weak self] in
                await self?.emitCoreEvent(PBCoreEvent.with {
                    $0.terminalInboundBytes = .with {
                        $0.streamID = sidStr
                        $0.rawEnvelope = raw
                        $0.fromDirect = fromDirect
                        $0.pcID = pcId
                    }
                })
            }
        }

        let openSocket: @Sendable (PBTerminalOpenSocketEffect) -> Void = { [weak self] eff in
            guard let self, !closed.withLock({ $0 }) else { return }
            let sidStr = eff.streamID
            let legBytes = eff.legID
            let nextSock = self.deviceLink.relay.openStream("/v1/term", hello: eff.helloBytes)
            let added = socketsState.withLock { s -> Bool in
                guard !closed.withLock({ $0 }) else { return false }
                s.all.append(nextSock)
                if eff.isInitial { s.active = nextSock }
                return true
            }
            guard added else {
                nextSock.cancel()
                return
            }

            Task.detached { [weak self] in
                guard let self else { return }
                let sleep = self.sleep
                let timeoutTask = Task {
                    if (try? await sleep(20_000)) != nil { nextSock.cancel() }
                }
                let isOpen = await nextSock.opened()
                timeoutTask.cancel()
                guard isOpen, !closed.withLock({ $0 }) else {
                    nextSock.cancel()
                    socketsState.withLock { s in
                        s.all.removeAll { $0 as AnyObject === nextSock as AnyObject }
                        if s.active as AnyObject? === nextSock as AnyObject { s.active = nil }
                    }
                    guard !closed.withLock({ $0 }) else { return }
                    if !openedSettled.withLock({ $0 }) && eff.isInitial {
                        settleOpen(.failure(UserError(L10nMessage.errorsCannotConnectToTerminal())))
                        stream.end()
                        return
                    }
                    let end = await nextSock.ended()
                    await self.emitCoreEvent(PBCoreEvent.with {
                        $0.terminalSocket = .with {
                            $0.streamID = sidStr
                            $0.legID = legBytes
                            $0.closed = true
                            $0.policyViolation = end.closeCode == policyViolation
                            if let err = end.error { $0.error = errorText(err) }
                        }
                    })
                    return
                }

                let prev = socketsState.withLock { s -> (any FrameStream)? in
                    let old = s.active
                    s.active = nextSock
                    if let old, old as AnyObject !== nextSock as AnyObject {
                        s.all.removeAll { $0 as AnyObject === old as AnyObject }
                        return old
                    }
                    return nil
                }
                prev?.close()

                await self.emitCoreEvent(PBCoreEvent.with {
                    $0.terminalSocket = .with {
                        $0.streamID = sidStr
                        $0.legID = legBytes
                        $0.opened = true
                    }
                })

                while let bytes = try? await nextSock.receive() {
                    if closed.withLock({ $0 }) { break }
                    let isCurrent = socketsState.withLock { $0.active as AnyObject? === nextSock as AnyObject }
                    guard isCurrent else { break }
                    sendInbound(sidStr, bytes, false, 0)
                }

                let isCurrent = socketsState.withLock { $0.active as AnyObject? === nextSock as AnyObject }
                guard isCurrent, !closed.withLock({ $0 }) else { return }
                let end = await nextSock.ended()
                await self.emitCoreEvent(PBCoreEvent.with {
                    $0.terminalSocket = .with {
                        $0.streamID = sidStr
                        $0.legID = legBytes
                        $0.closed = true
                        $0.policyViolation = end.closeCode == policyViolation
                        if let err = end.error { $0.error = errorText(err) }
                    }
                })
            }
        }

        let onDeliver: @Sendable (PBTerminalDeliverOutputEffect) -> Void = { eff in
            guard !closed.withLock({ $0 }) else { return }
            defer {
                if eff.consumedFrames > 0 || eff.consumedBytes > 0 {
                    backlog.releaseInbound(frames: eff.consumedFrames, bytes: eff.consumedBytes)
                }
            }
            for f in eff.frames {
                if closed.withLock({ $0 }) { break }
                var frame = PBTerminalFrame()
                switch f.kind {
                case .output(let data)?:
                    frame.output = data
                case .snapshot(let snap)?:
                    frame.snapshot.cols = snap.cols
                    frame.snapshot.rows = snap.rows
                    frame.snapshot.data = snap.data
                case .closed(let reason)?:
                    frame.closed = reason
                case nil:
                    continue
                }
                listener.onFrame(frame)
            }
        }

        let onStateChanged: @Sendable (PBTerminalStateChangedEffect) -> Void = { eff in
            guard !closed.withLock({ $0 }) else { return }
            if eff.openSettled && !openedSettled.withLock({ $0 }) {
                if eff.openOk {
                    settleOpen(.success(()))
                    return
                }
                let code = eff.reasonCode.isEmpty ? "failed" : eff.reasonCode
                let rendered = eff.hasOpenError
                    ? renderCoreText(eff.openError)
                    : (eff.hasReasonText ? renderCoreText(eff.reasonText) : "")
                let err: Error
                if TerminalPipeLimits.socketFailureCodes.contains(code) {
                    err = UserError(L10nMessage.errorsCannotConnectToTerminal())
                } else {
                    err = CommandError(
                        code,
                        rendered.isEmpty ? L10nMessage.errorsTerminalConnectionClosed() : LocalizedMessage(raw: rendered)
                    )
                }
                settleOpen(.failure(err))
                stream.end()
                return
            }
            switch eff.state {
            case .confirmInput:
                listener.onConfirmRebuild(.input)
            case .confirmRestart:
                listener.onConfirmRebuild(.restart)
            case .rebuild:
                stream.end(cancelPath: true)
                listener.onRebuildConfirmed()
            case .closed:
                let rendered = eff.hasReasonText ? renderCoreText(eff.reasonText) : ""
                finish(rendered.isEmpty ? L10n.errorsTerminalConnectionDropped() : rendered)
            default:
                break
            }
        }

        terminals.registerPending(opId, ActiveTerminalRegistry.PendingOpen(
            onOpenSocket: { eff in
                guard !closed.withLock({ $0 }) else { return }
                streamIdBox.withLock { $0 = eff.streamID }
                terminals.unregisterPending(opId)
                terminals.register(eff.streamID, ActiveTerminalRegistry.Entry(
                    sendRelay: { raw in
                        guard !closed.withLock({ $0 }) else { return }
                        let sock = socketsState.withLock { $0.active }
                        sock?.send(raw) { _ in }
                    },
                    onSentOut: {
                        backlog.releaseOutboundOne()
                    },
                    onOpenSocket: openSocket,
                    onCloseSocket: cancelAllSockets,
                    onDeliver: onDeliver,
                    onStateChanged: onStateChanged,
                    onDirectBytes: { mId, pcId, raw in
                        guard !closed.withLock({ $0 }),
                              sessionMachineId.isEmpty || mId == sessionMachineId else { return }
                        sendInbound(eff.streamID, raw, true, pcId)
                    }
                ))
                openSocket(eff)
            },
            onStateChanged: onStateChanged
        ))

        do {
            await emitCoreEvent(PBCoreEvent.with {
                $0.terminalOpen = .with {
                    $0.opID = opId
                    $0.sessionID = sid
                    $0.machineID = sessionMachineId
                    $0.terminalID = terminalId
                }
            })
            try await withTimeout(20_000, sleep: sleep) {
                try await withTaskCancellationHandler {
                    try await withCheckedThrowingContinuation { cont in
                        _ = openedWaiter.install(cont)
                    }
                } onCancel: {
                    settleOpen(.failure(CancellationError()))
                }
            }
            if closed.withLock({ $0 }) {
                throw UserError(L10nMessage.errorsIosTerminalClosed())
            }
        } catch is TimedOut {
            stream.end()
            throw UserError(L10nMessage.errorsCannotConnectToTerminal())
        } catch {
            stream.end()
            throw error
        }

        let sidStr = streamIdBox.withLock { $0 }
        return TermConn(
            sendFrame: { [weak self] frame in
                guard let self, !closed.withLock({ $0 }) else { return }
                let size = (try? frame.serializedData().count) ?? 0
                guard backlog.reserveOutbound(bytes: size) else {
                    finish(L10n.errorsTerminalInputBacklogReconnect())
                    return
                }
                var action = PBTerminalInputAction()
                action.streamID = sidStr
                switch frame.kind {
                case .input(let data)?:
                    action.input = data
                case .resize(let r)?:
                    action.resize = .with {
                        $0.cols = r.cols
                        $0.rows = r.rows
                        $0.fit = r.fit
                    }
                case .historyRequest(let h)?:
                    action.history = .with {
                        $0.requestID = h.requestID
                        $0.cursor = h.cursor
                    }
                default:
                    backlog.releaseOutboundOne()
                    return
                }
                let finalAction = action
                outboundChain.run { [weak self] in
                    await self?.emitCoreEvent(PBCoreEvent.with { $0.terminalInput = finalAction })
                }
            },
            closeLocal: { stream.end(cancelPath: false) },
            confirmRebuild: { [weak self] in
                guard let self, !closed.withLock({ $0 }), !sidStr.isEmpty else { return }
                Task { [weak self] in
                    await self?.emitCoreEvent(PBCoreEvent.with {
                        $0.terminalConfirmRebuild = .with { $0.streamID = sidStr }
                    })
                }
            }
        )
    }
}
