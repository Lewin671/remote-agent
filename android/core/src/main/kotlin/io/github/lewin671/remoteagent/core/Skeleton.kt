package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*

const val skeletonNote = "\n…（remote-agent：内容过大，请在终端查看/回答）"
const val skeletonMessage = "内容过大，请在终端查看/回答"
fun isSkeleton(text: String): Boolean = text.endsWith(skeletonNote)
fun isSkeleton(a: ApprovalRequested): Boolean = listOf(a.description, a.approvalId, a.command, a.toolInputJson, a.inputDigest).any(::isSkeleton)
fun isSkeleton(q: QuestionAsked): Boolean = isSkeleton(q.questionId) || q.questionsList.any { x ->
    listOf(x.id, x.text, x.header).any(::isSkeleton) || x.optionsList.any { isSkeleton(it.label) || isSkeleton(it.description) }
}
fun isSkeleton(e: Event): Boolean {
    if (listOf(e.sessionId, e.turnId, e.origin.deviceId, e.origin.requestId).any(::isSkeleton)) return true
    return when (e.kindCase) {
        Event.KindCase.USER_MESSAGE -> isSkeleton(e.userMessage.text)
        Event.KindCase.AGENT_MESSAGE -> isSkeleton(e.agentMessage.text)
        Event.KindCase.REASONING -> isSkeleton(e.reasoning.text)
        Event.KindCase.TOOL_CALL -> isSkeleton(e.toolCall.title)
        Event.KindCase.SCHEDULED_NOTIFICATION -> isSkeleton(e.scheduledNotification.text)
        Event.KindCase.TURN_ENDED -> isSkeleton(e.turnEnded.reason)
        Event.KindCase.TOKEN_USAGE -> isSkeleton(e.tokenUsage.planType)
        Event.KindCase.TURN_CONTEXT -> isSkeleton(e.turnContext.model)
        Event.KindCase.APPROVAL_REQUESTED -> isSkeleton(e.approvalRequested)
        Event.KindCase.APPROVAL_RESOLVED -> isSkeleton(e.approvalResolved.message)
        Event.KindCase.QUESTION_ASKED -> isSkeleton(e.questionAsked)
        Event.KindCase.QUESTION_ANSWERED -> e.questionAnswered.answersList.any { it.answersList.any(::isSkeleton) }
        Event.KindCase.SESSION_STATE -> isSkeleton(e.sessionState.detail)
        Event.KindCase.INPUT_STATE -> isSkeleton(e.inputState.text)
        Event.KindCase.PLAN_PROPOSED -> isSkeleton(e.planProposed.text)
        Event.KindCase.SETTINGS_CHANGED -> isSkeleton(e.settingsChanged.model)
        Event.KindCase.TOOL_STARTED -> isSkeleton(e.toolStarted.title)
        Event.KindCase.IMAGE_GENERATED -> isSkeleton(e.imageGenerated.revisedPrompt)
        Event.KindCase.SESSION_FORKED -> isSkeleton(e.sessionForked.sourceTitle)
        else -> false
    }
}
