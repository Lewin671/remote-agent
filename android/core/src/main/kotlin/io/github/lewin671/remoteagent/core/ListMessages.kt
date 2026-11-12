package io.github.lewin671.remoteagent.core

import java.time.Instant
import java.time.ZoneId

fun listProjectName(key: String): MessageText = key.split("/").lastOrNull { it.isNotEmpty() }?.messageText()
    ?: UiMessages.projectsUnknown().messageText()

fun listMachineName(m: MachineView?, id: String = ""): MessageText =
    (m?.meta?.hostname?.removeSuffix(".local")?.takeIf { it.isNotEmpty() } ?: m?.id?.take(8) ?: id.take(8).takeIf { it.isNotEmpty() })?.messageText()
        ?: UiMessages.commonUnknownComputer().messageText()

fun listStatus(st: Status): UiMessage = when (st) {
    Status.LOADING -> UiMessages.listStateAndroidLoading()
    Status.EXITED -> UiMessages.listStateEnded()
    Status.OFFLINE -> UiMessages.listStateOffline()
    Status.UNLINKED -> UiMessages.listStateUnlinked()
    Status.APPROVAL -> UiMessages.listStateApproval()
    Status.QUESTION -> UiMessages.listStateQuestion()
    Status.PLAN -> UiMessages.listStatePlan()
    Status.POPUP -> UiMessages.listStatePopup()
    Status.RUNNING -> UiMessages.listStateRunning()
    Status.STARTING -> UiMessages.listStateStarting()
    Status.IDLE -> UiMessages.listStateIdle()
}

fun listTitle(s: SessionView, zone: ZoneId = ZoneId.systemDefault()): MessageText {
    val title = plain(s.info?.title.orEmpty()).trim()
    if (title.isNotEmpty()) return title.messageText()
    val info = s.info ?: return UiMessages.newSessionAndroidNewSession().messageText()
    val created = if (info.hasCreatedAt()) info.createdAt.seconds * 1000 else 0
    return listOfNotNull(listProjectName(info.cwd), hhmm(created, zone).messageText().takeIf { created > 0 }).joinMessages(" · ")
}

fun listPreview(s: SessionView, status: Status): MessageText {
    val info = s.info ?: return "".messageText()
    return when (status) {
        Status.PLAN -> UiMessages.listStatePlan().messageText()
        Status.POPUP -> info.statusDetail.takeIf { it.isNotEmpty() }?.messageText() ?: UiMessages.listStatePopup().messageText()
        else -> plain(info.preview).takeIf { it.isNotEmpty() }?.messageText()
            ?: if (info.title.isEmpty()) UiMessages.listStateNoMessage().messageText() else "".messageText()
    }
}

fun listWhen(ms: Long, now: Long, zone: ZoneId = ZoneId.systemDefault()): MessageText {
    if (ms <= 0) return "".messageText()
    val elapsed = now - ms
    if (elapsed < 60_000) return UiMessages.timeJustNow().messageText()
    if (elapsed < 3_600_000) return UiMessages.timeMinutesAgo((elapsed / 60_000).toInt()).messageText()
    val then = Instant.ofEpochMilli(ms).atZone(zone).toLocalDate()
    val today = Instant.ofEpochMilli(now).atZone(zone).toLocalDate()
    if (then == today) return hhmm(ms, zone).messageText()
    if (then == today.minusDays(1)) return UiMessages.timeYesterday().messageText()
    return UiMessages.timeMonthDay(then.dayOfMonth, then.monthValue).messageText()
}
