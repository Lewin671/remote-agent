package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.core.transport.PeerState
import io.github.lewin671.remoteagent.core.transport.RelayPath
import io.github.lewin671.remoteagent.proto.*
import io.github.lewin671.remoteagent.proto.Event
import java.time.Instant
import java.time.ZoneId

// ---------------------------------------------------------------------------
// Transitional Step 3/4 view models & adapter
//
// Legacy screens (`Session`, `Schedules`, `Settings`, `Devices`, `Handoff`,
// `Recovery`, `ConnCheck`, `Files`, `Terminal`) still read `RemoteState` and
// `SessionEvents` until Step 3/4 migrates them to Go Core view models.
// This file projects Go Core's `SyncStateView` / `SessionDataView` into those
// structures and should be deleted when Step 3/4 completes.
// ---------------------------------------------------------------------------

const val PROJECT_PREFS = "project-prefs"
const val LOGIN_REFRESH_MARGIN_MS = 86_400_000L
const val CACHE_EVENT_PAGE = 128
const val SESSION_PAGE = 50
val settingsAgents: List<Agent> get() = AGENTS.map { it.agent }

data class LoginToken(val value: String, val expiresMs: Long) {
    val token: String get() = value
    override fun toString(): String = "LoginToken(expiresMs=$expiresMs)"
}

data class PrefsObject(val revision: Long, val prefs: ProjectPrefs) {
    val pinned: List<String> get() = prefs.pinnedList.map { it.path }
    val hidden: Set<String> get() = prefs.hiddenList.map { it.path }.toSet()

    companion object {
        val EMPTY = PrefsObject(0L, ProjectPrefs.newBuilder().setSchema(1).build())
    }
}

enum class ConnState {
    CONNECTING,
    ONLINE,
    OFFLINE,
}

enum class Link {
    ONLINE,
    WAITING,
    NO_NETWORK,
    NO_SERVER,
}

data class MachineView(
    val id: String,
    val online: Boolean,
    val lastSeenMs: Long,
    val meta: MachineMeta? = null,
    val metaRev: Long = 0,
    /** Runtime Link projection; never persisted or accepted from the server. */
    val linkState: PeerState? = null,
) {
    val pathState: PeerState get() = linkState ?: RelayPath.discovery(this)
}

data class SessionView(
    val id: String,
    val machineId: String,
    val info: Session? = null,
    val checkpoint: io.github.lewin671.remoteagent.proto.LiveSnapshot? = null,
    val checkpointStatus: String = "waiting",
    val metaRev: Long = 0,
    val lastSeq: Long = 0,
    val updatedMs: Long = 0,
    val listedEndedMs: Long = 0,
    val historyError: MessageText? = null,
)

data class SessionPlace(val endedMs: Long, val sessionId: String)

fun afterPlace(endedMs: Long, sessionId: String, place: SessionPlace): Boolean =
    endedMs != 0L &&
        (endedMs < place.endedMs || (endedMs == place.endedMs && sessionId > place.sessionId))

data class DeviceView(
    val id: String,
    val kind: io.github.lewin671.remoteagent.proto.DeviceKind,
    val createdMs: Long,
    val meta: DeviceMeta? = null,
    val metaRev: Long = 0,
    val lastSeenMs: Long = 0,
)

data class RemoteState(
    val machines: Map<String, MachineView> = emptyMap(),
    val sessions: Map<String, SessionView> = emptyMap(),
    val conn: ConnState = ConnState.CONNECTING,
    val error: MessageText = "".messageText(),
    val loaded: Boolean = false,
    val offlineSinceMs: Long = 0,
    val prefs: PrefsObject? = null,
    val devices: Map<String, DeviceView> = emptyMap(),
    val removed: Boolean = false,
    val sessionsNext: SessionPlace? = null,
    val loadingSessions: Boolean = false,
    val sessionsError: MessageText = "".messageText(),
    val sessionsListed: Boolean = true,
    val link: Link = Link.WAITING,
    val synced: Boolean = false,
    val authorityDeviceId: String = "",
    val leaseEndMs: Long = 0,
    val isAuthority: Boolean = false,
    val recoveryPending: Boolean = false,
    val recoveryReadyMs: Long = 0,
    val clockWarning: Boolean = false,
    val displayGrace: Boolean = false,
    val reachabilityUnknown: Boolean = false,
    val errorDetail: String = "",
    val connStep: MessageText = "".messageText(),
    val connStartedAtMs: Long? = null,
    val connError: MessageText = "".messageText(),
) {
    fun peer(machineId: String?): PeerState {
        val machine = machines[machineId]
        return machine?.linkState ?: RelayPath.discovery(machine, conn == ConnState.ONLINE, !removed, unlinked)
    }

    val authorityName: MessageText
        get() = localized { copy -> clientName(devices[authorityDeviceId], authorityDeviceId, devices.values, copy = copy) }

    val renewalAdvice: MessageText
        get() = if (isAuthority) UiMessages.securityRenewNow().messageText()
        else localized { copy -> copy(UiMessages.errorsAndroidOpenValue1ToRenew(authorityName.resolve(copy))) }

    val unlinked: Boolean
        get() = link == Link.NO_NETWORK || link == Link.NO_SERVER || (link == Link.WAITING && !synced)

    val moreSessions: Boolean
        get() = !sessionsListed || sessionsNext != null
}

fun formatTimestampToMessageText(
    arg: TimestampArg,
    zone: ZoneId = ZoneId.systemDefault(),
): MessageText {
    if (arg.unixMs <= 0) return "".messageText()
    val dt = Instant.ofEpochMilli(arg.unixMs).atZone(zone)
    val clock = String.format("%d:%02d", dt.hour, dt.minute)
    return when (arg.style) {
        TimestampStyle.TIMESTAMP_STYLE_TIME -> clock.messageText()
        TimestampStyle.TIMESTAMP_STYLE_WEEKDAY ->
            UiMessages.timeWeekday(dt.dayOfWeek.name.take(3).lowercase()).messageText()
        TimestampStyle.TIMESTAMP_STYLE_MONTH_DAY ->
            UiMessages.timeMonthDay(dt.dayOfMonth, dt.monthValue).messageText()
        TimestampStyle.TIMESTAMP_STYLE_MONTH_DAY_TIME -> localized { copy ->
            val day = copy(UiMessages.timeMonthDay(dt.dayOfMonth, dt.monthValue))
            copy(UiMessages.timeDayClock(clock = clock, day = day))
        }
        TimestampStyle.TIMESTAMP_STYLE_DATE ->
            UiMessages.timeCalendarDateYear(dt.dayOfMonth, dt.monthValue, dt.year).messageText()
        TimestampStyle.TIMESTAMP_STYLE_DATE_TIME -> localized { copy ->
            val day = copy(UiMessages.timeCalendarDateYear(dt.dayOfMonth, dt.monthValue, dt.year))
            copy(UiMessages.timeDayClock(clock = clock, day = day))
        }
        else -> "".messageText()
    }
}

fun coreTextToMessageText(t: CoreText?): MessageText {
    if (t == null) return "".messageText()
    if (t.joinCount > 0) {
        val sep = if (t.separatorKey.isNotEmpty()) UiMessage(t.separatorKey).messageText() else t.separatorLiteral.messageText()
        return localized { copy ->
            val s = sep.resolve(copy)
            t.joinList.map { coreTextToMessageText(it).resolve(copy) }.filter { it.isNotEmpty() }.joinToString(s)
        }
    }
    if (t.hasTimestamp()) {
        return formatTimestampToMessageText(t.timestamp)
    }
    if (t.key.isNotEmpty()) {
        return localized { copy ->
            val args = HashMap<String, Any>(t.argsCount)
            for (arg in t.argsList) {
                args[arg.name] = when (arg.valueCase) {
                    TextArg.ValueCase.INT_VALUE -> arg.intValue.toInt()
                    TextArg.ValueCase.FLOAT_VALUE -> arg.floatValue.toString()
                    TextArg.ValueCase.TEXT_VALUE -> coreTextToMessageText(arg.textValue).resolve(copy)
                    TextArg.ValueCase.TIMESTAMP_VALUE -> formatTimestampToMessageText(arg.timestampValue).resolve(copy)
                    TextArg.ValueCase.STR_VALUE -> arg.strValue
                    else -> ""
                }
            }
            copy(UiMessage(t.key, args))
        }
    }
    return t.literal.messageText()
}

internal fun SyncStateView.toRemoteState(
    networkAvailable: Boolean,
): RemoteState {
    val conn = when (connection) {
        CoreConnState.CONN_STATE_CONNECTED -> ConnState.ONLINE
        CoreConnState.CONN_STATE_OFFLINE -> ConnState.OFFLINE
        else -> ConnState.CONNECTING
    }
    val link = when (connection) {
        CoreConnState.CONN_STATE_CONNECTED -> Link.ONLINE
        CoreConnState.CONN_STATE_NO_NETWORK -> Link.NO_NETWORK
        CoreConnState.CONN_STATE_OFFLINE -> if (!networkAvailable) Link.NO_NETWORK else Link.NO_SERVER
        else -> if (!networkAvailable) Link.NO_NETWORK else Link.WAITING
    }
    val machineMap = machinesList.associate { m ->
        val base = MachineView(m.machineId, m.online, m.lastSeenMs, m.meta.takeIf { m.hasMeta() }, m.metaRev)
        val directReady = m.directReady
        val computed = RelayPath.discovery(base, ready = directReady || conn == ConnState.ONLINE, authorized = !removed, unlinked = !m.reachable && m.unlinked).copy(
            reachable = m.reachable || directReady,
            relayRequired = !directReady,
            knownOffline = !directReady && !m.online,
            protocolVersion = m.protocolVersion.takeIf { it > 0 },
            capabilities = if (m.hasMeta() && m.meta.hasCapabilities()) m.meta.capabilities else null,
        )
        m.machineId to base.copy(linkState = computed)
    }
    val sessionMap = sessionsList.associate { s ->
        s.sessionId to SessionView(
            id = s.sessionId,
            machineId = s.machineId,
            info = s.info.takeIf { s.hasInfo() },
            checkpoint = s.checkpoint.takeIf { s.hasCheckpoint() },
            checkpointStatus = if (s.checkpointInvalid) "invalid" else if (s.hasCheckpoint()) "ok" else "waiting",
            metaRev = s.metaRev,
            lastSeq = s.lastSeq,
            updatedMs = s.updatedMs,
            listedEndedMs = s.listedEndedMs,
            historyError = RETIRED_HISTORY_ERROR.takeIf { s.retiredHistory },
        )
    }
    val deviceMap = devicesList.associate { d ->
        d.deviceId to DeviceView(d.deviceId, d.kind, d.createdMs, d.meta.takeIf { d.hasMeta() }, d.metaRev, d.lastSeenMs)
    }
    val reasonText = if (hasConnectionReason()) coreTextToMessageText(connectionReason) else "".messageText()
    val stepText = if (hasConnectionStep()) coreTextToMessageText(connectionStep) else "".messageText()
    return RemoteState(
        machines = machineMap,
        sessions = sessionMap,
        conn = conn,
        error = reasonText,
        loaded = machinesListed || sessionsListed || prefsLoaded,
        offlineSinceMs = disconnectedAtMs,
        prefs = if (hasPrefs()) PrefsObject(prefsRevision, prefs) else null,
        devices = deviceMap,
        removed = removed,
        sessionsNext = if (hasMoreSessions && hasNextCursor()) SessionPlace(nextCursor.endedMs, nextCursor.sessionId) else null,
        loadingSessions = loadingMoreSessions,
        sessionsError = if (sessionListFailed && hasSessionListError()) coreTextToMessageText(sessionListError) else "".messageText(),
        sessionsListed = sessionsListed,
        link = link,
        synced = securityLoaded,
        authorityDeviceId = authorityDeviceId,
        leaseEndMs = leaseEndMs,
        isAuthority = isManager,
        recoveryPending = recoveryPending,
        recoveryReadyMs = recoveryReadyMs,
        clockWarning = accountClockSkewed,
        displayGrace = displayGrace,
        reachabilityUnknown = reachabilityUnknown,
        connStep = stepText,
        connError = if (conn == ConnState.OFFLINE) reasonText else "".messageText(),
    )
}

/** Seeds [coreHost] via M2 Core events so Compose/UI tests see [state]. */
private val testSeedSeq = java.util.concurrent.atomic.AtomicInteger(0)

internal fun seedCoreForTest(
    coreHost: CoreHost,
    selfDeviceId: String,
    state: RemoteState,
    modelLists: Map<Pair<String, Agent>, ListModelsResponse>,
) {
    val seedGen = testSeedSeq.incrementAndGet()
    val plainMachines = state.machines.values.map { m ->
        PlainMachine.newBuilder().setMachineId(m.id).setOnline(m.online).setLastSeenMs(m.lastSeenMs).setMetaRev(m.metaRev)
            .apply { m.meta?.let { setMeta(it) } }.build()
    }
    val plainSessions = state.sessions.values.map { s ->
        PlainSession.newBuilder().setSessionId(s.id).setMachineId(s.machineId).setLastSeq(s.lastSeq)
            .setUpdatedMs(s.updatedMs).setListedEndedMs(s.listedEndedMs).setMetaRev(s.metaRev)
            .apply { s.info?.let { setMeta(it) }; s.checkpoint?.let { setCheckpoint(it) } }.build()
    }
    val plainPrefs = PlainAccountPrefs.newBuilder()
        .setRevision(state.prefs?.revision ?: 0L)
        .setPrefs(state.prefs?.prefs ?: ProjectPrefs.newBuilder().setSchema(1).build())
        .build()
    val plainDevices = state.devices.values.map { d ->
        PlainDevice.newBuilder().setDeviceId(d.id).setKind(d.kind).setCreatedMs(d.createdMs)
            .setLastSeenMs(d.lastSeenMs).setMetaRev(d.metaRev).apply { d.meta?.let { setMeta(it) } }.build()
    }
    val pendingRpcs = ArrayList<RpcEffect>()
    var pendingStreamLeg = 0L
    fun send(block: CoreEvent.Builder.() -> Unit) {
        coreHost.dispatchOnWorker(CoreEvent.newBuilder().apply(block).build())
    }
    coreHost.onCacheRead = { eff ->
        if (eff.hasBootstrap()) send {
            setCacheBootstrapLoaded(
                CacheBootstrapLoadedEvent.newBuilder().setOpId(eff.bootstrap.opId)
                    .addAllMachines(plainMachines).addAllSessions(plainSessions).setPrefs(plainPrefs),
            )
        }
    }
    coreHost.onEnsureToken = { eff ->
        send {
            val tb = TokenResultEvent.newBuilder().setOpId(eff.opId)
            if (state.link != Link.NO_NETWORK) tb.setToken("test-token").setExpiresMs(Long.MAX_VALUE)
            else tb.setError(state.error.resolve { it.key }.ifEmpty { UiMessages.commonOffline().key })
            setTokenResult(tb)
        }
    }
    coreHost.onOpenStream = { eff -> if (state.conn == ConnState.ONLINE) pendingStreamLeg = eff.legId }
    coreHost.onRpc = { eff -> pendingRpcs.add(eff) }
    coreHost.onDispatchCommand = { eff ->
        val cmd = runCatching { Command.parseFrom(eff.commandBytes) }.getOrNull()
        val resultProto = when {
            cmd?.hasListModels() == true -> modelLists[eff.machineId to cmd.listModels.agent]?.let {
                CommandResult.newBuilder().setOutcome("executed").setReason("ok").setResultAvailable(true).setListModels(it).build()
            }
            cmd?.hasGetSettings() == true -> CommandResult.newBuilder().setOutcome("executed").setReason("ok").setResultAvailable(true)
                .setGetSettings(io.github.lewin671.remoteagent.proto.GetSettingsResponse.newBuilder().setSettings(io.github.lewin671.remoteagent.proto.DaemonSettings.getDefaultInstance())).build()
            else -> null
        }
        if (resultProto != null) send {
            setCommandOutcome(
                CommandOutcomeEvent.newBuilder().setRequestId(eff.requestId).setAttemptId(eff.attemptId)
                    .setSource(OutcomeSource.OUTCOME_SOURCE_DAEMON).setAttemptBound(true).setSigned(true)
                    .setHashesMatch(true).setOuterMatchesInner(true).setOuterOutcome("executed").setOuterReason("ok")
                    .setResult(resultProto),
            )
        }
    }
    send { setSyncLifecycle(SyncLifecycleAction.newBuilder().setKind(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_STOP)) }
    send {
        setSeedDemoWorld(
            SeedDemoWorldEvent.newBuilder().setSecurity(
                DemoSecurity.newBuilder()
                    .setIsMember(!state.removed).setRemoved(state.removed).setSelfDeviceId(selfDeviceId)
                    .setAuthorityDeviceId(state.authorityDeviceId.ifEmpty { if (state.isAuthority) selfDeviceId else "authority" })
                    .addAllTrustedMachineIds(state.machines.keys)
                    .addAllTrustedClientIds(
                        state.devices.values
                            .filter { it.kind == io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT }
                            .map { it.id },
                    )
                    .setHeadEpoch(1L).setLeaseEndMs(state.leaseEndMs)
                    .setClockSkewed(state.clockWarning).setRecoveryPending(state.recoveryPending)
                    .setRecoveryReadyMs(state.recoveryReadyMs).setNetworkGeneration(seedGen.toLong()),
            ),
        )
    }
    if (state.link == Link.NO_NETWORK) send {
        setNetworkChanged(NetworkChangedEvent.newBuilder().setAvailable(false).setSignature("none-$seedGen"))
    } else send {
        setNetworkChanged(NetworkChangedEvent.newBuilder().setAvailable(true).setSignature("test-$seedGen"))
    }
    send { setSyncLifecycle(SyncLifecycleAction.newBuilder().setKind(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_START)) }
    if (pendingStreamLeg != 0L) {
        val leg = pendingStreamLeg
        pendingStreamLeg = 0L
        send { setStreamReady(StreamReadyEvent.newBuilder().setLegId(leg).setServerTimeMs(0L)) }
    }
    var sentDevices = false
    while (pendingRpcs.isNotEmpty()) {
        val rpc = pendingRpcs.removeAt(0)
        when (rpc.kindCase) {
            RpcEffect.KindCase.FETCH_LISTS -> send {
                setListsFetched(
                    ListsFetchedEvent.newBuilder().setOpId(rpc.opId).addAllMachines(plainMachines)
                        .addAllSessions(plainSessions).addAllDevices(plainDevices).apply {
                            state.sessionsNext?.let { setNext(SessionCursor.newBuilder().setEndedMs(it.endedMs).setSessionId(it.sessionId)) }
                        },
                )
            }
            RpcEffect.KindCase.GET_ACCOUNT_PREFS -> send {
                setAccountPrefsFetched(AccountPrefsFetchedEvent.newBuilder().setOpId(rpc.opId).setPrefs(plainPrefs))
            }
            RpcEffect.KindCase.FETCH_DEVICES -> {
                sentDevices = true
                send {
                    setDevicesFetched(DevicesFetchedEvent.newBuilder().setOpId(rpc.opId).addAllDevices(plainDevices))
                }
            }
            else -> {}
        }
    }
    if (!sentDevices) {
        send { setDevicesFetched(DevicesFetchedEvent.newBuilder().setPushSeq(seedGen.toLong()).addAllDevices(plainDevices)) }
    }
    if (state.conn == ConnState.OFFLINE) {
        send { setSyncLifecycle(SyncLifecycleAction.newBuilder().setKind(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_STOP)) }
    }
    for (t in arrayOf(
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_HOME,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_PROJECT,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_NEW_SESSION,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_SYNC_STATE,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_SETTINGS,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_DEVICES,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_MACHINE_STATUS,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_MACHINE_SETTINGS,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_QUOTA,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_CLIENT_DETAIL,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_USAGE,
    )) coreHost.refreshScreenOnWorker(t)
}
