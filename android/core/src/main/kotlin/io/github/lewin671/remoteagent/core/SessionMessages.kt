package io.github.lewin671.remoteagent.core

fun sessionMode(mode: Mode): UiMessage = when (mode) {
    Mode.ASK -> UiMessages.modesAsk()
    Mode.AUTO -> UiMessages.modesAuto()
    Mode.PLAN -> UiMessages.modesPlan()
    Mode.FULL -> UiMessages.modesFullAccess()
}

fun terminalChangeMessages(changes: List<TerminalChange>, resolve: (UiMessage) -> String): List<UiMessage> = changes.map { change ->
    when (change) {
        is TerminalChange.ModeChanged -> UiMessages.sessionTerminalModeChange(change.mode?.let { resolve(sessionMode(it)) } ?: change.raw)
        is TerminalChange.ModelChanged -> UiMessages.sessionTerminalModelChange(listOf(change.model, change.effort).filter { it.isNotEmpty() }.joinToString(" · "))
    }
}

fun sessionOrigin(o: io.github.lewin671.remoteagent.proto.Origin?, state: RemoteState, me: String, machine: String): MessageText? {
    if (o == null) return null
    return when (o.kind) {
        io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_CLIENT -> when {
            o.deviceId == me -> UiMessages.sessionOriginSelf().messageText()
            o.deviceId.isEmpty() || state.devices.isEmpty() -> UiMessages.sessionOriginOther().messageText()
            else -> state.devices[o.deviceId]?.let { localized { copy -> clientName(it, o.deviceId, state.devices.values, copy = copy) } }
                ?: UiMessages.sessionOriginRemoved().messageText()
        }
        io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_LOCAL -> UiMessages.sessionOriginTerminal(machine).messageText()
        else -> null
    }
}

fun sessionDateTime(ms: Long, now: Long = System.currentTimeMillis(), zone: java.time.ZoneId = java.time.ZoneId.systemDefault()): MessageText {
    if (ms <= 0) return "".messageText()
    val date = java.time.Instant.ofEpochMilli(ms).atZone(zone).toLocalDate()
    val today = java.time.Instant.ofEpochMilli(now).atZone(zone).toLocalDate()
    return listOfNotNull(
        UiMessages.timeMonthDay(date.dayOfMonth, date.monthValue).messageText().takeIf { date != today },
        hhmm(ms, zone).messageText(),
    ).joinMessages(" ")
}
