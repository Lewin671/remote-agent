package io.github.lewin671.remoteagent.core.transport

import com.connectrpc.ProtocolClientConfig
import com.connectrpc.impl.ProtocolClient
import com.connectrpc.okhttp.ConnectOkHttpClient
import com.connectrpc.protocols.NetworkProtocol
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.DeviceSecurity
import io.github.lewin671.remoteagent.core.DeviceSecurityClient
import io.github.lewin671.remoteagent.core.LiteProtobufStrategy
import io.github.lewin671.remoteagent.core.PROTOCOL_VERSION
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.SigningKey
import io.github.lewin671.remoteagent.core.TestSecurity
import io.github.lewin671.remoteagent.core.TestServer
import io.github.lewin671.remoteagent.core.approvedClient
import io.github.lewin671.remoteagent.core.login
import io.github.lewin671.remoteagent.core.newAuthorityAccount
import io.github.lewin671.remoteagent.core.peer.Wire
import io.github.lewin671.remoteagent.core.value
import io.github.lewin671.remoteagent.proto.BeginImageUploadResponse
import io.github.lewin671.remoteagent.proto.Capabilities
import io.github.lewin671.remoteagent.proto.CapabilitySnapshot
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CommandResult
import io.github.lewin671.remoteagent.proto.CompleteImageUploadResponse
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.DirectEventSubscription
import io.github.lewin671.remoteagent.proto.DirectedPeerFrame
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.ForwardReply
import io.github.lewin671.remoteagent.proto.GetImageUploadStatusResponse
import io.github.lewin671.remoteagent.proto.GetLiveSnapshotRequest
import io.github.lewin671.remoteagent.proto.GetSharedFileChunkResponse
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.ImageFormat
import io.github.lewin671.remoteagent.proto.ImageUploadState
import io.github.lewin671.remoteagent.proto.ImageUploadStatus
import io.github.lewin671.remoteagent.proto.LiveSnapshot
import io.github.lewin671.remoteagent.proto.MachineCapabilities
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.MachineServiceClient
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.PeerFrame
import io.github.lewin671.remoteagent.proto.PutImageChunkResponse
import io.github.lewin671.remoteagent.proto.PutMachineRequest
import io.github.lewin671.remoteagent.proto.PutSessionRequest
import io.github.lewin671.remoteagent.proto.ServerFrame
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.StreamHello
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString.Companion.toByteString
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters
import org.junit.Assert.*
import org.junit.Test
import java.security.MessageDigest
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicInteger

private class FakeDataChannel(
    val id: Int,
    private val listener: RtcDataChannelListener,
    private val peerChannel: () -> FakeDataChannel?,
    openNow: Boolean = false,
) : RtcDataChannel {
    @Volatile var currentState: RtcChannelState = if (openNow) RtcChannelState.OPEN else RtcChannelState.CONNECTING
    @Volatile var buffered: Long = 0L
    @Volatile var closed = false
    @Volatile var holdSend = false
    val sentFrames = CopyOnWriteArrayList<ByteArray>()

    override fun state(): RtcChannelState = currentState
    override fun bufferedAmount(): Long = buffered

    fun markOpen() {
        if (closed) return
        currentState = RtcChannelState.OPEN
        listener.onStateChange()
    }

    fun deliver(bytes: ByteArray) {
        if (closed || currentState != RtcChannelState.OPEN) return
        listener.onMessage(bytes)
    }

    fun updateBufferedAmount(newAmount: Long) {
        val prev = buffered
        buffered = newAmount
        listener.onBufferedAmountChange(prev)
    }

    override fun send(data: ByteArray, binary: Boolean): Boolean {
        if (closed || currentState != RtcChannelState.OPEN || holdSend) return false
        sentFrames += data.copyOf()
        peerChannel()?.deliver(data.copyOf())
        return true
    }

    override fun close() {
        if (closed) return
        closed = true
        currentState = RtcChannelState.CLOSED
        listener.onStateChange()
    }
}

private class FakeRtcConnection(
    val id: Int,
    private val roleTag: String,
    private val listener: RtcPeerListener,
    private val byUfrag: MutableMap<String, FakeRtcConnection>,
    private val srflx: Boolean = true,
) : RtcPeerConnection {
    val ufrag = "ufrag$roleTag$id"
    val localChannels = HashMap<Int, FakeDataChannel>()
    @Volatile private var remotePeer: FakeRtcConnection? = null
    @Volatile var closed = false
    @Volatile var connState: RtcPeerState = RtcPeerState.NEW
    var autoConnectOnAnswer = true

    init {
        byUfrag[ufrag] = this
    }

    override fun createDataChannel(
        label: String,
        id: Int,
        ordered: Boolean,
        maxRetransmits: Int?,
        listener: RtcDataChannelListener,
    ): RtcDataChannel {
        val ch = FakeDataChannel(id, listener, { remotePeer?.localChannels?.get(id) })
        localChannels[id] = ch
        val remote = remotePeer
        if (connState == RtcPeerState.CONNECTED && remote?.localChannels?.get(id) != null) {
            ch.markOpen()
            remote.localChannels[id]?.markOpen()
        }
        return ch
    }

    override suspend fun createOffer(iceRestart: Boolean): String {
        val hex = id.toString(16).padStart(2, '0').uppercase()
        val fp = List(32) { hex }.joinToString(":")
        return "v=0\r\no=- $id 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 127.0.0.1\r\na=ice-ufrag:$ufrag\r\na=ice-pwd:passwordpasswordpassword$id\r\na=fingerprint:sha-256 $fp\r\na=setup:actpass\r\na=mid:0\r\na=sctp-port:5000\r\n"
    }

    override suspend fun createAnswer(): String {
        val hex = (id + 128).mod(256).toString(16).padStart(2, '0').uppercase()
        val fp = List(32) { hex }.joinToString(":")
        return "v=0\r\no=- $id 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 127.0.0.1\r\na=ice-ufrag:$ufrag\r\na=ice-pwd:passwordpasswordpassword$id\r\na=fingerprint:sha-256 $fp\r\na=setup:active\r\na=mid:0\r\na=sctp-port:5000\r\n"
    }

    override suspend fun setLocalDescription(offer: Boolean, sdp: String) {
        listener.onIceCandidate(
            if (srflx) RtcIceCandidate(
                candidate = "candidate:2 1 udp 1686052607 203.0.113.1 ${51000 + id} typ srflx raddr 127.0.0.1 rport ${50000 + id} ufrag $ufrag",
                sdpMid = "0",
                sdpMLineIndex = 0,
                isSrflx = true,
            ) else RtcIceCandidate(
                candidate = "candidate:1 1 udp 2122260223 192.0.2.1 ${51000 + id} typ host ufrag $ufrag",
                sdpMid = "0",
                sdpMLineIndex = 0,
                isSrflx = false,
            )
        )
        listener.onIceGatheringComplete()
    }

    override suspend fun setRemoteDescription(offer: Boolean, sdp: String) {
        val peerUfrag = Regex("a=ice-ufrag:(\\S+)").find(sdp)?.groupValues?.get(1)
        if (peerUfrag != null) {
            val found = byUfrag[peerUfrag]
            if (found != null) {
                remotePeer = found
                found.remotePeer = this
            }
        }
        if (!offer && autoConnectOnAnswer) {
            markConnected()
            remotePeer?.markConnected()
        }
    }

    override suspend fun addIceCandidate(
        candidate: String,
        sdpMid: String,
        sdpMLineIndex: Int,
        usernameFragment: String,
    ) = Unit

    fun markConnected() {
        if (closed) return
        connState = RtcPeerState.CONNECTED
        listener.onConnectionStateChange(RtcPeerState.CONNECTED)
        val remote = remotePeer
        for ((cid, ch) in localChannels.entries.toList()) {
            val peerCh = remote?.localChannels?.get(cid)
            if (peerCh != null) {
                ch.markOpen()
                peerCh.markOpen()
            }
        }
    }

    fun dropAllChannels() {
        localChannels.values.toList().forEach { it.close() }
        listener.onConnectionStateChange(RtcPeerState.DISCONNECTED)
    }

    fun emitStaleState(state: RtcPeerState) {
        listener.onConnectionStateChange(state)
    }

    fun emitCandidate(cand: RtcIceCandidate) {
        listener.onIceCandidate(cand)
    }

    override fun close() {
        if (closed) return
        closed = true
        connState = RtcPeerState.CLOSED
        localChannels.values.toList().forEach { it.close() }
        listener.onConnectionStateChange(RtcPeerState.CLOSED)
    }
}

private class FakeRtcBridge {
    private val idGen = AtomicInteger(1)
    private val byUfrag = java.util.concurrent.ConcurrentHashMap<String, FakeRtcConnection>()
    val phonePcs = CopyOnWriteArrayList<FakeRtcConnection>()
    val daemonPcs = CopyOnWriteArrayList<FakeRtcConnection>()

    /** False: the connections gather host candidates only, as a pair does whose STUN is unreachable. */
    @Volatile var srflx = true

    val phoneFactory = RtcFactory { _, listener ->
        FakeRtcConnection(idGen.getAndIncrement(), "p", listener, byUfrag, srflx).also { phonePcs += it }
    }

    val daemonFactory = RtcFactory { _, listener ->
        FakeRtcConnection(idGen.getAndIncrement(), "d", listener, byUfrag, srflx).also { daemonPcs += it }
    }
}

/** Minimal daemon-side direct + relay peer running against the real Go TestServer. */
private class DirectBridgeDaemon(
    val machine: Account,
    private val serverUrl: String,
    private val http: OkHttpClient,
    private val clock: () -> Long,
    private val daemonFactory: RtcFactory,
) : AutoCloseable {
    private val store = TestSecurity.get(serverUrl).stores.getValue(machine.deviceId)
    private val local = DeviceSecurityClient(store, machine.deviceId, SigningKey(machine.deviceSeed), machine.accountId) { after ->
        val r = store.readSecurity()
        io.github.lewin671.remoteagent.proto.GetSecurityStatesResponse.newBuilder()
            .setHeadEpoch(r.head.core.epoch.toLong())
            .setLease(ByteString.copyFrom(r.lease?.bytes() ?: byteArrayOf()))
            .apply {
                for (state in r.chain.filter { it.core.epoch > after }) {
                    val sb = io.github.lewin671.remoteagent.proto.SecurityState.newBuilder()
                        .setState(ByteString.copyFrom(DeviceSecurity.stateBytes(state)))
                    r.proofs[state.core.epoch]?.let { sb.setRecovery(ByteString.copyFrom(it)) }
                    addStates(sb)
                }
            }
            .build()
    }

    private fun purposeOf(kind: PayloadKind): DeviceSecurity.Purpose =
        DeviceSecurity.Purpose.entries.first { it.value == kind.number.toUInt() }

    private fun seal(h: Header, plain: ByteArray, seed: ByteArray? = null): Envelope =
        local.seal(purposeOf(h.kind), h.machineId, h.sessionId, h.requestId, h.seq.toULong(), h.deadlineMs.toULong(), plain, seed)

    private fun open(env: Envelope): Pair<Header, ByteArray> {
        val r = store.readSecurity()
        val w = DeviceSecurity.fromEnvelope(env)
        val canonical = DeviceSecurity.parseHeader(w.formed.header)
        val s = r.stateAt(canonical.epoch) ?: throw DeviceSecurity.Failure("unknown state")
        val key = r.contentKey(s.core.contentEpoch) ?: throw DeviceSecurity.Failure("unavailable")
        val seed = if (canonical.purpose.sessionKey) sessionKey else null
        val (h, plain) = DeviceSecurity.open(s, r.head, canonical.purpose.control, maxOf(clock().coerceAtLeast(0L).toULong(), r.floor), w, key, seed)
        return Header.newBuilder()
            .setVersion(DeviceSecurity.VERSION).setKindValue(h.kind.toInt()).setMachineId(h.machine)
            .setSessionId(h.session).setRequestId(h.request).setSeq(h.seq.toLong())
            .setDeadlineMs(h.deadline.toLong()).setSender(h.sender).setCompressionValue(h.compression.toInt())
            .build() to plain
    }
    private var token = ""
    private var ws: WebSocket? = null
    private var metaRev = 0L
    private val sessionKey = ByteArray(32) { 0x55 }
    private val sid = "s-direct"

    val relayCommands = CopyOnWriteArrayList<Command>()
    val directCommands = CopyOnWriteArrayList<Command>()
    var chunkSizeBytes = 512 shl 10
    var onCommand: (Command) -> Unit = {}
    @Volatile var dropDirectOnNextPut = false
    var onDropDirect: () -> Unit = {}

    private var daemonSession: DirectSession? = null
    private val paths = HashMap<Int, DirectChannel>()
    private val budget = DirectBudget()
    private val daemonInstance = ByteArray(16) { 0x11 }
    private val daemonBoot = ByteArray(32) { 0x22 }
    private val daemonScope = CoroutineScope(SupervisorJob() + Dispatchers.IO.limitedParallelism(1))

    private val api = MachineServiceClient(
        ProtocolClient(
            ConnectOkHttpClient(http),
            ProtocolClientConfig(
                host = serverUrl,
                serializationStrategy = LiteProtobufStrategy,
                networkProtocol = NetworkProtocol.CONNECT,
            ),
        )
    )

    suspend fun start() {
        token = login(Api(machine.server, http), machine)
        metaRev++
        val meta = MachineMeta.newBuilder()
            .setHostname("direct-mac.example")
            .setOs("darwin")
            .setHome("/Users/test")
            .setProtocolVersion(PROTOCOL_VERSION)
            .setCapabilities(
                MachineCapabilities.newBuilder()
                    .setImageUpload(true)
                    .setFileUpload(true)
                    .setMaxUploadBytes(256L shl 20)
                    .setMaxChunkBytes(512 shl 10)
                    .setSharedFileChunkBytes(512 shl 10)
                    .build()
            )
            .build()
        val mEnv = seal(Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_MACHINE_META).setMachineId(machine.deviceId).setSender(machine.deviceId).setSeq(metaRev).build(), meta.toByteArray())
        api.putMachine(PutMachineRequest.newBuilder().setMeta(mEnv).build(), Api.headers(token)).value()

        val kEnv = seal(Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_SESSION_KEY).setMachineId(machine.deviceId).setSessionId(sid).setSender(machine.deviceId).setSeq(1).build(), sessionKey)
        val sInfo = Session.newBuilder().setId(sid).setCwd("/tmp/work").setTitle("direct-test").setStatus(SessionStatus.SESSION_STATUS_IDLE).build()
        val sMeta = seal(Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_SESSION_META).setMachineId(machine.deviceId).setSessionId(sid).setSender(machine.deviceId).setSeq(1).build(), sInfo.toByteArray())
        api.putSession(PutSessionRequest.newBuilder().setSessionId(sid).setKey(kEnv).setMeta(sMeta).setEndedMs(0).build(), Api.headers(token)).value()

        val wsUrl = serverUrl.replaceFirst(Regex("^http"), "ws") + "/v1/stream"
        val ready = CompletableDeferred<Unit>()
        ws = http.newWebSocket(Request.Builder().url(wsUrl).build(), object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                webSocket.send(PeerFrame.newBuilder().setHello(StreamHello.newBuilder().setToken(token)).build().toByteArray().toByteString())
            }

            override fun onMessage(webSocket: WebSocket, bytes: okio.ByteString) {
                val f = ServerFrame.parseFrom(bytes.toByteArray())
                if (f.hasReady()) {
                    ready.complete(Unit)
                    return
                }
                if (f.hasForwardCall()) {
                    val call = f.forwardCall
                    val (h, pt) = open(call.command)
                    val cmd = Command.parseFrom(pt)
                    relayCommands += cmd
                    val res = handleCommand(cmd)
                    val binding = DeviceSecurity.requestIdentity(DeviceSecurity.parseHeader(call.command.securityHeader.toByteArray()), pt)
                    val proof = res.toBuilder().setRequestKeyHash(ByteString.copyFrom(binding.first)).setPayloadHash(ByteString.copyFrom(binding.second)).setOutcome("executed").setReason("completed").setResultAvailable(true).build()
                    val rEnv = seal(Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT).setMachineId(machine.deviceId).setSessionId(h.sessionId).setRequestId(h.requestId).setSender(machine.deviceId).build(), proof.toByteArray())
                    webSocket.send(PeerFrame.newBuilder().setForwardReply(ForwardReply.newBuilder().setCallId(call.callId).setResult(rEnv).setOutcome("executed").setReason("completed").setAttemptId(call.attemptId)).build().toByteArray().toByteString())
                    return
                }
                if (f.hasPeerFrameDelivery()) {
                    handlePeerDelivery(f.peerFrameDelivery.toByteArray(), webSocket)
                }
            }
        })
        ready.await()
    }

    private fun handlePeerDelivery(raw: ByteArray, socket: WebSocket) = daemonScope.launch {
        val r = store.readSecurity()
        val xPriv = r.peerBoxKey()
        val xPub = X25519PrivateKeyParameters(xPriv, 0).generatePublicKey().encoded
        val edPub = Ed25519PrivateKeyParameters(machine.deviceSeed, 0).generatePublicKey().encoded
        val selfId = Wire.Identity(Wire.utf8(machine.deviceId), 2L, daemonInstance, daemonBoot, edPub, xPub, machine.deviceSeed, xPriv)

        val f = Wire.parse(raw, 5)
        val senderIdStr = Wire.text(Wire.getBytes(f, 1))
        val phoneMem = r.head.core.member(senderIdStr) ?: return@launch
        val peerId = Wire.Identity(Wire.utf8(senderIdStr), 1L, ByteArray(0), ByteArray(0), phoneMem.ed, phoneMem.x, null, null)
        val codec = Wire(Wire.utf8(r.pin.account), r.pin.domain, DeviceSecurity.stateHash(r.head), r.head.core.epoch.toLong())

        val opened = runCatching { codec.open(raw, peerId, selfId, clock()) }.getOrNull()
        if (opened != null && opened.ctx.cls == 1L && opened.type == 1L) {
            peerId.instance = opened.ctx.senderInstance.copyOf()
            peerId.boot = opened.ctx.senderBoot.copyOf()
            daemonSession?.close()
            lateinit var sess: DirectSession
            sess = DirectSession(
                DirectSessionOptions(
                    self = selfId,
                    peer = peerId,
                    codec = codec,
                    connection = opened.ctx.connection.copyOf(),
                    deadline = opened.ctx.deadline,
                    budget = budget,
                    stun = emptyList(),
                    factory = daemonFactory,
                    valid = { true },
                    authorize = {},
                    send = { out ->
                        val df = Wire.parse(out.raw, 5)
                        val directed = DirectedPeerFrame.newBuilder()
                            .setToDevice(ByteString.copyFrom(Wire.getBytes(df, 2)))
                            .setFrameClassValue(Wire.getUint(df, 3).toInt())
                            .setFrameId(ByteString.copyFrom(Wire.getBytes(df, 4)))
                            .setSealed(ByteString.copyFrom(Wire.getBytes(df, 5)))
                            .build()
                        socket.send(PeerFrame.newBuilder().setDirectedPeerFrame(directed).build().toByteArray().toByteString())
                    },
                    chosen = {},
                    beforeOffer = {},
                    ready = { use, ch ->
                        paths[use] = ch
                        if (use == 2) sendCapabilities(codec, selfId, peerId, opened.ctx.connection.copyOf(), socket)
                    },
                    closed = { use -> if (use != null) paths.remove(use) else paths.clear() },
                    frame = { frameBytes, use ->
                        val env = Envelope.parseFrom(frameBytes)
                        val (h, pt) = open(env)
                        val cmd = Command.parseFrom(pt)
                        directCommands += cmd
                        if ((cmd.hasPutImageChunk() || cmd.hasSend()) && dropDirectOnNextPut) {
                            dropDirectOnNextPut = false
                            onDropDirect()
                            return@DirectSessionOptions
                        }
                        val res = handleCommand(cmd)
                        val binding = DeviceSecurity.requestIdentity(DeviceSecurity.parseHeader(env.securityHeader.toByteArray()), pt)
                        val proof = res.toBuilder().setRequestKeyHash(ByteString.copyFrom(binding.first)).setPayloadHash(ByteString.copyFrom(binding.second)).setOutcome("executed").setReason("completed").setResultAvailable(true).build()
                        val rEnv = seal(Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT).setMachineId(machine.deviceId).setSessionId(h.sessionId).setRequestId(h.requestId).setSender(machine.deviceId).build(), proof.toByteArray())
                        CoroutineScope(Dispatchers.IO).launch {
                            paths[use]?.write(rEnv.toByteArray()) {}
                        }
                    },
                    capability = {},
                    clock = clock,
                )
            )
            daemonSession = sess
            sess.receive(raw)
        } else {
            daemonSession?.receive(raw)
        }
    }

    private fun sendCapabilities(
        codec: Wire,
        selfId: Wire.Identity,
        peerId: Wire.Identity,
        conn: ByteArray,
        socket: WebSocket,
    ) {
        val expires = clock() + 30_000L
        fun emitControl(type: Long, seq: Long, payload: ByteArray) {
            val ctx = Wire.Context(selfId, peerId, conn, 0L, 1L, seq, clock(), 2L, false)
            val raw = codec.seal(selfId, peerId, ctx, type, payload, null, null, null)
            val df = Wire.parse(raw, 5)
            val directed = DirectedPeerFrame.newBuilder()
                .setToDevice(ByteString.copyFrom(Wire.getBytes(df, 2)))
                .setFrameClassValue(Wire.getUint(df, 3).toInt())
                .setFrameId(ByteString.copyFrom(Wire.getBytes(df, 4)))
                .setSealed(ByteString.copyFrom(Wire.getBytes(df, 5)))
                .build()
            socket.send(PeerFrame.newBuilder().setDirectedPeerFrame(directed).build().toByteArray().toByteString())
        }
        val capsControl = Wire.pb(
            Wire.b(1, ByteArray(16) { 4 }),
            Wire.u(2, 1L),
            Wire.u(2, 14L),
            Wire.u(3, 1L),
            Wire.u(3, 9L),
            Wire.u(4, expires),
            Wire.u(5, 1L),
            Wire.u(6, 1048576L),
        )
        emitControl(1L, 1L, capsControl)
        val caps = MachineCapabilities.newBuilder()
            .setImageUpload(true)
            .setFileUpload(true)
            .setMaxUploadBytes(256L shl 20)
            .setMaxChunkBytes(512 shl 10)
            .setSharedFileChunkBytes(512 shl 10)
            .build()
        val snap = CapabilitySnapshot.newBuilder()
            .setCapabilityId(ByteString.copyFrom(ByteArray(16) { 5 }))
            .setCapabilities(caps)
            .setExpiresAtMs(expires)
            .build()
        emitControl(14L, 2L, snap.toByteArray())
    }

    fun sealRelayExecuted(reqEnv: Envelope, attemptId: String): io.github.lewin671.remoteagent.proto.ForwardResponse {
        val (h, pt) = open(reqEnv)
        val cmd = Command.parseFrom(pt)
        val res = handleCommand(cmd)
        val binding = DeviceSecurity.requestIdentity(DeviceSecurity.parseHeader(reqEnv.securityHeader.toByteArray()), pt)
        val proof = res.toBuilder()
            .setRequestKeyHash(ByteString.copyFrom(binding.first))
            .setPayloadHash(ByteString.copyFrom(binding.second))
            .setOutcome("executed")
            .setReason("completed")
            .setResultAvailable(true)
            .build()
        val rEnv = seal(
            Header.newBuilder()
                .setKind(PayloadKind.PAYLOAD_KIND_COMMAND_RESULT)
                .setMachineId(machine.deviceId)
                .setSessionId(h.sessionId)
                .setRequestId(h.requestId)
                .setSender(machine.deviceId)
                .build(),
            proof.toByteArray(),
        )
        return io.github.lewin671.remoteagent.proto.ForwardResponse.newBuilder()
            .setResult(rEnv)
            .setOutcome("executed")
            .setReason("completed")
            .setAttemptId(attemptId)
            .build()
    }

    private fun handleCommand(cmd: Command): CommandResult {
        onCommand(cmd)
        val b = CommandResult.newBuilder()
        when (cmd.kindCase) {
            Command.KindCase.SEND -> b.setSend(io.github.lewin671.remoteagent.proto.SendResponse.newBuilder().setQueuePosition(1).build())
            Command.KindCase.GET_LIVE_SNAPSHOT -> b.setLiveSnapshot(LiveSnapshot.newBuilder().setSession(Session.newBuilder().setId(sid).build()).build())
            Command.KindCase.GET_SHARED_FILE_CHUNK -> {
                val data = ByteArray(chunkSizeBytes) { (cmd.getSharedFileChunk.offset.toInt() + it).toByte() }
                b.setGetSharedFileChunk(GetSharedFileChunkResponse.newBuilder().setData(ByteString.copyFrom(data)).setTotalBytes(32L shl 20).build())
            }
            Command.KindCase.BEGIN_IMAGE_UPLOAD -> {
                b.setBeginImageUpload(BeginImageUploadResponse.newBuilder().setStatus(
                    ImageUploadStatus.newBuilder().setTransferId(cmd.beginImageUpload.transferId).setState(ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING).setPersistedBytes(0).build()
                ))
            }
            Command.KindCase.PUT_IMAGE_CHUNK -> b.setPutImageChunk(PutImageChunkResponse.newBuilder().setStatus(
                ImageUploadStatus.newBuilder().setTransferId(cmd.putImageChunk.transferId).setState(ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING).setPersistedBytes(cmd.putImageChunk.offset + cmd.putImageChunk.data.size()).build()
            ))
            Command.KindCase.GET_IMAGE_UPLOAD_STATUS -> b.setGetImageUploadStatus(GetImageUploadStatusResponse.newBuilder().setStatus(
                ImageUploadStatus.newBuilder().setTransferId(cmd.getImageUploadStatus.transferId).setState(ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING).setPersistedBytes(chunkSizeBytes.toLong()).build()
            ))
            Command.KindCase.COMPLETE_IMAGE_UPLOAD -> b.setCompleteImageUpload(CompleteImageUploadResponse.newBuilder().setStatus(
                ImageUploadStatus.newBuilder().setTransferId(cmd.completeImageUpload.transferId).setState(ImageUploadState.IMAGE_UPLOAD_STATE_COMPLETE).build()
            ))
            Command.KindCase.GET_MACHINE_STATUS -> b.setMachineStatus(io.github.lewin671.remoteagent.proto.MachineStatus.getDefaultInstance())
            else -> b.setErrorCode("unimplemented")
        }
        return b.build()
    }

    override fun close() {
        daemonSession?.close()
        ws?.close(1000, null)
    }
}

class DirectTransportTest {
    private suspend fun awaitCondition(@Suppress("UNUSED_PARAMETER") what: String, @Suppress("UNUSED_PARAMETER") timeoutMs: Long, condition: () -> Boolean) {
        while (!condition()) {
            delay(25)
        }
    }

    private fun sha256(bytes: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(bytes)

    @Test
    fun directBudgetRejectsWrongOwnerAndNonBlockingCloseReleasesReservation() {
        val budget = DirectBudget()
        assertTrue(budget.acquire("m1", "owner-1", 1))
        assertFalse(budget.ownerAvailable("m1", "owner-2"))
        // Different owner or use cannot reserve or steal m1's quota.
        assertFalse(budget.reserve("m1", "owner-2", 1, 0, 100, 1))
        assertFalse(budget.reserve("m1", "owner-1", 2, 0, 100, 1))
        assertTrue(budget.reserve("m1", "owner-1", 1, 0, 1 shl 20, 1))
        assertFalse(budget.reserve("m1", "owner-1", 1, 0, (1 shl 20) + 1, 1))
        budget.free("m1", 0, 1 shl 20, 1)
        budget.release("m1", 1)
        assertTrue(budget.ownerAvailable("m1", "owner-2"))

        var pc: FakeRtcConnection? = null
        val factory = RtcFactory { _, listener ->
            FakeRtcConnection(1, "t", listener, HashMap()).also { pc = it }
        }
        val dc = DirectChannel(
            DirectChannelOptions(
                peer = "m1",
                connection = ByteArray(16) { 1 },
                use = 1,
                generation = 1L,
                deadline = System.currentTimeMillis() + 5_000L,
                budget = budget,
                valid = { true },
                stun = emptyList(),
                factory = factory,
                candidate = {},
                state = {},
                frame = {},
                closed = {},
            )
        )
        pc!!.markConnected()
        val fakeCh = pc!!.localChannels.values.first()
        fakeCh.markOpen()
        fakeCh.updateBufferedAmount(2 shl 20) // Force write() to wait for drain.

        val blockedWrite = CompletableDeferred<Throwable?>()
        Thread {
            runBlocking {
                blockedWrite.complete(runCatching { dc.write(ByteArray(128)) {} }.exceptionOrNull())
            }
        }.start()
        Thread.sleep(60)
        val closeStart = System.nanoTime()
        dc.close()
        val elapsedMs = (System.nanoTime() - closeStart) / 1_000_000
        assertTrue("close() must not block on buffered-amount waiters, took ${elapsedMs}ms", elapsedMs < 250)
        runBlocking { assertTrue(blockedWrite.await() is PathChanged) }
    }

    @Test
    fun aSignalStepInsideTheFreshnessWindowDoesNotSyncAgain() = runBlocking {
        var syncs = 0
        var fresh = true
        assertTrue(authorizeUnlessFresh({ fresh }, { syncs++ }))
        assertEquals("a fresh window needs no relay round trip", 0, syncs)
        fresh = false
        assertFalse(authorizeUnlessFresh({ fresh }, { syncs++ }))
        assertEquals("a stale window syncs once and still refuses", 1, syncs)
        assertTrue(authorizeUnlessFresh({ fresh }, { syncs++; fresh = true }))
        assertEquals(2, syncs)
    }

    private fun awaitLine(part: String, ms: Long = 2_000L): String? {
        val until = System.currentTimeMillis() + ms
        while (System.currentTimeMillis() < until) {
            DirectDiag.lines().lastOrNull { it.contains(part) }?.let { return it }
            Thread.sleep(20)
        }
        return null
    }

    private fun diagChannel(valid: () -> Boolean, deadline: Long, pcOut: (FakeRtcConnection) -> Unit = {}): DirectChannel {
        val factory = RtcFactory { _, listener -> FakeRtcConnection(1, "t", listener, HashMap()).also(pcOut) }
        return DirectChannel(
            DirectChannelOptions(
                peer = "secret-peer", connection = ByteArray(16) { 1 }, use = 2, generation = 7L, deadline = deadline,
                budget = DirectBudget(), valid = valid, stun = emptyList(), factory = factory,
                candidate = {}, state = {}, frame = {}, closed = {},
            ),
        )
    }

    @Test
    fun anAttemptSaysWhyItEnded() {
        DirectDiag.clear()
        // The gate refuses: not_allowed, with the stage times and no identifiers.
        val allowed = java.util.concurrent.atomic.AtomicBoolean(true)
        val a = diagChannel({ allowed.get() }, System.currentTimeMillis() + 5_000L)
        allowed.set(false)
        val line = awaitLine("end=not_allowed")
        assertNotNull("gate loss must be named: ${DirectDiag.lines()}", line)
        assertTrue(line!!.contains("use=2 gen=7") && line.contains("connected=-") && line.contains("confirm=-"))
        assertFalse("no peer id in the line", line.contains("secret-peer"))
        a.close()

        // The SDK reports failure: pc_failed, and the first reason is kept.
        DirectDiag.clear()
        var pc: FakeRtcConnection? = null
        val b = diagChannel({ true }, System.currentTimeMillis() + 5_000L) { pc = it }
        pc!!.markConnected()
        pc!!.emitStaleState(RtcPeerState.FAILED)
        val failed = awaitLine("end=pc_failed")
        assertNotNull(DirectDiag.lines().toString(), failed)
        assertTrue("DTLS time is kept", !failed!!.contains("connected=-"))
        b.close()
        assertEquals("one line per attempt", 1, DirectDiag.lines().count { it.startsWith("attempt") })

        // Unconfirmed at the round's deadline: round_deadline.
        DirectDiag.clear()
        val c = diagChannel({ true }, System.currentTimeMillis() + 250L)
        assertNotNull(DirectDiag.lines().toString(), awaitLine("end=round_deadline"))
        c.close()
    }

    @Test
    fun theSdkIsWarmedBesideTheHandshakeNotInsideIt() {
        val warmed = java.util.concurrent.CountDownLatch(1)
        val warmThread = java.util.concurrent.atomic.AtomicReference<String>()
        val factory = object : RtcFactory {
            override fun create(stunUrls: List<String>, listener: RtcPeerListener): RtcPeerConnection = throw IllegalStateException("no connection in this test")
            override fun prewarm() { warmThread.set(Thread.currentThread().name); warmed.countDown() }
        }
        val codec = Wire(Wire.utf8("acc-1"), Wire.utf8("domain"), sha256(Wire.utf8("head")), 1L)
        val selfSeed = sha256(Wire.utf8("self"))
        val selfBox = sha256(Wire.utf8("self-box"))
        val self = Wire.Identity(
            Wire.utf8("p1"), 1L, ByteArray(16) { 1 }, ByteArray(32) { 2 },
            SigningKey(selfSeed).publicKey, X25519PrivateKeyParameters(selfBox, 0).generatePublicKey().encoded, selfSeed, selfBox,
        )
        val peer = Wire.Identity(
            Wire.utf8("m1"), 2L, ByteArray(16) { 3 }, ByteArray(32) { 4 },
            Ed25519PrivateKeyParameters(sha256(Wire.utf8("peer")), 0).generatePublicKey().encoded,
            X25519PrivateKeyParameters(sha256(Wire.utf8("peer-box")), 0).generatePublicKey().encoded, null, null,
        )
        val session = DirectSession(
            DirectSessionOptions(
                self = self, peer = peer, codec = codec, connection = ByteArray(16) { 9 }, deadline = System.currentTimeMillis() + 5_000L,
                budget = DirectBudget(), stun = emptyList(), factory = factory,
                valid = { true }, authorize = {}, send = { true }, chosen = {}, beforeOffer = {}, ready = { _, _ -> }, closed = {}, frame = { _, _ -> }, capability = {},
            ),
        )
        assertTrue("prewarm was not called", warmed.await(2, java.util.concurrent.TimeUnit.SECONDS))
        assertFalse("prewarm must not run on the test thread", warmThread.get() == Thread.currentThread().name)
        session.close()
    }

    @Test
    fun directRetryDelayIsFullJitterBelowADoublingCeilingAndStartsAgainAfterAStablePath() {
        var j = 1.0
        val retry = DirectRetry { j }
        val ceilings = (1..8).map { retry.delay("m", null, 0L) }
        assertEquals(listOf(1_000L, 2_000L, 4_000L, 8_000L, 16_000L, 30_000L, 30_000L, 30_000L), ceilings)
        j = 0.25
        assertEquals("full jitter: a fraction of the ceiling", 7_500L, retry.delay("m", null, 0L))
        j = 0.0
        assertEquals("and it can be nothing", 0L, retry.delay("m", null, 0L))
        j = 1.0
        // A path that stayed up 30 s starts the sequence again; one that did not, does not.
        assertEquals(30_000L, retry.delay("m", 1_000L, 30_999L))
        assertEquals(1_000L, retry.delay("m", 1_000L, 31_000L))
        assertEquals(2_000L, retry.delay("m", null, 0L))
        // A new start (main connection back, new computer process, switch turned on) clears it.
        retry.reset("m")
        assertEquals(1_000L, retry.delay("m", null, 0L))
        // Each computer has its own sequence.
        assertEquals(1_000L, retry.delay("other", null, 0L))
        retry.clear()
        assertEquals(1_000L, retry.delay("m", null, 0L))
    }

    @Test
    fun aConnectedAndConfirmedPathIsUsedWithoutAReflexiveCandidate() = runBlocking {
        val codec = Wire(Wire.utf8("acc-1"), Wire.utf8("domain"), sha256(Wire.utf8("head")), 1L)
        val selfSeed = sha256(Wire.utf8("self")); val selfBox = sha256(Wire.utf8("self-box"))
        val peerSeed = sha256(Wire.utf8("peer")); val peerBox = sha256(Wire.utf8("peer-box"))
        val self = Wire.Identity(Wire.utf8("p1"), 1L, ByteArray(16) { 1 }, ByteArray(32) { 2 }, SigningKey(selfSeed).publicKey, X25519PrivateKeyParameters(selfBox, 0).generatePublicKey().encoded, selfSeed, selfBox)
        val peerForClient = Wire.Identity(Wire.utf8("m1"), 2L, ByteArray(16) { 3 }, ByteArray(32) { 4 }, Ed25519PrivateKeyParameters(peerSeed, 0).generatePublicKey().encoded, X25519PrivateKeyParameters(peerBox, 0).generatePublicKey().encoded, null, null)
        val selfDaemon = Wire.Identity(Wire.utf8("m1"), 2L, ByteArray(16) { 3 }, ByteArray(32) { 4 }, SigningKey(peerSeed).publicKey, X25519PrivateKeyParameters(peerBox, 0).generatePublicKey().encoded, peerSeed, peerBox)
        val peerForDaemon = Wire.Identity(Wire.utf8("p1"), 1L, ByteArray(16) { 1 }, ByteArray(32) { 2 }, SigningKey(selfSeed).publicKey, X25519PrivateKeyParameters(selfBox, 0).generatePublicKey().encoded, null, null)
        val bridge = FakeRtcBridge().also { it.srflx = false }
        val conn = ByteArray(16) { 9 }
        val deadline = System.currentTimeMillis() + 5_000L
        val events = CopyOnWriteArrayList<String>()
        val want = listOf("client:open:1", "client:open:2", "daemon:open:1", "daemon:open:2")
        val allReady = CompletableDeferred<Unit>()
        lateinit var clientSession: DirectSession
        lateinit var daemonSession: DirectSession
        fun options(me: Wire.Identity, other: Wire.Identity, factory: RtcFactory, send: (ByteArray) -> Unit, tag: String) = DirectSessionOptions(
            self = me, peer = other, codec = codec, connection = conn, deadline = deadline, budget = DirectBudget(), stun = emptyList(), factory = factory,
            valid = { true }, authorize = {}, send = { out -> send(out.raw); true }, chosen = {}, beforeOffer = {},
            ready = { use, _ ->
                events += "$tag:open:$use"
                if (events.containsAll(want)) allReady.complete(Unit)
            }, closed = {}, frame = { _, _ -> }, capability = {},
        )
        daemonSession = DirectSession(options(selfDaemon, peerForDaemon, bridge.daemonFactory, { clientSession.receive(it) }, "daemon"))
        clientSession = DirectSession(options(self, peerForClient, bridge.phoneFactory, { daemonSession.receive(it) }, "client"))
        clientSession.start()
        try {
            allReady.await()
            assertTrue("both uses ready on both sides without a reflexive candidate, got $events", events.containsAll(want))
            assertTrue(clientSession.established && daemonSession.established)
        } finally {
            clientSession.close()
            daemonSession.close()
        }
    }

    @Test
    fun directRoutingReadAndBulkFallbackHysteresis() {
        var now = 10_000L
        val routing = DirectRouting(now = { now })
        val readHint = RouteHint.Read

        // Ready => direct preferred immediately, with 1-in-8 relay control sample.
        assertEquals(RouteName.DIRECT, routing.readPath())
        var relayCount = 0
        repeat(16) {
            if (routing.choose(readHint, ready = true) == RouteName.RELAY) relayCount++
        }
        assertEquals(2, relayCount)

        // Direct slower than 1.5x relay + 20ms with >=8 direct and >=4 relay in 30s -> fallback.
        repeat(8) {
            now += 100L
            routing.observe(readHint, RouteName.DIRECT, 0, 80.0)
        }
        repeat(4) {
            now += 100L
            routing.observe(readHint, RouteName.RELAY, 0, 10.0)
        }
        assertEquals(RouteName.RELAY, routing.readPath())
        assertEquals(RouteName.RELAY, routing.choose(readHint, ready = true))
        routing.fail(readHint)
        now += 1_000L
        assertEquals(RouteName.RELAY, routing.choose(readHint, ready = true))

        // Bulk also starts on direct with 1-in-8 relay sampling and does not flicker on 1/8 sample.
        val bulk = RouteHint.Bulk(file = "s1/f1")
        assertEquals(RouteName.DIRECT, routing.filePath)
        routing.observe(bulk, RouteName.DIRECT, 4 shl 20, 100.0)
        routing.observe(bulk, RouteName.RELAY, 768 shl 10, 150.0)
        assertEquals(RouteName.DIRECT, routing.filePath)
        // Only when direct rate < 70% of relay rate does bulk fall back.
        routing.observe(RouteHint.Bulk("s1/f2"), RouteName.RELAY, 8 shl 20, 1000.0)
        routing.observe(RouteHint.Bulk("s1/f2"), RouteName.DIRECT, 2 shl 20, 1000.0)
        assertEquals(RouteName.RELAY, routing.filePath)
    }

    @Test
    fun directSessionIgnoresStalePeerConnectionCallbacks() = runBlocking {
        val codec = Wire(Wire.utf8("acc-1"), Wire.utf8("domain"), sha256(Wire.utf8("head")), 1L)
        val selfSeed = sha256(Wire.utf8("self"))
        val selfBox = sha256(Wire.utf8("self-box"))
        val peerSeed = sha256(Wire.utf8("peer"))
        val peerBox = sha256(Wire.utf8("peer-box"))

        val self = Wire.Identity(
            Wire.utf8("p1"), 1L, ByteArray(16) { 1 }, ByteArray(32) { 2 },
            SigningKey(selfSeed).publicKey, X25519PrivateKeyParameters(selfBox, 0).generatePublicKey().encoded, selfSeed, selfBox
        )
        val peerForClient = Wire.Identity(
            Wire.utf8("m1"), 2L, ByteArray(16) { 3 }, ByteArray(32) { 4 },
            Ed25519PrivateKeyParameters(peerSeed, 0).generatePublicKey().encoded, X25519PrivateKeyParameters(peerBox, 0).generatePublicKey().encoded, null, null
        )
        val selfDaemon = Wire.Identity(
            Wire.utf8("m1"), 2L, ByteArray(16) { 3 }, ByteArray(32) { 4 },
            SigningKey(peerSeed).publicKey, X25519PrivateKeyParameters(peerBox, 0).generatePublicKey().encoded, peerSeed, peerBox
        )
        val peerForDaemon = Wire.Identity(
            Wire.utf8("p1"), 1L, ByteArray(16) { 1 }, ByteArray(32) { 2 },
            SigningKey(selfSeed).publicKey, X25519PrivateKeyParameters(selfBox, 0).generatePublicKey().encoded, null, null
        )

        val bridge = FakeRtcBridge()
        val conn = ByteArray(16) { 9 }
        val deadline = System.currentTimeMillis() + 5_000L
        val pathEvents = CopyOnWriteArrayList<String>()
        lateinit var clientSession: DirectSession
        lateinit var daemonSession: DirectSession
        daemonSession = DirectSession(
            DirectSessionOptions(
                self = selfDaemon,
                peer = peerForDaemon,
                codec = codec,
                connection = conn,
                deadline = deadline,
                budget = DirectBudget(),
                stun = emptyList(),
                factory = bridge.daemonFactory,
                valid = { true },
                authorize = {},
                send = { out -> clientSession.receive(out.raw); true },
                chosen = {},
                beforeOffer = {},
                ready = { _, _ -> },
                closed = {},
                frame = { _, _ -> },
                capability = {},
            )
        )
        clientSession = DirectSession(
            DirectSessionOptions(
                self = self,
                peer = peerForClient,
                codec = codec,
                connection = conn,
                deadline = deadline,
                budget = DirectBudget(),
                stun = emptyList(),
                factory = bridge.phoneFactory,
                valid = { true },
                authorize = {},
                send = { out -> daemonSession.receive(out.raw); true },
                chosen = {},
                beforeOffer = {},
                ready = { use, _ -> pathEvents += "open:$use" },
                closed = { use -> pathEvents += "close:$use" },
                frame = { _, _ -> },
                capability = {},
            )
        )
        clientSession.start()
        while (bridge.phonePcs.isEmpty() || !pathEvents.contains("open:1")) delay(20)
        val firstPc = bridge.phonePcs.first()
        clientSession.retire()
        assertTrue(firstPc.closed)
        val countBefore = pathEvents.size
        firstPc.emitStaleState(RtcPeerState.FAILED)
        firstPc.emitCandidate(RtcIceCandidate("cand", "0", 0, false))
        delay(30)
        assertEquals(countBefore, pathEvents.size)
        clientSession.close()
        daemonSession.close()
    }

    @Test
    fun remoteDirectUpgradeFallbackAndUploadResume() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 1 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_CLIENT)
            val now = System.currentTimeMillis()
            var mono = 10_000L
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, { now }, bridge.daemonFactory)
            daemon.onCommand = { cmd ->
                val isDirect = cmd in daemon.directCommands
                mono += if (isDirect) 5L else 100L
            }
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = http,
                clock = { now },
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                monotonic = { mono },
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            try {
                remote.start()
                remote.state.first { it.conn == ConnState.ONLINE && daemonAccount.deviceId in it.machines }
                remote.syncSecurity()
                while (!remote.direct.ready(daemonAccount.deviceId, RouteHint.Read) ||
                    !remote.direct.ready(daemonAccount.deviceId, RouteHint.Bulk(file = "s-direct/f1")) ||
                    !remote.direct.ready(daemonAccount.deviceId, RouteHint.Bulk(file = "up-1", upload = UploadKind.IMAGE, bytes = 512 shl 10))
                ) delay(20)
                assertFalse(remote.peer(daemonAccount.deviceId).relayRequired)

                // Ready direct path is preferred immediately without any warm-up loop.
                val readHint = RouteHint.Read
                val routing = remote.direct.routing(daemonAccount.deviceId)
                assertEquals(RouteName.DIRECT, routing.choose(readHint, ready = true))

                val directBefore = daemon.directCommands.size
                remote.machineCommand(daemonAccount.deviceId, Command.newBuilder().setGetLiveSnapshot(GetLiveSnapshotRequest.newBuilder().setSessionId("s-direct").build()).build())
                assertTrue(daemon.directCommands.size > directBefore)
                assertTrue(daemon.directCommands.last().hasGetLiveSnapshot())

                // Bulk download uses direct immediately (7/8 direct, 1/8 relay control sample).
                for (i in 0 until 16) {
                    try {
                        val dl = remote.getSharedFileChunk("s-direct", "f1", (i.toLong() shl 19))
                        assertEquals(512 shl 10, dl.data.size())
                    } catch (t: Throwable) {
                        throw AssertionError("dl chunk $i failed (relay=${daemon.relayCommands.size}, direct=${daemon.directCommands.size}): ${t.message}", t)
                    }
                }
                val directDl = daemon.directCommands.count { it.hasGetSharedFileChunk() }
                val relayDl = daemon.relayCommands.count { it.hasGetSharedFileChunk() }
                assertTrue("expected download chunks on direct (directDl=$directDl, relayDl=$relayDl, ready=${remote.direct.ready(daemonAccount.deviceId, RouteHint.Bulk(file = "s-direct/f1"))}, filePath=${remote.filePath(daemonAccount.deviceId)})", directDl >= 10)
                assertEquals(RouteName.DIRECT, remote.filePath(daemonAccount.deviceId))

                // Upload also uses direct immediately, then dropping direct falls back to getImageUploadStatus on relay before retry.
                val ch = remote.uploadChannel(daemonAccount.deviceId)
                val upChunk = ByteArray(512 shl 10)
                ch.begin("up-1", ImageFormat.IMAGE_FORMAT_PNG, 20L shl 20, sha256(ByteArray(4)))
                for (i in 0 until 10) {
                    try {
                        ch.put("up-1", i.toLong() shl 19, upChunk)
                    } catch (t: Throwable) {
                        throw AssertionError("put chunk $i failed (relay=${daemon.relayCommands.size}, direct=${daemon.directCommands.size}): ${t.message}", t)
                    }
                }
                assertTrue("expected upload chunks on direct immediately", daemon.directCommands.count { it.hasPutImageChunk() } >= 6)

                val relayBefore = daemon.relayCommands.size
                daemon.onDropDirect = { bridge.phonePcs.toList().forEach { it.dropAllChannels() } }
                daemon.dropDirectOnNextPut = true
                try {
                    ch.put("up-1", 10L shl 19, upChunk)
                } catch (t: Throwable) {
                    throw AssertionError("fallback put chunk failed (relay=${daemon.relayCommands.size}, direct=${daemon.directCommands.size}): ${t.message}", t)
                }
                val newRelay = daemon.relayCommands.drop(relayBefore)
                val statusIdx = newRelay.indexOfFirst { it.hasGetImageUploadStatus() }
                val putIdx = newRelay.indexOfFirst { it.hasPutImageChunk() }
                assertTrue("getImageUploadStatus must precede fallback PutImageChunk: $newRelay", statusIdx in 0 until putIdx)
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    /** A long-lived app whose direct path drops must get a new one within the
     * designed backoff (1 s, then 2 s) and immediately prefer direct again. */
    @Test
    fun directIsReestablishedAfterAPathDropAndPrefersDirectImmediately() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 1 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, System::currentTimeMillis, bridge.daemonFactory)
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = http,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
                directJitter = { 1.0 }, // the ceiling itself: 1 s, then 2 s
            )
            val id = daemonAccount.deviceId
            try {
                remote.start()
                remote.state.first { it.conn == ConnState.ONLINE && id in it.machines }
                remote.syncSecurity()
                while (!remote.direct.ready(id, RouteHint.Read) || !remote.direct.ready(id, RouteHint.Bulk(file = "s/f"))) delay(20)
                assertEquals(RouteName.DIRECT, remote.direct.routing(id).readPath())
                assertEquals(RouteName.DIRECT, remote.direct.routing(id).filePath)
                val before = bridge.phonePcs.size

                // The path drops (the network under it changed); the main socket stays up.
                bridge.phonePcs.toList().forEach { it.dropAllChannels() }
                awaitCondition("the dropped path stops being ready", 3_000) { !remote.direct.ready(id, RouteHint.Read) }
                val droppedAt = System.nanoTime()

                // Full-jitter backoff (docs/transport.md §4), here at its ceiling: 1 s for the first
                // failure, plus at most one 1 s discovery tick plus the handshake.
                awaitCondition("direct is re-established after the path drop", 15_000) {
                    remote.direct.ready(id, RouteHint.Read) && remote.direct.ready(id, RouteHint.Bulk(file = "s/f"))
                }
                val backMs = (System.nanoTime() - droppedAt) / 1_000_000
                assertTrue("re-established after ${backMs}ms, expected within the first 1 s step (+ a tick and the handshake)", backMs in 900..4_000)
                assertTrue("a new peer connection was made", bridge.phonePcs.size > before)
                assertEquals(RouteName.DIRECT, remote.direct.routing(id).readPath())

                // A second drop soon after is the second failure: the ceiling doubles to 2 s.
                awaitCondition("the re-established path is ready to drop again", 10_000) { remote.direct.ready(id, RouteHint.Read) }
                val second = bridge.phonePcs.size
                bridge.phonePcs.toList().forEach { it.dropAllChannels() }
                awaitCondition("the second drop stops being ready", 3_000) { !remote.direct.ready(id, RouteHint.Read) }
                val droppedAgain = System.nanoTime()
                awaitCondition("direct is re-established after the second drop", 20_000) {
                    remote.direct.ready(id, RouteHint.Read) && remote.direct.ready(id, RouteHint.Bulk(file = "s/f"))
                }
                val backAgainMs = (System.nanoTime() - droppedAgain) / 1_000_000
                assertTrue("second failure waits up to 2 s, took ${backAgainMs}ms", backAgainMs in 1_900..5_500)
                assertTrue("a new peer connection again", bridge.phonePcs.size > second)
                assertEquals(RouteName.DIRECT, remote.direct.routing(id).readPath())
                assertTrue(remote.directEnabled(id))
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun routerDualPicksFirstTrustedResultAndIgnoresLateArrival() = runBlocking {
        val router = Router(
            object : Path {
                override fun status(machineId: String) = PeerState(ready = true, reachable = true, authorized = true, unlinked = false, relayRequired = false)
                override fun attempt(handoff: () -> Unit): PathAttempt = throw UnsupportedOperationException()
                override fun openStream(address: String, listener: FrameListener): FrameStream = throw UnsupportedOperationException()
            },
        )
        val releaseRelay = CompletableDeferred<DualLegResult<String>>()
        val got = router.dual(
            primary = { DualLegResult.Trusted("from-direct") },
            fallback = { releaseRelay.await() },
        )
        assertEquals("from-direct", got)
        releaseRelay.complete(DualLegResult.Trusted("late-relay"))
        assertEquals("from-direct", got)
    }

    @Test
    fun routerDualMultiThreadedStressDoesNotCrashOnUninitializedFallbackJob() = runBlocking {
        val router = Router(
            object : Path {
                override fun status(machineId: String) = PeerState(ready = true, reachable = true, authorized = true, unlinked = false, relayRequired = false)
                override fun attempt(handoff: () -> Unit): PathAttempt = throw UnsupportedOperationException()
                override fun openStream(address: String, listener: FrameListener): FrameStream = throw UnsupportedOperationException()
            },
        )
        repeat(200) {
            val got = kotlinx.coroutines.withContext(Dispatchers.Default) {
                router.dual(
                    primary = { DualLegResult.Trusted("direct") },
                    fallback = {
                        delay(1)
                        DualLegResult.Trusted("relay")
                    },
                )
            }
            // Guards the crash only: under load the 1 ms fallback may legitimately win.
            assertTrue(got == "direct" || got == "relay")
        }
    }

    @Test
    fun routerDualReturnsFastWinnerWithoutWaitingForStuckLosingLeg() = runBlocking {
        val router = Router(
            object : Path {
                override fun status(machineId: String) = PeerState(ready = true, reachable = true, authorized = true, unlinked = false, relayRequired = false)
                override fun attempt(handoff: () -> Unit): PathAttempt = throw UnsupportedOperationException()
                override fun openStream(address: String, listener: FrameListener): FrameStream = throw UnsupportedOperationException()
            },
        )
        val stuckEntered = CompletableDeferred<Unit>()
        val stuckRelease = java.util.concurrent.CountDownLatch(1)
        try {
            val got = kotlinx.coroutines.withContext(Dispatchers.IO) {
                router.dual(
                    primary = {
                        stuckEntered.await()
                        DualLegResult.Trusted("fast-direct")
                    },
                    fallback = {
                        stuckEntered.complete(Unit)
                        // Simulate non-interruptible blocking I/O (e.g., DNS lookup); released only after dual returns.
                        stuckRelease.await()
                        DualLegResult.Trusted("late-relay")
                    },
                )
            }
            assertEquals("fast-direct", got)
        } finally {
            stuckRelease.countDown()
        }
    }

    @Test
    fun dispatchCommandNeverReportsNotSentWhenDirectHandedOffAndRelayThrowsRuntimeException() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 11 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, System::currentTimeMillis, bridge.daemonFactory)
            daemon.onDropDirect = {
                bridge.phonePcs.toList().forEach { it.dropAllChannels() }
            }
            daemon.dropDirectOnNextPut = true
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = http,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            val id = daemonAccount.deviceId
            try {
                remote.start()
                remote.state.first { it.conn == ConnState.ONLINE && id in it.machines }
                remote.syncSecurity()
                awaitCondition("direct ready", 10_000) { remote.direct.ready(id, RouteHint.Read) }

                val origPath = remote.deviceLink.router.path
                remote.deviceLink = Link(
                    object : Path {
                        override fun status(machineId: String) = origPath.status(machineId)
                        override fun attempt(handoff: () -> Unit): PathAttempt = object : PathAttempt {
                            override val attemptId = "relay-runtime-ex"
                            override suspend fun prepare(prior: Boolean) {}
                            override suspend fun send(machineId: String, command: Envelope): io.github.lewin671.remoteagent.proto.ForwardResponse {
                                throw SecurityException("simulated OS background socket restriction")
                            }
                            override fun refusal(error: com.connectrpc.ConnectException): PathRefusal? = null
                            override suspend fun refreshAuth(prior: Boolean) {}
                        }
                        override fun openStream(address: String, listener: FrameListener) = origPath.openStream(address, listener)
                    },
                )
                val cmd = Command.newBuilder()
                    .setProtocolVersion(PROTOCOL_VERSION)
                    .setSend(io.github.lewin671.remoteagent.proto.SendRequest.newBuilder().setSessionId("s1").setText("hello").build())
                    .build()
                val eff = io.github.lewin671.remoteagent.core.proto.DispatchCommandEffect.newBuilder()
                    .setRequestId("req-dual-runtime-ex")
                    .setAttemptId("att-1")
                    .setMachineId(id)
                    .setSessionId("s1")
                    .setDeadlineMs(System.currentTimeMillis() + 5_000L)
                    .setCommandBytes(cmd.toByteString())
                    .build()
                val outcome = try {
                    remote.dispatchSingleCommand(eff)
                } catch (e: Exception) {
                    fail("dispatchSingleCommand leaked exception into executeDispatchCommand where it would become not_sent: $e")
                    return@runBlocking
                }
                assertFalse("must never report notForwarded=true after direct handed off: $outcome", outcome.notForwarded)
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun backgroundDisablesDirectAndForegroundRestoresIt() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 9 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, System::currentTimeMillis, bridge.daemonFactory)
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = http,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            val id = daemonAccount.deviceId
            try {
                remote.start()
                remote.foregroundChanged(true)
                remote.state.first { it.conn == ConnState.ONLINE && id in it.machines }
                remote.syncSecurity()
                awaitCondition("direct ready in foreground", 10_000) {
                    remote.direct.ready(id, RouteHint.Read)
                }

                remote.foregroundChanged(false)
                awaitCondition("background closes direct", 3_000) {
                    !remote.direct.ready(id, RouteHint.Read)
                }
                assertNull(remote.direct.attemptInteractive(id, {}))

                remote.foregroundChanged(true)
                awaitCondition("foreground restores direct", 10_000) {
                    remote.direct.ready(id, RouteHint.Read)
                }
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun syncBudgetTimeoutInvalidatesDirectEngineAndPreferenceDisablesSession() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 2 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, { System.currentTimeMillis() }, bridge.daemonFactory)
            daemon.start()
            val slowHttp = http.newBuilder().addInterceptor { chain ->
                if (chain.request().url.encodedPath.endsWith("/GetSecurityStates") && System.getProperty("delaySyncTest") == "1") {
                    Thread.sleep(600)
                }
                chain.proceed(chain.request())
            }.build()
            val remote = Remote(
                account = clientAccount,
                http = slowHttp,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            try {
                remote.start()
                remote.state.first { it.conn == ConnState.ONLINE && daemonAccount.deviceId in it.machines }
                remote.syncSecurity(5_000L)
                while (!remote.direct.ready(daemonAccount.deviceId, RouteHint.Read)) delay(20)

                // Local off tears down direct session immediately.
                remote.setDirectEnabled(daemonAccount.deviceId, false)
                assertFalse(remote.direct.ready(daemonAccount.deviceId, RouteHint.Read))
                remote.setDirectEnabled(daemonAccount.deviceId, true)
                while (!remote.direct.ready(daemonAccount.deviceId, RouteHint.Read)) delay(20)

                // Budget sync failure invalidates DirectEngine via onSyncFailed.
                System.setProperty("delaySyncTest", "1")
                val err = runCatching { remote.syncSecurity(100L) }.exceptionOrNull()
                System.clearProperty("delaySyncTest")
                assertNotNull(err)
                assertFalse(remote.direct.ready(daemonAccount.deviceId, RouteHint.Read))
            } finally {
                System.clearProperty("delaySyncTest")
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun directEngineCapabilityExpiryAndLateProbePreservesEstablishedSession() = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 3 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            var now = System.currentTimeMillis()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, { now }, bridge.daemonFactory)
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = http,
                clock = { now },
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            try {
                remote.start()
                remote.state.first { it.conn == ConnState.ONLINE && daemonAccount.deviceId in it.machines }
                remote.syncSecurity()
                while (!remote.direct.ready(daemonAccount.deviceId, RouteHint.Read) ||
                    !remote.direct.ready(daemonAccount.deviceId, RouteHint.Bulk(file = "up-check", upload = UploadKind.IMAGE, bytes = 64 shl 10))
                ) delay(20)

                // Oversized chunk or disallowed upload capability stays on relay.
                assertFalse(remote.direct.ready(daemonAccount.deviceId, RouteHint.Bulk(file = "up-big", upload = UploadKind.IMAGE, bytes = 2 shl 20)))

                // Inject a late Probe (same daemon boot, new connId) into DirectEngine: must not tear down the active established session.
                val pcCountBefore = bridge.phonePcs.size
                val r = TestSecurity.get(server.url).stores.getValue(daemonAccount.deviceId).readSecurity()
                val xPriv = r.peerBoxKey()
                val dId = Wire.Identity(
                    Wire.utf8(daemonAccount.deviceId), 2L, ByteArray(16) { 0x61 }, ByteArray(32) { 0x22 },
                    SigningKey(daemonAccount.deviceSeed).publicKey,
                    X25519PrivateKeyParameters(xPriv, 0).generatePublicKey().encoded,
                    daemonAccount.deviceSeed, xPriv,
                )
                val pMem = r.head.core.member(clientAccount.deviceId)!!
                val cId = Wire.Identity(Wire.utf8(clientAccount.deviceId), 1L, ByteArray(0), ByteArray(0), pMem.ed, pMem.x, null, null)
                val codec = Wire(Wire.utf8(r.pin.account), r.pin.domain, DeviceSecurity.stateHash(r.head), r.head.core.epoch.toLong())
                val conn2 = ByteArray(16) { 0x77 }
                val ctx = Wire.probeContext(dId, cId, conn2, 1L, now)
                val probeBody = Wire.pb(Wire.b(1, conn2), Wire.b(2, dId.instance), Wire.b(3, dId.boot))
                val rawProbe = codec.seal(dId, cId, ctx, 1L, probeBody, null, null, null)
                val activeWs = remote.activeStreamForTest!!
                remote.direct.receive(rawProbe, activeWs)
                delay(100)
                assertTrue(remote.direct.ready(daemonAccount.deviceId, RouteHint.Read))
                assertTrue(bridge.phonePcs.size >= pcCountBefore)

                // Advance past CapabilitySnapshot 30s expiry -> upload readiness becomes false, read stays ready.
                now += 31_000L
                assertFalse(remote.direct.ready(daemonAccount.deviceId, RouteHint.Bulk(file = "up-expired", upload = UploadKind.IMAGE, bytes = 64 shl 10)))
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun directChannelTickDetectsSilentClosedChannelAfterConfirm() = runBlocking {
        val budget = DirectBudget()
        val silentClose = java.util.concurrent.atomic.AtomicBoolean(false)
        val closedCalled = CompletableDeferred<Unit>()
        val pc = object : RtcPeerConnection {
            override fun createDataChannel(
                label: String,
                id: Int,
                ordered: Boolean,
                maxRetransmits: Int?,
                listener: RtcDataChannelListener,
            ): RtcDataChannel = object : RtcDataChannel {
                override fun state() = if (silentClose.get() && id == 0) RtcChannelState.CLOSED else RtcChannelState.OPEN
                override fun bufferedAmount() = 0L
                override fun send(data: ByteArray, binary: Boolean) = !silentClose.get()
                override fun close() {}
            }
            override suspend fun createOffer(iceRestart: Boolean) = "v=0"
            override suspend fun createAnswer() = "v=0"
            override suspend fun setLocalDescription(offer: Boolean, sdp: String) {}
            override suspend fun setRemoteDescription(offer: Boolean, sdp: String) {}
            override suspend fun addIceCandidate(candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) {}
            override fun maxMessageSize(): Int? = 65536
            override fun close() {}
        }
        val factory = object : RtcFactory {
            override fun create(stunUrls: List<String>, listener: RtcPeerListener) = pc
        }
        val dc = DirectChannel(
            DirectChannelOptions(
                peer = "m1",
                connection = ByteArray(16) { 2 },
                use = 1,
                generation = 1L,
                deadline = System.currentTimeMillis() + 5_000L,
                budget = budget,
                valid = { true },
                stun = emptyList(),
                factory = factory,
                candidate = {},
                state = {},
                frame = {},
                closed = { closedCalled.complete(Unit) },
            )
        )
        dc.confirm()
        assertTrue(dc.ready)
        // Simulate silent WebRTC DataChannel close without onStateChange callback.
        silentClose.set(true)
        closedCalled.await()
        assertTrue(dc.aborted)
        dc.close()
    }

    @Test
    fun remoteDualSendDispatchesIdenticalEnvelopeAndTakesFastDirectWinner(): Unit = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 12 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, System::currentTimeMillis, bridge.daemonFactory)
            val execCount = AtomicInteger(0)
            daemon.onCommand = { execCount.incrementAndGet() }
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = http,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            val id = daemonAccount.deviceId
            try {
                remote.start()
                remote.state.first { it.conn == ConnState.ONLINE && id in it.machines }
                remote.syncSecurity()
                awaitCondition("direct ready", 10_000) { remote.direct.ready(id, RouteHint.Read) }

                val relayEnvelopes = CopyOnWriteArrayList<ByteArray>()
                val relayCancelled = CompletableDeferred<Unit>()
                val relayBlocked = CompletableDeferred<Unit>()

                val origPath = remote.deviceLink.router.path
                remote.deviceLink = Link(
                    object : Path {
                        override fun status(machineId: String) = origPath.status(machineId)
                        override fun attempt(handoff: () -> Unit): PathAttempt = object : PathAttempt {
                            override val attemptId = "relay-slow-leg"
                            override suspend fun prepare(prior: Boolean) {}
                            override suspend fun send(machineId: String, command: Envelope): io.github.lewin671.remoteagent.proto.ForwardResponse {
                                relayEnvelopes += command.toByteArray()
                                try {
                                    relayBlocked.await()
                                } catch (c: kotlinx.coroutines.CancellationException) {
                                    relayCancelled.complete(Unit)
                                    throw c
                                }
                                error("unreachable")
                            }
                            override fun refusal(error: com.connectrpc.ConnectException): PathRefusal? = null
                            override suspend fun refreshAuth(prior: Boolean) {}
                        }
                        override fun openStream(address: String, listener: FrameListener) = origPath.openStream(address, listener)
                    },
                )

                val cmd = Command.newBuilder()
                    .setProtocolVersion(PROTOCOL_VERSION)
                    .setSend(io.github.lewin671.remoteagent.proto.SendRequest.newBuilder().setSessionId("s1").setText("dual-remote-send").build())
                    .build()
                val eff = io.github.lewin671.remoteagent.core.proto.DispatchCommandEffect.newBuilder()
                    .setRequestId("req-remote-dual-fast-1")
                    .setAttemptId("att-remote-1")
                    .setMachineId(id)
                    .setSessionId("s1")
                    .setDeadlineMs(System.currentTimeMillis() + 5_000L)
                    .setCommandBytes(cmd.toByteString())
                    .build()

                val outcome = remote.dispatchSingleCommand(eff)
                assertEquals("executed", outcome.outerOutcome)
                assertEquals(io.github.lewin671.remoteagent.core.proto.OutcomeSource.OUTCOME_SOURCE_DAEMON, outcome.source)
                assertFalse(outcome.notForwarded)
                assertEquals(1, outcome.result.send.queuePosition)
                assertEquals(1, execCount.get())
                assertEquals(1, daemon.directCommands.count { it.hasSend() && it.send.text == "dual-remote-send" })
                assertEquals(1, relayEnvelopes.size)
                val relayCmdEnv = Envelope.parseFrom(relayEnvelopes[0])
                val relayReqId = DeviceSecurity.parseHeader(relayCmdEnv.securityHeader.toByteArray()).request
                assertEquals("req-remote-dual-fast-1", relayReqId)
                relayCancelled.await()
                relayBlocked.complete(Unit)
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun remoteDualSendFallsBackToRelayAfterDirectPostHandoffDrop(): Unit = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 13 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, System::currentTimeMillis, bridge.daemonFactory)
            daemon.onDropDirect = {
                bridge.phonePcs.toList().forEach { it.dropAllChannels() }
            }
            daemon.dropDirectOnNextPut = true
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = http,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            val id = daemonAccount.deviceId
            try {
                remote.start()
                remote.state.first { it.conn == ConnState.ONLINE && id in it.machines }
                remote.syncSecurity()
                awaitCondition("direct ready", 10_000) { remote.direct.ready(id, RouteHint.Read) }

                val origPath = remote.deviceLink.router.path
                remote.deviceLink = Link(
                    object : Path {
                        override fun status(machineId: String) = origPath.status(machineId)
                        override fun attempt(handoff: () -> Unit): PathAttempt = object : PathAttempt {
                            override val attemptId = "relay-fallback-ok"
                            override suspend fun prepare(prior: Boolean) {}
                            override suspend fun send(machineId: String, command: Envelope): io.github.lewin671.remoteagent.proto.ForwardResponse {
                                handoff()
                                delay(40)
                                return daemon.sealRelayExecuted(command, attemptId)
                            }
                            override fun refusal(error: com.connectrpc.ConnectException): PathRefusal? = null
                            override suspend fun refreshAuth(prior: Boolean) {}
                        }
                        override fun openStream(address: String, listener: FrameListener) = origPath.openStream(address, listener)
                    },
                )
                val cmd = Command.newBuilder()
                    .setProtocolVersion(PROTOCOL_VERSION)
                    .setSend(io.github.lewin671.remoteagent.proto.SendRequest.newBuilder().setSessionId("s1").setText("dual-fallback-ok").build())
                    .build()
                val eff = io.github.lewin671.remoteagent.core.proto.DispatchCommandEffect.newBuilder()
                    .setRequestId("req-remote-dual-fallback-1")
                    .setAttemptId("att-remote-2")
                    .setMachineId(id)
                    .setSessionId("s1")
                    .setDeadlineMs(System.currentTimeMillis() + 5_000L)
                    .setCommandBytes(cmd.toByteString())
                    .build()

                val outcome = remote.dispatchSingleCommand(eff)
                assertEquals("executed", outcome.outerOutcome)
                assertEquals(1, outcome.result.send.queuePosition)
                assertFalse(outcome.notForwarded)
                assertEquals(io.github.lewin671.remoteagent.core.proto.OutcomeSource.OUTCOME_SOURCE_DAEMON, outcome.source)
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun foregroundResumeMustSyncSecurityBeforeNegotiatingDirect(): Unit = runBlocking {
        val server = TestServer()
        val failSecuritySync = java.util.concurrent.atomic.AtomicBoolean(false)
        val baseHttp = Api.defaultHttp()
        val clientHttp = baseHttp.newBuilder()
            .addInterceptor { chain ->
                if (failSecuritySync.get() && chain.request().url.encodedPath.contains("GetSecurityStates")) {
                    throw java.io.IOException("simulated post-wake GetSecurityStates failure")
                }
                chain.proceed(chain.request())
            }
            .build()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 14 }, server.url, baseHttp, server.setupToken)
            val daemonAccount = approvedClient(phone, baseHttp, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, baseHttp, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, baseHttp, System::currentTimeMillis, bridge.daemonFactory)
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = clientHttp,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            val id = daemonAccount.deviceId
            try {
                remote.start()
                remote.foregroundChanged(true)
                remote.state.first { it.conn == ConnState.ONLINE && id in it.machines }
                remote.syncSecurity()
                awaitCondition("initial direct ready", 10_000) { remote.direct.ready(id, RouteHint.Read) }
                val pcsBeforeBackground = bridge.phonePcs.size

                // Background + stop must invalidate direct security freshness and bump syncRevision.
                remote.foregroundChanged(false)
                remote.stop()
                awaitCondition("direct closed on stop", 3_000) { !remote.direct.ready(id, RouteHint.Read) }

                // Fail post-wake GetSecurityStates and resume within 20s window.
                failSecuritySync.set(true)
                remote.foregroundChanged(true)
                remote.start()
                remote.state.first { it.conn == ConnState.ONLINE && id in it.machines }
                delay(400)
                assertEquals(
                    "DirectEngine must not negotiate PeerConnections before post-wake security sync succeeds",
                    pcsBeforeBackground,
                    bridge.phonePcs.size,
                )

                // Once security sync recovers, normal foreground toggle resumes Direct without manual syncSecurity().
                failSecuritySync.set(false)
                remote.foregroundChanged(false)
                remote.foregroundChanged(true)
                awaitCondition("direct ready after post-wake sync", 10_000) { remote.direct.ready(id, RouteHint.Read) }
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun dualSendPreservesServerNotForwardedWhenDirectNeverHandedOff(): Unit = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 15 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, System::currentTimeMillis, bridge.daemonFactory)
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = http,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            val id = daemonAccount.deviceId
            try {
                remote.start()
                remote.foregroundChanged(true)
                remote.state.first { it.conn == ConnState.ONLINE && id in it.machines }
                remote.syncSecurity()
                awaitCondition("direct ready", 10_000) { remote.direct.ready(id, RouteHint.Read) }

                // Hold direct DataChannel before first byte (handoff() NOT called) and take daemon relay offline so TestServer returns NotForwarded.
                bridge.phonePcs.forEach { it.localChannels[0]?.buffered = 128L shl 10 }
                daemon.close()
                awaitCondition("daemon offline on server", 3_000) { remote.state.value.machines[id]?.online != true }

                val closer = launch {
                    delay(120)
                    bridge.phonePcs.toList().forEach { it.dropAllChannels() }
                }

                val cmd = Command.newBuilder()
                    .setProtocolVersion(PROTOCOL_VERSION)
                    .setSend(io.github.lewin671.remoteagent.proto.SendRequest.newBuilder().setSessionId("s1").setText("pre-handoff-not-forwarded").build())
                    .build()
                val eff = io.github.lewin671.remoteagent.core.proto.DispatchCommandEffect.newBuilder()
                    .setRequestId("req-dual-prehandoff-nf-1")
                    .setAttemptId(io.github.lewin671.remoteagent.core.newRequestId())
                    .setMachineId(id)
                    .setSessionId("s1")
                    .setDeadlineMs(System.currentTimeMillis() + 5_000L)
                    .setCommandBytes(cmd.toByteString())
                    .build()

                val outcome = remote.dispatchSingleCommand(eff)
                closer.join()
                assertTrue("must preserve notForwarded=true when Direct never handed off: $outcome", outcome.notForwarded)
                assertTrue("must preserve attemptBound=true from server refusal: $outcome", outcome.attemptBound)
                assertEquals(io.github.lewin671.remoteagent.core.proto.OutcomeSource.OUTCOME_SOURCE_SERVER, outcome.source)
                assertEquals("machine_offline", outcome.outerReason)
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun stopAndBackgroundClearDirectReadyAndDirectPeers(): Unit = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 16 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, System::currentTimeMillis, bridge.daemonFactory)
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = http,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            val id = daemonAccount.deviceId
            try {
                remote.start()
                remote.foregroundChanged(true)
                remote.state.first { it.conn == ConnState.ONLINE && id in it.machines }
                remote.syncSecurity()
                awaitCondition("direct ready in Remote and clientcore", 10_000) {
                    remote.direct.ready(id, RouteHint.Read) &&
                        remote.coreHost.syncStateView.value.machinesList.firstOrNull { it.machineId == id }?.directReady == true &&
                        remote.peer(id)?.directStatus?.stage == DirectStage.DIRECT_ACTIVE
                }

                // Direct stop() sets running=false before direct.setForeground(false), which previously skipped onDirectChanged().
                remote.stop()
                awaitCondition("direct cleared in clientcore and peer after stop", 5_000) {
                    val coreMachine = remote.coreHost.syncStateView.value.machinesList.firstOrNull { it.machineId == id }
                    coreMachine?.directReady != true &&
                        remote.peer(id)?.directStatus?.stage != DirectStage.DIRECT_ACTIVE
                }
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun backgroundStopAndResumeSyncsSecurityAndReconnectsDirectWithoutManualSync(): Unit = runBlocking {
        val server = TestServer()
        val getSecurityCount = AtomicInteger(0)
        val baseHttp = Api.defaultHttp()
        val countingHttp = baseHttp.newBuilder()
            .addInterceptor { chain ->
                if (chain.request().url.encodedPath.contains("GetSecurityStates")) {
                    getSecurityCount.incrementAndGet()
                }
                chain.proceed(chain.request())
            }
            .build()
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 17 }, server.url, baseHttp, server.setupToken)
            val daemonAccount = approvedClient(phone, baseHttp, DeviceKind.DEVICE_KIND_MACHINE)
            val clientAccount = approvedClient(phone, baseHttp, DeviceKind.DEVICE_KIND_CLIENT)
            val bridge = FakeRtcBridge()
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, baseHttp, System::currentTimeMillis, bridge.daemonFactory)
            daemon.start()
            val remote = Remote(
                account = clientAccount,
                http = countingHttp,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(clientAccount.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
            )
            val id = daemonAccount.deviceId
            try {
                remote.start()
                remote.foregroundChanged(true)
                awaitCondition("cold start direct ready via SyncSecurityEffect", 10_000) {
                    remote.direct.ready(id, RouteHint.Read) &&
                        remote.peer(id)?.directStatus?.stage == DirectStage.DIRECT_ACTIVE
                }
                val initialSyncCount = getSecurityCount.get()
                assertTrue("cold start should have synced security at least once", initialSyncCount >= 1)

                getSecurityCount.set(0)
                remote.foregroundChanged(false)
                remote.stop()
                awaitCondition("direct cleared after stop", 3_000) {
                    !remote.direct.ready(id, RouteHint.Read) &&
                        remote.peer(id)?.directStatus?.stage != DirectStage.DIRECT_ACTIVE
                }

                // Real resume path without any manual remote.syncSecurity():
                remote.start()
                remote.foregroundChanged(true)
                awaitCondition("direct automatically reconnects on resume via post-wake Touch+security sync", 10_000) {
                    remote.direct.ready(id, RouteHint.Read) &&
                        remote.peer(id)?.directStatus?.stage == DirectStage.DIRECT_ACTIVE
                }
                assertTrue("resume must trigger GetSecurityStates at least once", getSecurityCount.get() >= 1)

                // Also verify foregroundChanged(false) -> foregroundChanged(true) without stop() recovers Direct.
                getSecurityCount.set(0)
                remote.foregroundChanged(false)
                awaitCondition("direct closed on background", 3_000) { !remote.direct.ready(id, RouteHint.Read) }
                remote.foregroundChanged(true)
                awaitCondition("direct reconnects on foreground resume without stop", 10_000) {
                    remote.direct.ready(id, RouteHint.Read) &&
                        remote.peer(id)?.directStatus?.stage == DirectStage.DIRECT_ACTIVE
                }
                assertTrue("background->foreground resume must trigger GetSecurityStates", getSecurityCount.get() >= 1)
            } finally {
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun pairingNewMachineAndSetEnabledAfterFreshWindowRecoverDirectWithoutManualSync(): Unit = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        val nowMs = java.util.concurrent.atomic.AtomicLong(System.currentTimeMillis())
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 17 }, server.url, http, server.setupToken)
            val bridge = FakeRtcBridge()
            val remote = Remote(
                account = phone,
                http = http,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(phone.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
                clock = { nowMs.get() },
                monotonic = { nowMs.get() },
            )
            var daemon: DirectBridgeDaemon? = null
            try {
                remote.start()
                remote.foregroundChanged(true)
                remote.state.first { it.conn == ConnState.ONLINE }

                // Pair a new machine while the authority client stays open in foreground, matching Remote's
                // PutSecurityState pairing flow (security.install fires onInstall -> direct.install).
                // Start the daemon's stream before install() triggers Probe so TestServer does not bounce
                // the initial Probe with OFFLINE while the virtual clock is stationary.
                val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
                daemon = DirectBridgeDaemon(daemonAccount, server.url, http, { nowMs.get() }, bridge.daemonFactory)
                daemon.start()
                val updated = TestSecurity.find(server.url)!!.stores.getValue(phone.deviceId).readSecurity()
                remote.direct.install(updated)

                val id = daemonAccount.deviceId
                awaitCondition("paired machine becomes DIRECT_ACTIVE without manual syncSecurity", 10_000) {
                    remote.direct.ready(id, RouteHint.Read) &&
                        remote.peer(id)?.directStatus?.stage == DirectStage.DIRECT_ACTIVE
                }

                // Advance clock past the 20s freshUntil window and toggle Direct off/on (as E2E tests do).
                remote.setDirectEnabled(id, false)
                awaitCondition("direct turns off", 3_000) { !remote.direct.ready(id, RouteHint.Read) }
                nowMs.addAndGet(25_000L)
                remote.setDirectEnabled(id, true)
                awaitCondition("setDirectEnabled(true) after 20s window re-authorizes and recovers Direct", 10_000) {
                    remote.direct.ready(id, RouteHint.Read) &&
                        remote.peer(id)?.directStatus?.stage == DirectStage.DIRECT_ACTIVE
                }
            } finally {
                remote.close()
                daemon?.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun duplicateNetworkChangedAndProactiveKeepalivePreserveSessionWhileCloseReasonSurvivesClosed(): Unit = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        val nowMs = java.util.concurrent.atomic.AtomicLong(System.currentTimeMillis())
        val monoStartNs = System.nanoTime()
        val testMonotonic: () -> Long = { nowMs.get() + (System.nanoTime() - monoStartNs) / 1_000_000L }
        try {
            val phone = newAuthorityAccount(ByteArray(32) { 18 }, server.url, http, server.setupToken)
            val daemonAccount = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val unreachableDaemon = approvedClient(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val bridge = FakeRtcBridge()
            val keepaliveSyncs = AtomicInteger(0)
            val delayedHttp = http.newBuilder().addInterceptor { chain ->
                val slow = chain.request().url.encodedPath.endsWith("/GetSecurityStates") && System.getProperty("slowKeepaliveTest") == "1"
                if (slow) Thread.sleep(350)
                chain.proceed(chain.request()).also {
                    if (slow) keepaliveSyncs.incrementAndGet()
                }
            }.build()
            val remote = Remote(
                account = phone,
                http = delayedHttp,
                securityStorage = TestSecurity.find(server.url)?.stores?.get(phone.deviceId),
                rtcFactory = bridge.phoneFactory,
                directPrefs = InMemoryDirectPreferenceStore(),
                stunUrls = emptyList(),
                clock = { nowMs.get() },
                monotonic = testMonotonic,
            )
            val daemon = DirectBridgeDaemon(daemonAccount, server.url, http, { nowMs.get() }, bridge.daemonFactory)
            try {
                daemon.start()
                remote.start()
                remote.foregroundChanged(true)
                val id = daemonAccount.deviceId
                remote.state.first { it.conn == ConnState.ONLINE && id in it.machines && unreachableDaemon.deviceId in it.machines }
                remote.syncSecurity(5_000L)
                awaitCondition("direct active before network checks", 5_000) {
                    remote.direct.ready(id, RouteHint.Read)
                }

                // Initial baseline attach and duplicate identical callbacks must NOT invalidate direct.
                val diagBefore = DirectDiag.lines().size
                remote.networkChanged("wifi:net-1", true)
                remote.networkChanged("wifi:net-1", true)
                delay(150)
                assertTrue("duplicate networkChanged must keep Direct ready", remote.direct.ready(id, RouteHint.Read))
                assertFalse("no closed_by_owner after duplicate networkChanged", DirectDiag.lines().drop(diagBefore).any { "not_allowed" in it })

                // Advance clock inside the proactive <=7s window with slow GetSecurityStates and wait for timer keepalive.
                keepaliveSyncs.set(0)
                System.setProperty("slowKeepaliveTest", "1")
                nowMs.addAndGet(14_000L)
                awaitCondition("proactive keepalive completes GetSecurityStates", 5_000) {
                    keepaliveSyncs.get() >= 1
                }
                System.clearProperty("slowKeepaliveTest")
                assertTrue("proactive keepalive must preserve Direct across 14s mark", remote.direct.ready(id, RouteHint.Read))

                // Real network change tears down and re-establishes.
                remote.networkChanged("cell:net-2", true)
                awaitCondition("direct re-establishes after real networkChanged", 10_000) {
                    remote.direct.ready(id, RouteHint.Read)
                }

                // A non-responding peer that hits deadline preserves NO_RESPONSE failReason instead of clearing it in closed().
                val deadId = unreachableDaemon.deviceId
                awaitCondition("unreachable peer enters TRYING before deadline", 5_000) {
                    remote.direct.directStatus(deadId).stage == DirectStage.TRYING
                }
                nowMs.addAndGet(11_000L)
                awaitCondition("deadline close preserves NO_RESPONSE failReason", 5_000) {
                    remote.direct.directStatus(deadId).reason == DirectFailReason.NO_RESPONSE
                }
            } finally {
                System.clearProperty("slowKeepaliveTest")
                remote.close()
                daemon.close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun control10UnsupportedForControl15DoesNotCloseSessionOrFail() = runBlocking {
        val codec = Wire(Wire.utf8("acc-1"), Wire.utf8("domain"), sha256(Wire.utf8("head")), 1L)
        val selfSeed = sha256(Wire.utf8("self-u10")); val selfBox = sha256(Wire.utf8("self-box-u10"))
        val peerSeed = sha256(Wire.utf8("peer-u10")); val peerBox = sha256(Wire.utf8("peer-box-u10"))
        val self = Wire.Identity(Wire.utf8("p1"), 1L, ByteArray(16) { 1 }, ByteArray(32) { 2 }, SigningKey(selfSeed).publicKey, X25519PrivateKeyParameters(selfBox, 0).generatePublicKey().encoded, selfSeed, selfBox)
        val peerForClient = Wire.Identity(Wire.utf8("m1"), 2L, ByteArray(16) { 3 }, ByteArray(32) { 4 }, Ed25519PrivateKeyParameters(peerSeed, 0).generatePublicKey().encoded, X25519PrivateKeyParameters(peerBox, 0).generatePublicKey().encoded, null, null)
        val selfDaemon = Wire.Identity(Wire.utf8("m1"), 2L, ByteArray(16) { 3 }, ByteArray(32) { 4 }, SigningKey(peerSeed).publicKey, X25519PrivateKeyParameters(peerBox, 0).generatePublicKey().encoded, peerSeed, peerBox)
        val peerForDaemon = Wire.Identity(Wire.utf8("p1"), 1L, ByteArray(16) { 1 }, ByteArray(32) { 2 }, SigningKey(selfSeed).publicKey, X25519PrivateKeyParameters(selfBox, 0).generatePublicKey().encoded, null, null)
        val bridge = FakeRtcBridge().also { it.srflx = false }
        val conn = ByteArray(16) { 7 }
        val nowMs = java.util.concurrent.atomic.AtomicLong(1_700_000_000_000L)
        val deadline = nowMs.get() + 5_000L
        val events = CopyOnWriteArrayList<String>()
        val clientFramesSent = CopyOnWriteArrayList<Wire.Opened>()
        val unsupportedSentBack = CopyOnWriteArrayList<Wire.Opened>()
        val failReasons = CopyOnWriteArrayList<DirectFailReason>()
        lateinit var clientSession: DirectSession
        lateinit var daemonSession: DirectSession
        clientSession = DirectSession(
            DirectSessionOptions(
                self = self, peer = peerForClient, codec = codec, connection = conn, deadline = deadline, budget = DirectBudget(), stun = emptyList(), factory = bridge.phoneFactory,
                valid = { true }, authorize = {},
                send = { out ->
                    runCatching { clientFramesSent += codec.openSent(out.raw, self, peerForClient, nowMs.get()) }
                    daemonSession.receive(out.raw)
                    true
                },
                chosen = {}, beforeOffer = {}, ready = { use, _ -> events += "client:open:$use" }, closed = { events += "client:closed:$it" },
                closeReason = { failReasons += it }, frame = { _, _ -> }, capability = {}, clock = { nowMs.get() },
            ),
        )
        daemonSession = DirectSession(
            DirectSessionOptions(
                self = selfDaemon, peer = peerForDaemon, codec = codec, connection = conn, deadline = deadline, budget = DirectBudget(), stun = emptyList(), factory = bridge.daemonFactory,
                valid = { true }, authorize = {},
                send = { out ->
                    runCatching {
                        val o = codec.openSent(out.raw, selfDaemon, peerForDaemon, nowMs.get())
                        if (o.ctx.cls == 2L && o.type == 10L) unsupportedSentBack += o
                    }
                    clientSession.receive(out.raw)
                    true
                },
                chosen = {}, beforeOffer = {}, ready = { use, _ -> events += "daemon:open:$use" }, closed = {}, frame = { _, _ -> }, capability = {}, clock = { nowMs.get() },
            ),
        )
        clientSession.start()
        try {
            awaitCondition("both sessions ready", 4_000) {
                events.containsAll(listOf("client:open:1", "daemon:open:1"))
            }
            val subBody = DirectEventSubscription.newBuilder().addSessionIds("sess-1").build().toByteArray()
            clientSession.sendControl(15L, subBody)
            awaitCondition("Control 15 rejected with Control 10 Unsupported and controlWindow cleared", 3_000) {
                unsupportedSentBack.isNotEmpty() && clientSession.controlPendingCount == 0
            }
            val countBefore = clientFramesSent.count { it.ctx.cls == 2L && it.type == 15L }
            nowMs.addAndGet(1_600L)
            // Drive one deterministic tick pass after advancing injected clock past retry intervals.
            awaitCondition("tick processes advanced clock without retrying cleared Control 15", 1_000) {
                clientSession.controlPendingCount == 0
            }
            val countAfter = clientFramesSent.count { it.ctx.cls == 2L && it.type == 15L }
            assertEquals("Control 15 must stop retrying after Control 10 Unsupported", countBefore, countAfter)
            assertTrue("DirectSession must remain active after Control 10 on controlWindow", clientSession.active)
            assertTrue("PEER_UNSUPPORTED must not be reported", DirectFailReason.PEER_UNSUPPORTED !in failReasons)
            assertTrue("client PCs must remain open", events.none { it.startsWith("client:closed:") })
        } finally {
            clientSession.close()
            daemonSession.close()
        }
    }
}
