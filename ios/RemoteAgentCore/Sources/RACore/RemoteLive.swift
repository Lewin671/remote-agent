import Foundation
import RAProto

func isObservedAction(_ command: PBCommand) -> Bool {
    switch command.kind {
    case .resolveApproval?, .answerQuestion?, .answerPlan?: return true
    default: return false
    }
}
func observedCommand(_ command: PBCommand, snapshot: PBLiveSnapshot?) throws -> PBCommand {
    guard isObservedAction(command) else { return command }
    guard let snapshot else { throw CommandError("failed_precondition", L10nMessage.errorsIosTheCurrentRequestHasNotBeenVerifiedByThe()) }
    var result = command
    switch command.kind {
    case .resolveApproval(let request)?:
        guard
            let event = snapshot.actions.first(where: { event in
                if case .approvalRequested(let a)? = event.kind {
                    return a.approvalID == request.approvalID
                }
                return false
            }), event.seq > 0
        else { throw DeviceSecurity.Failure.stale }
        result.resolveApproval.observedEventSeq = event.seq
    case .answerQuestion(let request)?:
        guard
            let event = snapshot.actions.first(where: { event in
                if case .questionAsked(let q)? = event.kind {
                    return q.questionID == request.questionID
                }
                return false
            }), event.seq > 0
        else { throw DeviceSecurity.Failure.stale }
        result.answerQuestion.observedEventSeq = event.seq
    case .answerPlan?:
        guard snapshot.planEventSeq > 0, snapshot.planScreenHash.count == 32 else {
            throw DeviceSecurity.Failure.stale
        }
        result.answerPlan.observedEventSeq = snapshot.planEventSeq
        result.answerPlan.observedScreenHash = snapshot.planScreenHash
    default: break
    }
    return result
}

extension Remote {
    func fetchLiveSnapshot(_ sid: String) async throws -> PBLiveSnapshot {
        guard let session = sessions[sid] else { throw CommandError("not_found", L10nMessage.errorsIosUnknownSession()) }
        var command = PBCommand()
        command.getLiveSnapshot.sessionID = sid
        let reply = try await forward(
            prepare(machineId: session.machineId, sessionId: sid, command))
        guard case .liveSnapshot(let snapshot)? = reply.kind, snapshot.session.id == sid else {
            throw DeviceSecurity.Failure.untrusted
        }
        return snapshot
    }
    func refreshLiveSnapshot(_ sid: String) async {
        do {
            let snapshot = try await fetchLiveSnapshot(sid)
            guard !closed else { return }
            liveSnapshots[sid] = snapshot
            liveErrors[sid] = nil
        } catch {
            guard !closed else { return }
            liveSnapshots[sid] = nil
            liveErrors[sid] = L10n.errorsPendingRequestsOnTheComputerCouldNotBeVerified()
        }
        if liveSessionEnded(sid) { clearEndedLiveState(sid) }
        else if let buffer = buffers[sid] { publishEvents(sid, buffer) }
    }
    func startLivePolling(_ sid: String) {
        guard liveJobs[sid] == nil, !closed else { return }
        liveJobs[sid] = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled, await self.livePollingTick(sid) {
                do { try await self.sleep(5000) } catch { break }
            }
            await self.finishLivePolling(sid)
        }
    }
    /// A disconnected machine may return; an ended or unfollowed session is done.
    func livePollingTick(_ sid: String) async -> Bool {
        clearEndedLiveState(sid)
        guard !livePollingFinished(sid) else { return false }
        if let session = sessions[sid], controlPeer(session.machineId).available {
            await refreshLiveSnapshot(sid)
        }
        return !livePollingFinished(sid)
    }

    private func liveSessionEnded(_ sid: String) -> Bool {
        sessions[sid]?.info?.status == .exited || sessions[sid]?.info?.hasEndedAt == true
    }

    func clearEndedLiveState(_ sid: String) {
        guard liveSessionEnded(sid) else { return }
        let snapshot = liveSnapshots.removeValue(forKey: sid)
        let error = liveErrors.removeValue(forKey: sid)
        if snapshot != nil || error != nil, let buffer = buffers[sid] { publishEvents(sid, buffer) }
    }

    private func livePollingFinished(_ sid: String) -> Bool {
        closed || !running || eventFeeds.withLock { $0[sid]?.followers ?? 0 } == 0 || liveSessionEnded(sid)
    }
    private func finishLivePolling(_ sid: String) {
        liveJobs[sid] = nil
        clearEndedLiveState(sid)
        // Metadata may have resumed the session after the last tick decided to stop.
        if !Task.isCancelled && !livePollingFinished(sid) { startLivePolling(sid) }
    }
}

public func currentState(_ snapshot: SessionEvents, agent: PBAgent = defaultAgent) -> PBProjectionState {
    var projection = EventProjection()
    return projection.update(snapshot.stateEvents, agent: agent, live: snapshot.live)
}
