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

/// What a plan the live snapshot says is pending reads as: the plan event it
/// names (`planEventSeq`), the clean Markdown the CLI proposed, when `events`
/// (the readable, verified history, each event bound to its place in the
/// session) hold it. Only without it does the terminal's text stand in, box
/// lines and all. Whether a plan is pending, and what an answer is bound to
/// (the event's seq and the screen's hash), stay the snapshot's alone.
public func livePlanText(_ live: PBLiveSnapshot, events: some RandomAccessCollection<PBEvent>) -> String {
    let seq = live.planEventSeq
    guard seq > 0 else { return live.planScreen }
    // A whole history holds event n at n - 1; otherwise look for it.
    var event: PBEvent?
    if seq <= UInt64(events.count) {
        let e = events[events.index(events.startIndex, offsetBy: Int(seq) - 1)]
        if e.seq == seq { event = e }
    }
    if event == nil { event = events.last { $0.seq == seq } }
    guard case .planProposed(let plan)? = event?.kind,
        !plan.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, !isSkeleton(plan.text)
    else { return live.planScreen }
    return plan.text
}

/// Event histories are for reading. Only an owner response to a fresh request
/// supplies current decisions; missing freshness is visible to the caller. A
/// pending plan's text is read from the history event the response names
/// (`livePlanText`).
public func currentActions(_ history: Derived, live: PBLiveSnapshot?) -> Derived {
    currentActions(history, live: live, events: [PBEvent]())
}

public func currentActions(
    _ history: Derived, live: PBLiveSnapshot?, events: some RandomAccessCollection<PBEvent>
) -> Derived {
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
    for (id, detail) in observed.inputDetails.entries {
        result.inputDetails[id] = retainingInputMetadata(detail, from: history.inputDetails[id])
        if var accepted = result.accepted[id], detail.stage != .unspecified {
            accepted.stage = detail.stage
            result.accepted[id] = accepted
        }
    }
    // The live snapshot can contain truncated text and need not repeat the
    // original sender. Keep the accepted history; only missing entries recover.
    for (id, accepted) in observed.accepted where result.accepted[id] == nil { result.accepted[id] = accepted }
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
        plan.text = livePlanText(live, events: events)
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
        if conn == .online, let session = sessions[sid], machines[session.machineId]?.online == true {
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

public func currentState(_ snapshot: SessionEvents, agent: PBAgent = .codex) -> Derived {
    let events = snapshot.unverifiedLegacy ? snapshot.verifiedEvents : snapshot.events
    return currentActions(derive(events, agent: agent), live: snapshot.live, events: events)
}
