package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.withStarted
import kotlin.coroutines.CoroutineContext
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob

/** Cancel suspended signing work on stop; retain its durable material for an explicit retry. */
@Composable
internal fun rememberAuthorityScope(): CoroutineScope {
    val parent = rememberCoroutineScope()
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val scope = remember(parent, lifecycle) { AuthorityScope(parent.coroutineContext, lifecycle) }
    DisposableEffect(scope, lifecycle) {
        val observer = LifecycleEventObserver { _, event ->
            when (event) {
                Lifecycle.Event.ON_START -> scope.start()
                Lifecycle.Event.ON_STOP,
                Lifecycle.Event.ON_DESTROY -> scope.stop()
                else -> Unit
            }
        }
        lifecycle.addObserver(observer)
        onDispose {
            lifecycle.removeObserver(observer)
            scope.stop()
        }
    }
    return scope
}

private class AuthorityScope(private val parent: CoroutineContext, lifecycle: Lifecycle) :
    CoroutineScope {
    private var job =
        SupervisorJob(parent[Job]).also {
            if (!lifecycle.currentState.isAtLeast(Lifecycle.State.STARTED)) it.cancel()
        }
    override val coroutineContext: CoroutineContext
        get() = parent + job

    fun start() {
        if (!job.isActive) job = SupervisorJob(parent[Job])
    }

    fun stop() {
        job.cancel()
    }
}

/** Startup replay may refresh an expired certificate, so it has the same foreground lifetime. */
@Composable
internal fun AuthorityEffect(key: Any?, block: suspend CoroutineScope.() -> Unit) {
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(lifecycle, key) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED, block)
    }
}

/** Start once after the page first becomes visible; resuming must retain pairing consent. */
@Composable
internal fun AuthorityStartEffect(key: Any?, action: () -> Unit) {
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(lifecycle, key) { lifecycle.withStarted(action) }
}
