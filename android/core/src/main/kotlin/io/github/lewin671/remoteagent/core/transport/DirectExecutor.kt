package io.github.lewin671.remoteagent.core.transport

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.DirectDcReadStatsEffect
import io.github.lewin671.remoteagent.core.proto.DirectDcStateEvent
import io.github.lewin671.remoteagent.core.proto.DirectDcStatsEvent
import io.github.lewin671.remoteagent.core.proto.DirectPcAddCandidatesEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcCandidatesAddedEvent
import io.github.lewin671.remoteagent.core.proto.DirectPcCloseEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcClosedEvent
import io.github.lewin671.remoteagent.core.proto.DirectPcConfirmEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcCreateEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcCreatedEvent
import io.github.lewin671.remoteagent.core.proto.DirectPcDescribeEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcDescriptionEvent
import io.github.lewin671.remoteagent.core.proto.DirectPcLocalCandidateEvent
import io.github.lewin671.remoteagent.core.proto.DirectPcProbeReceivedEvent
import io.github.lewin671.remoteagent.core.proto.DirectPcRemoteSetEvent
import io.github.lewin671.remoteagent.core.proto.DirectPcSendProbeEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcSetRemoteEffect
import io.github.lewin671.remoteagent.core.proto.DirectPcStateEvent
import io.github.lewin671.remoteagent.core.proto.DirectPcStunErrorEvent
import io.github.lewin671.remoteagent.core.proto.DirectSignalSendEffect
import io.github.lewin671.remoteagent.core.proto.DirectStateKind
import io.github.lewin671.remoteagent.core.proto.DirectStatsReport
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.DirectedPeerFrame
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.PeerFrame
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.withTimeout
import java.nio.ByteBuffer
import java.nio.charset.CodingErrorAction
import java.nio.charset.StandardCharsets
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicBoolean

private fun decodeUtf8Strict(bytes: ByteArray): String =
    StandardCharsets.UTF_8.newDecoder()
        .onMalformedInput(CodingErrorAction.REPORT)
        .onUnmappableCharacter(CodingErrorAction.REPORT)
        .decode(ByteBuffer.wrap(bytes))
        .toString()

/**
 * Platform executor for Core's direct-path coordinator: owns WebRTC PCs,
 * leaf Ed25519/X25519 operations, and main-WS signaling framing.
 * Bulk AES-GCM and batch signature verification stay outside Core.
 */
internal class DirectExecutor(
    private val emit: (CoreEvent) -> Unit,
    private val onPushedEvent: (machineId: String, pcId: Long, env: Envelope, header: Header, size: Int) -> Unit,
    private val onTerminalOutput: ((machineId: String, pcId: Long, env: Envelope, header: Header, raw: ByteArray) -> Unit)? = null,
    private val factory: RtcFactory? = null,
    private val clock: () -> Long = System::currentTimeMillis,
    private val monotonic: () -> Long = { System.nanoTime() / 1_000_000L },
    private val dispatcher: CoroutineDispatcher = Dispatchers.Default,
    private val bulkReceiveDispatcher: CoroutineDispatcher = CpuLanes.bulk,
) {
    private class PcEntry(
        val machineId: String,
        val use: Int,
        @Volatile var confirmed: Boolean = false,
        @Volatile var closedByCore: Boolean = false,
    ) {
        lateinit var channel: DirectChannel
    }

    private class PendingRpc(
        val pcId: Long,
        val machineId: String,
        val response: CompletableDeferred<Envelope>,
    )

    val budget = DirectBudget()
    private val pcs = ConcurrentHashMap<Long, PcEntry>()
    private val pendingRpc = ConcurrentHashMap<String, PendingRpc>()
    @Volatile private var leg: FrameStream? = null
    private val closed = AtomicBoolean(false)

    private inline fun emitEvent(block: CoreEvent.Builder.() -> Unit) {
        if (!closed.get()) emit(CoreEvent.newBuilder().apply(block).build())
    }

    fun attachLeg(stream: FrameStream) {
        leg = stream
    }

    fun detachLeg(stream: FrameStream) {
        if (leg === stream) leg = null
    }

    fun detachAll() {
        leg = null
    }

    fun signalSend(eff: DirectSignalSendEffect): Boolean {
        val stream = leg ?: return false
        if (closed.get()) return false
        val frame = PeerFrame.newBuilder().setDirectedPeerFrame(
            DirectedPeerFrame.newBuilder()
                .setToDevice(eff.toDevice)
                .setFrameClassValue(eff.frameClass)
                .setFrameId(eff.frameId)
                .setSealed(eff.sealed),
        ).build().toByteArray()
        return try {
            stream.send(frame)
        } catch (_: Exception) {
            false
        }
    }

    fun pcCreate(eff: DirectPcCreateEffect) {
        val rtc = factory
        if (closed.get() || rtc == null) {
            emitEvent {
                setDirectPcCreated(DirectPcCreatedEvent.newBuilder().setPcId(eff.pcId).setOk(false))
            }
            return
        }
        pcs.remove(eff.pcId)?.let { prev ->
            prev.closedByCore = true
            rejectPendingForPc(eff.pcId)
            runCatching { prev.channel.close() }
        }
        val entry = PcEntry(
            machineId = eff.machineId,
            use = eff.use,
        )
        // Register before constructing DirectChannel so synchronous SDK callbacks never miss the entry.
        pcs[eff.pcId] = entry
        try {
            val ch = DirectChannel(
                DirectChannelOptions(
                    peer = eff.machineId,
                    roundId = eff.roundId,
                    use = eff.use,
                    generation = eff.generation,
                    budget = budget,
                    valid = { !closed.get() && pcs[eff.pcId] === entry },
                    stun = eff.stunServersList,
                    factory = rtc,
                    candidate = { cand ->
                        if (!closed.get() && pcs[eff.pcId] === entry) {
                            val b = DirectPcLocalCandidateEvent.newBuilder().setPcId(eff.pcId)
                            if (cand == null) b.setEndOfCandidates(true) else b.setCandidate(cand.candidate)
                            emitEvent { setDirectPcLocalCandidate(b) }
                        }
                    },
                    state = { kind: DirectStateKind, value: String ->
                        if (!closed.get() && pcs[eff.pcId] === entry) {
                            emitEvent {
                                setDirectPcState(
                                    DirectPcStateEvent.newBuilder()
                                        .setPcId(eff.pcId)
                                        .setKind(kind)
                                        .setValue(value),
                                )
                            }
                        }
                    },
                    dcState = { open ->
                        if (!closed.get() && pcs[eff.pcId] === entry) {
                            emitEvent {
                                setDirectDcState(DirectDcStateEvent.newBuilder().setPcId(eff.pcId).setOpen(open))
                            }
                        }
                    },
                    stunError = { code, address ->
                        if (!closed.get() && pcs[eff.pcId] === entry) {
                            emitEvent {
                                setDirectPcStunError(
                                    DirectPcStunErrorEvent.newBuilder()
                                        .setPcId(eff.pcId)
                                        .setCode(code)
                                        .setAddress(address),
                                )
                            }
                        }
                    },
                    probeReply = { raw ->
                        if (!closed.get() && pcs[eff.pcId] === entry) {
                            emitEvent {
                                setDirectPcProbeReceived(
                                    DirectPcProbeReceivedEvent.newBuilder()
                                        .setPcId(eff.pcId)
                                        .setRaw(ByteString.copyFrom(raw)),
                                )
                            }
                        }
                    },
                    frame = { raw -> handleDcFrame(eff.pcId, entry, raw) },
                    closed = {
                        pcs.remove(eff.pcId, entry)
                        rejectPendingForPc(eff.pcId)
                        if (!entry.closedByCore) {
                            emitEvent {
                                setDirectPcClosed(DirectPcClosedEvent.newBuilder().setPcId(eff.pcId))
                            }
                        }
                    },
                    clock = clock,
                    monotonic = monotonic,
                    dispatcher = dispatcher,
                    receiveDispatcher = if (eff.use == 2) bulkReceiveDispatcher else dispatcher,
                ),
            )
            entry.channel = ch
            pcs[eff.pcId] = entry
            emitEvent {
                setDirectPcCreated(DirectPcCreatedEvent.newBuilder().setPcId(eff.pcId).setOk(true))
            }
        } catch (_: Exception) {
            pcs.remove(eff.pcId, entry)
            emitEvent {
                setDirectPcCreated(DirectPcCreatedEvent.newBuilder().setPcId(eff.pcId).setOk(false))
            }
        }
    }

    suspend fun pcDescribe(eff: DirectPcDescribeEffect) {
        val entry = pcs[eff.pcId]
        if (entry == null) {
            emitEvent {
                setDirectPcDescription(DirectPcDescriptionEvent.newBuilder().setPcId(eff.pcId).setOk(false))
            }
            return
        }
        try {
            val sdp = entry.channel.description(eff.offer, eff.iceRestart)
            emitEvent {
                setDirectPcDescription(
                    DirectPcDescriptionEvent.newBuilder()
                        .setPcId(eff.pcId)
                        .setSdp(ByteString.copyFrom(sdp))
                        .setOk(true),
                )
            }
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            emitEvent {
                setDirectPcDescription(DirectPcDescriptionEvent.newBuilder().setPcId(eff.pcId).setOk(false))
            }
        }
    }

    suspend fun pcSetRemote(eff: DirectPcSetRemoteEffect) {
        val entry = pcs[eff.pcId]
        if (entry == null) {
            emitEvent {
                setDirectPcRemoteSet(DirectPcRemoteSetEvent.newBuilder().setPcId(eff.pcId).setOk(false).setError("closed"))
            }
            return
        }
        try {
            entry.channel.remote(eff.sdp.toByteArray(), eff.offer)
            emitEvent {
                setDirectPcRemoteSet(DirectPcRemoteSetEvent.newBuilder().setPcId(eff.pcId).setOk(true))
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            emitEvent {
                setDirectPcRemoteSet(
                    DirectPcRemoteSetEvent.newBuilder()
                        .setPcId(eff.pcId)
                        .setOk(false)
                        .setError(e.javaClass.simpleName),
                )
            }
        }
    }

    suspend fun pcAddCandidates(eff: DirectPcAddCandidatesEffect) {
        val entry = pcs[eff.pcId]
        if (entry == null) {
            emitEvent {
                setDirectPcCandidatesAdded(
                    DirectPcCandidatesAddedEvent.newBuilder().setPcId(eff.pcId).setOk(false).setError("closed"),
                )
            }
            return
        }
        try {
            for (c in eff.candidatesList) {
                val line = decodeUtf8Strict(c.line.toByteArray())
                val ufrag = if (c.ufrag.isEmpty) "" else decodeUtf8Strict(c.ufrag.toByteArray())
                val mid = if (c.mid.isEmpty) "" else decodeUtf8Strict(c.mid.toByteArray())
                entry.channel.addCandidate(line, mid, c.mlineIndex, ufrag)
            }
            emitEvent {
                setDirectPcCandidatesAdded(DirectPcCandidatesAddedEvent.newBuilder().setPcId(eff.pcId).setOk(true))
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            emitEvent {
                setDirectPcCandidatesAdded(
                    DirectPcCandidatesAddedEvent.newBuilder()
                        .setPcId(eff.pcId)
                        .setOk(false)
                        .setError(e.javaClass.simpleName),
                )
            }
        }
    }

    fun pcConfirm(eff: DirectPcConfirmEffect) {
        val entry = pcs[eff.pcId] ?: return
        if (entry.machineId != eff.machineId || entry.use != eff.use) return
        entry.confirmed = true
        entry.channel.confirm()
    }

    fun pcClose(eff: DirectPcCloseEffect) {
        val entry = pcs.remove(eff.pcId) ?: return
        entry.closedByCore = true
        rejectPendingForPc(eff.pcId)
        entry.channel.close()
    }

    fun pcSendProbe(eff: DirectPcSendProbeEffect) {
        val entry = pcs[eff.pcId] ?: return
        entry.channel.sendProbe(eff.raw.toByteArray())
    }

    suspend fun sendTerminalFrame(pcId: Long, raw: ByteArray): Boolean {
        val entry = pcs[pcId] ?: return false
        if (!entry.confirmed || !entry.channel.ready || entry.use != 1) return false
        return try {
            entry.channel.write(raw) {}
            true
        } catch (_: Exception) {
            false
        }
    }

    suspend fun dcReadStats(eff: DirectDcReadStatsEffect) {
        val entry = pcs[eff.pcId]
        val report = if (entry != null) entry.channel.readStats() else DirectStatsReport.getDefaultInstance()
        emitEvent {
            setDirectDcStats(DirectDcStatsEvent.newBuilder().setPcId(eff.pcId).setReport(report))
        }
    }

    suspend fun sendRpc(
        pcId: Long,
        requestId: String,
        sealedBytes: ByteArray,
        timeoutMs: Long,
        handoff: () -> Unit,
    ): Envelope {
        val entry = pcs[pcId]
        if (entry == null || !entry.confirmed || !entry.channel.ready || pendingRpc.size >= 128) {
            throw PathChanged()
        }
        val rpcKey = "$pcId:$requestId"
        val response = CompletableDeferred<Envelope>()
        val pending = PendingRpc(pcId, entry.machineId, response)
        if (pendingRpc.putIfAbsent(rpcKey, pending) != null) throw PathChanged()

        val boundedMs = if (timeoutMs > 0L) timeoutMs.coerceIn(1L, 30_000L) else 3_000L
        return try {
            withTimeout(boundedMs) {
                entry.channel.write(sealedBytes, handoff)
                response.await()
            }
        } catch (e: TimeoutCancellationException) {
            throw PathChanged()
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            throw PathChanged()
        } finally {
            pendingRpc.remove(rpcKey, pending)
        }
    }

    private fun handleDcFrame(pcId: Long, entry: PcEntry, raw: ByteArray) {
        if (closed.get() || pcs[pcId] !== entry) return
        try {
            val env = Envelope.parseFrom(raw)
            val header = Header.parseFrom(env.header)
            if (entry.use == 1 && header.kind == PayloadKind.PAYLOAD_KIND_EVENT) {
                if (entry.confirmed && header.machineId == entry.machineId && header.sender == entry.machineId) {
                    onPushedEvent(entry.machineId, pcId, env, header, raw.size)
                }
                return
            }
            if (entry.use == 1 && header.kind == PayloadKind.PAYLOAD_KIND_TERMINAL_OUTPUT) {
                if (entry.confirmed && header.machineId == entry.machineId && header.sender == entry.machineId) {
                    onTerminalOutput?.invoke(entry.machineId, pcId, env, header, raw)
                }
                return
            }
            val rpcKey = "$pcId:${header.requestId}"
            val pending = pendingRpc[rpcKey] ?: return
            if (pending.machineId == entry.machineId && header.machineId == entry.machineId) {
                pendingRpc.remove(rpcKey, pending)
                pending.response.complete(env)
            }
        } catch (_: Exception) {
            // Remote authenticates every routed result.
        }
    }

    private fun rejectPendingForPc(pcId: Long) {
        val iter = pendingRpc.entries.iterator()
        while (iter.hasNext()) {
            val e = iter.next()
            if (e.value.pcId == pcId && pendingRpc.remove(e.key, e.value)) {
                e.value.response.completeExceptionally(PathChanged())
            }
        }
    }

    fun close() {
        if (!closed.compareAndSet(false, true)) return
        leg = null
        val snapshot = pcs.entries.toList()
        pcs.clear()
        for ((_, entry) in snapshot) {
            entry.closedByCore = true
            runCatching { entry.channel.close() }
        }
        val pendSnapshot = pendingRpc.entries.toList()
        pendingRpc.clear()
        for ((_, p) in pendSnapshot) {
            p.response.completeExceptionally(PathChanged())
        }
    }
}
