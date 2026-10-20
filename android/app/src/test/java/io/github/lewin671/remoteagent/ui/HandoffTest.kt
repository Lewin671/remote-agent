package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.DeviceView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.DeviceMeta
import org.junit.After
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class HandoffTest {
    @get:Rule val ui = createComposeRule()
    private val self = "1111111111111111"
    private val remote =
        Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), self), Api.defaultHttp())

    @After
    fun close() {
        remote.close()
    }

    private fun device(
        name: String,
        platform: String = "android",
        kind: DeviceKind = DeviceKind.DEVICE_KIND_CLIENT,
        offerSize: Int = 32,
        id: String = name,
    ) =
        DeviceView(
            id,
            kind,
            0,
            DeviceMeta.newBuilder()
                .setName(name)
                .setPlatform(platform)
                .setHandoffRecoveryEd(ByteString.copyFrom(ByteArray(offerSize)))
                .build(),
        )

    private fun show(vararg devices: DeviceView) {
        // RemoteState's directory contains only verified current members and opened self-signed metadata.
        remote.showForTest(RemoteState(devices = devices.associateBy { it.id }, isAuthority = true))
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote) { Column { HandoffControls() } }
            }
        }
    }

    @Test
    fun clientsWithOffersAreEligibleRegardlessOfPlatform() {
        show(device("Android"), device("Web", "web"), device("iOS", "ios"), device("Missing", ""), device("Other", "other"))
        for (name in listOf("Android", "Web", "iOS", "Missing", "Other")) {
            ui.onNodeWithText("交接给 $name").assertExists()
        }
    }

    @Test
    fun machineClaimingAndroidAndUnknownKindsAreNotEligible() {
        show(
            device("Machine", kind = DeviceKind.DEVICE_KIND_MACHINE),
            device("Unknown", kind = DeviceKind.DEVICE_KIND_UNSPECIFIED),
        )
        ui.onNodeWithText("交接给 Machine").assertDoesNotExist()
        ui.onNodeWithText("交接给 Unknown").assertDoesNotExist()
        ui.onNodeWithText("等待新手机生成并保存恢复码").assertExists()
    }

    @Test
    fun selfAndMissingOrMalformedOffersAreNotEligible() {
        show(
            device("Self", id = self),
            device("Missing", offerSize = 0),
            device("Short", offerSize = 31),
            device("Long", offerSize = 33),
            DeviceView("NoMeta", DeviceKind.DEVICE_KIND_CLIENT, 0),
        )
        for (name in listOf("Self", "Missing", "Short", "Long", "NoMeta")) {
            ui.onNodeWithText("交接给 $name").assertDoesNotExist()
        }
        ui.onNodeWithText("等待新手机生成并保存恢复码").assertExists()
    }
}
