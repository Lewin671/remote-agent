package io.github.lewin671.remoteagent.push

import androidx.compose.runtime.Composable
import androidx.lifecycle.compose.LifecycleStartEffect
import io.github.lewin671.remoteagent.RaApp

/** Observe the current navigation destination, not a session retained in the back stack. */
@Composable
internal fun VisiblePushSession(app: RaApp, session: String?) {
    LifecycleStartEffect(session) {
        app.visibleSession = session
        onStopOrDispose { app.visibleSession = null }
    }
}
