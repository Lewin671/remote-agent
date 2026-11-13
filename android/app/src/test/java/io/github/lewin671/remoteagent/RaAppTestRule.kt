package io.github.lewin671.remoteagent

import android.content.Context
import android.content.ContextWrapper
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import org.junit.rules.ExternalResource
import org.junit.rules.TestRule
import org.robolectric.RuntimeEnvironment
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** A manually attached app graph, without onCreate or network/lifecycle observers. */
class RaAppTestRule(
    private val preparePendingStore: Boolean = false,
) : ExternalResource() {
    private var fixture: AttachedRaApp? = null
    val app: RaApp get() = checkNotNull(fixture).app

    override fun before() {
        fixture = AttachedRaApp(RuntimeEnvironment.getApplication())
        if (preparePendingStore) {
            // UI fixtures exercise real sealed draft IO with a synthetic data
            // key. Robolectric has no AndroidKeyStore provider; its absence is
            // not a disk failure or a composer error in these UI scenarios.
            val key = ByteArray(32).also { java.security.SecureRandom().nextBytes(it) }
            app.pendingStore.javaClass.getDeclaredField("cachedKey").apply { isAccessible = true }
                .set(app.pendingStore, key)
        }
    }

    /** Install inside the Compose rule: drain jobs while its scheduler lives,
     * then let Compose dispose its content before the outer rule restores the app. */
    fun stopJobs(awaitStopped: (() -> Boolean) -> Unit): TestRule = object : ExternalResource() {
        override fun after() { fixture?.stopJobs(awaitStopped) }
    }

    override fun after() {
        val owner = fixture
        fixture = null
        owner?.close()
    }
}

/** Restore the exact previous singleton even if the test fails; nested fixtures remain independent. */
internal class AttachedRaApp(context: Context) : AutoCloseable {
    private val instance = RaApp::class.java.getDeclaredField("instance").apply { isAccessible = true }
    private val previous = instance.get(null)
    val app = RaApp().also {
        ContextWrapper::class.java.getDeclaredMethod("attachBaseContext", Context::class.java)
            .apply { isAccessible = true }.invoke(it, context)
        instance.set(null, it)
    }

    override fun close() {
        try { stopJobs(null) }
        finally { instance.set(null, previous) }
    }

    fun stopJobs(awaitStopped: ((() -> Boolean) -> Unit)?) {
        val pairing = RaApp::class.java.getDeclaredField("pairingScope").apply { isAccessible = true }
            .get(app) as CoroutineScope
        val jobs = listOfNotNull(app.scope.coroutineContext[Job], pairing.coroutineContext[Job])
        jobs.forEach { it.cancel() }
        // Cancellation alone does not finish a running draft IO operation.
        // Compose fixtures pump their own test scheduler before it is torn
        // down; plain Robolectric fixtures pump the Android main looper.
        if (awaitStopped != null) awaitStopped { jobs.all { it.isCompleted } }
        else {
            val stopped = CountDownLatch(jobs.size)
            jobs.forEach { it.invokeOnCompletion { stopped.countDown() } }
            val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5)
            while (stopped.count > 0) {
                org.robolectric.shadows.ShadowLooper.idleMainLooper()
                check(stopped.await(1, TimeUnit.MILLISECONDS) || System.nanoTime() < deadline) {
                    "fixture app jobs did not stop"
                }
            }
        }
    }
}
