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

    fun delivery(call: DispatchState.Call): PathAttempt {
        val evidence = call.delivery()
        val attempt = router.path.attempt { evidence.dispatch() }
        return object : PathAttempt by attempt {
            override fun refusal(error: ConnectException): PathRefusal? {
                val proof = attempt.refusal(error)
                if (proof != null) evidence.notForwarded()
                return proof
            }
        }
    }

    private val streams = mutableSetOf<Stream>()
    fun openStream(stop: () -> Unit, onClose: (MessageText) -> Unit): Stream = synchronized(streams) {
        Stream(stop, onClose).also { streams.add(it) }
    }
    inner class Stream(private val stop: () -> Unit, private val onClose: (MessageText) -> Unit) {
        private val ended = AtomicBoolean(false)
        fun end(reason: MessageText?) {
            if (!ended.compareAndSet(false, true)) return
            synchronized(streams) { streams.remove(this) }
            stop()
            // Only Link's relay-only policy closes the product stream.
            if (reason != null) onClose(reason)
        }
    }
    fun closeStreams() {
        val owned = synchronized(streams) { streams.toList() }
        for (stream in owned) stream.end(null)
    }
}
