// Remote keeps the app in sync with the server, as web/src/remote.ts does:
// it logs in, holds the decrypted machines, sessions and events, follows the
// push stream, and sends sealed commands. All state changes run one at a
// time on a single-threaded dispatcher, so they interleave only where they
// suspend, like the page's event loop.
package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CommandResult
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.ForwardRequest
import io.github.lewin671.remoteagent.proto.GetEventsRequest
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.ListMachinesRequest
import io.github.lewin671.remoteagent.proto.ListModelsResponse
import io.github.lewin671.remoteagent.proto.ListRemoteSessionsRequest
import io.github.lewin671.remoteagent.proto.LoginResponse
import io.github.lewin671.remoteagent.proto.Machine
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.PeerFrame
import io.github.lewin671.remoteagent.proto.RemoteSession
import io.github.lewin671.remoteagent.proto.RemoveDeviceRequest
import io.github.lewin671.remoteagent.proto.ServerFrame
import io.github.lewin671.remoteagent.proto.SessionCursor
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.StoredEvent
import io.github.lewin671.remoteagent.proto.StreamHello
import io.github.lewin671.remoteagent.proto.TermHello
import io.github.lewin671.remoteagent.proto.TerminalFrame
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ListModelsRequest
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
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
import java.util.TreeMap
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeUnit
import kotlin.random.Random

enum class ConnState { CONNECTING, ONLINE, OFFLINE }

/**
 * Which stretch of the way to the computer is down, the nearest one only
 * (client-v30 「断线」): past it nothing is known. WAITING is a connection
 * being made or just lost, which mostly comes back by itself.
 */
enum class Link { ONLINE, WAITING, NO_NETWORK, NO_SERVER }

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
    val metaRev: Long = 0,
    val lastSeq: Long = 0,
    val updatedMs: Long = 0,
    /**
     * When the session ended as the server lists it (RemoteSession.ended_ms),
     * 0 while it has not. It only places the session among the pages of the
     * listing; what is shown and sorted by comes from [info].
     */
    val listedEndedMs: Long = 0,
)

/** A place in the server's listing of ended sessions: the latest ended first, by session ID where those are equal. */
data class SessionPlace(val endedMs: Long, val sessionId: String)

/** Whether a session ended at [endedMs] comes after [place] in the listing; one that has not ended is never there. */
fun afterPlace(endedMs: Long, sessionId: String, place: SessionPlace): Boolean =
    endedMs != 0L && (endedMs < place.endedMs || (endedMs == place.endedMs && sessionId > place.sessionId))

/** Ended sessions asked for per page of the listing. */
const val SESSION_PAGE = 50

/** How long after the lists could not be fetched they are asked for again; twice as long each time, up to half a minute. */
private const val LIST_RETRY_MS = 2_000L

data class RemoteState(
    val machines: Map<String, MachineView> = emptyMap(),
    val sessions: Map<String, SessionView> = emptyMap(),
    val conn: ConnState = ConnState.CONNECTING,
    val error: String = "",
    /** The lists have been fetched once since start, or read from the cache. */
    val loaded: Boolean = false,
    /** When the connection was last lost; 0 while online. Timers stop here: what happened since is not known. */
    val offlineSinceMs: Long = 0,
    /** The account's project preferences; null until read (from the server or the cache). */
    val prefs: PrefsObject? = null,
    /** The account's devices, machines and clients (ListDevices). */
    val devices: Map<String, DeviceView> = emptyMap(),
    /** The server no longer knows this device: removed from the account, or the server's data is gone. */
    val removed: Boolean = false,
    /**
     * Where the next page of ended sessions starts (architecture §8
     * 「会话列表分页」): [sessions] holds every session that has not ended and
     * the ended ones up to here, besides any fetched on their own. Null when
     * every page was fetched, and before the first one ([sessionsListed]).
     */
    val sessionsNext: SessionPlace? = null,
    /** A page of the listing is being fetched. */
    val loadingSessions: Boolean = false,
    /** Why the last page could not be fetched; empty once one was, or the first page came again. */
    val sessionsError: String = "",
    /**
     * The listing's first page was fetched in this run. Before that the
     * sessions held are what the cache kept of an earlier run, and how many
     * pages there are is not known.
     */
    val sessionsListed: Boolean = true,
    val link: Link = Link.WAITING,
    /** The lists came from the server since start: what they say of the computers is not from the cache. */
    val synced: Boolean = false,
    /** The failure behind [error] in its own words, for the details. */
    val errorDetail: String = "",
) {
    /**
     * Nothing here is known to still hold: the server is out of reach, or
     * has not been reached yet. A connection lost a moment ago does not count.
     */
    val unlinked: Boolean get() = link == Link.NO_NETWORK || link == Link.NO_SERVER || (link == Link.WAITING && !synced)

    /** The server has, or may have, ended sessions that were not fetched yet. */
    val moreSessions: Boolean get() = !sessionsListed || sessionsNext != null
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
)

/** Where envelopes are cached between runs. Only ciphertext is stored. */
interface CipherCache {
    suspend fun machines(): List<Machine>
    suspend fun sessions(): List<RemoteSession>
    suspend fun events(sessionId: String): List<StoredEvent>
    suspend fun putMachine(m: Machine)
    suspend fun putSession(s: RemoteSession)
    suspend fun putEvents(sessionId: String, events: List<StoredEvent>)

    /** Forgets a session's events, e.g. when one of them does not open; the session stays listed. */
    suspend fun dropEvents(sessionId: String)
    suspend fun removeMachine(id: String)

    /** A session that no longer exists: its entry and its events. */
    suspend fun removeSession(id: String)

    /** Takes a session off the cached list, which is the listing's first page; its events stay. */
    suspend fun unlistSession(id: String)

    /**
     * Notes whose a session outside the cached list is (one of a later page,
     * or fetched by its ID), so that events kept for it go with its machine.
     * A session already noted, listed or not, is left as it is.
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
)

interface TermListener {
    /** Called on the socket's reader thread, in order. */
    fun onFrame(frame: TerminalFrame)
    fun onClose(reason: String)
}

class TermConn internal constructor(private val ws: WebSocket, private val seal: (TerminalFrame) -> ByteArray, private val closeLocal: () -> Unit) {
    fun send(frame: TerminalFrame) {
        ws.send(seal(frame).toByteString())
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
    /** Stream frames waiting to be handled before the stream is dropped and caught up from history. */
    private val frameQueue: Int = 1024,
    private val historyIdleMs: Long = 60_000,
    private val historyIdleCount: Int = 8,
    private val historyIdleBytes: Long = 16L * 1024 * 1024,
    private val streamOpenMs: Long = STREAM_OPEN_MS,
    /** How long a lost connection is only being remade, before the server counts as out of reach. */
    private val linkGraceMs: Long = 10_000,
) {
    private val keys = Keys.derive(account.root)
    private val device = SigningKey(account.deviceSeed)
    val api = Api(account.server, http)
    private val dispatcher = Dispatchers.IO.limitedParallelism(1)
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)

    private val _state = MutableStateFlow(RemoteState())
    val state: StateFlow<RemoteState> = _state.asStateFlow()

    private val machines = HashMap<String, MachineView>()
    private val sessions = HashMap<String, SessionView>()
    private val sessionKeys = ConcurrentHashMap<String, ByteArray>()
    private val buffers = HashMap<String, Buffer>()
    private val eventFlows = ConcurrentHashMap<String, MutableStateFlow<SessionEvents>>()
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
    private val streamHttp = http.newBuilder().pingInterval(STREAM_PING_SECONDS, TimeUnit.SECONDS).build()
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
    /** Putting what the cache holds in place, started once; no connection is made before it is done. */
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
    // The same for the preferences while there is no copy of them at all: nothing of them can be changed until one is read.
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
            beginConnect()
        }
    }

    /** Disconnects, e.g. when the app goes to the background. */
    fun stop() {
        scope.launch {
            running = false
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
     * Whether the device has a network at all. Losing it ends the connection
     * at once rather than at the next ping; getting one starts over.
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
        if (conn == ConnState.ONLINE) offlineSince = 0 else if (offlineSince == 0L) offlineSince = clock()
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
        val link = when {
            conn == ConnState.ONLINE -> Link.ONLINE
            !network || unresolved -> Link.NO_NETWORK
            !waited -> Link.WAITING
            else -> Link.NO_SERVER
        }
        _state.value = RemoteState(
            machines.toMap(), sessions.toMap(), conn, error, loaded, offlineSince, prefs, devices.toMap(), removed,
            sessionsNext = next, loadingSessions = pageJob != null, sessionsError = pageError, sessionsListed = firstPage,
            link = link, synced = synced, errorDetail = errorDetail,
        )
    }

    /**
     * Logs in, one login at a time: the server keeps few challenges per
     * device, and a denial has to mean what it says. A server restarted
     * between the two calls forgets the challenge; denied a fresh one too,
     * it no longer knows this device, and nothing connects until it joins again.
     */
    private suspend fun signIn(): String = signingIn.withLock {
        try {
            signedIn(loginResponse(api, account))
        } catch (first: ConnectException) {
            if (first.code != Code.PERMISSION_DENIED) throw first
            try {
                signedIn(loginResponse(api, account))
            } catch (e: ConnectException) {
                if (e.code == Code.PERMISSION_DENIED) withContext(dispatcher) {
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
     * Waits until what the cache holds is in place. It is read once, however
     * many connections wait: one made while it was still being read would
     * have its lists overwritten by what was cached before them.
     */
    private suspend fun restored() {
        if (cache == null) return
        val job = restoring ?: scope.launch { readCache(cache) }.also { restoring = it }
        job.join()
    }

    private suspend fun readCache(cache: CipherCache) {
        try {
            for (m in cache.machines()) putMachine(m.toBuilder().setOnline(false).build(), save = false)
            for (s in cache.sessions()) {
                listed += s.sessionId
                putSession(s, save = false, publish = false)
            }
            cache.blob(PROJECT_PREFS)?.let { putPrefs(io.github.lewin671.remoteagent.proto.AccountPrefsObject.parseFrom(it), save = false) }
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
            if (token.isEmpty() || tokenExpires < clock() + 86_400_000) token = withTimeout(streamOpenMs) { signIn() }
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
        val frames = Channel<ByteArray>(frameQueue, onUndeliveredElement = { queuedBytes.addAndGet(-it.size.toLong()) })
        val hello = PeerFrame.newBuilder().setHello(StreamHello.newBuilder().setToken(token)).build().toByteArray()
        val refused = java.util.concurrent.atomic.AtomicBoolean(false)
        val broken = java.util.concurrent.atomic.AtomicReference<Throwable?>(null)
        val socket = streamHttp.newWebSocket(
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

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
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
                val histories = buffers.keys + eventFlows.filterValues { it.subscriptionCount.value > 0 }.keys
                for (sid in histories.toList()) scope.launch { loadEvents(sid); fetchEvents(sid) }
                scope.launch { loadPrefs() }
                scope.launch { loadDevices() }
            }
            ServerFrame.KindCase.ACCOUNT_PREFS_UPDATED -> {
                if (f.accountPrefsUpdated.`object`.objectId == PROJECT_PREFS) putPrefs(f.accountPrefsUpdated.`object`)
            }
            ServerFrame.KindCase.DEVICE_UPDATED -> {
                bump(devicePushes, f.deviceUpdated.device.id)
                putDevice(f.deviceUpdated.device)
                publish()
            }
            ServerFrame.KindCase.DEVICE_REMOVED -> {
                bump(devicePushes, f.deviceRemoved.deviceId)
                devices.remove(f.deviceRemoved.deviceId)
                publish()
            }
            ServerFrame.KindCase.MACHINE_REMOVED -> {
                val id = f.machineRemoved.machineId
                bump(removedPushes, id)
                dropMachine(id)
                publish()
            }
            ServerFrame.KindCase.MACHINE_UPDATED -> {
                bump(machinePushes, f.machineUpdated.machine.id)
                putMachine(f.machineUpdated.machine)
            }
            ServerFrame.KindCase.SESSION_UPDATED -> {
                bump(sessionPushes, f.sessionUpdated.session.sessionId)
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
                    ingest(sid, f.eventsAppended.eventsList, fillGaps = true, save = true)
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
     * Reloads machines and the first page of sessions: every session that
     * has not ended and the latest ended ones. Pushes that arrive while the
     * lists are in flight are newer and win. Returns whether the lists are
     * in place. When they could not be fetched [RemoteState.sessionsError]
     * says why, and it is tried again by itself, ever more slowly, while the
     * connection holds.
     */
    suspend fun refresh(): Boolean = withContext(dispatcher) {
        val run = ++refreshRun
        listRetry?.cancel()
        listRetry = null
        listsInFlight.value++
        try {
            val mp = HashMap(machinePushes)
            val sp = HashMap(sessionPushes)
            val rp = HashMap(removedPushes)
            val ms = api.client.listMachines(ListMachinesRequest.getDefaultInstance(), Api.headers(token)).value()
            val ss = api.client.listRemoteSessions(ListRemoteSessionsRequest.newBuilder().setLimit(SESSION_PAGE).build(), Api.headers(token)).value()
            // Putting the lists in place suspends on the cache: one at a time.
            applying.withLock {
                // Asked for again since: that answer is the newer one, and this one would undo it.
                if (run != refreshRun) return@withContext false
                // The listing starts over: a later page still in flight continued the old one.
                listing++
                // Forget what the server no longer has, e.g. a machine another
                // device removed while this one was offline.
                val mids = ms.machinesList.map { it.id }.toSet()
                val sids = ss.sessionsList.map { it.sessionId }.toSet()
                val end = if (ss.hasNext()) SessionPlace(ss.next.endedMs, ss.next.sessionId) else null
                for (id in machines.keys.toList()) if (id !in mids && !pushed(machinePushes, mp, id)) dropMachine(id)
                fun hasMachine(id: String) = id in mids || machines.containsKey(id)
                // A session held here that this page does not have (architecture §8
                // 「重新连接后」): one that ended and comes after the page's end is
                // a later page's and stays; any other goes. Without a later page
                // the page is the whole listing, and what it lacks does not exist.
                for (v in sessions.values.toList()) {
                    if (v.id in sids || pushed(sessionPushes, sp, v.id)) continue
                    if (end != null && hasMachine(v.machineId) && afterPlace(v.listedEndedMs, v.id, end)) continue
                    sessions.remove(v.id)
                    sessionKeys.remove(v.id)
                    listed.remove(v.id)
                    // Its events go with it only when it is known not to exist; else it
                    // may be on a later page with what is held here out of date.
                    try {
                        if (end == null || !hasMachine(v.machineId)) cache?.removeSession(v.id) else cache?.unlistSession(v.id)
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
                for (s in ss.sessionsList) if (!pushed(removedPushes, rp, s.machineId)) putSession(s, publish = false)
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

    /** Asks for the lists again after a while, unless they were asked for meanwhile or the connection is gone. */
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

    private fun pushed(now: Map<String, Int>, then: Map<String, Int>, id: String) = (now[id] ?: 0) != (then[id] ?: 0)

    /** Whether the listing's first page, as last fetched, would hold this session: the cache keeps that page. */
    private fun onFirstPage(rs: RemoteSession): Boolean =
        firstPage && firstEnd.let { it == null || !afterPlace(rs.endedMs, rs.sessionId, it) }

    /**
     * Fetches the next page of ended sessions, which is the first one while
     * that has not come ([RemoteState.sessionsListed]); calls made while one
     * is in flight wait for that one. Returns whether there are more after it.
     * Throws when the page could not be fetched, which [RemoteState.sessionsError]
     * then says until a page comes.
     */
    suspend fun loadMoreSessions(): Boolean = withContext(dispatcher) {
        pageJob ?: scope.async {
            try {
                fetchPage()
            } finally {
                pageJob = null
                publish()
            }
        }.also {
            pageJob = it
            publish()
        }
    }.await()

    /** Fetches every page that is left, e.g. to filter by title; calls made meanwhile wait for the same run. */
    suspend fun loadAllSessions() = withContext(dispatcher) {
        allJob ?: scope.async {
            try {
                while (!firstPage || next != null) loadMoreSessions()
            } finally {
                allJob = null
            }
        }.also { allJob = it }
    }.await()

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
                throw CommandException(CommandException.UNAVAILABLE, pageError.ifEmpty { "没能读取会话列表" })
            }
            val after = next ?: return false
            val epoch = listing
            val rp = HashMap(removedPushes)
            val res = try {
                api.client.listRemoteSessions(
                    ListRemoteSessionsRequest.newBuilder()
                        .setAfter(SessionCursor.newBuilder().setEndedMs(after.endedMs).setSessionId(after.sessionId))
                        .setLimit(SESSION_PAGE).build(),
                    Api.headers(currentToken()),
                ).value()
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Exception) {
                // The first page was fetched again meanwhile: what failed continued the listing before it.
                if (epoch != listing) continue
                log("remote-agent: " + errorText(e))
                pageError = errorText(e)
                throw e
            }
            // The first page was fetched again meanwhile: this page continued the
            // listing before it. The caller still wants a page: the one after the new cursor.
            if (epoch != listing) continue
            // Putting a session suspends on the cache: the listing may start over meanwhile too.
            for (s in res.sessionsList) if (epoch == listing && accepts(s, rp)) putSession(s, save = false, publish = false)
            if (epoch != listing) continue
            next = if (res.hasNext()) SessionPlace(res.next.endedMs, res.next.sessionId) else null
            pageError = ""
            return next != null
        }
    }

    /**
     * Whether a session the server answered a request with is taken: not
     * one of a machine removed since the request was made ([removalsThen]),
     * nor of one the listing of machines does not have.
     */
    private fun accepts(rs: RemoteSession, removalsThen: Map<String, Int>): Boolean =
        !pushed(removedPushes, removalsThen, rs.machineId) && (!firstPage || machines.containsKey(rs.machineId))

    /**
     * Fetches one session by its ID when it is not held here: one opened
     * from a notification or a link may be on a page not fetched yet.
     * Returns whether it is held now; false means the server does not have
     * it. Throws when the server could not be asked.
     */
    suspend fun fetchSession(id: String): Boolean = withContext(dispatcher) {
        if (sessions[id] != null) return@withContext CompletableDeferred(true)
        byId[id] ?: scope.async {
            try {
                val rp = HashMap(removedPushes)
                val res = api.client.listRemoteSessions(ListRemoteSessionsRequest.newBuilder().addSessionIds(id).build(), Api.headers(currentToken())).value()
                val rs = res.sessionsList.firstOrNull { it.sessionId == id }
                // A session of a machine removed meanwhile, or one the listing of
                // machines no longer has, is as good as gone.
                if (rs != null && accepts(rs, rp)) putSession(rs, save = onFirstPage(rs))
                sessions[id] != null
            } finally {
                byId.remove(id)
            }
        }.also { byId[id] = it }
    }.await()

    private suspend fun putMachine(m: Machine, staleOnline: Boolean = false, save: Boolean = true) {
        val old = machines[m.id]
        var view = old ?: MachineView(m.id, m.online, m.lastSeenMs)
        if (!staleOnline) view = view.copy(online = m.online)
        if (m.lastSeenMs > view.lastSeenMs) view = view.copy(lastSeenMs = m.lastSeenMs)
        var store = old == null
        if (m.hasMeta()) {
            try {
                val (h, meta) = Envelopes.openMessage(keys.meta, m.meta, Place(PayloadKind.PAYLOAD_KIND_MACHINE_META, machineId = m.id), MachineMeta::parseFrom)
                if (ucmp(h.seq, view.metaRev) >= 0) {
                    view = view.copy(meta = meta, metaRev = h.seq)
                    store = true
                }
            } catch (e: Exception) {
                report(e)
            }
        }
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

    private suspend fun putSession(rs: RemoteSession, save: Boolean = true, publish: Boolean = true) {
        var view = sessions[rs.sessionId]
        val removals = removedPushes[rs.machineId] ?: 0
        try {
            if (view == null) {
                val (h, raw) = Envelopes.open(keys.wrap, rs.key)
                Envelopes.expect(h, Place(PayloadKind.PAYLOAD_KIND_SESSION_KEY, machineId = rs.machineId, sessionId = rs.sessionId))
                if (raw.size != KEY_SIZE) throw OpenException("bad session key")
                if ((removedPushes[rs.machineId] ?: 0) != removals) return
                sessionKeys[rs.sessionId] = raw
                view = SessionView(rs.sessionId, rs.machineId)
            }
            if (view.machineId != rs.machineId) throw OpenException("会话记录的机器被改动")
            view = view.copy(lastSeq = umax(view.lastSeq, rs.lastSeq), updatedMs = maxOf(view.updatedMs, rs.updatedMs))
            var store = sessions[rs.sessionId] == null
            if (store) view = view.copy(listedEndedMs = rs.endedMs)
            if (rs.hasMeta()) {
                val key = sessionKeys[rs.sessionId]!!
                val (h, info) = Envelopes.openMessage(
                    key, rs.meta,
                    Place(PayloadKind.PAYLOAD_KIND_SESSION_META, machineId = rs.machineId, sessionId = rs.sessionId),
                    Session::parseFrom,
                )
                if (ucmp(h.seq, view.metaRev) >= 0) {
                    // The server stores when it ended with the metadata.
                    view = view.copy(info = info, metaRev = h.seq, listedEndedMs = rs.endedMs)
                    store = true
                }
            }
            sessions[rs.sessionId] = view
            if ((eventFlows[rs.sessionId]?.subscriptionCount?.value ?: 0) > 0) scope.launch { loadEvents(rs.sessionId) }
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

    private val prefsCodec = PrefsCodec(keys.prefs, account.deviceId)

    /**
     * Reads the preferences. With no copy of them here nothing of them can
     * be changed, so a read that fails then is tried again by itself, ever
     * more slowly, while the connection holds.
     */
    private suspend fun loadPrefs() {
        prefsRetry?.cancel()
        prefsRetry = null
        val gen = generation
        try {
            val o = api.client.getAccountPrefs(io.github.lewin671.remoteagent.proto.GetAccountPrefsRequest.newBuilder().setObjectId(PROJECT_PREFS).build(), Api.headers(token)).value()
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
                if (running && conn == ConnState.ONLINE && gen == generation && prefs == null) loadPrefs()
            }
        }
    }

    /** Takes an object from the server when it is not older than what is known here. */
    private suspend fun putPrefs(o: io.github.lewin671.remoteagent.proto.AccountPrefsObject, save: Boolean = true) {
        val opened = try {
            prefsCodec.open(o)
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
     * Pins, unpins, hides or restores a project for the whole account.
     * Returns once the server has it; a conflict is redone on the newer
     * object. Needs the connection: offline, changes wait.
     */
    suspend fun changePrefs(op: PrefsOp) {
        val base = withContext(dispatcher) {
            if (conn != ConnState.ONLINE) throw CommandException(CommandException.UNAVAILABLE, "没有连上服务器，连上后才能修改")
            prefs ?: PrefsObject.EMPTY
        }
        var stored: io.github.lewin671.remoteagent.proto.AccountPrefsObject? = null
        val result = writePrefsOp(
            prefsCodec, base, op, newRequestId(), clock(),
            put = { expected, env ->
                val r = api.client.putAccountPrefs(
                    io.github.lewin671.remoteagent.proto.PutAccountPrefsRequest.newBuilder().setObjectId(PROJECT_PREFS).setExpectedRevision(expected).setEnvelope(env).build(),
                    Api.headers(currentToken()),
                ).value()
                if (r.applied) stored = r.`object`.takeIf { it.hasEnvelope() } ?: io.github.lewin671.remoteagent.proto.AccountPrefsObject.newBuilder().setObjectId(PROJECT_PREFS).setRevision(expected + 1).setEnvelope(env).build()
                PutResult(r.applied, r.`object`)
            },
            get = {
                api.client.getAccountPrefs(io.github.lewin671.remoteagent.proto.GetAccountPrefsRequest.newBuilder().setObjectId(PROJECT_PREFS).build(), Api.headers(currentToken())).value().`object`.also { stored = it }
            },
        )
        withContext(dispatcher) {
            val o = stored
            if (o != null && o.revision == result.revision) putPrefs(o) else {
                prefs = newerPrefs(prefs, result)
                publish()
            }
        }
    }

    /** Reads the device list again, for a page whose times are as of opening it. */
    suspend fun refreshDevices() = withContext(dispatcher) { loadDevices() }

    private suspend fun loadDevices() {
        try {
            val dp = HashMap(devicePushes)
            val r = api.client.listDevices(io.github.lewin671.remoteagent.proto.ListDevicesRequest.getDefaultInstance(), Api.headers(token)).value()
            // A device pushed while the list was in flight is as the push left it:
            // the list may be from before, and nothing would correct it afterwards.
            val ids = r.devicesList.map { it.id }.toSet()
            devices.keys.retainAll { it in ids || pushed(devicePushes, dp, it) }
            for (d in r.devicesList) if (!pushed(devicePushes, dp, d.id)) putDevice(d)
        } catch (e: Exception) {
            report(e)
        }
        publish()
    }

    private fun putDevice(d: io.github.lewin671.remoteagent.proto.Device) {
        val old = devices[d.id]
        var view = DeviceView(d.id, d.kind, d.createdMs, old?.meta, old?.metaRev ?: 0, d.lastSeenMs)
        if (d.hasMeta() && ucmp(d.metaRevision, view.metaRev) >= 0) {
            try {
                val (_, meta) = Envelopes.openMessage(
                    keys.deviceMeta, d.meta,
                    Place(PayloadKind.PAYLOAD_KIND_DEVICE_META, requestId = d.id, seq = d.metaRevision),
                    io.github.lewin671.remoteagent.proto.DeviceMeta::parseFrom,
                )
                view = view.copy(meta = meta, metaRev = d.metaRevision)
            } catch (e: Exception) {
                report(e)
            }
        }
        devices[d.id] = view
    }

    /**
     * Names a client device for the whole account, keeping what else its
     * metadata says; a conflict is redone on the stored metadata.
     */
    suspend fun renameDevice(id: String, name: String) = changeDeviceMeta(id) { it.setName(name) }

    /** Says what this client is and runs on, keeping the name it was given. */
    suspend fun describeSelf(platform: String, description: String) =
        changeDeviceMeta(account.deviceId) { it.setPlatform(platform).setDescription(description) }

    private suspend fun changeDeviceMeta(id: String, change: (io.github.lewin671.remoteagent.proto.DeviceMeta.Builder) -> Unit) {
        var base = withContext(dispatcher) { devices[id] } ?: throw CommandException("not_found", "没有这台设备")
        repeat(5) {
            val meta = (base.meta?.toBuilder() ?: io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder()).also(change).build()
            val env = Envelopes.seal(
                keys.deviceMeta,
                Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_DEVICE_META).setRequestId(id).setSeq(base.metaRev + 1).setSender(account.deviceId).build(),
                meta,
            )
            val r = api.client.putDeviceMeta(
                io.github.lewin671.remoteagent.proto.PutDeviceMetaRequest.newBuilder().setDeviceId(id).setExpectedRevision(base.metaRev).setMeta(env).build(),
                Api.headers(currentToken()),
            ).value()
            withContext(dispatcher) {
                if (r.hasDevice()) putDevice(r.device)
                if (r.applied && (!r.hasDevice() || !r.device.hasMeta())) devices[id] = base.copy(meta = meta, metaRev = base.metaRev + 1)
                publish()
            }
            if (r.applied) return
            base = withContext(dispatcher) { devices[id] } ?: throw CommandException("not_found", "没有这台设备")
        }
        throw CommandException("aborted", "设备名称一直在被其他客户端修改，没能保存")
    }

    /**
     * Shows fixed lists and histories without a server, for layout tests and
     * screenshots of the screens. Only for a Remote that was never started:
     * nothing is fetched, and commands fail as they would offline.
     */
    fun showForTest(
        state: RemoteState,
        events: Map<String, List<Event>> = emptyMap(),
        modelLists: Map<Pair<String, Agent>, ListModelsResponse> = emptyMap(),
    ) {
        check(!running) { "showForTest on a started Remote" }
        _state.value = state
        for ((key, response) in modelLists) models["${key.first}/${key.second.number}"] = CompletableDeferred(response)
        for ((sid, list) in events) eventFlows.getOrPut(sid) { MutableStateFlow(SessionEvents()) }.value = SessionEvents(list, loaded = true)
    }

    // --- events ---

    /** A session's events; loads them from the cache and the server on first use. */
    fun events(sid: String): StateFlow<SessionEvents> {
        val flow = eventFlows.getOrPut(sid) {
            MutableStateFlow(SessionEvents()).also { f ->
                scope.launch {
                    f.subscriptionCount.collect { count ->
                        if (count > 0) scope.launch { loadEvents(sid) }
                        else { buffers[sid]?.usedAt = clock(); trimHistory() }
                    }
                }
            }
        }
        scope.launch {
            if (historySweep == null) historySweep = scope.launch {
                while (true) { delay(historyIdleMs.coerceAtLeast(1)); trimHistory() }
            }
            buffers[sid]?.usedAt = clock()
            loadEvents(sid)
        }
        return flow
    }

    private fun trimHistory() {
        val idle = buffers.entries.filter { (sid, _) -> eventFlows[sid]?.subscriptionCount?.value == 0 }
            .sortedBy { it.value.usedAt }
        var bytes = idle.sumOf { it.value.bytes }
        var count = idle.size
        for ((sid, b) in idle) {
            if (sessions[sid] == null || count > historyIdleCount || bytes > historyIdleBytes || clock() - b.usedAt >= historyIdleMs) {
                buffers.remove(sid)
                // Keep the small flow identity for consumers about to subscribe;
                // its subscription watcher reloads a reclaimed history.
                eventFlows[sid]?.value = SessionEvents()
                count--; bytes -= b.bytes
            }
        }
    }

    private suspend fun loadEvents(sid: String) {
        if (buffers[sid] != null || sessions[sid] == null) return
        val b = Buffer().apply { usedAt = clock() }
        buffers[sid] = b
        val cached = try { cache?.events(sid).orEmpty() }
        catch (e: Exception) { report(e); emptyList() }
        if (buffers[sid] !== b) return
        if (cached.isNotEmpty()) {
            ingest(sid, cached, fillGaps = false, save = false)
            if (buffers[sid] !== b) return
            // A cached copy that does not open is dropped and the history
            // fetched whole (architecture §10 「后台和内存」): the cache keeps
            // an event as first stored, so the bad one would stay for good.
            if (b.error != null) {
                try { cache?.dropEvents(sid) } catch (e: Exception) { report(e) }
                if (buffers[sid] !== b) return
                b.events = PagedList.empty()
                b.pending.clear()
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
                    val res = api.client.getEvents(
                        GetEventsRequest.newBuilder().setSessionId(sid).setAfterSeq(before.toLong()).setLimit(500).build(),
                        Api.headers(token),
                    ).value()
                    if (!running || gen != generation || buffers[sid] !== b) return
                    ingest(sid, res.eventsList, fillGaps = false, save = true)
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

    private suspend fun ingest(sid: String, stored: List<StoredEvent>, fillGaps: Boolean, save: Boolean) {
        val b = buffers[sid] ?: return
        val s = sessions[sid] ?: return
        val key = sessionKeys[sid] ?: return
        val opened = ArrayList<StoredEvent>()
        for (se in stored) {
            if (ucmp(se.seq, b.events.size.toLong()) <= 0 || b.pending.containsKey(se.seq)) continue
            try {
                val (_, ev) = Envelopes.openMessage(
                    key, se.envelope,
                    Place(PayloadKind.PAYLOAD_KIND_EVENT, machineId = s.machineId, sessionId = sid, seq = se.seq),
                    Event::parseFrom,
                )
                if (ev.seq != se.seq || ev.sessionId != sid) throw OpenException("事件的位置被改动")
                b.pending[se.seq] = ev
                opened += se
                // What is held is the event as opened; its ciphertext may be packed.
                b.bytes += ev.serializedSize + 256L
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
        sessions[sid]?.let { cur -> sessions[sid] = cur.copy(lastSeq = umax(cur.lastSeq, b.events.size.toLong())) }
        if (fillGaps && b.pending.isNotEmpty()) scope.launch { fetchEvents(sid) }
        publishEvents(sid, b)
        publish()
        trimHistory()
    }

    private fun publishEvents(sid: String, b: Buffer) {
        if (buffers[sid] !== b) return
        eventFlows.getOrPut(sid) { MutableStateFlow(SessionEvents()) }.value = SessionEvents(b.events, b.loaded, b.error, b.fetchError)
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
        val env = signEnvelope(device, Envelopes.seal(
            keys.command,
            Header.newBuilder()
                .setKind(PayloadKind.PAYLOAD_KIND_COMMAND)
                .setMachineId(machineId)
                .setSessionId(sessionId)
                .setRequestId(requestId)
                .setDeadlineMs(deadline)
                .setSender(account.deviceId)
                .build(),
            cmd.toBuilder().setProtocolVersion(PROTOCOL_VERSION).build(),
        ))
        return PreparedCommand(machineId, sessionId, requestId, deadline, env)
    }

    /** Sends a command about a session. */
    suspend fun command(sid: String, cmd: Command): CommandResult {
        val s = withContext(dispatcher) { sessions[sid] } ?: throw CommandException("not_found", "未知会话")
        return forward(prepare(s.machineId, sid, cmd))
    }

    /** Sends a command about a machine, such as starting a session. */
    suspend fun machineCommand(machineId: String, cmd: Command): CommandResult = forward(prepare(machineId, "", cmd))

    /**
     * How long one round trip to the server takes now, in ms: a request that does nothing
     * there (连接检测). Throws when no answer comes in [timeoutMs].
     */
    suspend fun pingServer(timeoutMs: Long = 8_000): Long = withContext(Dispatchers.IO) {
        val call = http.newBuilder().callTimeout(timeoutMs, java.util.concurrent.TimeUnit.MILLISECONDS).build()
            .newCall(okhttp3.Request.Builder().url(api.server.trimEnd('/') + "/healthz").header("Cache-Control", "no-store").build())
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

    /** How long one round trip to a computer takes, through the server and back: a sealed command that only reads its settings. */
    suspend fun pingMachine(machineId: String, timeoutMs: Long = 15_000): Long {
        val started = System.nanoTime()
        try {
            withTimeout(timeoutMs) { machineCommand(machineId, Command.newBuilder().setGetSettings(io.github.lewin671.remoteagent.proto.GetSettingsRequest.getDefaultInstance()).build()) }
        } catch (e: kotlinx.coroutines.TimeoutCancellationException) {
            throw java.io.IOException("没有回应", e)
        }
        return (System.nanoTime() - started) / 1_000_000
    }

    /**
     * Forwards a prepared command and returns the opened result. Transport
     * failures are retried with the same request ID, which the daemon runs at
     * most once; when the outcome stays unknown the error says so.
     */
    suspend fun forward(p: PreparedCommand, retryOfUnknown: Boolean = false): CommandResult {
        // A machine of another protocol version would refuse it: say which
        // side to update instead of sending.
        protocolMismatch(state.value.machines[p.machineId])?.let { throw CommandException("failed_precondition", it) }
        // A command whose outcome is unknown is sent even when expired: the
        // daemon may still have its result in its log.
        if (!retryOfUnknown && clock() >= p.deadlineMs) throw CommandException(CommandException.EXPIRED, "这条指令已过期，没有发出")
        var attempt = 0
        // Once a try may have reached the daemon, a later try that surely did
        // not (machine gone, network down) says nothing about that one: the
        // outcome stays unknown.
        var mayHaveRun = retryOfUnknown
        fun notSent(code: String, text: String, later: String) =
            if (mayHaveRun) CommandException(CommandException.UNKNOWN, "不确定电脑有没有执行：请求可能已经送到，之后$later") else CommandException(code, text)
        var waiting = false
        try {
            while (true) {
                waiting = false
                try {
                    if (token.isEmpty()) token = relogin(mayHaveRun)
                    kotlinx.coroutines.currentCoroutineContext().ensureActive()
                    waiting = true
                    val res = api.client.forward(
                        ForwardRequest.newBuilder().setMachineId(p.machineId).setCommand(p.envelope).build(),
                        Api.headers(token),
                    ).value()
                    val (_, msg) = try {
                        Envelopes.openMessage(
                            keys.command, res.result,
                            Place(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT, machineId = p.machineId, sessionId = p.sessionId, requestId = p.requestId),
                            CommandResult::parseFrom,
                        )
                    } catch (e: OpenException) {
                        // A result that does not open says nothing about whether
                        // the command ran; retrying the same request asks again.
                        throw CommandException(CommandException.UNKNOWN, "电脑的回复打不开，不确定指令有没有执行")
                    }
                    if (msg.errorCode.isNotEmpty()) throw CommandException(msg.errorCode, msg.errorMessage.ifEmpty { msg.errorCode }, fromDaemon = true)
                    return msg
                } catch (ce: ConnectException) {
                    waiting = false
                    val cause = ce.exception
                    if (cause is java.net.UnknownHostException || cause is java.net.ConnectException) {
                        throw notSent("network", "网络不通，指令没有发出", "网络断了")
                    }
                    if (ce.code == Code.UNAUTHENTICATED && attempt < 3) {
                        attempt++
                        token = relogin(mayHaveRun)
                        continue
                    }
                    // ABORTED: the daemon could not seal a result, as on the web.
                    val unknown = ce.code in setOf(Code.DEADLINE_EXCEEDED, Code.UNKNOWN, Code.INTERNAL_ERROR, Code.CANCELED, Code.ABORTED)
                    if (unknown) mayHaveRun = true
                    if (unknown && attempt < 3 && clock() + 2000 < p.deadlineMs) {
                        attempt++
                        delay(1000L * attempt)
                        continue
                    }
                    if (ce.code == Code.UNAVAILABLE && cause == null) throw notSent(CommandException.UNAVAILABLE, "电脑不在线，指令没有发出", "电脑离线了")
                    if (unknown || ce.code == Code.UNAVAILABLE) {
                        throw CommandException(CommandException.UNKNOWN, "不确定电脑有没有执行：${errorText(ce)}")
                    }
                    throw CommandException(ce.code.codeName, errorText(ce))
                }
            }
        } catch (e: CancellationException) {
            if (waiting || mayHaveRun) throw CommandException(CommandException.UNKNOWN, "不确定电脑有没有执行：已停止等待回复。重试会发送同一个请求")
            throw e
        }
    }

    /**
     * Logs in again during a forward. When an earlier try may already have
     * run the command, a failure here is no answer about it: it stays unknown.
     */
    private suspend fun relogin(mayHaveRun: Boolean): String = try {
        signIn()
    } catch (e: Exception) {
        if (!mayHaveRun) throw e
        throw CommandException(CommandException.UNKNOWN, "不确定电脑有没有执行：请求可能已经送到，之后登录失败（${errorText(e)}）")
    }

    /** The models a machine's CLI offers, refreshed when a picker opens. */
    suspend fun listModels(machineId: String, agent: Agent = Agent.AGENT_UNSPECIFIED, fresh: Boolean = false): ListModelsResponse {
        val d = withContext(dispatcher) {
            val k = "$machineId/${agent.number}"
            models[k]?.takeUnless { it.isCompleted && (fresh || it.getCompletionExceptionOrNull() != null) } ?: scope.async {
                val r = machineCommand(machineId, Command.newBuilder().setListModels(ListModelsRequest.newBuilder().setAgent(agent)).build())
                if (!r.hasListModels()) throw CommandException("internal", "意外的回复")
                r.listModels
            }.also { models[k] = it }
        }
        return d.await()
    }

    /** Unregisters a device; a machine's sessions go with it. */
    suspend fun removeDevice(deviceId: String) {
        api.client.removeDevice(RemoveDeviceRequest.newBuilder().setDeviceId(deviceId).build(), Api.headers(token)).value()
        withContext(dispatcher) {
            dropMachine(deviceId)
            bump(devicePushes, deviceId)
            devices.remove(deviceId)
            publish()
        }
    }

    /** The bearer token of the current login, for services used outside this class. */
    suspend fun currentToken(): String {
        if (token.isEmpty()) token = signIn()
        return token
    }

    // --- images ---

    /** A display copy fetched for viewing: its pixels, or why there are none. */
    sealed interface ImageResult {
        class Loaded(val payload: io.github.lewin671.remoteagent.proto.ImagePayload) : ImageResult
        /** Kept past its retention: only the preview in the event is left. */
        data object Expired : ImageResult
        /** The server never had it. */
        data object Missing : ImageResult
        /** The bytes are not the image they claim to be, or do not open. */
        data class Corrupt(val why: String) : ImageResult
    }

    /**
     * An image's display copy, from the cache of envelopes or the server.
     * Only the envelope is cached; it is opened and checked in memory each
     * time: bound to this machine, session and image, and data that starts
     * with its format's signature.
     */
    suspend fun image(sid: String, imageId: String): ImageResult {
        val s = withContext(dispatcher) { sessions[sid] } ?: throw CommandException("not_found", "未知会话")
        val key = sessionKeys[sid] ?: throw CommandException("not_found", "未知会话")
        val cacheKey = "$sid/$imageId"
        var env = try {
            cache?.image(cacheKey)?.let { Envelope.parseFrom(it) }
        } catch (e: Exception) {
            null
        }
        if (env == null) {
            val r = try {
                api.client.getImage(io.github.lewin671.remoteagent.proto.GetImageRequest.newBuilder().setSessionId(sid).setImageId(imageId).build(), Api.headers(currentToken())).value()
            } catch (e: ConnectException) {
                if (e.code == Code.NOT_FOUND) return ImageResult.Missing
                throw e
            }
            if (r.expired || !r.hasImage()) return ImageResult.Expired
            env = r.image
        }
        val payload = try {
            Envelopes.openMessage(
                key, env,
                Place(PayloadKind.PAYLOAD_KIND_IMAGE, machineId = s.machineId, sessionId = sid, requestId = imageId),
                io.github.lewin671.remoteagent.proto.ImagePayload::parseFrom,
            ).second
        } catch (e: OpenException) {
            return ImageResult.Corrupt(e.message ?: "打不开")
        }
        if (detectFormat(payload.data.toByteArray()) != payload.format) return ImageResult.Corrupt("图片内容和格式对不上")
        try {
            cache?.putImage(cacheKey, env.toByteArray())
        } catch (e: Exception) {
            report(e)
        }
        return ImageResult.Loaded(payload)
    }

    /** The image upload commands on one machine. */
    fun uploadChannel(machineId: String): UploadChannel = object : UploadChannel {
        private suspend fun run(b: Command.Builder.() -> Unit): CommandResult = machineCommand(machineId, Command.newBuilder().apply(b).build())

        override suspend fun begin(transferId: String, format: io.github.lewin671.remoteagent.proto.ImageFormat, total: Long, sha256: ByteArray) =
            run {
                beginImageUpload = io.github.lewin671.remoteagent.proto.BeginImageUploadRequest.newBuilder().setTransferId(transferId).setFormat(format)
                    .setTotalBytes(total).setSha256(com.google.protobuf.ByteString.copyFrom(sha256)).build()
            }.beginImageUpload.status

        override suspend fun put(transferId: String, offset: Long, data: ByteArray) =
            run {
                putImageChunk = io.github.lewin671.remoteagent.proto.PutImageChunkRequest.newBuilder().setTransferId(transferId).setOffset(offset)
                    .setData(com.google.protobuf.ByteString.copyFrom(data)).build()
            }.putImageChunk.status

        override suspend fun status(transferId: String) =
            run { getImageUploadStatus = io.github.lewin671.remoteagent.proto.GetImageUploadStatusRequest.newBuilder().setTransferId(transferId).build() }.getImageUploadStatus.status

        override suspend fun complete(transferId: String) =
            run { completeImageUpload = io.github.lewin671.remoteagent.proto.CompleteImageUploadRequest.newBuilder().setTransferId(transferId).build() }.completeImageUpload.status
    }

    // --- terminal ---

    /**
     * Joins a session's terminal stream. Frames are sealed with the session key
     * and numbered; each side refuses frames out of order.
     */
    suspend fun openTerminal(sid: String, listener: TermListener): TermConn {
        val s = withContext(dispatcher) { sessions[sid] } ?: throw CommandException("not_found", "未知会话")
        val key = sessionKeys[sid] ?: throw CommandException("not_found", "未知会话")
        val tok = currentToken()
        val streamId = newRequestId()
        val opened = CompletableDeferred<Unit>()
        val closed = java.util.concurrent.atomic.AtomicBoolean(false)
        fun finish(reason: String) {
            if (closed.compareAndSet(false, true)) listener.onClose(reason)
        }
        var last = 0L
        val url = account.server.replaceFirst(Regex("^http"), "ws") + "/v1/term"
        val ws = streamHttp.newWebSocket(
            Request.Builder().url(url).build(),
            object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) {
                    val hello = TermHello.newBuilder().setToken(tok).setStreamId(streamId).setMachineId(s.machineId).build()
                    webSocket.send(hello.toByteArray().toByteString())
                    opened.complete(Unit)
                }

                override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                    if (closed.get()) return
                    try {
                        val env = Envelope.parseFrom(bytes.toByteArray())
                        val (h, frame) = Envelopes.openMessage(
                            key, env,
                            Place(PayloadKind.PAYLOAD_KIND_TERMINAL_OUTPUT, machineId = s.machineId, sessionId = sid, requestId = streamId),
                            TerminalFrame::parseFrom,
                        )
                        if (ucmp(h.seq, last) <= 0) throw OpenException("终端帧顺序不对")
                        last = h.seq
                        listener.onFrame(frame)
                    } catch (e: Exception) {
                        finish(errorText(e))
                        webSocket.cancel()
                    }
                }

                override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                    webSocket.close(1000, null)
                    finish("终端连接断开")
                }

                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = finish("终端连接断开")

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    opened.completeExceptionally(Exception("连不上终端"))
                    finish("终端连接断开：" + errorText(t))
                }
            },
        )
        try {
            withTimeout(20_000) { opened.await() }
            command(sid, Command.newBuilder().setOpenTerminal(
                io.github.lewin671.remoteagent.proto.OpenTerminalRequest.newBuilder().setSessionId(sid).setStreamId(streamId),
            ).build())
        } catch (e: Exception) {
            closed.set(true)
            ws.cancel()
            throw e
        }
        var seq = 0L
        val lock = Any()
        return TermConn(
            ws,
            seal = { frame ->
                synchronized(lock) {
                    seq++
                    Envelopes.seal(
                        key,
                        Header.newBuilder()
                            .setKind(PayloadKind.PAYLOAD_KIND_TERMINAL_INPUT)
                            .setMachineId(s.machineId)
                            .setSessionId(sid)
                            .setRequestId(streamId)
                            .setSeq(seq)
                            .setSender(account.deviceId)
                            .build(),
                        frame,
                    ).toByteArray()
                }
            },
            closeLocal = { closed.set(true) },
        )
    }

    private fun report(e: Throwable) {
        // Only the error text: never content or keys.
        log("remote-agent: " + errorText(e))
        error = errorText(e)
    }
}
