package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.DeviceKind
import org.junit.After
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class ArchiveDeletionTest {
    @get:Rule val ui = createComposeRule()
    private val id = "1111111111111111"
    private val remote =
        Remote(
            Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "2222222222222222"),
            Api.defaultHttp(),
        )
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val archive =
        RemoteState(
            machines = mapOf(id to MachineView(id, false, 0, null)),
            conn = ConnState.OFFLINE,
            loaded = true,
            authorityPhone = true,
        )
    private val current =
        archive.copy(devices = mapOf(id to DeviceView(id, DeviceKind.DEVICE_KIND_MACHINE, 0)))

    @After
    fun close() {
        remote.close()
    }

    @Test
    fun deletionIsOnlyOfferedForAnArchiveOfARemovedComputerToTheAuthority() {
        remote.showForTest(current)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    DeviceScreen(id)
                }
            }
        }
        ui.onNodeWithText("删除这台电脑的记录").assertDoesNotExist()
        ui.runOnIdle { remote.showForTest(archive) }
        ui.onNodeWithText("删除这台电脑的记录").performScrollTo().assertIsDisplayed()
        ui.runOnIdle { remote.showForTest(archive.copy(authorityPhone = false)) }
        ui.onNodeWithText("删除这台电脑的记录").assertDoesNotExist()
        ui.runOnIdle { remote.showForTest(current) }
        ui.onNodeWithText("删除这台电脑的记录").assertDoesNotExist()
    }
}
