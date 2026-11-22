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
            Account("http://127.0.0.1:1", "2222222222222222"),
            Api.defaultHttp(),
        )
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private val archive =
        RemoteState(
            machines = mapOf(id to MachineView(id, false, 0, null)),
            conn = ConnState.OFFLINE,
            loaded = true,
            isAuthority = true,
        )
    private val current =
        archive.copy(
            conn = ConnState.ONLINE,
            devices = mapOf(id to DeviceView(id, DeviceKind.DEVICE_KIND_MACHINE, 0)),
        )

    @After
    fun close() {
        remote.close()
    }

    @Test
    fun removedArchiveExplainsItsStatusAndNeverOffersOrdinaryRemoval() {
        remote.showForTest(archive)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    DeviceScreen(id)
                }
            }
        }
        ui.waitUntil(3000) { remote.machineSettingsView.value.machineId == id && remote.machineSettingsView.value.found }
        ui.onNodeWithText("本机已被移出账户").assertExists()
        ui.onNodeWithText("移除这台电脑…").assertDoesNotExist()
        ui.onNodeWithText("移除会立即断开连接，保留中继上的会话和图片；它需要重新加入账户才能连接。").assertDoesNotExist()
        ui.onNodeWithText("删除这台电脑的记录…").performScrollTo().assertIsDisplayed()
        ui.runOnIdle { remote.showForTest(current) }
        ui.waitUntil(3000) { remote.machineSettingsView.value.member }
        ui.onNodeWithText("本机已被移出账户").assertDoesNotExist()
        ui.onNodeWithText("移除这台电脑…").performScrollTo().assertIsDisplayed()
        ui.onNodeWithText("删除这台电脑的记录…").assertDoesNotExist()
    }

    @Test
    fun removedClientDetailDoesNotOfferOrdinaryRemoval() {
        remote.showForTest(archive)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    ClientScreen(id)
                }
            }
        }
        ui.waitUntil(3000) { remote.clientDetailView.value.deviceId == id }
        ui.onNodeWithText("这个客户端已不在当前成员列表中").assertExists()
        ui.onNodeWithText("移除这个客户端").assertDoesNotExist()
        ui.runOnIdle {
            remote.showForTest(
                archive.copy(
                    conn = ConnState.ONLINE,
                    devices = mapOf(id to DeviceView(id, DeviceKind.DEVICE_KIND_CLIENT, 0)),
                ),
            )
        }
        ui.waitUntil(3000) { remote.clientDetailView.value.found && remote.clientDetailView.value.canRemove }
        ui.onNodeWithText("这个客户端已不在当前成员列表中").assertDoesNotExist()
        ui.onNodeWithText("移除这个客户端").assertExists()
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
        ui.waitUntil(3000) { remote.machineSettingsView.value.found && remote.machineSettingsView.value.member }
        ui.onNodeWithText("删除这台电脑的记录…").assertDoesNotExist()
        ui.runOnIdle { remote.showForTest(archive) }
        ui.waitUntil(3000) { !remote.machineSettingsView.value.member }
        ui.onNodeWithText("删除这台电脑的记录…").performScrollTo().assertIsDisplayed()
        ui.runOnIdle { remote.showForTest(archive.copy(isAuthority = false)) }
        ui.waitForIdle()
        ui.onNodeWithText("删除这台电脑的记录…").assertDoesNotExist()
        ui.onNodeWithText("重新加入并接续会话").assertDoesNotExist()
        ui.runOnIdle { remote.showForTest(current.copy(isAuthority = false)) }
        ui.waitUntil(3000) { remote.machineSettingsView.value.member }
        ui.onNodeWithText("移除这台电脑…").assertDoesNotExist()
        ui.runOnIdle { remote.showForTest(current) }
        ui.waitForIdle()
        ui.onNodeWithText("删除这台电脑的记录…").assertDoesNotExist()
    }
}
