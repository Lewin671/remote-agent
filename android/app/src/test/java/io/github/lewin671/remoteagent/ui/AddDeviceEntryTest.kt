package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** Only the admin phone can add a device: any other phone gets the words web and iOS show, never a dead 添加设备. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class AddDeviceEntryTest {
    @get:Rule val ui = createComposeRule()
    private val machine = "1111111111111111"
    private val remote =
        Remote(
            Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "2222222222222222"),
            Api.defaultHttp(),
        )
    private var paired = 0
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, { paired++ }, { _, _ -> }, {}, {}, {})
    // Offline: the devices page refreshes from the server when online, which this test has none of.
    private val empty = RemoteState(conn = ConnState.OFFLINE, loaded = true)
    private val withMachine = empty.copy(machines = mapOf(machine to MachineView(machine, true, 0)))

    @After
    fun close() {
        remote.close()
    }

    private fun show(state: RemoteState, content: @androidx.compose.runtime.Composable (RemoteState) -> Unit) {
        remote.showForTest(state)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) { content(state) }
            }
        }
    }

    @Test
    fun theWordsAreTheOnesWebAndIosUse() {
        assertEquals("要添加电脑或客户端，在管理端手机上打开「设置 › 添加设备」。", ADD_DEVICE_HINT)
        assertEquals("还没有电脑。在管理端手机上打开「设置 › 添加设备」，把电脑加进来。", NO_MACHINES_HINT)
    }

    @Test
    fun settingsOffersAddingOnlyOnTheAdminPhone() {
        show(withMachine.copy(isAuthority = true)) { Column { MachinesGroup(it, 0) } }
        ui.onNodeWithText("添加设备").performClick()
        assertEquals(1, paired)
        ui.onNodeWithText(ADD_DEVICE_HINT).assertDoesNotExist()
    }

    @Test
    fun settingsOnAnotherPhoneSaysWhereToAdd() {
        show(withMachine) { Column { MachinesGroup(it, 0) } }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(ADD_DEVICE_HINT).assertExists()
    }

    @Test
    fun settingsOnAnotherPhoneWithNoComputerSaysSo() {
        show(empty) { Column { MachinesGroup(it, 0) } }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(NO_MACHINES_HINT).assertExists()
    }

    @Test
    fun homeEmptyStateKeepsItsButtonOnlyOnTheAdminPhone() {
        show(empty.copy(isAuthority = true)) { EmptyAccount(it) }
        ui.onNodeWithText("添加设备").performClick()
        assertEquals(1, paired)
        ui.onNodeWithText(NO_MACHINES_HINT).assertDoesNotExist()
    }

    @Test
    fun homeEmptyStateOnAnotherPhoneSaysWhereToAdd() {
        show(empty) { EmptyAccount(it) }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(NO_MACHINES_HINT).assertExists()
    }

    @Test
    fun devicesPageNamesItsEntryAsSettingsDoes() {
        show(withMachine.copy(isAuthority = true)) { DevicesScreen() }
        ui.onNodeWithText("添加电脑或客户端").assertDoesNotExist()
        ui.onNodeWithText("添加设备").performScrollTo().performClick()
        assertEquals(1, paired)
        ui.onNodeWithText(ADD_DEVICE_HINT).assertDoesNotExist()
    }

    @Test
    fun devicesPageOnAnotherPhoneSaysWhereToAdd() {
        show(withMachine) { DevicesScreen() }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(ADD_DEVICE_HINT).performScrollTo().assertExists()
    }
}
