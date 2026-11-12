package io.github.lewin671.remoteagent

import android.content.Context
import android.content.ContextWrapper
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.cancel
import org.junit.rules.ExternalResource
import org.robolectric.RuntimeEnvironment

/** A manually attached app graph, without onCreate or network/lifecycle observers. */
class RaAppTestRule : ExternalResource() {
    private var fixture: AttachedRaApp? = null
    val app: RaApp get() = checkNotNull(fixture).app

    override fun before() {
        fixture = AttachedRaApp(RuntimeEnvironment.getApplication())
    }

    override fun after() {
        fixture?.close()
        fixture = null
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
        try {
            app.scope.cancel()
            // Pairing work has its own owner, independent of the main app scope.
            (RaApp::class.java.getDeclaredField("pairingScope").apply { isAccessible = true }
                .get(app) as CoroutineScope).cancel()
        } finally {
            instance.set(null, previous)
        }
    }
}
