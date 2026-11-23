package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.SyncStateView

/** Automatic metadata work, separate from user-requested edits. */
@Composable
fun DeviceDescription(
    sync: SyncStateView,
    id: String,
    description: String,
    save: suspend () -> Unit,
) {
    val me = sync.devicesList.firstOrNull { it.deviceId == id }
    LaunchedEffect(me?.metaRev, sync.connection, sync.leaseEndMs) {
        if (
            me != null &&
                (!me.hasMeta() || me.meta.platform != "android" || me.meta.description != description) &&
                sync.connection == CoreConnState.CONN_STATE_CONNECTED
        ) {
            runCatching { save() }
        }
    }
}
