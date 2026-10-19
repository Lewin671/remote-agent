package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.LifecycleRegistry
import androidx.lifecycle.compose.LocalLifecycleOwner
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class AuthorityLifecycleTest {
    @get:Rule val ui = createComposeRule()

    @Test
    fun initialPairingStartsInForegroundOnceAndRetainsConsentOnResume() {
        val owner =
            object : LifecycleOwner {
                val registry = LifecycleRegistry(this)
                override val lifecycle: Lifecycle
                    get() = registry
            }
        var starts = 0
        var startedWork = 0
        ui.runOnUiThread { owner.registry.currentState = Lifecycle.State.CREATED }
        ui.setContent {
            CompositionLocalProvider(LocalLifecycleOwner provides owner) {
                val scope = rememberAuthorityScope()
                AuthorityStartEffect(Unit) {
                    starts++
                    scope.launch { startedWork++ }
                }
            }
        }
        ui.runOnIdle { assertEquals("pairing started before its foreground scope", 0, starts) }
        ui.runOnIdle { owner.registry.currentState = Lifecycle.State.STARTED }
        ui.waitUntil { startedWork == 1 }
        ui.runOnIdle { owner.registry.currentState = Lifecycle.State.CREATED }
        ui.runOnIdle { owner.registry.currentState = Lifecycle.State.STARTED }
        ui.runOnIdle {
            assertEquals("resuming replaced the original pairing", 1, starts)
            assertEquals(1, startedWork)
        }
    }

    @Test
    fun waitingAuthorityWorkStopsInBackgroundAndCanBeRetriedInForeground() {
        val owner =
            object : LifecycleOwner {
                val registry = LifecycleRegistry(this)
                override val lifecycle: Lifecycle
                    get() = registry
            }
        lateinit var scope: CoroutineScope
        val release = CompletableDeferred<Unit>()
        var entered = false
        var signed = 0
        ui.runOnUiThread { owner.registry.currentState = Lifecycle.State.STARTED }
        ui.setContent {
            CompositionLocalProvider(LocalLifecycleOwner provides owner) {
                scope = rememberAuthorityScope()
            }
        }
        ui.runOnIdle {
            scope.launch {
                entered = true
                release.await()
                signed++
            }
        }
        ui.waitUntil { entered }
        ui.runOnIdle {
            owner.registry.currentState = Lifecycle.State.CREATED
            assertFalse("scope did not observe ON_STOP", scope.coroutineContext[Job]!!.isActive)
        }
        ui.runOnIdle { release.complete(Unit) }
        ui.runOnIdle { assertEquals("authority work survived backgrounding", 0, signed) }
        ui.runOnIdle { owner.registry.currentState = Lifecycle.State.STARTED }
        ui.runOnIdle { scope.launch { signed++ } }
        ui.runOnIdle { assertEquals(1, signed) }
    }
}
