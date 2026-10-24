package io.github.lewin671.remoteagent.push

import android.app.Application
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.LifecycleRegistry
import androidx.lifecycle.compose.LocalLifecycleOwner
import io.github.lewin671.remoteagent.RaApp
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class VisiblePushSessionTest {
    @get:Rule val ui = createComposeRule()

    @Test
    fun navigationBackgroundAndDisposalClearTheVisibleSession() {
        val app = RaApp()
        val owner = object : LifecycleOwner {
            val registry = LifecycleRegistry(this)
            override val lifecycle: Lifecycle get() = registry
        }
        val session = mutableStateOf<String?>(null)
        val shown = mutableStateOf(true)
        ui.runOnUiThread { owner.registry.currentState = Lifecycle.State.STARTED }
        ui.setContent {
            CompositionLocalProvider(LocalLifecycleOwner provides owner) {
                if (shown.value) VisiblePushSession(app, session.value)
            }
        }
        ui.runOnIdle { assertNull(app.visibleSession); session.value = "target-session" }
        ui.runOnIdle { assertEquals("target-session", app.visibleSession); session.value = "other-session" }
        ui.runOnIdle { assertEquals("other-session", app.visibleSession); session.value = null }
        ui.runOnIdle { assertNull(app.visibleSession); session.value = "target-session" }
        ui.runOnIdle { assertEquals("target-session", app.visibleSession); owner.registry.currentState = Lifecycle.State.CREATED }
        ui.runOnIdle { assertNull("background retained a visible session", app.visibleSession) }
        ui.runOnIdle { owner.registry.currentState = Lifecycle.State.STARTED }
        ui.runOnIdle { assertEquals("target-session", app.visibleSession); shown.value = false }
        ui.runOnIdle { assertNull("leaving the authenticated UI retained a visible session", app.visibleSession) }
    }
}
