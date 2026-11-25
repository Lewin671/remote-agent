package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.SyncDeviceView
import io.github.lewin671.remoteagent.core.proto.SyncStateView
import io.github.lewin671.remoteagent.proto.DeviceKind
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class DeviceDescriptionTest {
    @get:Rule val ui = createComposeRule()

    @Test
    fun renewalRetriesALeasePausedDescriptionWithoutAConnectionOrMetadataChange() {
        var sync by
            mutableStateOf(
                SyncStateView.newBuilder()
                    .setConnection(CoreConnState.CONN_STATE_CONNECTED)
                    .setLeaseEndMs(1)
                    .addDevices(
                        SyncDeviceView.newBuilder()
                            .setDeviceId("me")
                            .setKind(DeviceKind.DEVICE_KIND_CLIENT),
                    )
                    .build(),
            )
        var calls = 0
        ui.setContent {
            DeviceDescription(sync, "me", "Android test") {
                calls++
                if (calls == 1) throw IllegalStateException("lease")
            }
        }
        ui.runOnIdle {
            assertEquals(1, calls)
            sync = sync.toBuilder().setLeaseEndMs(2).build()
        }
        ui.waitForIdle()
        ui.runOnIdle { assertEquals(2, calls) }
    }

    @Test
    fun existingNonEmptyDescriptionDoesNotLoopWritesAcrossRenewals() {
        var sync by
            mutableStateOf(
                SyncStateView.newBuilder()
                    .setConnection(CoreConnState.CONN_STATE_CONNECTED)
                    .setLeaseEndMs(1)
                    .addDevices(
                        SyncDeviceView.newBuilder()
                            .setDeviceId("me")
                            .setKind(DeviceKind.DEVICE_KIND_CLIENT)
                            .setMeta(
                                io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder()
                                    .setPlatform("android")
                                    .setDescription("Earlier Android name"),
                            ),
                    )
                    .build(),
            )
        var calls = 0
        ui.setContent {
            DeviceDescription(sync, "me", "Newer Android name") {
                calls++
            }
        }
        ui.runOnIdle {
            assertEquals(0, calls)
            sync = sync.toBuilder().setLeaseEndMs(2).build()
        }
        ui.waitForIdle()
        ui.runOnIdle { assertEquals(0, calls) }
    }
}
