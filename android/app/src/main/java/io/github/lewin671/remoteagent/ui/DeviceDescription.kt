package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.RemoteState

/** Automatic metadata work, separate from user-requested edits. */
@Composable
fun DeviceDescription(
    state: RemoteState,
    id: String,
    description: String,
    save: suspend () -> Unit,
) {
    LaunchedEffect(state.devices[id]?.metaRev, state.conn, state.leaseEndMs) {
        val me = state.devices[id]
        if (
            me != null &&
                (me.meta?.platform != "android" || me.meta?.description != description) &&
                state.conn == ConnState.ONLINE
        ) {
            runCatching { save() }
        }
    }
}
