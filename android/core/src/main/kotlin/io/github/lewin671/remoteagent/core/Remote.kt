// Remote keeps the app in sync with the server, as web/src/remote.ts does:
// it logs in, holds the decrypted machines, sessions and events, follows the
// push stream, and sends sealed commands. All state changes run one at a
// time on a single-threaded dispatcher, so they interleave only where they
// suspend, like the page's event loop.
package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovePairingRequest
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CommandResult
import io.github.lewin671.remoteagent.proto.DeleteMachineRecordsRequest
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.ForwardRequest
import io.github.lewin671.remoteagent.proto.GetEventsRequest
import io.github.lewin671.remoteagent.proto.GetPairingRequest
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.ListMachinesRequest
import io.github.lewin671.remoteagent.proto.ListModelsRequest
import io.github.lewin671.remoteagent.proto.ListModelsResponse
import io.github.lewin671.remoteagent.proto.ListRemoteSessionsRequest
import io.github.lewin671.remoteagent.proto.LoginResponse
import io.github.lewin671.remoteagent.proto.Machine
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.PairApproval
import io.github.lewin671.remoteagent.proto.PairingState
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.PeerFrame
import io.github.lewin671.remoteagent.proto.PutSecurityStateRequest
import io.github.lewin671.remoteagent.proto.RemoteSession
import io.github.lewin671.remoteagent.proto.SecurityState
import io.github.lewin671.remoteagent.proto.ServerFrame
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionCursor
import io.github.lewin671.remoteagent.proto.StoredEvent
import io.github.lewin671.remoteagent.proto.StreamHello
import io.github.lewin671.remoteagent.proto.TermHello
import io.github.lewin671.remoteagent.proto.TerminalFrame
import java.util.TreeMap
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeUnit
import kotlin.random.Random
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.async
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString

enum class ConnState {
    CONNECTING,
    ONLINE,
    OFFLINE,
}

/**
 * Which stretch of the way to the computer is down, the nearest one only (client-v30 「断线」): past it
 * nothing is known. WAITING is a connection being made or just lost, which mostly comes back by
 * itself.
 */
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
    val checkpointStatus: String = "waiting",
    val metaRev: Long = 0,
    val lastSeq: Long = 0,
    val updatedMs: Long = 0,
    /**
     * When the session ended as the server lists it (RemoteSession.ended_ms), 0 while it has not.
     * It only places the session among the pages of the listing; what is shown and sorted by comes
     * from [info].
     */
    val listedEndedMs: Long = 0,
)

/**
 * A place in the server's listing of ended sessions: the latest ended first, by session ID where
 * those are equal.
 */
data class SessionPlace(val endedMs: Long, val sessionId: String)

/**
 * Whether a session ended at [endedMs] comes after [place] in the listing; one that has not ended
 * is never there.
 */
fun afterPlace(endedMs: Long, sessionId: String, place: SessionPlace): Boolean =
    endedMs != 0L &&
        (endedMs < place.endedMs || (endedMs == place.endedMs && sessionId > place.sessionId))

/** Ended sessions asked for per page of the listing. */
const val SESSION_PAGE = 50

/**
 * How long after the lists could not be fetched they are asked for again; twice as long each time,
 * up to half a minute.
 */
private const val LIST_RETRY_MS = 2_000L

data class RemoteState(
    val machines: Map<String, MachineView> = emptyMap(),
    val sessions: Map<String, SessionView> = emptyMap(),
    val conn: ConnState = ConnState.CONNECTING,
    val error: String = "",
    /** The lists have been fetched once since start, or read from the cache. */
    val loaded: Boolean = false,
    /**
     * When the connection was last lost; 0 while online. Timers stop here: what happened since is
     * not known.
     */
    val offlineSinceMs: Long = 0,
    /** The account's project preferences; null until read (from the server or the cache). */
    val prefs: PrefsObject? = null,
    /** The account's devices, machines and clients (ListDevices). */
    val devices: Map<String, DeviceView> = emptyMap(),
    /**
     * The server no longer knows this device: removed from the account, or the server's data is
     * gone.
     */
    val removed: Boolean = false,
    /**
     * Where the next page of ended sessions starts (architecture §8 「会话列表分页」): [sessions] holds
     * every session that has not ended and the ended ones up to here, besides any fetched on their
     * own. Null when every page was fetched, and before the first one ([sessionsListed]).
     */
    val sessionsNext: SessionPlace? = null,
    /** A page of the listing is being fetched. */
    val loadingSessions: Boolean = false,
    /** Why the last page could not be fetched; empty once one was, or the first page came again. */
    val sessionsError: String = "",
    /**
     * The listing's first page was fetched in this run. Before that the sessions held are what the
     * cache kept of an earlier run, and how many pages there are is not known.
     */
    val sessionsListed: Boolean = true,
    val link: Link = Link.WAITING,
    /**
     * The lists came from the server since start: what they say of the computers is not from the
     * cache.
     */
    val synced: Boolean = false,
    val authorityDeviceId: String = "",
    /** End of the current membership lease, unix ms. */
    val leaseEndMs: Long = 0,
    /** This phone is the account's authority. */
    val authorityPhone: Boolean = false,
    /** The relay says a recovery waits for this authority phone to veto or accept it. */
    val recoveryPending: Boolean = false,
    /** This phone's clock is more than five minutes ahead of the server or a state. */
    val clockWarning: Boolean = false,
    /** The failure behind [error] in its own words, for the details. */
    val errorDetail: String = "",
) {
    /**
     * Nothing here is known to still hold: the server is out of reach, or has not been reached yet.
     * A connection lost a moment ago does not count.
     */
    val unlinked: Boolean
        get() =
            link == Link.NO_NETWORK || link == Link.NO_SERVER || (link == Link.WAITING && !synced)

    /** The server has, or may have, ended sessions that were not fetched yet. */
    val moreSessions: Boolean
        get() = !sessionsListed || sessionsNext != null
}

/** A device of the account; a client's name is in its sealed metadata. */
data class DeviceView(
    val id: String,
    val kind: io.github.lewin671.remoteagent.proto.DeviceKind,
    val createdMs: Long,
    val meta: io.github.lewin671.remoteagent.proto.DeviceMeta? = null,
    val metaRev: Long = 0,
    /** When it was last used, 0 without a record. */
    val lastSeenMs: Long = 0,
)

/** A session's events, contiguous from seq 1. */
data class SessionEvents(
    val events: List<Event> = emptyList(),
    val loaded: Boolean = false,
    /** Set when an event does not open: history stops before it. */
    val error: String? = null,
    /** Set when the last fetch failed; the next connection fetches again. */
    val fetchError: String? = null,
    val live: io.github.lewin671.remoteagent.proto.LiveSnapshot? = null,
    val unverifiedLegacy: Boolean = false,
    val liveError: String? = null,
    val verifiedEvents: List<Event> = events,
    val unavailableCount: Int = 0,
)

/** Where envelopes are cached between runs. Only ciphertext is stored. */
interface CipherCache {
    suspend fun machines(): List<Machine>

    suspend fun sessions(): List<RemoteSession>

    suspend fun events(sessionId: String): List<StoredEvent>

    suspend fun putMachine(m: Machine)

    suspend fun putSession(s: RemoteSession)

    suspend fun putEvents(sessionId: String, events: List<StoredEvent>)

    /**
     * Forgets a session's events, e.g. when one of them does not open; the session stays listed.
     */
    suspend fun dropEvents(sessionId: String)

    suspend fun removeMachine(id: String)

    /** A session that no longer exists: its entry and its events. */
    suspend fun removeSession(id: String)

    /** Takes a session off the cached list, which is the listing's first page; its events stay. */
    suspend fun unlistSession(id: String)

    /**
     * Notes whose a session outside the cached list is (one of a later page, or fetched by its ID),
     * so that events kept for it go with its machine. A session already noted, listed or not, is
     * left as it is.
     */
    suspend fun keepOwner(s: RemoteSession) {}

    /** An image's display copy as fetched: its sealed envelope, never decrypted bytes. */
    suspend fun image(key: String): ByteArray? = null

    suspend fun putImage(key: String, envelope: ByteArray) {}

    /** Other sealed objects by name, such as the account's project preferences. */
    suspend fun blob(name: String): ByteArray? = null

    suspend fun putBlob(name: String, data: ByteArray) {}
}

/** A sealed command ready to send; resending it reuses its request ID and deadline. */
class PreparedCommand(
    val machineId: String,
    val sessionId: String,
    val requestId: String,
    val deadlineMs: Long,
    val envelope: Envelope,
    internal val command: Command? = null,
) {
    internal val sealMutex = kotlinx.coroutines.sync.Mutex()
    internal var authorizedEnvelope: Envelope? = null

    override fun toString() = "PreparedCommand(request=$requestId)"
}

interface TermListener {
    /** Called on the socket's reader thread, in order. */
    fun onFrame(frame: TerminalFrame)

    fun onClose(reason: String)
}

class TermConn
internal constructor(
    private val ws: WebSocket,
    private val sendFrame: (TerminalFrame) -> Unit,
    private val closeLocal: () -> Unit,
) {
    fun send(frame: TerminalFrame) {
        sendFrame(frame)
    }

    fun close() {
        closeLocal()
        ws.close(1000, null)
    }
}

internal fun ucmp(a: Long, b: Long): Int = java.lang.Long.compareUnsigned(a, b)

internal fun umax(a: Long, b: Long): Long = if (ucmp(a, b) >= 0) a else b

const val COMMAND_TTL_MS = 60_000L

/** How often stream connections ping; the server pings at the same pace. */
private const val STREAM_PING_SECONDS = 20L
private const val STREAM_OPEN_MS = 15_000L
private const val STREAM_FRAME_BYTES = 2 * 1024 * 1024
private const val STREAM_QUEUE_BYTES = 8L * 1024 * 1024

/** The WebSocket close code the server refuses a hello with. */
private const val POLICY_VIOLATION = 1008

@OptIn(ExperimentalCoroutinesApi::class)
class Remote(
    val account: Account,
    private val http: OkHttpClient,
    private val cache: CipherCache? = null,
    private val log: (String) -> Unit = {},
    private val clock: () -> Long = System::currentTimeMillis,
    /**
     * Stream frames waiting to be handled before the stream is dropped and caught up from history.
     */
    private val frameQueue: Int = 1024,
    private val historyIdleMs: Long = 60_000,
    private val historyIdleCount: Int = 8,
    private val historyIdleBytes: Long = 16L * 1024 * 1024,
    private val streamOpenMs: Long = STREAM_OPEN_MS,
    /**
     * How long a lost connection is only being remade, before the server counts as out of reach.
     */
    private val linkGraceMs: Long = 10_000,
    private val securityStorage: DeviceSecurity.Storage? = null,
) {
    private val keys: Keys? = if (account.hasRoot) Keys.derive(account.root) else null
    private val device = SigningKey(account.deviceSeed)
    val api = Api(account.server, http)
    val security =
        DeviceSecurityClient(
            securityStorage ?: MissingDeviceSecurityStorage,
            account.deviceId,
            device,
            expectedAccount = account.accountId.ifEmpty { null },
            clock = clock,
        ) { after ->
            api.client
                .getSecurityStates(
                    io.github.lewin671.remoteagent.proto.GetSecurityStatesRequest.newBuilder()
                        .setAfterEpoch(after.toLong())
                        .build(),
                    Api.headers(currentToken()),
                )
                .value()
        }
    private var authorityDeviceId = ""
    private val trustedMembers = HashMap<String, DeviceSecurity.Member>()
    private val archiveMachines = HashSet<String>()
    private val trustedMetadata = HashMap<String, Envelope>()
    private var directoryVersion = 0L
    private var directoryHead: ByteArray? = null
    private var recoveryObjectsPending = true
    private var leaseEnd = 0L
    @Volatile private var serverAhead = false
    private var stateJob: Job? = null
    private val dispatcher = Dispatchers.IO.limitedParallelism(1)
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    private var authorityJob = SupervisorJob(scope.coroutineContext[Job])

    private val _state = MutableStateFlow(RemoteState())
    val state: StateFlow<RemoteState> = _state.asStateFlow()

    private val machines = HashMap<String, MachineView>()
    private val sessions = HashMap<String, SessionView>()
    private val sessionKeys = ConcurrentHashMap<String, ByteArray>()
    private val buffers = HashMap<String, Buffer>()
    private val eventFlows = ConcurrentHashMap<String, MutableStateFlow<SessionEvents>>()
    private val liveErrors = ConcurrentHashMap<String, String>()
    private val liveSnapshots =
        ConcurrentHashMap<String, io.github.lewin671.remoteagent.proto.LiveSnapshot>()
    private val liveJobs = HashMap<String, Job>()
    private val legacySessions = HashSet<String>()
    private var conn = ConnState.CONNECTING
    private var error = ""
    private var loaded = false
    private var offlineSince = 0L
    private var errorDetail = ""
    // The device has a network, by the system's word; a server whose address
    // does not resolve says the same of a network that only looks up.
    private var network = true
    private var unresolved = false
    private var synced = false
    private var grace: Job? = null
    private var waited = false
    private var prefs: PrefsObject? = null
    private val devices = HashMap<String, DeviceView>()

    // Pushes seen per machine, session and machine removal, so that a list
    // fetched before a push cannot undo it.
    private val machinePushes = HashMap<String, Int>()
    private val sessionPushes = HashMap<String, Int>()
    private val removedPushes = HashMap<String, Int>()
    // And per device, registered, changed or removed: the same for the list of devices.
    private val devicePushes = HashMap<String, Int>()

    @Volatile private var token = ""
    @Volatile private var tokenExpires = 0L
    // The stream pings so that a link that died silently, as when a phone
    // changes networks, is noticed and replaced. Requests share its pool.
    private val streamHttp =
        http.newBuilder().pingInterval(STREAM_PING_SECONDS, TimeUnit.SECONDS).build()
    private var ws: WebSocket? = null
    private var retry = 1000L
    private var running = false
    private var removed = false
    private val signingIn = Mutex()
    private var reconnect: Job? = null
    private var connecting: Job? = null
    private var opening: Job? = null
    // Bumped by stop: a connection attempt suspended in login when the app
    // went to the background must not open the stream afterwards.
    private var generation = 0
    /**
     * Putting what the cache holds in place, started once; no connection is made before it is done.
     */
    private var restoring: Job? = null
    private var historySweep: Job? = null

    // The listing of ended sessions comes a page at a time. next is where the
    // page after the ones fetched starts, firstEnd where the first page ended:
    // what the cache keeps is that page. A fetch of the first page starts the
    // listing over (listing): a page asked for before that continued the old
    // one and is asked for again from the new cursor. While a first page is on
    // its way (listsInFlight) no other page is asked for. Of several first
    // pages in flight, as when a stream was replaced while the one before was
    // still being listed, only the latest asked for is put in place (refreshRun).
    private val listsInFlight = MutableStateFlow(0)
    private var refreshRun = 0
    private val applying = Mutex()
    private var listRetry: Job? = null
    private var listBackoff = LIST_RETRY_MS
    // The same for the preferences while there is no copy of them at all: nothing of them can be
    // changed until one is read.
    private var prefsRetry: Job? = null
    private var prefsBackoff = LIST_RETRY_MS
    private var next: SessionPlace? = null
    private var firstEnd: SessionPlace? = null
    private var firstPage = false
    private var listing = 0
    private var pageJob: Deferred<Boolean>? = null
    private var allJob: Deferred<Unit>? = null
    private val byId = HashMap<String, Deferred<Boolean>>()
    private var pageError = ""
    private val listed = HashSet<String>()
    private val models = HashMap<String, Deferred<ListModelsResponse>>()

    private class Buffer {
        var events = PagedList.empty<Event>()
        var verifiedEvents = PagedList.empty<Event>()
        val verifiedSeqs = HashSet<Long>()
        val unavailableSeqs = HashSet<Long>()
        var bytes = 0L
        var usedAt = 0L
        val pending = TreeMap<Long, Event>(java.lang.Long::compareUnsigned)
        var fetching = false
        var refetch = false
        var loaded = false
        var error: String? = null
        // The last fetch failed; cleared by the next one that works.
        var fetchError: String? = null
    }

    // --- lifecycle ---

    /** Connects and keeps reconnecting until [stop]. */
    fun start() {
        scope.launch {
            if (running || removed) return@launch
            running = true
            if (!authorityJob.isActive) authorityJob = SupervisorJob(scope.coroutineContext[Job])
            stateJob = scope.launch {
                // The first pull follows the connection; a fresh login is not
                // needed before it and would be a second login.
                delay(5_000)
                while (running) {
                    try {
                        syncSecurity()
                        renewLeaseIfNeeded()
                    } catch (e: Exception) {
                        if (e is CancellationException) throw e
                    }
                    delay(60_000)
                }
            }
            beginConnect()
        }
    }

    /** Disconnects, e.g. when the app goes to the background. */
    fun stop() {
        scope.launch {
            running = false
            authorityJob.cancel()
            stateJob?.cancel()
            stateJob = null
            generation++
            reconnect?.cancel()
            reconnect = null
            listRetry?.cancel()
            listRetry = null
            prefsRetry?.cancel()
            prefsRetry = null
            connecting?.cancel()
            connecting = null
            opening?.cancel()
            opening = null
            ws?.cancel()
            ws = null
            conn = ConnState.OFFLINE
            error = ""
            errorDetail = ""
            unresolved = false
            publish()
        }
    }

    /**
     * Whether the device has a network at all. Losing it ends the connection at once rather than at
     * the next ping; getting one starts over.
     */
    fun setNetwork(available: Boolean) {
        scope.launch {
            if (network == available) return@launch
            network = available
            unresolved = false
            grace?.cancel()
            grace = null
            waited = false
            if (running && !removed) {
                retry = 1000
                beginConnect()
            } else publish()
        }
    }

    /** Ends this Remote for good. */
    fun close() {
        stop()
        scope.launch { scope.cancel() }
    }

    /** Reconnects now instead of waiting for the backoff, e.g. after the network came back. */
    fun reconnectNow() {
        scope.launch {
            if (!running || removed) return@launch
            retry = 1000
            beginConnect()
        }
    }

    private fun publish() {
        if (conn == ConnState.ONLINE) offlineSince = 0
        else if (offlineSince == 0L) offlineSince = clock()
        if (conn == ConnState.ONLINE || !running) {
            grace?.cancel()
            grace = null
            waited = false
        } else if (!waited && grace == null) {
            grace = scope.launch {
                delay(linkGraceMs)
                grace = null
                waited = true
                publish()
            }
        }
        val link =
            when {
                conn == ConnState.ONLINE -> Link.ONLINE
                !network || unresolved -> Link.NO_NETWORK
                !waited -> Link.WAITING
                else -> Link.NO_SERVER
            }
        _state.value =
            RemoteState(
                machines.toMap(),
                sessions.toMap(),
                conn,
                error,
                loaded,
                offlineSince,
                prefs,
                devices.toMap(),
                removed,
                sessionsNext = next,
                loadingSessions = pageJob != null,
                sessionsError = pageError,
                sessionsListed = firstPage,
                link = link,
                synced = synced,
                errorDetail = errorDetail,
                authorityDeviceId = authorityDeviceId,
                leaseEndMs = leaseEnd,
                authorityPhone = authorityDeviceId == account.deviceId,
                recoveryPending = security.recoveryPending,
                clockWarning = serverAhead || security.clockSkew,
            )
    }

    /**
     * Logs in, one login at a time: the server keeps few challenges per device, and a denial has to
     * mean what it says. A server restarted between the two calls forgets the challenge; denied a
     * fresh one too, it no longer knows this device, and nothing connects until it joins again.
     */
    private suspend fun signIn(): String = signingIn.withLock {
        try {
            signedIn(loginResponse(api, account))
        } catch (first: ConnectException) {
            if (first.code != Code.PERMISSION_DENIED) throw first
            try {
                signedIn(loginResponse(api, account))
            } catch (e: ConnectException) {
                if (e.code == Code.PERMISSION_DENIED)
                    withContext(dispatcher) {
                        removed = true
                        running = false
                        generation++
                        reconnect?.cancel()
                        reconnect = null
                        ws?.close(1000, null)
                        ws = null
                        conn = ConnState.OFFLINE
                        publish()
                    }
                throw e
            }
        }
    }

    private fun signedIn(res: LoginResponse): String {
        tokenExpires = res.expiresMs
        return res.token
    }

    /** Tries the login again after the server turned this device away. */
    fun retryLogin() {
        scope.launch {
            removed = false
            publish()
        }
        start()
    }

    /**
     * Waits until what the cache holds is in place. It is read once, however many connections wait:
     * one made while it was still being read would have its lists overwritten by what was cached
     * before them.
     */
    private suspend fun restored() {
        if (cache == null) return
        val job = restoring ?: scope.launch { readCache(cache) }.also { restoring = it }
        job.join()
    }

    private suspend fun readCache(cache: CipherCache) {
        try {
            val identity = security.identity()
            trustedMembers.putAll(identity.second.core.members.associateBy { it.id })
            archiveMachines.addAll(
                security.chain().flatMap { it.core.members }.filter { it.kind == 1u }.map { it.id },
            )
            authorityDeviceId = identity.second.core.authorityID
            leaseEnd = security.record().leaseExpiration()
            for (m in cache.machines()) {
                if (m.hasMeta() && !m.meta.securityHeader.isEmpty)
                    trustedMetadata["machine:${m.id}"] = m.meta
                putMachine(m.toBuilder().setOnline(false).build(), save = false)
            }
            for (s in cache.sessions()) {
                if (s.hasMeta() && !s.meta.securityHeader.isEmpty)
                    trustedMetadata["session:${s.machineId}:${s.sessionId}"] = s.meta
                listed += s.sessionId
                putSession(s, save = false, publish = false)
            }
            cache.blob(PROJECT_PREFS)?.let {
                putPrefs(
                    io.github.lewin671.remoteagent.proto.AccountPrefsObject.parseFrom(it),
                    save = false,
                )
            }
            if (machines.isNotEmpty() || sessions.isNotEmpty()) loaded = true
        } catch (e: Exception) {
            report(e)
        }
        publish()
    }

    /** Replaces the whole attempt, including a login suspended before a socket exists. */
    private fun beginConnect() {
        if (!running || removed) return
        generation++
        reconnect?.cancel()
        reconnect = null
        connecting?.cancel()
        opening?.cancel()
        opening = null
        val old = ws
        ws = null
        old?.cancel()
        connecting = scope.launch { connect() }
    }

    private suspend fun connect() {
        if (!running) return
        val gen = generation
        conn = ConnState.CONNECTING
        publish()
        restored()
        if (!running || gen != generation) return
        try {
            // A token kept from the last connection saves the two login calls
            // each time the app comes back. One about to expire is not worth a
            // refused connection; the server refusing it closes the stream,
            // and the next try logs in.
            if (token.isEmpty() || tokenExpires < clock() + 86_400_000)
                token = withTimeout(streamOpenMs) { signIn() }
        } catch (e: Exception) {
            if (e is CancellationException && e !is TimeoutCancellationException) throw e
            if (running && gen == generation) fail(e)
            return
        }
        if (!running || gen != generation) return
        val url = account.server.replaceFirst(Regex("^http"), "ws") + "/v1/stream"
        // Bounded: a reader that falls behind drops the stream and catches up
        // from history after reconnecting, instead of queueing without end.
        val queuedBytes = java.util.concurrent.atomic.AtomicLong()
        val frames =
            Channel<ByteArray>(
                frameQueue,
                onUndeliveredElement = { queuedBytes.addAndGet(-it.size.toLong()) },
            )
        val hello =
            PeerFrame.newBuilder()
                .setHello(StreamHello.newBuilder().setToken(token))
                .build()
                .toByteArray()
        val refused = java.util.concurrent.atomic.AtomicBoolean(false)
        val broken = java.util.concurrent.atomic.AtomicReference<Throwable?>(null)
        val socket =
            streamHttp.newWebSocket(
                Request.Builder().url(url).build(),
                object : WebSocketListener() {
                    override fun onOpen(webSocket: WebSocket, response: Response) {
                        webSocket.send(hello.toByteString())
                    }

                    override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                        if (bytes.size > STREAM_FRAME_BYTES) {
                            webSocket.cancel()
                            frames.cancel()
                            return
                        }
                        if (queuedBytes.addAndGet(bytes.size.toLong()) > STREAM_QUEUE_BYTES) {
                            queuedBytes.addAndGet(-bytes.size.toLong())
                            webSocket.cancel()
                            frames.cancel()
                            return
                        }
                        if (frames.trySend(bytes.toByteArray()).isFailure) {
                            queuedBytes.addAndGet(-bytes.size.toLong())
                            webSocket.cancel()
                            frames.cancel()
                        }
                    }

                    override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                        if (code == POLICY_VIOLATION) refused.set(true)
                        webSocket.close(1000, null)
                        frames.close()
                    }

                    override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                        frames.close()
                    }

                    override fun onFailure(
                        webSocket: WebSocket,
                        t: Throwable,
                        response: Response?,
                    ) {
                        broken.set(t)
                        frames.close()
                    }
                },
            )
        ws = socket
        opening = scope.launch {
            delay(streamOpenMs)
            if (ws === socket && gen == generation && conn != ConnState.ONLINE) {
                ws = null
                generation++
                socket.cancel()
                frames.cancel()
                fail(Exception("连接超时，正在重连"))
            }
        }
        scope.launch {
            try {
                for (b in frames) {
                    try {
                        if (ws !== socket || gen != generation) break
                        val f = ServerFrame.parseFrom(b)
                        if (f.kindCase == ServerFrame.KindCase.READY) {
                            opening?.cancel()
                            opening = null
                            serverAhead =
                                clock() - f.ready.serverTimeMs >
                                    DeviceSecurity.MAX_CLOCK_SKEW.toLong()
                            publish()
                        }
                        onFrame(f)
                        if (ws !== socket || gen != generation) break
                    } finally {
                        // A frame remains in flight while its handler is suspended.
                        queuedBytes.addAndGet(-b.size.toLong())
                    }
                }
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                report(e)
            } finally {
                frames.cancel()
                socket.cancel()
                if (ws === socket) {
                    ws = null
                    generation++
                    opening?.cancel()
                    opening = null
                    // Only an explicit refusal invalidates the token.
                    if (refused.get()) token = ""
                    fail(broken.get() ?: Exception("与 server 的连接断开"))
                }
            }
        }
    }

    private fun fail(e: Throwable) {
        conn = ConnState.OFFLINE
        error = errorText(e)
        errorDetail = detailText(e)
        unresolved = unresolvedHost(e)
        publish()
        if (!running) return
        val delayMs = retry + (Random.nextDouble() * retry * 0.25).toLong()
        retry = minOf(retry * 2, 30_000)
        reconnect = scope.launch {
            delay(delayMs)
            // Do not cancel this retry job from beginConnect while it runs.
            reconnect = null
            beginConnect()
        }
    }

    private suspend fun onFrame(f: ServerFrame) {
        when (f.kindCase) {
            ServerFrame.KindCase.HEARTBEAT -> Unit
            ServerFrame.KindCase.READY -> {
                val gen = generation
                retry = 1000
                conn = ConnState.ONLINE
                error = ""
                errorDetail = ""
                unresolved = false
                publish()
                // Subscribed first, then catch up: nothing falls in between.
                refresh()
                if (!running || gen != generation) return
                val histories =
                    buffers.keys + eventFlows.filterValues { it.subscriptionCount.value > 0 }.keys
                for (sid in histories.toList()) scope.launch {
                    loadEvents(sid)
                    fetchEvents(sid)
                }
                scope.launch { loadPrefs() }
                scope.launch { loadDevices() }
            }
            ServerFrame.KindCase.ACCOUNT_PREFS_UPDATED -> {
                if (f.accountPrefsUpdated.`object`.objectId == PROJECT_PREFS)
                    putPrefs(f.accountPrefsUpdated.`object`)
            }
            ServerFrame.KindCase.DEVICE_UPDATED -> {
                bump(devicePushes, f.deviceUpdated.device.id)
                putDevice(f.deviceUpdated.device)
                publish()
            }
            ServerFrame.KindCase.DEVICE_REMOVED -> loadDevices()
            ServerFrame.KindCase.MACHINE_REMOVED -> refresh()
            ServerFrame.KindCase.MACHINE_UPDATED -> {
                bump(machinePushes, f.machineUpdated.machine.id)
                loadDevices()
                putMachine(f.machineUpdated.machine)
            }
            ServerFrame.KindCase.SESSION_UPDATED -> {
                bump(sessionPushes, f.sessionUpdated.session.sessionId)
                loadDevices()
                putSession(f.sessionUpdated.session, save = onFirstPage(f.sessionUpdated.session))
            }
            ServerFrame.KindCase.EVENTS_APPENDED -> {
                val sid = f.eventsAppended.sessionId
                val s = sessions[sid]
                if (s == null || buffers[sid] == null) {
                    if (s != null) {
                        var last = s.lastSeq
                        for (e in f.eventsAppended.eventsList) last = umax(last, e.seq)
                        sessions[sid] = s.copy(lastSeq = last)
                    }
                    publish()
                } else {
                    ingest(
                        sid,
                        f.eventsAppended.eventsList,
                        fillGaps = true,
                        save = true,
                        live = true,
                    )
                }
            }
            else -> {}
        }
    }

    private fun bump(pushes: HashMap<String, Int>, id: String) {
        pushes[id] = (pushes[id] ?: 0) + 1
    }

    private suspend fun dropMachine(id: String) {
        // However it was found gone, pushed or by a listing: a session of its that is being
        // opened for a page or a lookup asked for before must not be taken after this.
        removedPushes[id] = (removedPushes[id] ?: 0) + 1
        machines.remove(id)
        val sids = sessions.values.filter { it.machineId == id }.map { it.id }
        for (sid in sids) {
            sessions.remove(sid)
            sessionKeys.remove(sid)
            listed.remove(sid)
        }
        try {
            // The cache only lists the first page: the others held here go one by one.
            for (sid in sids) cache?.removeSession(sid)
            cache?.removeMachine(id)
        } catch (e: Exception) {
            report(e)
        }
    }

    /**
     * Reloads machines and the first page of sessions: every session that has not ended and the
     * latest ended ones. Pushes that arrive while the lists are in flight are newer and win.
     * Returns whether the lists are in place. When they could not be fetched
     * [RemoteState.sessionsError] says why, and it is tried again by itself, ever more slowly,
     * while the connection holds.
     */
    suspend fun refresh(): Boolean =
        withContext(dispatcher) {
            val run = ++refreshRun
            listRetry?.cancel()
            listRetry = null
            listsInFlight.value++
            try {
                val mp = HashMap(machinePushes)
                val sp = HashMap(sessionPushes)
                val rp = HashMap(removedPushes)
                loadDevices()
                val ms =
                    api.client
                        .listMachines(ListMachinesRequest.getDefaultInstance(), Api.headers(token))
                        .value()
                val ss =
                    api.client
                        .listRemoteSessions(
                            ListRemoteSessionsRequest.newBuilder().setLimit(SESSION_PAGE).build(),
                            Api.headers(token),
                        )
                        .value()
                // Putting the lists in place suspends on the cache: one at a time.
                applying.withLock {
                    // Asked for again since: that answer is the newer one, and this one would undo
                    // it.
                    if (run != refreshRun) return@withContext false
                    // The listing starts over: a later page still in flight continued the old one.
                    listing++
                    // Forget what the server no longer has, e.g. a machine another
                    // device removed while this one was offline.
                    val mids = ms.machinesList.map { it.id }.toSet()
                    val sids = ss.sessionsList.map { it.sessionId }.toSet()
                    val end =
                        if (ss.hasNext()) SessionPlace(ss.next.endedMs, ss.next.sessionId) else null
                    for (id in machines.keys.toList()) if (
                        id !in mids && !pushed(machinePushes, mp, id)
                    )
                        dropMachine(id)
                    fun hasMachine(id: String) = id in mids || machines.containsKey(id)
                    // A session held here that this page does not have (architecture §8
                    // 「重新连接后」): one that ended and comes after the page's end is
                    // a later page's and stays; any other goes. Without a later page
                    // the page is the whole listing, and what it lacks does not exist.
                    for (v in sessions.values.toList()) {
                        if (v.id in sids || pushed(sessionPushes, sp, v.id)) continue
                        if (
                            end != null &&
                                hasMachine(v.machineId) &&
                                afterPlace(v.listedEndedMs, v.id, end)
                        )
                            continue
                        sessions.remove(v.id)
                        sessionKeys.remove(v.id)
                        listed.remove(v.id)
                        // Its events go with it only when it is known not to exist; else it
                        // may be on a later page with what is held here out of date.
                        try {
                            if (end == null || !hasMachine(v.machineId)) cache?.removeSession(v.id)
                            else cache?.unlistSession(v.id)
                        } catch (e: Exception) {
                            report(e)
                        }
                    }
                    // The cache keeps the first page only.
                    for (id in listed.toList()) {
                        if (id in sids || pushed(sessionPushes, sp, id)) continue
                        listed.remove(id)
                        try {
                            cache?.unlistSession(id)
                        } catch (e: Exception) {
                            report(e)
                        }
                    }
                    for (m in ms.machinesList) {
                        if (pushed(removedPushes, rp, m.id)) continue
                        putMachine(m, staleOnline = pushed(machinePushes, mp, m.id))
                    }
                    for (s in ss.sessionsList) if (!pushed(removedPushes, rp, s.machineId))
                        putSession(s, publish = false)
                    next = end
                    firstEnd = end
                    firstPage = true
                    synced = true
                    pageError = ""
                    listBackoff = LIST_RETRY_MS
                    loaded = true
                    publish()
                }
                true
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Exception) {
                report(e)
                // What is held stays as it was, with the cursor it had.
                if (run == refreshRun) {
                    pageError = errorText(e)
                    retryListing(run)
                }
                publish()
                false
            } finally {
                listsInFlight.value--
            }
        }

    /**
     * Asks for the lists again after a while, unless they were asked for meanwhile or the
     * connection is gone.
     */
    private fun retryListing(run: Int) {
        val wait = listBackoff
        listBackoff = minOf(listBackoff * 2, 30_000)
        listRetry = scope.launch {
            delay(wait)
            // Not this job's to cancel itself from refresh.
            listRetry = null
            if (running && conn == ConnState.ONLINE && run == refreshRun) refresh()
        }
    }

    private fun pushed(now: Map<String, Int>, then: Map<String, Int>, id: String) =
        (now[id] ?: 0) != (then[id] ?: 0)

    /**
     * Whether the listing's first page, as last fetched, would hold this session: the cache keeps
     * that page.
     */
    private fun onFirstPage(rs: RemoteSession): Boolean =
        firstPage && firstEnd.let { it == null || !afterPlace(rs.endedMs, rs.sessionId, it) }

    /**
     * Fetches the next page of ended sessions, which is the first one while that has not come
     * ([RemoteState.sessionsListed]); calls made while one is in flight wait for that one. Returns
     * whether there are more after it. Throws when the page could not be fetched, which
     * [RemoteState.sessionsError] then says until a page comes.
     */
    suspend fun loadMoreSessions(): Boolean =
        withContext(dispatcher) {
                pageJob
                    ?: scope
                        .async {
                            try {
                                fetchPage()
                            } finally {
                                pageJob = null
                                publish()
                            }
                        }
                        .also {
                            pageJob = it
                            publish()
                        }
            }
            .await()

    /**
     * Fetches every page that is left, e.g. to filter by title; calls made meanwhile wait for the
     * same run.
     */
    suspend fun loadAllSessions() =
        withContext(dispatcher) {
                allJob
                    ?: scope
                        .async {
                            try {
                                while (!firstPage || next != null) loadMoreSessions()
                            } finally {
                                allJob = null
                            }
                        }
                        .also { allJob = it }
            }
            .await()

    private suspend fun fetchPage(): Boolean {
        while (true) {
            // A first page on its way, or being put in place, has the cursor to go on from;
            // where none was held, it is the page that was asked for.
            val listedBefore = firstPage
            listsInFlight.first { it == 0 }
            if (!listedBefore && firstPage) return next != null
            if (conn != ConnState.ONLINE) {
                // Nothing is asked for in the background, or over a connection that is gone.
                pageError = "没有连上服务器"
                throw CommandException(CommandException.UNAVAILABLE, pageError)
            }
            if (!firstPage) {
                // The first page never came: it is the one to fetch.
                if (refresh()) return next != null
                // Asked for again meanwhile: that one is waited for above.
                if (listsInFlight.value > 0 || firstPage) continue
                throw CommandException(
                    CommandException.UNAVAILABLE,
                    pageError.ifEmpty { "没能读取会话列表" },
                )
            }
            val after = next ?: return false
            val epoch = listing
            val rp = HashMap(removedPushes)
            val res =
                try {
                    api.client
                        .listRemoteSessions(
                            ListRemoteSessionsRequest.newBuilder()
                                .setAfter(
                                    SessionCursor.newBuilder()
                                        .setEndedMs(after.endedMs)
                                        .setSessionId(after.sessionId),
                                )
                                .setLimit(SESSION_PAGE)
                                .build(),
                            Api.headers(currentToken()),
                        )
                        .value()
                } catch (e: kotlinx.coroutines.CancellationException) {
                    throw e
                } catch (e: Exception) {
                    // The first page was fetched again meanwhile: what failed continued the listing
                    // before it.
                    if (epoch != listing) continue
                    log("remote-agent: " + errorText(e))
                    pageError = errorText(e)
                    throw e
                }
            // The first page was fetched again meanwhile: this page continued the
            // listing before it. The caller still wants a page: the one after the new cursor.
            if (epoch != listing) continue
            // Putting a session suspends on the cache: the listing may start over meanwhile too.
            for (s in res.sessionsList) if (epoch == listing && accepts(s, rp))
                putSession(s, save = false, publish = false)
            if (epoch != listing) continue
            next = if (res.hasNext()) SessionPlace(res.next.endedMs, res.next.sessionId) else null
            pageError = ""
            return next != null
        }
    }

    /**
     * Whether a session the server answered a request with is taken: not one of a machine removed
     * since the request was made ([removalsThen]), nor of one the listing of machines does not
     * have.
     */
    private fun accepts(rs: RemoteSession, removalsThen: Map<String, Int>): Boolean =
        !pushed(removedPushes, removalsThen, rs.machineId) &&
            (!firstPage || machines.containsKey(rs.machineId))

    /**
     * Fetches one session by its ID when it is not held here: one opened from a notification or a
     * link may be on a page not fetched yet. Returns whether it is held now; false means the server
     * does not have it. Throws when the server could not be asked.
     */
    suspend fun fetchSession(id: String): Boolean =
        withContext(dispatcher) {
                if (sessions[id] != null) return@withContext CompletableDeferred(true)
                byId[id]
                    ?: scope
                        .async {
                            try {
                                val rp = HashMap(removedPushes)
                                val res =
                                    api.client
                                        .listRemoteSessions(
                                            ListRemoteSessionsRequest.newBuilder()
                                                .addSessionIds(id)
                                                .build(),
                                            Api.headers(currentToken()),
                                        )
                                        .value()
                                val rs = res.sessionsList.firstOrNull { it.sessionId == id }
                                // A session of a machine removed meanwhile, or one the listing of
                                // machines no longer has, is as good as gone.
                                if (rs != null && accepts(rs, rp))
                                    putSession(rs, save = onFirstPage(rs))
                                sessions[id] != null
                            } finally {
                                byId.remove(id)
                            }
                        }
                        .also { byId[id] = it }
            }
            .await()

    private suspend fun putMachine(
        incoming: Machine,
        staleOnline: Boolean = false,
        save: Boolean = true,
    ) {
        if (trustedMembers[incoming.id]?.kind != 1u && incoming.id !in archiveMachines) return
        val builder = incoming.toBuilder().clearMeta()
        trustedMetadata["machine:${incoming.id}"]?.let { builder.setMeta(it) }
        val m = builder.build()
        val old = machines[m.id]
        var view = old ?: MachineView(m.id, m.online, m.lastSeenMs)
        if (!staleOnline) view = view.copy(online = m.online)
        if (m.lastSeenMs > view.lastSeenMs) view = view.copy(lastSeenMs = m.lastSeenMs)
        var store = old == null
        if (m.hasMeta()) {
            try {
                val (h, meta) =
                    openSecureMessage(
                        keys?.meta ?: ByteArray(0),
                        m.meta,
                        Place(PayloadKind.PAYLOAD_KIND_MACHINE_META, machineId = m.id),
                        MachineMeta::parseFrom,
                    )
                if (ucmp(h.seq, view.metaRev) >= 0) {
                    view = view.copy(meta = meta, metaRev = h.seq)
                    store = true
                }
            } catch (e: Exception) {
                report(e)
            }
        }
        if (
            (trustedMembers[m.id]?.kind != 1u && m.id !in archiveMachines) ||
                ucmp(machines[m.id]?.metaRev ?: 0L, view.metaRev) > 0
        )
            return
        machines[m.id] = view
        if (save && store) {
            try {
                cache?.putMachine(m)
            } catch (e: Exception) {
                report(e)
            }
        }
        publish()
    }

    private suspend fun putSession(
        incoming: RemoteSession,
        save: Boolean = true,
        publish: Boolean = true,
    ) {
        if (
            trustedMembers[incoming.machineId]?.kind != 1u && incoming.machineId !in archiveMachines
        )
            return
        // The metadata is opened and its origin checked below; the relay cannot forge it.
        val rs = incoming
        var view = sessions[rs.sessionId]
        val removals = removedPushes[rs.machineId] ?: 0
        try {
            if (view == null) {
                val route = Header.parseFrom(rs.key.header)
                val (h, raw) =
                    if (route.version == 1)
                        Envelopes.open(
                            keys?.wrap ?: throw OpenException("历史不可读：这台设备没有账户根密钥"),
                            rs.key,
                        )
                    else {
                        val (h, raw) = security.openHistory(rs.key)
                        projection(h) to raw
                    }
                Envelopes.expect(
                    h,
                    Place(
                        PayloadKind.PAYLOAD_KIND_SESSION_KEY,
                        machineId = rs.machineId,
                        sessionId = rs.sessionId,
                    ),
                )
                if (raw.size != KEY_SIZE) throw OpenException("bad session key")
                if ((removedPushes[rs.machineId] ?: 0) != removals) return
                sessionKeys[rs.sessionId] = raw
                view = SessionView(rs.sessionId, rs.machineId)
            }
            if (view.machineId != rs.machineId) {
                val (_, info) =
                    openSecureMessage(
                        sessionKeys[rs.sessionId]!!,
                        rs.meta,
                        Place(
                            PayloadKind.PAYLOAD_KIND_SESSION_META,
                            machineId = rs.machineId,
                            sessionId = rs.sessionId,
                        ),
                        Session::parseFrom,
                    )
                if (
                    view.machineId !in
                        machineReplacementOrigins(
                            security.record(),
                            rs.machineId,
                            info.machineReplacementsList,
                        )
                )
                    throw OpenException("会话记录的机器被改动")
                val (h, seed) = security.openHistory(rs.key)
                Envelopes.expect(
                    projection(h),
                    Place(
                        PayloadKind.PAYLOAD_KIND_SESSION_KEY,
                        machineId = rs.machineId,
                        sessionId = rs.sessionId,
                    ),
                )
                if (seed.size != KEY_SIZE) throw OpenException("bad session key")
                sessionKeys[rs.sessionId] = seed
                view = view.copy(machineId = rs.machineId, metaRev = 0)
            }
            view =
                view.copy(
                    lastSeq = umax(view.lastSeq, rs.lastSeq),
                    updatedMs = maxOf(view.updatedMs, rs.updatedMs),
                )
            var store = sessions[rs.sessionId] == null
            if (store) view = view.copy(listedEndedMs = rs.endedMs)
            if (rs.hasMeta()) {
                val key = sessionKeys[rs.sessionId]!!
                val (h, info) =
                    openSecureMessage(
                        key,
                        rs.meta,
                        Place(
                            PayloadKind.PAYLOAD_KIND_SESSION_META,
                            machineId = rs.machineId,
                            sessionId = rs.sessionId,
                        ),
                        Session::parseFrom,
                    )
                if (ucmp(h.seq, view.metaRev) >= 0) {
                    // The server stores when it ended with the metadata.
                    val checkpoint =
                        info.currentCheckpoint
                            .toByteArray()
                            .takeIf { it.isNotEmpty() }
                            ?.let {
                                io.github.lewin671.remoteagent.proto.LiveSnapshot.parseFrom(it)
                            }
                    if (
                        checkpoint != null &&
                            (checkpoint.session.id != rs.sessionId ||
                                ucmp(checkpoint.session.lastSeq, info.lastSeq) > 0 ||
                                checkpoint.actionsList.any {
                                    it.sessionId != rs.sessionId || ucmp(it.seq, info.lastSeq) > 0
                                })
                    )
                        throw OpenException("密文无效：当前状态 checkpoint 不匹配")
                    view =
                        view.copy(
                            info = info,
                            metaRev = h.seq,
                            listedEndedMs = rs.endedMs,
                            checkpoint = checkpoint,
                            checkpointStatus = if (checkpoint == null) "waiting" else "ready",
                        )
                    store = true
                }
            }
            if (
                (removedPushes[rs.machineId] ?: 0) != removals ||
                    (trustedMembers[rs.machineId]?.kind != 1u &&
                        rs.machineId !in archiveMachines) ||
                    ucmp(sessions[rs.sessionId]?.metaRev ?: 0L, view.metaRev) > 0
            )
                return
            sessions[rs.sessionId] = view
            if ((eventFlows[rs.sessionId]?.subscriptionCount?.value ?: 0) > 0)
                scope.launch { loadEvents(rs.sessionId) }
            if (save && store) {
                listed += rs.sessionId
                cache?.putSession(rs)
            } else if (!save) {
                cache?.keepOwner(rs)
            }
        } catch (e: Exception) {
            report(e)
        }
        if (publish) publish()
    }

    // --- account preferences and devices ---

    /**
     * Reads the preferences. With no copy of them here nothing of them can be changed, so a read
     * that fails then is tried again by itself, ever more slowly, while the connection holds.
     */
    private suspend fun loadPrefs() {
        prefsRetry?.cancel()
        prefsRetry = null
        val gen = generation
        try {
            val o =
                api.client
                    .getAccountPrefs(
                        io.github.lewin671.remoteagent.proto.GetAccountPrefsRequest.newBuilder()
                            .setObjectId(PROJECT_PREFS)
                            .build(),
                        Api.headers(token),
                    )
                    .value()
            putPrefs(o.`object`)
            prefsBackoff = LIST_RETRY_MS
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
            // A connection made since reads them itself.
            if (prefs != null || !running || gen != generation || prefsRetry != null) return
            val wait = prefsBackoff
            prefsBackoff = minOf(prefsBackoff * 2, 30_000)
            prefsRetry = scope.launch {
                delay(wait)
                // Not this job's to cancel itself from loadPrefs.
                prefsRetry = null
                if (running && conn == ConnState.ONLINE && gen == generation && prefs == null)
                    loadPrefs()
            }
        }
    }

    /** Takes an object from the server when it is not older than what is known here. */
    private suspend fun putPrefs(
        o: io.github.lewin671.remoteagent.proto.AccountPrefsObject,
        save: Boolean = true,
    ) {
        if (o.revision == 0L || !o.hasEnvelope() || o.envelope.securityHeader.isEmpty) {
            prefs = newerPrefs(prefs, PrefsObject.EMPTY)
            publish()
            return
        }
        val opened =
            try {
                val (h, p) =
                    openSecureMessage(
                        keys?.prefs ?: ByteArray(0),
                        o.envelope,
                        Place(PayloadKind.PAYLOAD_KIND_ACCOUNT_PREFS, requestId = PROJECT_PREFS),
                        io.github.lewin671.remoteagent.proto.ProjectPrefs::parseFrom,
                    )
                PrefsObject(h.seq, p)
            } catch (e: Exception) {
                report(e)
                return
            }
        val before = prefs
        prefs = newerPrefs(before, opened)
        if (save && prefs === opened && o.revision > 0) {
            try {
                cache?.putBlob(PROJECT_PREFS, o.toByteArray())
            } catch (e: Exception) {
                report(e)
            }
        }
        publish()
    }

    /**
     * Pins, unpins, hides or restores a project for the whole account. Returns once the server has
     * it; a conflict is redone on the newer object. Needs the connection: offline, changes wait.
     */
    private suspend fun readTrustedPrefs(): PrefsObject {
        val o =
            api.client
                .getAccountPrefs(
                    io.github.lewin671.remoteagent.proto.GetAccountPrefsRequest.newBuilder()
                        .setObjectId(PROJECT_PREFS)
                        .build(),
                    Api.headers(currentToken()),
                )
                .value()
                .`object`
        if (o.revision == 0L || !o.hasEnvelope() || o.envelope.securityHeader.isEmpty)
            return PrefsObject.EMPTY
        val (h, p) =
            openSecureMessage(
                keys?.prefs ?: ByteArray(0),
                o.envelope,
                Place(PayloadKind.PAYLOAD_KIND_ACCOUNT_PREFS, requestId = PROJECT_PREFS),
                io.github.lewin671.remoteagent.proto.ProjectPrefs::parseFrom,
            )
        return PrefsObject(h.seq, p)
    }

    suspend fun changePrefs(op: PrefsOp) {
        val operation = newRequestId()
        repeat(5) {
            val base = readTrustedPrefs()
            if (base.prefs.recentOpsList.contains(operation)) {
                withContext(dispatcher) {
                    prefs = base
                    publish()
                }
                return
            }
            val next = applyPrefsOp(base.prefs, op, operation, clock())
            try {
                val env =
                    security.seal(
                        DeviceSecurity.Purpose.PREFS,
                        request = PROJECT_PREFS,
                        seq = (base.revision + 1).toULong(),
                        plain = next.toByteArray(),
                    )
                val applied =
                    api.client
                        .putAccountPrefs(
                            io.github.lewin671.remoteagent.proto.PutAccountPrefsRequest.newBuilder()
                                .setObjectId(PROJECT_PREFS)
                                .setExpectedRevision(base.revision)
                                .setEnvelope(env)
                                .build(),
                            Api.headers(currentToken()),
                        )
                        .value()
                        .applied
                if (!applied) return@repeat
                val current = readTrustedPrefs()
                withContext(dispatcher) {
                    prefs = current
                    publish()
                }
                return
            } catch (e: DeviceSecurity.Failure) {
                if (e.code != "conflict") throw e
            }
        }
        throw CommandException("aborted", "项目偏好正在变化，请重试")
    }

    /** Reads the device list again, for a page whose times are as of opening it. */
    suspend fun refreshDevices() = withContext(dispatcher) { loadDevices() }

    /** Pulls the signed state chain now; unknown states are also fetched while opening. */
    suspend fun syncSecurity() {
        val changed =
            try {
                syncSecurityOnce()
            } catch (e: DeviceSecurity.Failure) {
                if (e.code != "clock") throw e
                publish()
                return
            }
        if (changed || recoveryObjectsPending) withContext(dispatcher) { loadDevices() }
    }

    private suspend fun syncSecurityOnce(): Boolean {
        val changed =
            withContext(dispatcher) {
                val r = security.sync()
                resumeSecurityOperation()
                try {
                    recoveryObjectsPending = true
                    republishRecoveryObjects(r)
                    recoveryObjectsPending = false
                } catch (e: DeviceSecurity.Failure) {
                    // This maintenance write stays pending until renewal.
                    // Reading the directory and stored content still works.
                    if (e.code != "lease") throw e
                }
                authorityDeviceId = r.head.core.authorityID
                leaseEnd = r.leaseExpiration()
                publish()
                directoryHead?.contentEquals(DeviceSecurity.stateHash(r.head)) != true
            }
        return changed
    }

    /**
     * Objects owned by clients cross a recovery boundary via CAS, using only snapshots this
     * retained device can verify and decrypt. Checking the stored content epoch also retries an
     * interrupted republish after a restart.
     */
    private suspend fun republishRecoveryObjects(r: DeviceSecurity.Record) {
        val boundary =
            r.proofs.keys.maxOrNull()?.let { r.stateAt(it)?.core?.contentEpoch } ?: return
        val headers = Api.headers(currentToken())
        retryRecoveryPublication {
            val obj =
                api.client
                    .getAccountPrefs(
                        io.github.lewin671.remoteagent.proto.GetAccountPrefsRequest.newBuilder()
                            .setObjectId(PROJECT_PREFS)
                            .build(),
                        headers,
                    )
                    .value()
                    .`object`
            if (!obj.hasEnvelope() || obj.revision == 0L) return@retryRecoveryPublication true
            val h = DeviceSecurity.parseHeader(obj.envelope.securityHeader.toByteArray())
            if (h.contentEpoch >= boundary) return@retryRecoveryPublication true
            if (
                h.purpose != DeviceSecurity.Purpose.PREFS ||
                    h.request != PROJECT_PREFS ||
                    h.seq != obj.revision.toULong()
            )
                throw DeviceSecurity.Failure("untrusted")
            val snapshot =
                try {
                    security
                        .openMessage(
                            obj.envelope,
                            true,
                            parse = io.github.lewin671.remoteagent.proto.ProjectPrefs::parseFrom,
                        )
                        .second
                } catch (e: DeviceSecurity.Failure) {
                    if (e.code == "unavailable") return@retryRecoveryPublication true else throw e
                }
            val env =
                security.seal(
                    DeviceSecurity.Purpose.PREFS,
                    request = PROJECT_PREFS,
                    seq = (obj.revision + 1).toULong(),
                    plain = snapshot.toByteArray(),
                )
            val result =
                api.client
                    .putAccountPrefs(
                        io.github.lewin671.remoteagent.proto.PutAccountPrefsRequest.newBuilder()
                            .setObjectId(PROJECT_PREFS)
                            .setExpectedRevision(obj.revision)
                            .setEnvelope(env)
                            .build(),
                        headers,
                    )
                    .value()
            result.applied
        }
        retryRecoveryPublication {
            val own =
                api.client
                    .listDevices(
                        io.github.lewin671.remoteagent.proto.ListDevicesRequest
                            .getDefaultInstance(),
                        headers,
                    )
                    .value()
                    .devicesList
                    .firstOrNull { it.id == account.deviceId }
                    ?: return@retryRecoveryPublication true
            if (!own.hasMeta()) return@retryRecoveryPublication true
            val h = DeviceSecurity.parseHeader(own.meta.securityHeader.toByteArray())
            if (h.contentEpoch >= boundary) return@retryRecoveryPublication true
            if (
                h.purpose != DeviceSecurity.Purpose.DEVICE_META ||
                    h.sender != account.deviceId ||
                    h.seq != own.metaRevision.toULong()
            )
                throw DeviceSecurity.Failure("untrusted")
            val snapshot =
                try {
                    security
                        .openMessage(
                            own.meta,
                            true,
                            parse = io.github.lewin671.remoteagent.proto.DeviceMeta::parseFrom,
                        )
                        .second
                } catch (e: DeviceSecurity.Failure) {
                    if (e.code == "unavailable") return@retryRecoveryPublication true else throw e
                }
            val env =
                security.seal(
                    DeviceSecurity.Purpose.DEVICE_META,
                    request = account.deviceId,
                    seq = (own.metaRevision + 1).toULong(),
                    plain = snapshot.toByteArray(),
                )
            val result =
                api.client
                    .putDeviceMeta(
                        io.github.lewin671.remoteagent.proto.PutDeviceMetaRequest.newBuilder()
                            .setDeviceId(account.deviceId)
                            .setExpectedRevision(own.metaRevision)
                            .setMeta(env)
                            .build(),
                        headers,
                    )
                    .value()
            result.applied
        }
    }

    private val securityOperationLock = Mutex()

    /** Resume exact persisted state publication and pairing delivery after a crash or lost ack. */
    suspend fun resumeSecurityOperation() = securityOperationLock.withLock {
        resumeSecurityOperationLocked()
    }

    private suspend fun resumeSecurityOperationLocked() {
        val storage = securityStorage ?: throw DeviceSecurity.Failure("storage")
        var op = storage.readSecurityOperation() ?: return
        if (op.kind == "lease") {
            val r = security.sync()
            val hash = DeviceSecurity.stateHash(r.head)
            if (!hash.contentEquals(op.parent)) {
                // A verified successor makes this old-head certificate obsolete.
                if (r.chain.none { DeviceSecurity.stateHash(it).contentEquals(op.parent) })
                    throw DeviceSecurity.Failure("conflict")
                storage.clearSecurityOperation()
                return
            }
            var req = PutSecurityStateRequest.parseFrom(op.signed)
            var cert = HeadLease.parse(req.lease.toByteArray())
            cert.verify(r.head)
            if (maxOf(clock().toULong(), r.floor) >= cert.notAfter) {
                cert =
                    authorityForeground().let {
                        HeadLease.sign(
                            r.head,
                            SigningKey(account.deviceSeed),
                            security.signingTime(clock()),
                        )
                    }
                req =
                    PutSecurityStateRequest.newBuilder()
                        .setLease(com.google.protobuf.ByteString.copyFrom(cert.bytes()))
                        .build()
                storage.writeSecurityOperation(SecurityOperation("lease", hash, req.toByteArray()))
            }
            api.client.putSecurityState(req, Api.headers(currentToken())).value()
            security.install(emptyList(), lease = cert)
            storage.clearSecurityOperation()
            leaseEnd = cert.notAfter.toLong()
            publish()
            return
        }
        if (op.kind != "state") {
            reconcileCompletedAdoption(account, security.record(), storage)
            return
        }
        val request = PutSecurityStateRequest.parseFrom(op.signed)
        val states = request.statesList.map { DeviceSecurity.parseState(it.state.toByteArray()) }
        val target = states.last()
        if (op.phase == SecurityOperation.Phase.DELIVERED) {
            val installed =
                security.record().stateAt(target.core.epoch)
                    ?: throw DeviceSecurity.Failure("conflict")
            if (
                !DeviceSecurity.stateHash(installed).contentEquals(DeviceSecurity.stateHash(target))
            )
                throw DeviceSecurity.Failure("conflict")
            storage.clearSecurityOperation()
            return
        }
        fun saveRetryCertificate(certificate: HeadLease?) {
            val saved =
                request
                    .toBuilder()
                    .clearLease()
                    .apply {
                        certificate?.let {
                            setLease(com.google.protobuf.ByteString.copyFrom(it.bytes()))
                        }
                    }
                    .build()
            if (saved.lease == request.lease) return
            val updated =
                SecurityOperation(
                    op.kind,
                    op.parent,
                    saved.toByteArray(),
                    op.local,
                    op.delivery,
                    op.phase,
                    op.codeSaved,
                )
            storage.writeSecurityOperation(updated)
            op = updated
        }
        // sync() validates a strict extension before reconciliation.
        if (
            target.core.authorityID != account.deviceId &&
                op.phase == SecurityOperation.Phase.PUBLISHED
        ) {
            storage.clearSecurityOperation()
            return
        }
        // Fetch before ordinary reconciliation; the retiring phone may lose
        // its token as the very result of the handoff it is completing.
        if (target.core.authorityID == account.deviceId) security.sync()
        val verified = security.record()
        val installed = verified.stateAt(target.core.epoch)
        if (installed != null) {
            if (
                !DeviceSecurity.stateHash(installed).contentEquals(DeviceSecurity.stateHash(target))
            )
                throw DeviceSecurity.Failure("conflict")
            // The signed state is immutable. Its independent availability certificate
            // can be renewed after a lost ack or a long offline interval.
            if (
                verified.head.core.epoch == target.core.epoch &&
                    target.core.authorityID == account.deviceId &&
                    runCatching {
                        verified.lease?.valid(target, maxOf(clock().toULong(), verified.floor))
                            ?: throw DeviceSecurity.Failure("lease")
                    }
                        .isFailure
            ) {
                val certificate =
                    retryCertificate(request, target, verified.floor)
                        ?: throw DeviceSecurity.Failure("lease")
                val renewal =
                    PutSecurityStateRequest.newBuilder()
                        .setLease(com.google.protobuf.ByteString.copyFrom(certificate.bytes()))
                        .build()
                saveRetryCertificate(certificate)
                api.client.putSecurityState(renewal, Api.headers(currentToken())).value()
                security.install(emptyList(), lease = certificate)
            }
        } else {
            if (!DeviceSecurity.stateHash(verified.head).contentEquals(op.parent))
                throw DeviceSecurity.Failure("conflict")
            val certificate = retryCertificate(request, target, verified.floor)
            val retry =
                request
                    .toBuilder()
                    .clearLease()
                    .apply {
                        certificate?.let {
                            setLease(com.google.protobuf.ByteString.copyFrom(it.bytes()))
                        }
                    }
                    .build()
            saveRetryCertificate(certificate)
            api.client.putSecurityState(retry, Api.headers(currentToken())).value()
            security.install(
                states,
                request.statesList.map {
                    it.recovery.toByteArray().takeIf { raw -> raw.isNotEmpty() }
                },
                certificate,
            )
        }
        storage.writeSecurityOperation(op.advanced(SecurityOperation.Phase.PUBLISHED))
        if (op.delivery.isNotEmpty()) {
            val delivery = ApprovePairingRequest.parseFrom(op.delivery)
            api.pairing.approvePairing(delivery, Api.headers(currentToken())).value()
            val status =
                api.pairing
                    .getPairing(
                        GetPairingRequest.newBuilder().setPairingId(delivery.pairingId).build(),
                        Api.headers(currentToken()),
                    )
                    .value()
            // Keep the exact payload until the candidate confirms receipt,
            // including across authority-process death after approval.
            if (status.state != PairingState.PAIRING_STATE_DELIVERED) return
        }
        storage.writeSecurityOperation(op.advanced(SecurityOperation.Phase.DELIVERED))
        storage.clearSecurityOperation()
        withContext(dispatcher) {
            authorityDeviceId = security.head().core.authorityID
            leaseEnd = security.record().leaseExpiration()
            publish()
        }
    }

    private suspend fun retryCertificate(
        request: PutSecurityStateRequest,
        target: DeviceSecurity.State,
        floor: ULong,
    ): HeadLease? {
        val now = maxOf(clock().toULong(), floor)
        val original =
            request.lease.toByteArray().takeIf { it.isNotEmpty() }?.let { HeadLease.parse(it) }
        original?.verify(target)
        if (original != null && now < original.notAfter) {
            original.valid(target, now)
            return original
        }
        // Only this state's authority can renew it. A retiring phone may still
        // finish its exact handoff without attaching an expired certificate.
        return if (target.core.authorityID == account.deviceId)
            authorityForeground().let {
                HeadLease.sign(
                    target,
                    SigningKey(account.deviceSeed),
                    maxOf(now, target.core.issuedAt).toLong(),
                )
            }
        else null
    }

    fun pendingPairingRequest(pairingId: String): ApprovePairingRequest? {
        val op = securityStorage?.readSecurityOperation() ?: return null
        if (op.kind != "state" || op.delivery.isEmpty()) return null
        return ApprovePairingRequest.parseFrom(op.delivery).takeIf { it.pairingId == pairingId }
    }

    suspend fun finishPairingOperation(pairingId: String) = securityOperationLock.withLock {
        val storage = securityStorage ?: throw DeviceSecurity.Failure("storage")
        val op = storage.readSecurityOperation() ?: return@withLock
        if (
            op.kind == "state" &&
                op.delivery.isNotEmpty() &&
                ApprovePairingRequest.parseFrom(op.delivery).pairingId == pairingId
        ) {
            storage.writeSecurityOperation(op.advanced(SecurityOperation.Phase.DELIVERED))
            storage.clearSecurityOperation()
        }
    }

    // An owner may revoke an approved candidate whose delivery expired. This
    // ends that delivery intent only after its exact state is locally verified;
    // removal then signs a child, never a competing successor.
    private suspend fun cancelPairDeliveryForRemoval(deviceId: String): Boolean =
        securityOperationLock.withLock {
            val storage = securityStorage ?: return@withLock false
            val op = storage.readSecurityOperation() ?: return@withLock false
            if (op.kind != "state" || op.delivery.isEmpty()) return@withLock false
            val delivery = ApprovePairingRequest.parseFrom(op.delivery)
            val candidate =
                PairApproval.parseFrom(delivery.approval).candidateDevicePublicKey.toByteArray()
            if (io.github.lewin671.remoteagent.core.deviceId(candidate) != deviceId)
                return@withLock false
            val record = security.sync()
            val target =
                DeviceSecurity.parseState(
                    PutSecurityStateRequest.parseFrom(op.signed)
                        .statesList
                        .last()
                        .state
                        .toByteArray(),
                )
            val installed = record.stateAt(target.core.epoch) ?: return@withLock false
            if (
                !DeviceSecurity.stateHash(installed).contentEquals(DeviceSecurity.stateHash(target))
            )
                throw DeviceSecurity.Failure("conflict")
            storage.clearSecurityOperation()
            true
        }

    /** All state-changing security flows share one durable publication slot. */
    suspend fun publishSecurityStates(
        states: List<DeviceSecurity.State>,
        proofs: List<ByteArray?> = emptyList(),
        delivery: ByteArray = byteArrayOf(),
        certificate: HeadLease? = null,
    ) {
        if (serverAhead || security.clockSkew) throw DeviceSecurity.Failure("clock")
        require(states.isNotEmpty())
        val storage = securityStorage ?: throw DeviceSecurity.Failure("storage")
        val req = PutSecurityStateRequest.newBuilder()
        states.forEachIndexed { i, state ->
            req.addStates(
                SecurityState.newBuilder()
                    .setState(
                        com.google.protobuf.ByteString.copyFrom(DeviceSecurity.stateBytes(state)),
                    )
                    .apply {
                        proofs.getOrNull(i)?.let {
                            setRecovery(com.google.protobuf.ByteString.copyFrom(it))
                        }
                    },
            )
        }
        val lease =
            certificate
                ?: if (states.last().core.authorityID == account.deviceId)
                    authorityForeground().let {
                        HeadLease.sign(
                            states.last(),
                            SigningKey(account.deviceSeed),
                            security.signingTime(clock()),
                        )
                    }
                else null
        lease?.let { req.setLease(com.google.protobuf.ByteString.copyFrom(it.bytes())) }
        val pending = storage.readSecurityOperation()
        if (pending != null) {
            if (pending.kind != "state" || !pending.signed.contentEquals(req.build().toByteArray()))
                throw DeviceSecurity.Failure("pending")
        } else
            storage.writeSecurityOperation(
                SecurityOperation(
                    "state",
                    states.first().core.parent,
                    req.build().toByteArray(),
                    delivery = delivery,
                ),
            )
        resumeSecurityOperation()
    }

    /** No authority signature may be made by work canceled on backgrounding. */
    internal suspend fun authorityForeground() {
        kotlinx.coroutines.currentCoroutineContext().ensureActive()
        authorityJob.ensureActive()
    }

    /** Foreground lease renewal, at most once every 12 hours (architecture §9). */
    fun renewLeaseIfNeeded() = renewLease(force = false)

    /** The user asked to renew now; the same signed state, earlier than the automatic rule. */
    fun renewLeaseNow() = renewLease(force = true)

    private fun renewLease(force: Boolean) {
        scope.launch {
            try {
                withContext(authorityJob) {
                    resumeSecurityOperation()
                    val head = security.head()
                    if (head.core.authorityID != account.deviceId) return@withContext
                    if (
                        !force &&
                            !Authority.needsRenewal(
                                security.record().lease?.takeIf {
                                    it.head.contentEquals(DeviceSecurity.stateHash(head))
                                },
                                clock(),
                            )
                    )
                        return@withContext
                    val lease =
                        authorityForeground().let {
                            HeadLease.sign(
                                head,
                                SigningKey(account.deviceSeed),
                                security.signingTime(clock()),
                            )
                        }
                    val request =
                        PutSecurityStateRequest.newBuilder()
                            .setLease(com.google.protobuf.ByteString.copyFrom(lease.bytes()))
                            .build()
                    val storage = securityStorage ?: throw DeviceSecurity.Failure("storage")
                    storage.writeSecurityOperation(
                        SecurityOperation(
                            "lease",
                            DeviceSecurity.stateHash(head),
                            request.toByteArray(),
                        ),
                    )
                    resumeSecurityOperation()
                }
            } catch (e: Exception) {
                if (e is CancellationException) throw e
                report(e)
            }
        }
    }

    /** New phone shows its code before publishing this signed, public-only offer. */
    fun prepareHandoffCode(): Pair<String, ByteArray> {
        val (seed, pub) = Authority.newRecovery()
        return RecoveryCode.encode(seed, security.identity().first.genesis) to pub
    }

    suspend fun offerHandoff(recoveryEd: ByteArray) {
        require(recoveryEd.size == 32)
        changeDeviceMeta(account.deviceId) {
            it.setHandoffRecoveryEd(com.google.protobuf.ByteString.copyFrom(recoveryEd))
        }
    }

    suspend fun handoffAuthority(incoming: String) {
        resumeSecurityOperation()
        syncSecurity()
        val head = security.head()
        if (head.core.authorityID != account.deviceId) throw DeviceSecurity.Failure("untrusted")
        val offer =
            devices[incoming]?.meta?.handoffRecoveryEd?.toByteArray()
                ?: throw DeviceSecurity.Failure("pending")
        if (offer.size != 32) throw CommandException("failed_precondition", "请先在新手机生成并保存恢复码")
        val old =
            security.contentKey(head.core.contentEpoch)
                ?: throw DeviceSecurity.Failure("unavailable")
        publishSecurityStates(
            listOf(
                authorityForeground().let {
                    Authority.handoff(
                        head,
                        incoming,
                        offer,
                        old,
                        Authority.newSecret(),
                        SigningKey(account.deviceSeed),
                        security.signingTime(clock()),
                    )
                },
            ),
        )
    }

    /** The signed chain from the genesis, for a pairing payload. */
    fun chain(): List<DeviceSecurity.State> = security.chain()

    fun chainProofs(): Map<ULong, ByteArray> = security.chainProofs()

    /**
     * A pending recovery the authority phone can veto. The relay tells the signed-in authority
     * phone; an unauthenticated lookup no longer does.
     */
    suspend fun pendingRecovery(): PendingRecoveryInfo? {
        if (authorityDeviceId != account.deviceId) return null
        syncSecurity()
        return if (security.recoveryPending) PendingRecoveryInfo(security.recoveryReadyMs) else null
    }

    /**
     * Signs a newer state with a fresh recovery key but does not publish it yet: the user must save
     * the new code first. The state goes out only in [publishRecovery], so a crash cannot leave the
     * account without a code the user knows.
     */
    suspend fun prepareRecovery(): PreparedRecovery {
        syncSecurity()
        val head = security.head()
        if (head.core.authorityID != account.deviceId)
            throw CommandException("failed_precondition", "只有管理端手机能否决恢复")
        val secret =
            security.contentKey(head.core.contentEpoch)
                ?: throw DeviceSecurity.Failure("unavailable")
        val (seed, pub) = Authority.newRecovery()
        val state =
            authorityForeground().let {
                Authority.veto(
                    head,
                    pub,
                    secret,
                    SigningKey(account.deviceSeed),
                    security.signingTime(clock()),
                )
            }
        val (pin, _) = security.identity()
        return PreparedRecovery(RecoveryCode.encode(seed, pin.genesis), state, seed)
    }

    suspend fun publishRecovery(prepared: PreparedRecovery) {
        resumeSecurityOperation()
        syncSecurity()
        val head = security.head()
        val recoveryEd = SigningKey(prepared.newRecoverySeed).publicKey
        if (head.core.recoveryEd.contentEquals(recoveryEd)) return
        if (!DeviceSecurity.stateHash(head).contentEquals(prepared.state.core.parent))
            throw DeviceSecurity.Failure("stale")
        publishSecurityStates(listOf(prepared.state))
    }

    private suspend fun loadDevices() {
        val version = ++directoryVersion
        try {
            val (_, head) = security.identity()
            val stateHash = DeviceSecurity.stateHash(head)
            val members = head.core.members.associateBy { it.id }
            val listed =
                api.client
                    .listDevices(
                        io.github.lewin671.remoteagent.proto.ListDevicesRequest
                            .getDefaultInstance(),
                        Api.headers(token),
                    )
                    .value()
            val machineRows =
                api.client
                    .listMachines(ListMachinesRequest.getDefaultInstance(), Api.headers(token))
                    .value()
            val metadata = HashMap<String, Envelope>()
            for (d in listed.devicesList) {
                if (d.hasMeta() && d.meta.securityHeader.size() > 0)
                    metadata["device:${d.id}"] = d.meta
            }
            for (m in machineRows.machinesList) {
                if (m.hasMeta() && m.meta.securityHeader.size() > 0)
                    metadata["machine:${m.id}"] = m.meta
            }
            val newDevices = HashMap<String, DeviceView>()
            val newMachines = HashMap(machines)
            for (m in head.core.members) {
                val hint = listed.devicesList.firstOrNull { it.id == m.id }
                var view =
                    DeviceView(
                        m.id,
                        io.github.lewin671.remoteagent.proto.DeviceKind.forNumber(m.kind.toInt()),
                        hint?.createdMs ?: 0L,
                        lastSeenMs = hint?.lastSeenMs ?: 0L,
                    )
                val env = metadata[(if (m.kind == 1u) "machine:" else "device:") + m.id]
                if (env != null) {
                    try {
                        if (m.kind == 2u) {
                            val (h, meta) =
                                openSecureMessage(
                                    keys?.deviceMeta ?: ByteArray(0),
                                    env,
                                    Place(PayloadKind.PAYLOAD_KIND_DEVICE_META, requestId = m.id),
                                    io.github.lewin671.remoteagent.proto.DeviceMeta::parseFrom,
                                )
                            view = view.copy(meta = meta, metaRev = h.seq)
                        } else {
                            val (h, meta) =
                                openSecureMessage(
                                    keys?.meta ?: ByteArray(0),
                                    env,
                                    Place(PayloadKind.PAYLOAD_KIND_MACHINE_META, machineId = m.id),
                                    MachineMeta::parseFrom,
                                )
                            val old = machines[m.id]
                            newMachines[m.id] =
                                if (old == null)
                                    MachineView(m.id, false, 0L, meta = meta, metaRev = h.seq)
                                else old.copy(meta = meta, metaRev = h.seq)
                        }
                    } catch (e: Exception) {
                        if (e is CancellationException) throw e
                        devices[m.id]?.let {
                            view = view.copy(meta = it.meta, metaRev = it.metaRev)
                        }
                        if (m.kind == 1u && !newMachines.containsKey(m.id))
                            newMachines[m.id] = MachineView(m.id, false, 0L)
                    }
                } else if (m.kind == 1u && !newMachines.containsKey(m.id)) {
                    newMachines[m.id] = MachineView(m.id, false, 0L)
                }
                newDevices[m.id] = view
            }
            if (version != directoryVersion) return
            if (!DeviceSecurity.stateHash(security.head()).contentEquals(stateHash))
                throw DeviceSecurity.Failure("stale")
            for ((id, d) in newDevices) if (
                (devices[id]?.metaRev ?: 0L) > d.metaRev &&
                    d.kind == io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT
            )
                throw DeviceSecurity.Failure("stale")
            archiveMachines.clear()
            archiveMachines.addAll(
                security.chain().flatMap { it.core.members }.filter { it.kind == 1u }.map { it.id },
            )
            for ((id, m) in machines) if (id in archiveMachines && id !in members)
                newMachines[id] = m.copy(online = false)
            trustedMembers.clear()
            trustedMembers.putAll(members)
            trustedMetadata.clear()
            trustedMetadata.putAll(metadata)
            devices.clear()
            devices.putAll(newDevices)
            machines.clear()
            machines.putAll(newMachines.filterKeys { it in members || it in archiveMachines })
            authorityDeviceId = head.core.authorityID
            directoryHead = stateHash
            leaseEnd = security.record().leaseExpiration()
            publish()
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            report(e)
        }
        publish()
    }

    private suspend fun putDevice(d: io.github.lewin671.remoteagent.proto.Device) {
        loadDevices()
    }

    /**
     * Names a client device for the whole account, keeping what else its metadata says; a conflict
     * is redone on the stored metadata.
     */
    suspend fun renameDevice(id: String, name: String) = changeDeviceMeta(id) { it.setName(name) }

    /** Says what this client is and runs on, keeping the name it was given. */
    suspend fun describeSelf(platform: String, description: String) =
        changeDeviceMeta(account.deviceId) { it.setPlatform(platform).setDescription(description) }

    private suspend fun changeDeviceMeta(
        id: String,
        change: (io.github.lewin671.remoteagent.proto.DeviceMeta.Builder) -> Unit,
    ) {
        repeat(5) {
            withContext(dispatcher) { loadDevices() }
            val current =
                withContext(dispatcher) { devices[id] }
                    ?: throw CommandException("not_found", "没有这台设备")
            val next =
                (current.meta?.toBuilder()
                        ?: io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder())
                    .also(change)
                    .build()
            try {
                val env =
                    security.seal(
                        DeviceSecurity.Purpose.DEVICE_META,
                        request = id,
                        seq = (current.metaRev + 1).toULong(),
                        plain = next.toByteArray(),
                    )
                val applied =
                    api.client
                        .putDeviceMeta(
                            io.github.lewin671.remoteagent.proto.PutDeviceMetaRequest.newBuilder()
                                .setDeviceId(id)
                                .setExpectedRevision(current.metaRev)
                                .setMeta(env)
                                .build(),
                            Api.headers(currentToken()),
                        )
                        .value()
                        .applied
                if (!applied) return@repeat
                withContext(dispatcher) { loadDevices() }
                return
            } catch (e: DeviceSecurity.Failure) {
                if (e.code != "conflict") throw e
            }
        }
        throw CommandException("aborted", "设备信息正在变化，请重试")
    }

    /**
     * Fixed layout-test values only, on a stopped engine. This never installs a security record,
     * grants, content keys, or any network authority.
     */
    fun showForTest(
        state: RemoteState,
        events: Map<String, List<Event>> = emptyMap(),
        modelLists: Map<Pair<String, Agent>, ListModelsResponse> = emptyMap(),
    ) {
        check(!running) { "showForTest on a started Remote" }
        _state.value = state
        for ((key, response) in modelLists) models["${key.first}/${key.second.number}"] =
            CompletableDeferred(response)
        for ((sid, list) in events) eventFlows
            .getOrPut(sid) { MutableStateFlow(SessionEvents()) }
            .value = SessionEvents(list, loaded = true)
    }

    // --- events ---

    /** A session's events; loads them from the cache and the server on first use. */
    fun events(sid: String): StateFlow<SessionEvents> {
        val flow =
            eventFlows.getOrPut(sid) {
                MutableStateFlow(SessionEvents()).also { f ->
                    scope.launch {
                        f.subscriptionCount.collect { count ->
                            if (count > 0) {
                                scope.launch { loadEvents(sid) }
                                if (liveJobs[sid] == null)
                                    liveJobs[sid] = scope.launch {
                                        while (true) {
                                            if (running) refreshLiveSnapshot(sid)
                                            delay(5000)
                                        }
                                    }
                            } else {
                                liveJobs.remove(sid)?.cancel()
                                liveSnapshots.remove(sid)
                                buffers[sid]?.usedAt = clock()
                                trimHistory()
                            }
                        }
                    }
                }
            }
        scope.launch {
            if (historySweep == null)
                historySweep = scope.launch {
                    while (true) {
                        delay(historyIdleMs.coerceAtLeast(1))
                        trimHistory()
                    }
                }
            buffers[sid]?.usedAt = clock()
            loadEvents(sid)
        }
        return flow
    }

    private fun trimHistory() {
        val idle =
            buffers.entries
                .filter { (sid, _) -> eventFlows[sid]?.subscriptionCount?.value == 0 }
                .sortedBy { it.value.usedAt }
        var bytes = idle.sumOf { it.value.bytes }
        var count = idle.size
        for ((sid, b) in idle) {
            if (
                sessions[sid] == null ||
                    count > historyIdleCount ||
                    bytes > historyIdleBytes ||
                    clock() - b.usedAt >= historyIdleMs
            ) {
                buffers.remove(sid)
                // Keep the small flow identity for consumers about to subscribe;
                // its subscription watcher reloads a reclaimed history.
                eventFlows[sid]?.value = SessionEvents()
                count--
                bytes -= b.bytes
            }
        }
    }

    private suspend fun loadEvents(sid: String) {
        if (buffers[sid] != null || sessions[sid] == null) return
        val b = Buffer().apply { usedAt = clock() }
        buffers[sid] = b
        val cached =
            try {
                cache?.events(sid).orEmpty()
            } catch (e: Exception) {
                report(e)
                emptyList()
            }
        if (buffers[sid] !== b) return
        if (cached.isNotEmpty()) {
            ingest(sid, cached, fillGaps = false, save = false, live = false)
            if (buffers[sid] !== b) return
            // A cached copy that does not open is dropped and the history
            // fetched whole (architecture §10 「后台和内存」): the cache keeps
            // an event as first stored, so the bad one would stay for good.
            if (b.error != null) {
                try {
                    cache?.dropEvents(sid)
                } catch (e: Exception) {
                    report(e)
                }
                if (buffers[sid] !== b) return
                b.events = PagedList.empty()
                b.pending.clear()
                b.verifiedSeqs.clear()
                b.unavailableSeqs.clear()
                b.verifiedEvents = PagedList.empty()
                b.bytes = 0
                b.error = null
                publishEvents(sid, b)
            }
        }
        fetchEvents(sid)
        trimHistory()
    }

    /** Fetches everything after the last contiguous event. */
    private suspend fun fetchEvents(sid: String) {
        val b = buffers[sid] ?: return
        if (sessions[sid] == null || !running) return
        if (b.fetching) {
            b.refetch = true
            return
        }
        b.fetching = true
        val gen = generation
        try {
            do {
                if (!running || gen != generation || buffers[sid] !== b) return
                b.refetch = false
                while (true) {
                    if (!running || gen != generation || buffers[sid] !== b) return
                    val before = b.events.size
                    val res =
                        api.client
                            .getEvents(
                                GetEventsRequest.newBuilder()
                                    .setSessionId(sid)
                                    .setAfterSeq(before.toLong())
                                    .setLimit(500)
                                    .build(),
                                Api.headers(token),
                            )
                            .value()
                    if (!running || gen != generation || buffers[sid] !== b) return
                    ingest(sid, res.eventsList, fillGaps = false, save = true, live = false)
                    if (!running || gen != generation || buffers[sid] !== b) return
                    // An event that does not open stops progress; asking for the
                    // same page again would not help.
                    if (!res.more || b.events.size == before) break
                }
            } while (b.refetch)
            b.loaded = true
            b.fetchError = null
        } catch (e: Exception) {
            if (!running || gen != generation) return
            report(e)
            // Shown on the session with a retry; the next Ready fetches again too.
            b.fetchError = "没能读取历史：" + errorText(e)
        } finally {
            b.fetching = false
            if (b.refetch && running && gen != generation) scope.launch { fetchEvents(sid) }
            publishEvents(sid, b)
        }
    }

    private suspend fun ingest(
        sid: String,
        stored: List<StoredEvent>,
        fillGaps: Boolean,
        save: Boolean,
        live: Boolean = false,
    ) {
        val b = buffers[sid] ?: return
        val s = sessions[sid] ?: return
        val key = sessionKeys[sid] ?: return
        val opened = ArrayList<StoredEvent>()
        for (se in stored) {
            if (ucmp(se.seq, b.events.size.toLong()) <= 0 || b.pending.containsKey(se.seq)) continue
            try {
                val (_, ev) =
                    openSecureMessage(
                        key,
                        se.envelope,
                        Place(
                            PayloadKind.PAYLOAD_KIND_EVENT,
                            machineId = s.machineId,
                            sessionId = sid,
                            seq = se.seq,
                        ),
                        Event::parseFrom,
                        live,
                    )
                if (ev.seq != se.seq || ev.sessionId != sid) throw OpenException("事件的位置被改动")
                if (Header.parseFrom(se.envelope.header).version == DeviceSecurity.VERSION)
                    b.verifiedSeqs.add(se.seq)
                b.pending[se.seq] = ev
                opened += se
                // What is held is the event as opened; its ciphertext may be packed.
                b.bytes += ev.serializedSize + 256L
            } catch (e: RecoveryHistoryUnavailable) {
                if (buffers[sid] !== b) return
                if (ucmp(se.seq, b.events.size.toLong()) <= 0 || b.pending.containsKey(se.seq))
                    continue
                b.pending[se.seq] = Event.newBuilder().setSeq(se.seq).setSessionId(sid).build()
                b.unavailableSeqs.add(se.seq)
                opened += se
                b.bytes += 256L
            } catch (e: Exception) {
                report(e)
                b.error = "有一条记录读不出来，历史停在这里，后面的内容和状态可能不全"
                break
            }
        }
        val added = ArrayList<Event>()
        while (true) {
            val next = (b.events.size + added.size).toLong() + 1
            val ev = b.pending.remove(next) ?: break
            added += ev
        }
        b.events = b.events.append(added)
        b.verifiedEvents = b.verifiedEvents.append(added.filter { it.seq in b.verifiedSeqs })
        if (save && opened.isNotEmpty()) {
            try {
                cache?.putEvents(sid, opened)
            } catch (e: Exception) {
                report(e)
            }
        }
        if (buffers[sid] !== b) return
        // The cache write suspended: newer metadata may have arrived, or the
        // session gone. Only the event progress is ours to update.
        sessions[sid]?.let { cur ->
            sessions[sid] = cur.copy(lastSeq = umax(cur.lastSeq, b.events.size.toLong()))
        }
        if (fillGaps && b.pending.isNotEmpty()) scope.launch { fetchEvents(sid) }
        publishEvents(sid, b)
        publish()
        trimHistory()
    }

    private fun publishEvents(sid: String, b: Buffer) {
        if (buffers[sid] !== b) return
        eventFlows.getOrPut(sid) { MutableStateFlow(SessionEvents()) }.value =
            SessionEvents(
                b.events,
                b.loaded,
                b.error,
                b.fetchError,
                liveSnapshots[sid],
                legacySessions.contains(sid),
                liveErrors[sid]
                    ?: if (sessions[sid]?.checkpointStatus == "waiting") "等待所属电脑更新当前状态" else null,
                b.verifiedEvents,
                b.unavailableSeqs.size,
            )
    }

    /** Asks for missing events again, e.g. after one failed to open. */
    fun retryEvents(sid: String) {
        scope.launch {
            buffers[sid]?.error = null
            fetchEvents(sid)
        }
    }

    // --- commands ---

    /** Seals a command. The header names no session for machine commands. */
    fun prepare(machineId: String, sessionId: String, cmd: Command): PreparedCommand {
        val requestId = newRequestId()
        val deadline = clock() + COMMAND_TTL_MS
        // Signed by this device, so a daemon can tell who sent it
        // (architecture §9).
        val versioned = cmd.toBuilder().setProtocolVersion(PROTOCOL_VERSION).build()
        val observed =
            if (isObservedAction(versioned) && observedSeq(versioned) == 0L)
                observeAction(versioned, eventFlows[sessionId]?.value?.live)
            else versioned
        return PreparedCommand(
            machineId,
            sessionId,
            requestId,
            deadline,
            Envelope.getDefaultInstance(),
            observed,
        )
    }

    /** Sends a command about a session. */
    suspend fun command(sid: String, cmd: Command): CommandResult {
        val s =
            withContext(dispatcher) { sessions[sid] } ?: throw CommandException("not_found", "未知会话")
        var actual = cmd
        if (
            cmd.kindCase in
                setOf(
                    Command.KindCase.RESOLVE_APPROVAL,
                    Command.KindCase.ANSWER_QUESTION,
                    Command.KindCase.ANSWER_PLAN,
                )
        ) {
            val shown =
                liveSnapshots[sid] ?: throw CommandException("failed_precondition", "当前请求尚未由电脑核实")
            val approval = cmd.kindCase == Command.KindCase.RESOLVE_APPROVAL
            val plan = cmd.kindCase == Command.KindCase.ANSWER_PLAN
            val id =
                if (approval) cmd.resolveApproval.approvalId
                else if (!plan) cmd.answerQuestion.questionId else ""
            val displayed =
                shown.actionsList.firstOrNull {
                    if (approval) it.hasApprovalRequested() && it.approvalRequested.approvalId == id
                    else it.hasQuestionAsked() && it.questionAsked.questionId == id
                }
            val seq = if (plan) shown.planEventSeq else displayed?.seq ?: 0L
            if (seq == 0L) throw CommandException("failed_precondition", "请求已不可操作")
            refreshLiveSnapshot(sid)
            val fresh =
                liveSnapshots[sid] ?: throw CommandException("failed_precondition", "无法核实当前请求")
            if (plan) {
                if (fresh.planEventSeq != seq || !fresh.planScreenHash.equals(shown.planScreenHash))
                    throw CommandException("failed_precondition", "计划画面已变化，请重新核对")
                actual =
                    cmd.toBuilder()
                        .setAnswerPlan(
                            cmd.answerPlan
                                .toBuilder()
                                .setObservedEventSeq(seq)
                                .setObservedScreenHash(shown.planScreenHash),
                        )
                        .build()
            } else {
                val current =
                    fresh.actionsList.firstOrNull {
                        if (approval)
                            it.hasApprovalRequested() && it.approvalRequested.approvalId == id
                        else it.hasQuestionAsked() && it.questionAsked.questionId == id
                    }
                if (current == null || current != displayed)
                    throw CommandException("failed_precondition", "当前请求已变化，请重新核对")
                actual =
                    if (approval)
                        cmd.toBuilder()
                            .setResolveApproval(
                                cmd.resolveApproval.toBuilder().setObservedEventSeq(seq),
                            )
                            .build()
                    else
                        cmd.toBuilder()
                            .setAnswerQuestion(
                                cmd.answerQuestion.toBuilder().setObservedEventSeq(seq),
                            )
                            .build()
            }
        }
        return forward(prepare(s.machineId, sid, actual))
    }

    private fun isObservedAction(cmd: Command) =
        cmd.kindCase in
            setOf(
                Command.KindCase.RESOLVE_APPROVAL,
                Command.KindCase.ANSWER_QUESTION,
                Command.KindCase.ANSWER_PLAN,
            )

    private fun observedSeq(cmd: Command): Long =
        when (cmd.kindCase) {
            Command.KindCase.RESOLVE_APPROVAL -> cmd.resolveApproval.observedEventSeq
            Command.KindCase.ANSWER_QUESTION -> cmd.answerQuestion.observedEventSeq
            Command.KindCase.ANSWER_PLAN -> cmd.answerPlan.observedEventSeq
            else -> 0L
        }

    private fun observeAction(
        cmd: Command,
        snapshot: io.github.lewin671.remoteagent.proto.LiveSnapshot?,
    ): Command {
        if (!isObservedAction(cmd)) return cmd
        val fresh = snapshot ?: throw CommandException("failed_precondition", "当前请求尚未由电脑核实")
        return when (cmd.kindCase) {
            Command.KindCase.RESOLVE_APPROVAL -> {
                val event =
                    fresh.actionsList.firstOrNull {
                        it.hasApprovalRequested() &&
                            it.approvalRequested.approvalId == cmd.resolveApproval.approvalId
                    } ?: throw DeviceSecurity.Failure("stale")
                cmd.toBuilder()
                    .setResolveApproval(
                        cmd.resolveApproval.toBuilder().setObservedEventSeq(event.seq),
                    )
                    .build()
            }
            Command.KindCase.ANSWER_QUESTION -> {
                val event =
                    fresh.actionsList.firstOrNull {
                        it.hasQuestionAsked() &&
                            it.questionAsked.questionId == cmd.answerQuestion.questionId
                    } ?: throw DeviceSecurity.Failure("stale")
                cmd.toBuilder()
                    .setAnswerQuestion(
                        cmd.answerQuestion.toBuilder().setObservedEventSeq(event.seq),
                    )
                    .build()
            }
            Command.KindCase.ANSWER_PLAN -> {
                if (fresh.planEventSeq == 0L || fresh.planScreenHash.size() != 32)
                    throw DeviceSecurity.Failure("stale")
                cmd.toBuilder()
                    .setAnswerPlan(
                        cmd.answerPlan
                            .toBuilder()
                            .setObservedEventSeq(fresh.planEventSeq)
                            .setObservedScreenHash(fresh.planScreenHash),
                    )
                    .build()
            }
            else -> cmd
        }
    }

    private suspend fun fetchLiveSnapshot(
        sid: String,
    ): io.github.lewin671.remoteagent.proto.LiveSnapshot {
        val session =
            withContext(dispatcher) { sessions[sid] } ?: throw CommandException("not_found", "未知会话")
        val result =
            forward(
                prepare(
                    session.machineId,
                    sid,
                    Command.newBuilder()
                        .setGetLiveSnapshot(
                            io.github.lewin671.remoteagent.proto.GetLiveSnapshotRequest.newBuilder()
                                .setSessionId(sid),
                        )
                        .build(),
                ),
            )
        if (!result.hasLiveSnapshot() || result.liveSnapshot.session.id != sid)
            throw DeviceSecurity.Failure()
        return result.liveSnapshot
    }

    private suspend fun refreshLiveSnapshot(sid: String) {
        try {
            val fresh = fetchLiveSnapshot(sid)
            if (!running) return
            liveSnapshots[sid] = fresh
            liveErrors.remove(sid)
            buffers[sid]?.let { publishEvents(sid, it) }
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            liveSnapshots.remove(sid)
            liveErrors[sid] = "无法核实电脑上的待处理请求，请重试或在电脑终端处理"
            buffers[sid]?.let { publishEvents(sid, it) }
        }
    }

    /** Sends a command about a machine, such as starting a session. */
    suspend fun machineCommand(machineId: String, cmd: Command): CommandResult =
        forward(prepare(machineId, "", cmd))

    /**
     * How long one round trip to the server takes now, in ms: a request that does nothing there
     * (连接检测). Throws when no answer comes in [timeoutMs].
     */
    suspend fun pingServer(timeoutMs: Long = 8_000): Long =
        withContext(Dispatchers.IO) {
            val call =
                http
                    .newBuilder()
                    .callTimeout(timeoutMs, java.util.concurrent.TimeUnit.MILLISECONDS)
                    .build()
                    .newCall(
                        okhttp3.Request.Builder()
                            .url(api.server.trimEnd('/') + "/healthz")
                            .header("Cache-Control", "no-store")
                            .build(),
                    )
            val started = System.nanoTime()
            try {
                call.execute().use { res ->
                    res.body?.bytes()
                    if (!res.isSuccessful) throw java.io.IOException("服务器返回 ${res.code}")
                }
            } catch (e: java.io.InterruptedIOException) {
                throw java.io.IOException("没有回应", e)
            }
            (System.nanoTime() - started) / 1_000_000
        }

    /**
     * How long one round trip to a computer takes, through the server and back: a sealed command
     * that only reads its settings.
     */
    suspend fun pingMachine(machineId: String, timeoutMs: Long = 15_000): Long {
        val started = System.nanoTime()
        try {
            withTimeout(timeoutMs) {
                machineCommand(
                    machineId,
                    Command.newBuilder()
                        .setGetSettings(
                            io.github.lewin671.remoteagent.proto.GetSettingsRequest
                                .getDefaultInstance(),
                        )
                        .build(),
                )
            }
        } catch (e: kotlinx.coroutines.TimeoutCancellationException) {
            throw java.io.IOException("没有回应", e)
        }
        return (System.nanoTime() - started) / 1_000_000
    }

    /**
     * Forwards a prepared command and returns the opened result. Transport failures are retried
     * with the same request ID, which the daemon runs at most once; when the outcome stays unknown
     * the error says so.
     */
    suspend fun authorize(p: PreparedCommand): Envelope {
        try {
            return authorizeIntent(p)
        } catch (e: Exception) {
            if (e is CancellationException) throw e
            throw CommandException("authorization_unavailable", "安全授权或当前请求尚未核实，操作没有发出；请重试")
        }
    }

    private suspend fun authorizeIntent(p: PreparedCommand): Envelope =
        p.sealMutex.withLock {
            val head = security.head()
            val previous = p.authorizedEnvelope
            if (previous != null) {
                val h = DeviceSecurity.parseHeader(previous.securityHeader.toByteArray())
                // A membership change rotates the content key; the same request
                // (ID and deadline) is resealed under the current epoch.
                if (h.contentEpoch == head.core.contentEpoch) return@withLock previous
            }
            val env =
                if (p.command != null) {
                    // A retry keeps the exact original observation. The executor's
                    // durable request log settles it before considering a new action.
                    if (previous == null && isObservedAction(p.command)) {
                        val fresh = fetchLiveSnapshot(p.sessionId)
                        if (observeAction(p.command, fresh) != p.command)
                            throw CommandException("failed_precondition", "当前请求已变化，请重新核对")
                    }
                    if (serverAhead || security.clockSkew)
                        throw CommandException("failed_precondition", "手机时钟比服务器快，远程控制暂停；请校准系统时间")
                    if (!Authority.leaseValid(security.record(), clock()))
                        throw CommandException("failed_precondition", "远程控制已暂停：打开手机 App 上续期后再试")
                    security.seal(
                        DeviceSecurity.Purpose.COMMAND,
                        p.machineId,
                        p.sessionId,
                        p.requestId,
                        deadline = p.deadlineMs.toULong(),
                        plain = p.command.toByteArray(),
                    )
                } else {
                    DeviceSecurity.fromEnvelope(p.envelope)
                    p.envelope
                }
            p.authorizedEnvelope = env
            env
        }

    suspend fun forward(p: PreparedCommand, retryOfUnknown: Boolean = false): CommandResult {
        // A machine of another protocol version would refuse it: say which
        // side to update instead of sending.
        protocolMismatch(state.value.machines[p.machineId])?.let {
            throw CommandException("failed_precondition", it)
        }
        if (!retryOfUnknown && state.value.machines[p.machineId]?.online == false)
            throw CommandException(CommandException.UNAVAILABLE, "电脑不在线，指令没有发出")
        // A command whose outcome is unknown is sent even when expired: the
        // daemon may still have its result in its log.
        if (!retryOfUnknown && clock() >= p.deadlineMs)
            throw CommandException(CommandException.EXPIRED, "这条指令已过期，没有发出")
        var attempt = 0
        // Once a try may have reached the daemon, a later try that surely did
        // not (machine gone, network down) says nothing about that one: the
        // outcome stays unknown.
        var mayHaveRun = retryOfUnknown
        fun notSent(code: String, text: String, later: String) =
            if (mayHaveRun)
                CommandException(CommandException.UNKNOWN, "不确定电脑有没有执行：请求可能已经送到，之后$later")
            else CommandException(code, text)
        var waiting = false
        // A previous try may have got the command to the daemon: a later
        // "machine offline" then says nothing about it.
        try {
            while (true) {
                waiting = false
                try {
                    if (token.isEmpty()) token = relogin(mayHaveRun)
                    kotlinx.coroutines.currentCoroutineContext().ensureActive()
                    val authorized = authorize(p)
                    kotlinx.coroutines.currentCoroutineContext().ensureActive()
                    waiting = true
                    mayHaveRun = true
                    val res =
                        api.client
                            .forward(
                                ForwardRequest.newBuilder()
                                    .setMachineId(p.machineId)
                                    .setCommand(authorized)
                                    .build(),
                                Api.headers(token),
                            )
                            .value()
                    val (_, msg) =
                        try {
                            openSecureMessage(
                                keys?.command ?: ByteArray(0),
                                res.result,
                                Place(
                                    PayloadKind.PAYLOAD_KIND_COMMAND_RESULT,
                                    machineId = p.machineId,
                                    sessionId = p.sessionId,
                                    requestId = p.requestId,
                                ),
                                CommandResult::parseFrom,
                            )
                        } catch (e: Exception) {
                            // A result that does not open says nothing about whether
                            // the command ran; retrying the same request asks again.
                            throw CommandException(CommandException.UNKNOWN, "电脑的回复打不开，不确定指令有没有执行")
                        }
                    if (msg.errorCode.isNotEmpty())
                        throw CommandException(
                            msg.errorCode,
                            msg.errorMessage.ifEmpty { msg.errorCode },
                            fromDaemon = true,
                        )
                    return msg
                } catch (ce: ConnectException) {
                    waiting = false
                    val cause = ce.exception
                    if (
                        cause is java.net.UnknownHostException || cause is java.net.ConnectException
                    ) {
                        throw notSent("network", "网络不通，指令没有发出", "网络断了")
                    }
                    if (ce.code == Code.UNAUTHENTICATED && attempt < 3) {
                        attempt++
                        token = relogin(mayHaveRun)
                        continue
                    }
                    // ABORTED: the daemon could not seal a result, as on the web.
                    val unknown =
                        ce.code in
                            setOf(
                                Code.DEADLINE_EXCEEDED,
                                Code.UNKNOWN,
                                Code.INTERNAL_ERROR,
                                Code.CANCELED,
                                Code.ABORTED,
                            )
                    if (unknown) {
                        mayHaveRun = true
                    }
                    if (unknown && attempt < 3 && clock() + 2000 < p.deadlineMs) {
                        attempt++
                        delay(1000L * attempt)
                        continue
                    }
                    // An unsigned relay status cannot settle a request once the
                    // ciphertext may have reached it. Retry the original ID.
                    if (unknown || ce.code == Code.UNAVAILABLE) {
                        throw CommandException(
                            CommandException.UNKNOWN,
                            "不确定电脑有没有执行：${errorText(ce)}",
                        )
                    }
                    throw CommandException(CommandException.UNKNOWN, "中继未提供可验证的执行结果，请重试同一请求")
                }
            }
        } catch (e: CancellationException) {
            if (waiting || mayHaveRun)
                throw CommandException(CommandException.UNKNOWN, "不确定电脑有没有执行：已停止等待回复。重试会发送同一个请求")
            throw e
        } catch (e: Exception) {
            // Only an authenticated daemon answer settles a dispatched request.
            if (e is CommandException && e.fromDaemon) throw e
            if (mayHaveRun) throw CommandException(CommandException.UNKNOWN, "未收到可验证的执行结果，请重试同一请求")
            throw e
        }
    }

    /**
     * Logs in again during a forward. When an earlier try may already have run the command, a
     * failure here is no answer about it: it stays unknown.
     */
    private suspend fun relogin(mayHaveRun: Boolean): String =
        try {
            signIn()
        } catch (e: Exception) {
            if (!mayHaveRun) throw e
            throw CommandException(
                CommandException.UNKNOWN,
                "不确定电脑有没有执行：请求可能已经送到，之后登录失败（${errorText(e)}）",
            )
        }

    /** The models a machine's CLI offers, refreshed when a picker opens. */
    suspend fun listModels(
        machineId: String,
        agent: Agent = Agent.AGENT_UNSPECIFIED,
        fresh: Boolean = false,
    ): ListModelsResponse {
        val d =
            withContext(dispatcher) {
                val k = "$machineId/${agent.number}"
                models[k]?.takeUnless {
                    it.isCompleted && (fresh || it.getCompletionExceptionOrNull() != null)
                }
                    ?: scope
                        .async {
                            val r =
                                machineCommand(
                                    machineId,
                                    Command.newBuilder()
                                        .setListModels(
                                            ListModelsRequest.newBuilder().setAgent(agent),
                                        )
                                        .build(),
                                )
                            if (!r.hasListModels()) throw CommandException("internal", "意外的回复")
                            r.listModels
                        }
                        .also { models[k] = it }
            }
        return d.await()
    }

    /**
     * Removes a device by publishing a newer signed state. The server revokes the device and, for a
     * machine, deletes its relay copy; other devices drop it on their next 60 s pull.
     */
    suspend fun removeDevice(deviceId: String) {
        if (!cancelPairDeliveryForRemoval(deviceId)) resumeSecurityOperation()
        syncSecurity()
        val head = security.head()
        if (head.core.authorityID != account.deviceId)
            throw CommandException("failed_precondition", "请在手机 App 上删除设备")
        if (deviceId == head.core.authorityID)
            throw CommandException("failed_precondition", "管理端手机不能被普通删除")
        if (head.core.member(deviceId) == null) return
        val old =
            security.contentKey(head.core.contentEpoch)
                ?: throw DeviceSecurity.Failure("unavailable")
        val next =
            authorityForeground().let {
                Authority.remove(
                    head,
                    listOf(deviceId),
                    old,
                    Authority.newSecret(),
                    SigningKey(account.deviceSeed),
                    security.signingTime(clock()),
                )
            }
        publishSecurityStates(listOf(next))
        withContext(dispatcher) { loadDevices() }
    }

    suspend fun deleteMachineRecords(machineId: String) {
        if (security.head().core.authorityID != account.deviceId)
            throw DeviceSecurity.Failure("untrusted")
        api.client
            .deleteMachineRecords(
                DeleteMachineRecordsRequest.newBuilder().setMachineId(machineId).build(),
                Api.headers(currentToken()),
            )
            .value()
        withContext(dispatcher) { refresh() }
    }

    /** The bearer token of the current login, for services used outside this class. */
    suspend fun currentToken(): String {
        if (token.isEmpty()) token = signIn()
        return token
    }

    // --- images ---

    /** A display copy fetched for viewing: its pixels, or why there are none. */
    sealed interface ImageResult {
        class Loaded(
            val payload: io.github.lewin671.remoteagent.proto.ImagePayload,
            val unverifiedLegacy: Boolean = false,
        ) : ImageResult

        /** Kept past its retention: only the preview in the event is left. */
        data object Expired : ImageResult

        /** The server never had it. */
        data object Missing : ImageResult

        /** The bytes are not the image they claim to be, or do not open. */
        data class Corrupt(val why: String) : ImageResult
    }

    /**
     * An image's display copy, from the cache of envelopes or the server. Only the envelope is
     * cached; it is opened and checked in memory each time: bound to this machine, session and
     * image, and data that starts with its format's signature.
     */
    suspend fun image(sid: String, imageId: String): ImageResult {
        val s =
            withContext(dispatcher) { sessions[sid] } ?: throw CommandException("not_found", "未知会话")
        val key = sessionKeys[sid] ?: throw CommandException("not_found", "未知会话")
        val cacheKey = "$sid/$imageId"
        var env =
            try {
                cache?.image(cacheKey)?.let { Envelope.parseFrom(it) }
            } catch (e: Exception) {
                null
            }
        if (env == null) {
            val r =
                try {
                    api.client
                        .getImage(
                            io.github.lewin671.remoteagent.proto.GetImageRequest.newBuilder()
                                .setSessionId(sid)
                                .setImageId(imageId)
                                .build(),
                            Api.headers(currentToken()),
                        )
                        .value()
                } catch (e: ConnectException) {
                    if (e.code == Code.NOT_FOUND) return ImageResult.Missing
                    throw e
                }
            if (r.expired || !r.hasImage()) return ImageResult.Expired
            env = r.image
        }
        val payload =
            try {
                openSecureMessage(
                        key,
                        env,
                        Place(
                            PayloadKind.PAYLOAD_KIND_IMAGE,
                            machineId = s.machineId,
                            sessionId = sid,
                            requestId = imageId,
                        ),
                        io.github.lewin671.remoteagent.proto.ImagePayload::parseFrom,
                        live = false,
                    )
                    .second
            } catch (e: OpenException) {
                return ImageResult.Corrupt(e.message ?: "打不开")
            }
        if (detectFormat(payload.data.toByteArray()) != payload.format)
            return ImageResult.Corrupt("图片内容和格式对不上")
        try {
            cache?.putImage(cacheKey, env.toByteArray())
        } catch (e: Exception) {
            report(e)
        }
        return ImageResult.Loaded(payload, Header.parseFrom(env.header).version == 1)
    }

    /** The image upload commands on one machine. */
    fun uploadChannel(machineId: String): UploadChannel =
        object : UploadChannel {
            private suspend fun run(b: Command.Builder.() -> Unit): CommandResult =
                machineCommand(machineId, Command.newBuilder().apply(b).build())

            override suspend fun begin(
                transferId: String,
                format: io.github.lewin671.remoteagent.proto.ImageFormat,
                total: Long,
                sha256: ByteArray,
            ) = run {
                beginImageUpload =
                    io.github.lewin671.remoteagent.proto.BeginImageUploadRequest.newBuilder()
                        .setTransferId(transferId)
                        .setFormat(format)
                        .setTotalBytes(total)
                        .setSha256(com.google.protobuf.ByteString.copyFrom(sha256))
                        .build()
            }
                .beginImageUpload
                .status

            override suspend fun put(transferId: String, offset: Long, data: ByteArray) = run {
                putImageChunk =
                    io.github.lewin671.remoteagent.proto.PutImageChunkRequest.newBuilder()
                        .setTransferId(transferId)
                        .setOffset(offset)
                        .setData(com.google.protobuf.ByteString.copyFrom(data))
                        .build()
            }
                .putImageChunk
                .status

            override suspend fun status(transferId: String) = run {
                getImageUploadStatus =
                    io.github.lewin671.remoteagent.proto.GetImageUploadStatusRequest.newBuilder()
                        .setTransferId(transferId)
                        .build()
            }
                .getImageUploadStatus
                .status

            override suspend fun complete(transferId: String) = run {
                completeImageUpload =
                    io.github.lewin671.remoteagent.proto.CompleteImageUploadRequest.newBuilder()
                        .setTransferId(transferId)
                        .build()
            }
                .completeImageUpload
                .status
        }

    // --- terminal ---

    /**
     * Joins a session's terminal stream. Frames are sealed with the session key and numbered; each
     * side refuses frames out of order.
     */
    suspend fun openTerminal(sid: String, listener: TermListener): TermConn {
        val s =
            withContext(dispatcher) { sessions[sid] } ?: throw CommandException("not_found", "未知会话")
        val key = sessionKeys[sid] ?: throw CommandException("not_found", "未知会话")
        val tok = currentToken()
        val streamId = newRequestId()
        val opened = CompletableDeferred<Unit>()
        val closed = java.util.concurrent.atomic.AtomicBoolean(false)
        fun finish(reason: String) {
            if (closed.compareAndSet(false, true)) listener.onClose(reason)
        }
        var last = 0L
        val incoming = kotlinx.coroutines.channels.Channel<ByteArray>(64)
        data class QueuedInput(val frame: TerminalFrame, val deadline: Long)
        val outgoing = kotlinx.coroutines.channels.Channel<QueuedInput>(64)
        val queuedBytes = java.util.concurrent.atomic.AtomicLong(0)
        val reader = scope.launch {
            try {
                for (raw in incoming) {
                    try {
                        val env = Envelope.parseFrom(raw)
                        val (h, frame) =
                            openSecureMessage(
                                key,
                                env,
                                Place(
                                    PayloadKind.PAYLOAD_KIND_TERMINAL_OUTPUT,
                                    machineId = s.machineId,
                                    sessionId = sid,
                                    requestId = streamId,
                                ),
                                TerminalFrame::parseFrom,
                            )
                        if (ucmp(h.seq, last) <= 0) throw OpenException("终端帧顺序不对")
                        last = h.seq
                        listener.onFrame(frame)
                    } finally {
                        queuedBytes.addAndGet(-raw.size.toLong())
                    }
                }
            } catch (e: Exception) {
                finish(errorText(e))
            }
        }
        val url = account.server.replaceFirst(Regex("^http"), "ws") + "/v1/term"
        val ws =
            streamHttp.newWebSocket(
                Request.Builder().url(url).build(),
                object : WebSocketListener() {
                    override fun onOpen(webSocket: WebSocket, response: Response) {
                        val hello =
                            TermHello.newBuilder()
                                .setToken(tok)
                                .setStreamId(streamId)
                                .setMachineId(s.machineId)
                                .build()
                        webSocket.send(hello.toByteArray().toByteString())
                        opened.complete(Unit)
                    }

                    override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                        if (closed.get()) return
                        val raw = bytes.toByteArray()
                        if (
                            raw.size > 1 shl 20 ||
                                queuedBytes.addAndGet(raw.size.toLong()) > 2L * 1024 * 1024 ||
                                incoming.trySend(raw).isFailure
                        ) {
                            finish("终端输出积压，已停止接收")
                            webSocket.cancel()
                            incoming.close()
                        }
                    }

                    override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                        webSocket.close(1000, null)
                        finish("终端连接断开")
                    }

                    override fun onClosed(webSocket: WebSocket, code: Int, reason: String) =
                        finish("终端连接断开")

                    override fun onFailure(
                        webSocket: WebSocket,
                        t: Throwable,
                        response: Response?,
                    ) {
                        opened.completeExceptionally(Exception("连不上终端"))
                        finish("终端连接断开：" + errorText(t))
                    }
                },
            )
        try {
            withTimeout(20_000) { opened.await() }
            command(
                sid,
                Command.newBuilder()
                    .setOpenTerminal(
                        io.github.lewin671.remoteagent.proto.OpenTerminalRequest.newBuilder()
                            .setSessionId(sid)
                            .setStreamId(streamId),
                    )
                    .build(),
            )
        } catch (e: Exception) {
            closed.set(true)
            ws.cancel()
            throw e
        }
        var seq = 0L
        val writer = scope.launch {
            try {
                for (item in outgoing) {
                    val frame = item.frame
                    if (closed.get()) break
                    seq++
                    val env =
                        sealSecureMessage(
                            key,
                            Header.newBuilder()
                                .setKind(PayloadKind.PAYLOAD_KIND_TERMINAL_INPUT)
                                .setMachineId(s.machineId)
                                .setSessionId(sid)
                                .setRequestId(streamId)
                                .setSeq(seq)
                                .setDeadlineMs(item.deadline)
                                .setSender(account.deviceId)
                                .build(),
                            frame,
                        )
                    if (!closed.get()) ws.send(env.toByteArray().toByteString())
                }
            } catch (e: Exception) {
                finish(errorText(e))
                ws.cancel()
            }
        }
        return TermConn(
            ws,
            sendFrame = { frame ->
                if (
                    frame.serializedSize > 64 * 1024 ||
                        outgoing.trySend(QueuedInput(frame, clock() + 60_000)).isFailure
                ) {
                    finish("终端输入等待授权，已暂停")
                    ws.cancel()
                }
            },
            closeLocal = {
                closed.set(true)
                incoming.close()
                outgoing.close()
                reader.cancel()
                writer.cancel()
            },
        )
    }

    private fun report(e: Throwable) {
        // Only the error text: never content or keys.
        log("remote-agent: " + errorText(e))
        if (System.getenv("RA_DEBUG_STACK") != null) e.printStackTrace()
        error = errorText(e)
    }

    private fun projection(h: DeviceSecurity.Header): Header =
        Header.newBuilder()
            .setVersion(DeviceSecurity.VERSION)
            .setKindValue(h.kind.toInt())
            .setMachineId(h.machine)
            .setSessionId(h.session)
            .setRequestId(h.request)
            .setSeq(h.seq.toLong())
            .setDeadlineMs(h.deadline.toLong())
            .setSender(h.sender)
            .setCompressionValue(h.compression.toInt())
            .build()

    private fun historicalPlace(want: Place, header: Header, historical: Boolean): Place {
        if (
            historical &&
                want.kind in
                    setOf(PayloadKind.PAYLOAD_KIND_EVENT, PayloadKind.PAYLOAD_KIND_IMAGE) &&
                header.machineId != want.machineId &&
                header.machineId in
                    machineReplacementOrigins(
                        security.record(),
                        want.machineId,
                        sessions[want.sessionId]?.info?.machineReplacementsList.orEmpty(),
                    )
        )
            return want.copy(machineId = header.machineId)
        return want
    }

    private suspend fun <T : com.google.protobuf.MessageLite> openSecureMessage(
        legacyKey: ByteArray,
        env: Envelope,
        want: Place,
        parse: (ByteArray) -> T,
        live: Boolean = true,
    ): Pair<Header, T> {
        val history =
            !live ||
                want.kind in
                    setOf(
                        PayloadKind.PAYLOAD_KIND_ACCOUNT_PREFS,
                        PayloadKind.PAYLOAD_KIND_DEVICE_META,
                        PayloadKind.PAYLOAD_KIND_MACHINE_META,
                        PayloadKind.PAYLOAD_KIND_SESSION_META,
                        PayloadKind.PAYLOAD_KIND_IMAGE,
                    )
        val route = Header.parseFrom(env.header)
        if (
            want.kind in setOf(PayloadKind.PAYLOAD_KIND_EVENT, PayloadKind.PAYLOAD_KIND_IMAGE) &&
                route.version == 1
        ) {
            legacySessions.add(want.sessionId)
            return Envelopes.openMessage(
                legacyKey,
                env,
                historicalPlace(want, route, history),
                parse,
            )
        }
        val h = DeviceSecurity.parseHeader(env.securityHeader.toByteArray())
        val seed = if (h.purpose.sessionKey) sessionKeys[h.session] else null
        val (header, m) =
            try {
                security.openMessage(
                    env,
                    history,
                    seed,
                    parse,
                )
            } catch (e: RecoveryHistoryUnavailable) {
                Envelopes.expect(
                    projection(e.header),
                    historicalPlace(
                        want,
                        projection(e.header),
                        history,
                    ),
                )
                throw e
            }
        val projected = projection(header)
        Envelopes.expect(
            projected,
            historicalPlace(want, projected, history),
        )
        return projected to m
    }

    private suspend fun sealSecureMessage(
        legacyKey: ByteArray,
        h: Header,
        m: com.google.protobuf.MessageLite,
    ): Envelope {
        var purpose =
            DeviceSecurity.Purpose.entries.firstOrNull { it.value == h.kindValue.toUInt() }
                ?: throw DeviceSecurity.Failure()
        if (m is TerminalFrame) {
            purpose =
                when (m.kindCase) {
                    TerminalFrame.KindCase.INPUT -> DeviceSecurity.Purpose.TERMINAL_INPUT
                    TerminalFrame.KindCase.RESIZE -> DeviceSecurity.Purpose.TERMINAL_RESIZE
                    TerminalFrame.KindCase.HISTORY_REQUEST ->
                        DeviceSecurity.Purpose.TERMINAL_HISTORY
                    else -> purpose
                }
        }
        val seed = if (purpose.sessionKey) sessionKeys[h.sessionId] else null
        return security.seal(
            purpose,
            h.machineId,
            h.sessionId,
            h.requestId,
            h.seq.toULong(),
            h.deadlineMs.toULong(),
            m.toByteArray(),
            seed,
        )
    }
}
