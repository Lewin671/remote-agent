package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
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
        var state by
            mutableStateOf(
                RemoteState(
                    conn = ConnState.ONLINE,
                    leaseEndMs = 1,
                    devices = mapOf("me" to DeviceView("me", DeviceKind.DEVICE_KIND_CLIENT, 0)),
                ),
            )
        var calls = 0
        ui.setContent {
            DeviceDescription(state, "me", "Android test") {
                calls++
                if (calls == 1) throw DeviceSecurity.Failure("lease")
            }
        }
        ui.runOnIdle {
            assertEquals(1, calls)
            state = state.copy(leaseEndMs = 2)
        }
        ui.waitForIdle()
        ui.runOnIdle { assertEquals(2, calls) }
    }
}
