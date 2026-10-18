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
    guard let snapshot else { throw CommandError("failed_precondition", "当前请求尚未由电脑核实") }
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

/// Event histories are for reading. Only an owner response to a fresh request
/// supplies current decisions; missing freshness is visible to the caller.
public func currentActions(_ history: Derived, live: PBLiveSnapshot?) -> Derived {
    var result = history
    result.approvals = OrderedMap()
    result.questions = OrderedMap()
    result.plan = nil
    guard let live else { return result }
    let observed = derive(live.actions)
    for id in history.inputs.keys {
        if let status = history.inputs[id], status == .queued || status == .heldByCli,
            observed.inputs[id] == nil
        {
            result.inputs[id] = .unconfirmed
            result.inputDetails[id] = nil
        }
    }
    for id in observed.inputs.keys { result.inputs[id] = observed.inputs[id] }
    for id in observed.inputDetails.keys { result.inputDetails[id] = observed.inputDetails[id] }
    for (id, accepted) in observed.accepted { result.accepted[id] = accepted }
    result.running = live.running
    for event in live.actions {
        switch event.kind {
        case .approvalRequested(let request)?: result.approvals[request.approvalID] = request
        case .questionAsked(let request)?:
            if !request.async { result.questions[request.questionID] = request }
        default: break
        }
    }
    if live.planEventSeq > 0 && live.planScreenHash.count == 32 {
        var plan = PBPlanProposed()
        plan.text = live.planScreen
        result.plan = plan
    }
    return result
}

extension Remote {
    func fetchLiveSnapshot(_ sid: String) async throws -> PBLiveSnapshot {
        guard let session = sessions[sid] else { throw CommandError("not_found", "未知会话") }
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
            liveErrors[sid] = "无法核实电脑上的待处理请求，请重试或在电脑终端处理"
        }
        if let buffer = buffers[sid] { publishEvents(sid, buffer) }
    }
    func startLivePolling(_ sid: String) {
        guard liveJobs[sid] == nil, !closed else { return }
        liveJobs[sid] = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                await self.refreshLiveSnapshot(sid)
                if await self.livePollingFinished(sid) { break }
                do { try await Task.sleep(for: .seconds(5)) } catch { break }
            }
            await self.finishLivePolling(sid)
        }
    }
    private func livePollingFinished(_ sid: String) -> Bool {
        closed || !running || eventFeeds.withLock { $0[sid]?.followers ?? 0 } == 0
    }
    private func finishLivePolling(_ sid: String) { liveJobs[sid] = nil }
}

public func currentState(_ snapshot: SessionEvents, agent: PBAgent = .codex) -> Derived {
    currentActions(
        derive(snapshot.unverifiedLegacy ? snapshot.verifiedEvents : snapshot.events, agent: agent),
        live: snapshot.live)
}
