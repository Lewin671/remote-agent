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
    fun removedArchiveExplainsItsStatusAndNeverOffersOrdinaryRemoval() {
        remote.showForTest(archive)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) {
                    DeviceScreen(id)
                }
            }
        }
        ui.onNodeWithText("已移除 · 保留的会话档案").assertExists()
        ui.onNodeWithText("移除这台机器").assertDoesNotExist()
        ui.onNodeWithText("移除会立即断开连接，保留中继上的会话和图片；它需要重新加入账户才能连接。").assertDoesNotExist()
        ui.onNodeWithText("让这台电脑重新加入并接续会话").assertExists()
        ui.onNodeWithText("删除这台电脑的记录").assertExists()
        ui.runOnIdle { remote.showForTest(current) }
        ui.onNodeWithText("已移除 · 保留的会话档案").assertDoesNotExist()
        ui.onNodeWithText("移除这台机器").assertExists()
        ui.onNodeWithText("删除这台电脑的记录").assertDoesNotExist()
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
        ui.onNodeWithText("这个客户端已不在当前成员列表中").assertExists()
        ui.onNodeWithText("移除这个客户端").assertDoesNotExist()
        ui.runOnIdle {
            remote.showForTest(
                archive.copy(
                    devices = mapOf(id to DeviceView(id, DeviceKind.DEVICE_KIND_CLIENT, 0)),
                ),
            )
        }
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
        ui.onNodeWithText("删除这台电脑的记录").assertDoesNotExist()
        ui.runOnIdle { remote.showForTest(archive) }
        ui.onNodeWithText("删除这台电脑的记录").performScrollTo().assertIsDisplayed()
        ui.runOnIdle { remote.showForTest(archive.copy(authorityPhone = false)) }
        ui.onNodeWithText("删除这台电脑的记录").assertDoesNotExist()
        ui.runOnIdle { remote.showForTest(current) }
        ui.onNodeWithText("删除这台电脑的记录").assertDoesNotExist()
    }
}
