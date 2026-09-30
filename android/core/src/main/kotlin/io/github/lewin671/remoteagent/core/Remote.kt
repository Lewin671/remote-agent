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
import io.github.lewin671.remoteagent.proto.Machine
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.PeerFrame
import io.github.lewin671.remoteagent.proto.RemoteSession
import io.github.lewin671.remoteagent.proto.RemoveDeviceRequest
import io.github.lewin671.remoteagent.proto.ServerFrame
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.StoredEvent
import io.github.lewin671.remoteagent.proto.StreamHello
import io.github.lewin671.remoteagent.proto.TermHello
import io.github.lewin671.remoteagent.proto.TerminalFrame
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ListModelsRequest
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
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
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
import kotlin.random.Random

enum class ConnState { CONNECTING, ONLINE, OFFLINE }

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
)

data class RemoteState(
    val machines: Map<String, MachineView> = emptyMap(),
    val sessions: Map<String, SessionView> = emptyMap(),
    val conn: ConnState = ConnState.CONNECTING,
    val error: String = "",
    /** The lists have been fetched once since start, or read from the cache. */
    val loaded: Boolean = false,
)

/** A session's events, contiguous from seq 1. */
data class SessionEvents(
    val events: List<Event> = emptyList(),
    val loaded: Boolean = false,
    /** Set when an event does not open: history stops before it. */
    val error: String? = null,
)

/** Where envelopes are cached between runs. Only ciphertext is stored. */
interface CipherCache {
    suspend fun machines(): List<Machine>
    suspend fun sessions(): List<RemoteSession>
    suspend fun events(sessionId: String): List<StoredEvent>
    suspend fun putMachine(m: Machine)
    suspend fun putSession(s: RemoteSession)
    suspend fun putEvents(sessionId: String, events: List<StoredEvent>)
    suspend fun removeMachine(id: String)
    suspend fun removeSession(id: String)
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

@OptIn(ExperimentalCoroutinesApi::class)
class Remote(
    val account: Account,
    private val http: OkHttpClient,
    private val cache: CipherCache? = null,
    private val log: (String) -> Unit = {},
    private val clock: () -> Long = System::currentTimeMillis,
    /** Stream frames waiting to be handled before the stream is dropped and caught up from history. */
    private val frameQueue: Int = 1024,
) {
    private val keys = Keys.derive(account.root)
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

    // Pushes seen per machine, session and machine removal, so that a list
    // fetched before a push cannot undo it.
    private val machinePushes = HashMap<String, Int>()
    private val sessionPushes = HashMap<String, Int>()
    private val removedPushes = HashMap<String, Int>()

    @Volatile private var token = ""
    private var ws: WebSocket? = null
    private var retry = 1000L
    private var running = false
    private var reconnect: Job? = null
    // Bumped by stop: a connection attempt suspended in login when the app
    // went to the background must not open the stream afterwards.
    private var generation = 0
    private var cacheRead = false
    private val models = HashMap<String, Deferred<ListModelsResponse>>()

    private class Buffer {
        val events = ArrayList<Event>()
        val pending = TreeMap<Long, Event>(java.lang.Long::compareUnsigned)
        var fetching = false
        var refetch = false
        var loaded = false
        var error: String? = null
    }

    // --- lifecycle ---

    /** Connects and keeps reconnecting until [stop]. */
    fun start() {
        scope.launch {
            if (running) return@launch
            running = true
            readCache()
            connect()
        }
    }

    /** Disconnects, e.g. when the app goes to the background. */
    fun stop() {
        scope.launch {
            running = false
            generation++
            reconnect?.cancel()
            reconnect = null
            ws?.close(1000, null)
            ws = null
            conn = ConnState.OFFLINE
            error = ""
            publish()
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
            if (!running || conn == ConnState.ONLINE || ws != null) return@launch
            reconnect?.cancel()
            retry = 1000
            connect()
        }
    }

    private fun publish() {
        _state.value = RemoteState(machines.toMap(), sessions.toMap(), conn, error, loaded)
    }

    private suspend fun readCache() {
        if (cacheRead || cache == null) return
        cacheRead = true
        try {
            for (m in cache.machines()) putMachine(m.toBuilder().setOnline(false).build(), save = false)
            for (s in cache.sessions()) putSession(s, save = false)
            if (machines.isNotEmpty() || sessions.isNotEmpty()) loaded = true
        } catch (e: Exception) {
            report(e)
        }
        publish()
    }

    private suspend fun connect() {
        if (!running) return
        val gen = generation
        conn = ConnState.CONNECTING
        publish()
        try {
            token = login(api, account)
        } catch (e: Exception) {
            if (gen == generation) fail(e)
            return
        }
        if (!running || gen != generation) return
        val url = account.server.replaceFirst(Regex("^http"), "ws") + "/v1/stream"
        // Bounded: a reader that falls behind drops the stream and catches up
        // from history after reconnecting, instead of queueing without end.
        val frames = Channel<ByteArray>(frameQueue)
        val hello = PeerFrame.newBuilder().setHello(StreamHello.newBuilder().setToken(token)).build().toByteArray()
        val socket = http.newWebSocket(
            Request.Builder().url(url).build(),
            object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) {
                    webSocket.send(hello.toByteString())
                }

                override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                    if (frames.trySend(bytes.toByteArray()).isFailure) {
                        webSocket.cancel()
                        frames.close()
                    }
                }

                override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                    webSocket.close(1000, null)
                    frames.close()
                }

                override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                    frames.close()
                }

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    frames.close()
                }
            },
        )
        ws = socket
        scope.launch {
            for (b in frames) {
                if (ws !== socket) break
                val f = try {
                    ServerFrame.parseFrom(b)
                } catch (e: Exception) {
                    socket.cancel()
                    break
                }
                onFrame(f)
            }
            frames.cancel()
            if (ws === socket) {
                ws = null
                fail(Exception("与 server 的连接断开"))
            }
        }
    }

    private fun fail(e: Throwable) {
        conn = ConnState.OFFLINE
        error = errorText(e)
        publish()
        if (!running) return
        val delayMs = retry + (Random.nextDouble() * retry * 0.25).toLong()
        retry = minOf(retry * 2, 30_000)
        reconnect = scope.launch {
            delay(delayMs)
            connect()
        }
    }

    private suspend fun onFrame(f: ServerFrame) {
        when (f.kindCase) {
            ServerFrame.KindCase.READY -> {
                retry = 1000
                conn = ConnState.ONLINE
                error = ""
                publish()
                // Subscribed first, then catch up: nothing falls in between.
                refresh()
                for (sid in buffers.keys.toList()) scope.launch { fetchEvents(sid) }
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
                putSession(f.sessionUpdated.session)
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
        machines.remove(id)
        for (sid in sessions.values.filter { it.machineId == id }.map { it.id }) {
            sessions.remove(sid)
            sessionKeys.remove(sid)
        }
        try {
            cache?.removeMachine(id)
        } catch (e: Exception) {
            report(e)
        }
    }

    /** Reloads machines and sessions. Pushes that arrive while the lists are in flight are newer and win. */
    suspend fun refresh() = withContext(dispatcher) {
        try {
            val mp = HashMap(machinePushes)
            val sp = HashMap(sessionPushes)
            val rp = HashMap(removedPushes)
            fun pushed(now: Map<String, Int>, then: Map<String, Int>, id: String) = (now[id] ?: 0) != (then[id] ?: 0)
            val ms = api.client.listMachines(ListMachinesRequest.getDefaultInstance(), Api.headers(token)).value()
            val ss = api.client.listRemoteSessions(ListRemoteSessionsRequest.getDefaultInstance(), Api.headers(token)).value()
            // Forget what the server no longer has, e.g. a machine another
            // device removed while this one was offline.
            val mids = ms.machinesList.map { it.id }.toSet()
            val sids = ss.sessionsList.map { it.sessionId }.toSet()
            for (id in machines.keys.toList()) if (id !in mids && !pushed(machinePushes, mp, id)) dropMachine(id)
            for (id in sessions.keys.toList()) {
                if (id !in sids && !pushed(sessionPushes, sp, id)) {
                    sessions.remove(id)
                    sessionKeys.remove(id)
                    cache?.removeSession(id)
                }
            }
            for (m in ms.machinesList) {
                if (pushed(removedPushes, rp, m.id)) continue
                putMachine(m, staleOnline = pushed(machinePushes, mp, m.id))
            }
            for (s in ss.sessionsList) if (!pushed(removedPushes, rp, s.machineId)) putSession(s)
            loaded = true
            publish()
        } catch (e: Exception) {
            report(e)
        }
    }

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

    private suspend fun putSession(rs: RemoteSession, save: Boolean = true) {
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
            if (rs.hasMeta()) {
                val key = sessionKeys[rs.sessionId]!!
                val (h, info) = Envelopes.openMessage(
                    key, rs.meta,
                    Place(PayloadKind.PAYLOAD_KIND_SESSION_META, machineId = rs.machineId, sessionId = rs.sessionId),
                    Session::parseFrom,
                )
                if (ucmp(h.seq, view.metaRev) >= 0) {
                    view = view.copy(info = info, metaRev = h.seq)
                    store = true
                }
            }
            sessions[rs.sessionId] = view
            if (save && store) cache?.putSession(rs)
        } catch (e: Exception) {
            report(e)
        }
        publish()
    }

    // --- events ---

    /** A session's events; loads them from the cache and the server on first use. */
    fun events(sid: String): StateFlow<SessionEvents> {
        val flow = eventFlows.getOrPut(sid) { MutableStateFlow(SessionEvents()) }
        scope.launch {
            if (buffers[sid] != null) return@launch
            val b = Buffer()
            buffers[sid] = b
            val cached = try {
                cache?.events(sid).orEmpty()
            } catch (e: Exception) {
                report(e)
                emptyList()
            }
            if (cached.isNotEmpty()) ingest(sid, cached, fillGaps = false, save = false)
            fetchEvents(sid)
        }
        return flow
    }

    /** Fetches everything after the last contiguous event. */
    private suspend fun fetchEvents(sid: String) {
        val b = buffers[sid] ?: return
        if (sessions[sid] == null) return
        if (b.fetching) {
            b.refetch = true
            return
        }
        b.fetching = true
        try {
            do {
                b.refetch = false
                while (true) {
                    val before = b.events.size
                    val res = api.client.getEvents(
                        GetEventsRequest.newBuilder().setSessionId(sid).setAfterSeq(before.toLong()).setLimit(500).build(),
                        Api.headers(token),
                    ).value()
                    ingest(sid, res.eventsList, fillGaps = false, save = true)
                    // An event that does not open stops progress; asking for the
                    // same page again would not help.
                    if (!res.more || b.events.size == before) break
                }
            } while (b.refetch)
            b.loaded = true
        } catch (e: Exception) {
            report(e)
        } finally {
            b.fetching = false
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
            } catch (e: Exception) {
                report(e)
                b.error = "有一条记录读不出来，历史停在这里，后面的内容和状态可能不全"
                break
            }
        }
        while (true) {
            val next = b.events.size.toLong() + 1
            val ev = b.pending.remove(next) ?: break
            b.events += ev
        }
        if (save && opened.isNotEmpty()) {
            try {
                cache?.putEvents(sid, opened)
            } catch (e: Exception) {
                report(e)
            }
        }
        // The cache write suspended: newer metadata may have arrived, or the
        // session gone. Only the event progress is ours to update.
        sessions[sid]?.let { cur -> sessions[sid] = cur.copy(lastSeq = umax(cur.lastSeq, b.events.size.toLong())) }
        if (fillGaps && b.pending.isNotEmpty()) scope.launch { fetchEvents(sid) }
        publishEvents(sid, b)
        publish()
    }

    private fun publishEvents(sid: String, b: Buffer) {
        eventFlows.getOrPut(sid) { MutableStateFlow(SessionEvents()) }.value = SessionEvents(ArrayList(b.events), b.loaded, b.error)
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
        val env = Envelopes.seal(
            keys.command,
            Header.newBuilder()
                .setKind(PayloadKind.PAYLOAD_KIND_COMMAND)
                .setMachineId(machineId)
                .setSessionId(sessionId)
                .setRequestId(requestId)
                .setDeadlineMs(deadline)
                .setSender(account.deviceId)
                .build(),
            cmd,
        )
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
     * Forwards a prepared command and returns the opened result. Transport
     * failures are retried with the same request ID, which the daemon runs at
     * most once; when the outcome stays unknown the error says so.
     */
    suspend fun forward(p: PreparedCommand, retryOfUnknown: Boolean = false): CommandResult {
        // A command whose outcome is unknown is sent even when expired: the
        // daemon may still have its result in its log.
        if (!retryOfUnknown && clock() >= p.deadlineMs) throw CommandException(CommandException.EXPIRED, "这条指令已过期，没有发出")
        var attempt = 0
        // Once a try may have reached the daemon, a later try that surely did
        // not (machine gone, network down) says nothing about that one: the
        // outcome stays unknown.
        var mayHaveRun = retryOfUnknown
        fun notSent(code: String, text: String) =
            if (mayHaveRun) CommandException(CommandException.UNKNOWN, "不确定电脑有没有执行：$text") else CommandException(code, text)
        while (true) {
            try {
                if (token.isEmpty()) token = login(api, account)
                val res = api.client.forward(
                    ForwardRequest.newBuilder().setMachineId(p.machineId).setCommand(p.envelope).build(),
                    Api.headers(token),
                ).value()
                val (_, msg) = Envelopes.openMessage(
                    keys.command, res.result,
                    Place(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT, machineId = p.machineId, sessionId = p.sessionId, requestId = p.requestId),
                    CommandResult::parseFrom,
                )
                if (msg.errorCode.isNotEmpty()) throw CommandException(msg.errorCode, msg.errorMessage.ifEmpty { msg.errorCode })
                return msg
            } catch (ce: ConnectException) {
                val cause = ce.exception
                if (cause is java.net.UnknownHostException || cause is java.net.ConnectException) {
                    throw notSent("network", "网络不通，指令没有发出")
                }
                if (ce.code == Code.UNAUTHENTICATED && attempt < 3) {
                    attempt++
                    token = login(api, account)
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
                if (ce.code == Code.UNAVAILABLE && cause == null) throw notSent(CommandException.UNAVAILABLE, "电脑不在线，指令没有发出")
                if (unknown || ce.code == Code.UNAVAILABLE) {
                    throw CommandException(CommandException.UNKNOWN, "不确定电脑有没有执行：${errorText(ce)}")
                }
                throw CommandException(ce.code.codeName, errorText(ce))
            }
        }
    }

    /** The models a machine's CLI offers, fetched once per run. */
    suspend fun listModels(machineId: String, agent: Agent = Agent.AGENT_UNSPECIFIED): ListModelsResponse {
        val d = withContext(dispatcher) {
            val k = "$machineId/${agent.number}"
            models[k]?.takeUnless { it.isCompleted && it.getCompletionExceptionOrNull() != null } ?: scope.async {
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
            publish()
        }
    }

    /** The bearer token of the current login, for services used outside this class. */
    suspend fun currentToken(): String {
        if (token.isEmpty()) token = login(api, account)
        return token
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
        val ws = http.newWebSocket(
            Request.Builder().url(url).build(),
            object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) {
                    val hello = TermHello.newBuilder().setToken(tok).setStreamId(streamId).setMachineId(s.machineId).build()
                    webSocket.send(hello.toByteArray().toByteString())
                    opened.complete(Unit)
                }

                override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
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
