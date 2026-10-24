import RAProto

public let skeletonNote = "\n…（remote-agent：内容过大，请在终端查看/回答）"
public let skeletonMessage = "内容过大，请在终端查看/回答"
public func isSkeleton(_ text: String) -> Bool { text.hasSuffix(skeletonNote) }
public func isSkeleton(_ a: PBApprovalRequested) -> Bool {
  [a.description_p, a.approvalID, a.command, a.toolInputJson, a.inputDigest].contains(
    where: isSkeleton)
}
public func isSkeleton(_ q: PBQuestionAsked) -> Bool {
  isSkeleton(q.questionID)
    || q.questions.contains { x in
      [x.id, x.text, x.header].contains(where: isSkeleton)
        || x.options.contains { isSkeleton($0.label) || isSkeleton($0.description_p) }
    }
}
public func isSkeleton(_ e: PBEvent) -> Bool {
  if [e.sessionID, e.turnID, e.origin.deviceID, e.origin.requestID].contains(where: isSkeleton) {
    return true
  }
  switch e.kind {
  case .userMessage(let x): return isSkeleton(x.text)
  case .agentMessage(let x): return isSkeleton(x.text)
  case .reasoning(let x): return isSkeleton(x.text)
  case .toolCall(let x): return isSkeleton(x.title)
  case .scheduledNotification(let x): return isSkeleton(x.text)
  case .turnEnded(let x): return isSkeleton(x.reason)
  case .tokenUsage(let x): return isSkeleton(x.planType)
  case .turnContext(let x): return isSkeleton(x.model)
  case .approvalRequested(let x): return isSkeleton(x)
  case .approvalResolved(let x): return isSkeleton(x.message)
  case .questionAsked(let x): return isSkeleton(x)
  case .questionAnswered(let x):
    return x.answers.contains { $0.answers.contains(where: isSkeleton) }
  case .sessionState(let x): return isSkeleton(x.detail)
  case .inputState(let x): return isSkeleton(x.text)
  case .planProposed(let x): return isSkeleton(x.text)
  case .settingsChanged(let x): return isSkeleton(x.model)
  case .toolStarted(let x): return isSkeleton(x.title)
  case .imageGenerated(let x): return isSkeleton(x.revisedPrompt)
  case .sessionForked(let x): return isSkeleton(x.sourceTitle)
  default: return false
  }
}
