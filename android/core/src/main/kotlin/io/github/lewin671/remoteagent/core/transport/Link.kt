package io.github.lewin671.remoteagent.core.transport

import com.connectrpc.ConnectException
import io.github.lewin671.remoteagent.core.*
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.withContext
import java.util.concurrent.atomic.AtomicBoolean

internal class Link(path: Path) {
    val router = Router(path)
    private val published = kotlinx.coroutines.flow.MutableStateFlow<Map<String, PeerState>>(emptyMap())
    val peers: kotlinx.coroutines.flow.StateFlow<Map<String, PeerState>> = published
    fun snapshot(ids: Set<String>): Map<String, PeerState> = ids.associateWith(::status).also { published.value = it }
    fun status(machineId: String) = router.status(machineId)

    suspend fun <T> call(p: PreparedCommand, prior: Boolean, dispatcher: CoroutineDispatcher, work: suspend (DispatchState.Call) -> T): T {
        val call = p.dispatch.open(prior)
        try { return withContext(dispatcher) { work(call) } }
        catch (error: Exception) {
            val command = error as? CommandException
            if (call.uncertain && command?.unknown != true && !(command?.fromDaemon == true && command.outcome != Outcome.NOT_SENT)) {
                throw CommandException(CommandException.UNKNOWN, localized { copy -> copy(UiMessages.errorsAndroidThePreviousOutcomeIsStillUnknownValue1RetryingSendsThe(errorText(error).resolve(copy))) }, outcome = Outcome.UNKNOWN)
            }
            throw error
        } finally { call.close() }
    }

    fun delivery(
        call: DispatchState.Call,
        direct: ((() -> Unit) -> PathAttempt?)? = null,
    ): PathAttempt {
        val evidence = call.delivery()
        val handoff: () -> Unit = { evidence.dispatch() }
        val attempt = direct?.invoke(handoff) ?: router.path.attempt(handoff)
        return object : PathAttempt by attempt {
            override fun refusal(error: ConnectException): PathRefusal? {
                val proof = attempt.refusal(error)
                if (proof != null) evidence.notForwarded()
                return proof
            }
        }
    }

    private val streams = mutableSetOf<Stream>()
    private var closed = false
    fun openStream(stop: (MessageText?) -> Unit, onClose: (MessageText) -> Unit): Stream {
        val stream = Stream(stop, onClose)
        val registered = synchronized(streams) {
            if (closed) false else { streams.add(stream); true }
        }
        if (!registered) stream.end(null)
        return stream
    }
    inner class Stream(private val stop: (MessageText?) -> Unit, private val onClose: (MessageText) -> Unit) {
        private val ended = AtomicBoolean(false)
        fun end(reason: MessageText?) {
            if (!ended.compareAndSet(false, true)) return
            synchronized(streams) { streams.remove(this) }
            // Only Link's relay-only policy closes the product stream.
            cleanup({ stop(reason) }, { if (reason != null) onClose(reason) })
        }
    }
    fun closeStreams() {
        val owned = synchronized(streams) { closed = true; streams.toList() }
        cleanup(*owned.map { stream -> { stream.end(null) } }.toTypedArray())
    }
}
