package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.proto.*
import java.util.concurrent.atomic.AtomicInteger

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
)

data class SessionView(
    val id: String,
    val machineId: String,
    val info: Session? = null,
    val checkpoint: io.github.lewin671.remoteagent.proto.LiveSnapshot? = null,
    val metaRev: Long = 0,
    val lastSeq: Long = 0,
    val updatedMs: Long = 0,
)

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
    val prefs: PrefsObject? = null,
    val devices: Map<String, DeviceView> = emptyMap(),
    val removed: Boolean = false,
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
    val connStep: MessageText = "".messageText(),
    val connStartedAtMs: Long? = null,
    val connError: MessageText = "".messageText(),
    val archivedMachines: Set<String> = emptySet(),
)

/** Converts a test [RemoteState] fixture into a [ConnectionViewModel] for direct composable rendering tests. */
fun RemoteState.toTestConnectionViewModel(selfDeviceId: String = "self"): ConnectionViewModel {
    val connProto = when {
        link == Link.NO_NETWORK -> CoreConnState.CONN_STATE_NO_NETWORK
        conn == ConnState.ONLINE -> CoreConnState.CONN_STATE_CONNECTED
        conn == ConnState.OFFLINE -> CoreConnState.CONN_STATE_OFFLINE
        else -> CoreConnState.CONN_STATE_CONNECTING
    }
    val kept = loaded || sessions.isNotEmpty()
    val bannerKind = when {
        link == Link.NO_NETWORK -> ConnBannerKind.CONN_BANNER_KIND_NO_NETWORK
        removed || conn == ConnState.OFFLINE -> ConnBannerKind.CONN_BANNER_KIND_NO_SERVER
        conn == ConnState.ONLINE -> ConnBannerKind.CONN_BANNER_KIND_NONE
        else -> ConnBannerKind.CONN_BANNER_KIND_WAITING
    }
    val reasonStr = connError.resolve { it.key }.ifEmpty { error.resolve { it.key } }
    val b = ConnectionViewModel.newBuilder()
        .setState(connProto)
        .setConnected(conn == ConnState.ONLINE && !removed)
        .setRemoved(removed)
        .setDisplayGrace(displayGrace)
        .setReachabilityUnknown(reachabilityUnknown)
        .setLiveLoaded(loaded || synced)
    if (bannerKind != ConnBannerKind.CONN_BANNER_KIND_NONE && !displayGrace) {
        val banner = ConnBannerView.newBuilder()
            .setKind(bannerKind)
            .setVisible(true)
            .setKeptData(kept)
        when (bannerKind) {
            ConnBannerKind.CONN_BANNER_KIND_NO_NETWORK ->
                banner.setText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey(if (kept) "connection.no_network_previous" else "connection.no_network_empty"))
            ConnBannerKind.CONN_BANNER_KIND_NO_SERVER -> {
                banner.setShowDetails(true)
                banner.setErrorTone(!kept)
                banner.setText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey("connection.server_retrying"))
                if (!kept && reasonStr.isNotEmpty()) {
                    banner.setFailedText(
                        io.github.lewin671.remoteagent.core.proto.Text.newBuilder()
                            .setKey("connection.server_failed")
                            .addArgs(io.github.lewin671.remoteagent.core.proto.TextArg.newBuilder().setName("reason").setStrValue(reasonStr)),
                    )
                }
            }
            else ->
                banner.setText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setKey(if (loaded) "connection.reconnecting" else "connection.connecting"))
        }
        b.setBanner(banner)
    }
    val details = ConnectionServerDetailsView.newBuilder()
        .setShowDisconnectedAt(synced)
    if (reasonStr.isNotEmpty()) {
        details.setReasonText(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral(reasonStr))
    }
    b.setServerDetails(details)
    return b.build()
}

private val testSeedSeq = AtomicInteger(0)

fun Remote.showForTest(
    state: RemoteState,
    modelLists: Map<Pair<String, Agent>, ListModelsResponse> = emptyMap(),
    sessions: Map<String, SessionViewModel> = emptyMap(),
) {
    coreHost.runOnWorkerBlocking {
        prepareForTest(sessions)
        seedCoreForTest(coreHost, account.deviceId, state, modelLists)
    }
}

/**
 * Test-only barrier that blocks until all previously enqueued [CoreHost] mailbox events
 * and pending renders have completed. Do not use with a paused `StandardTestDispatcher`
 * worker or from coroutines running on the worker dispatcher.
 */
fun CoreHost.flushMailboxForTest() = flushMailboxBlocking()

internal fun seedCoreForTest(
    coreHost: CoreHost,
    selfDeviceId: String,
    state: RemoteState,
    modelLists: Map<Pair<String, Agent>, ListModelsResponse>,
) {
    val seedGen = testSeedSeq.incrementAndGet()
    val demoMachines = state.machines.values.map { m ->
        io.github.lewin671.remoteagent.core.proto.DemoMachine.newBuilder()
            .setMachineId(m.id)
            .setOnline(m.online)
            .setLastSeenMs(m.lastSeenMs)
            .apply { m.meta?.let { setRawMeta(it.toByteString()) } }
            .build()
    }
    val demoSessions = state.sessions.values.map { s ->
        io.github.lewin671.remoteagent.core.proto.DemoSession.newBuilder()
            .setSessionId(s.id)
            .setMachineId(s.machineId)
            .setLastSeq(s.lastSeq)
            .setUpdatedMs(s.updatedMs)
            .apply {
                s.info?.let { info ->
                    val b = info.toBuilder().setId(s.id).setLastSeq(s.lastSeq)
                    setRawMeta(b.build().toByteString())
                }
                s.checkpoint?.let { cp ->
                    val cpb = cp.toBuilder()
                    if (cpb.hasSession()) {
                        cpb.setSession(cpb.session.toBuilder().setId(s.id).setLastSeq(s.lastSeq))
                    }
                    setRawCheckpoint(cpb.build().toByteString())
                }
            }
            .build()
    }
    val prefsRev = state.prefs?.revision ?: 0L
    val rawPrefs = (state.prefs?.prefs ?: ProjectPrefs.newBuilder().setSchema(1).build()).toByteString()
    val demoDevices = state.devices.values.map { d ->
        io.github.lewin671.remoteagent.core.proto.DemoDevice.newBuilder()
            .setDeviceId(d.id)
            .setKind(d.kind)
            .setCreatedMs(d.createdMs)
            .setLastSeenMs(d.lastSeenMs)
            .apply { d.meta?.let { setRawMeta(it.toByteString()) } }
            .build()
    }
    var pendingStreamLeg = 0L
    fun send(block: CoreEvent.Builder.() -> Unit) {
        coreHost.dispatchOnWorker(CoreEvent.newBuilder().apply(block).build())
    }
    coreHost.onCacheRead = { eff ->
        if (eff.hasBootstrap()) send {
            setCacheBootstrapLoaded(CacheBootstrapLoadedEvent.newBuilder().setOpId(eff.bootstrap.opId))
        }
    }
    coreHost.onOpenStream = { eff -> pendingStreamLeg = eff.legId }
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
                    .setRawResult(resultProto.toByteString()),
            )
        }
    }
    send { setSyncLifecycle(SyncLifecycleAction.newBuilder().setKind(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_STOP)) }
    send {
        setSeedDemoWorld(
            SeedDemoWorldEvent.newBuilder()
                .addAllMachines(demoMachines)
                .addAllSessions(demoSessions)
                .addAllDevices(demoDevices)
                .setReplaceMachines(true)
                .setReplaceSessions(true)
                .setReplaceDevices(true)
                .setRawPrefs(rawPrefs)
                .setPrefsRevision(prefsRev)
                .setSecurity(
                    DemoSecurity.newBuilder()
                        .setIsMember(!state.removed).setRemoved(state.removed).setSelfDeviceId(selfDeviceId)
                        .setAuthorityDeviceId(state.authorityDeviceId.ifEmpty { if (state.isAuthority) selfDeviceId else "authority" })
                        .addAllTrustedMachineIds(state.machines.keys - state.archivedMachines)
                        .addAllArchiveMachineIds(state.archivedMachines)
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
    send { setNetworkChanged(NetworkChangedEvent.newBuilder().setAvailable(true).setSignature("test-$seedGen")) }
    send { setSyncLifecycle(SyncLifecycleAction.newBuilder().setKind(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_START)) }
    if (pendingStreamLeg != 0L) {
        val leg = pendingStreamLeg
        pendingStreamLeg = 0L
        send { setStreamReady(StreamReadyEvent.newBuilder().setLegId(leg).setServerTimeMs(0L)) }
        when {
            state.link == Link.NO_NETWORK -> send {
                setNetworkChanged(NetworkChangedEvent.newBuilder().setAvailable(false).setSignature("none-$seedGen"))
            }
            state.conn == ConnState.OFFLINE -> send {
                setSyncLifecycle(SyncLifecycleAction.newBuilder().setKind(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_STOP))
            }
            state.conn == ConnState.CONNECTING -> send {
                setStreamClosed(StreamClosedEvent.newBuilder().setLegId(leg))
            }
        }
    } else if (state.link == Link.NO_NETWORK) {
        send { setNetworkChanged(NetworkChangedEvent.newBuilder().setAvailable(false).setSignature("none-$seedGen")) }
    } else if (state.conn == ConnState.OFFLINE) {
        send { setSyncLifecycle(SyncLifecycleAction.newBuilder().setKind(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_STOP)) }
    }
    for (t in arrayOf(
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_HOME,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_PROJECT,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_NEW_SESSION,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_CONNECTION,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_SETTINGS,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_DEVICES,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_MACHINE_STATUS,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_MACHINE_SETTINGS,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_QUOTA,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_CLIENT_DETAIL,
        io.github.lewin671.remoteagent.core.proto.ScreenTarget.SCREEN_TARGET_USAGE,
    )) coreHost.refreshScreenOnWorker(t)
}

fun RemoteState.toTestSecuritySummaryView(selfId: String): io.github.lewin671.remoteagent.core.proto.SecuritySummaryView {
    val authDev = devices[authorityDeviceId]
    val authName = authDev?.meta?.name?.takeIf { it.isNotEmpty() }
        ?: authDev?.meta?.description?.takeIf { it.isNotEmpty() }
        ?: ""
    val canPrepare = !isAuthority && devices[selfId]?.kind == io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT
    return io.github.lewin671.remoteagent.core.proto.SecuritySummaryView.newBuilder()
        .setIsManager(isAuthority)
        .setAuthorityDeviceId(authorityDeviceId)
        .setAuthorityName(authName)
        .setRecoveryPending(recoveryPending)
        .setRecoveryReadyMs(recoveryReadyMs)
        .setLeaseEndMs(leaseEndMs)
        .setCanPrepareHandoff(canPrepare)
        .build()
}
