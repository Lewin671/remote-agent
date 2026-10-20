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

/** Only the authority can add devices; other clients show its directory name. */
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
    private val authority = DeviceView(
        "3333333333333333", io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT, 0,
        io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder().setName("Desk Browser").setPlatform("web").build(),
    )
    private val empty = RemoteState(conn = ConnState.OFFLINE, loaded = true,
        authorityDeviceId = authority.id, devices = mapOf(authority.id to authority))
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
        assertEquals("要添加电脑或客户端，在管理端（Desk Browser）上打开「设置 › 添加设备」。", addDeviceHint(empty))
        assertEquals("还没有电脑。在管理端（Desk Browser）上打开「设置 › 添加设备」，把电脑加进来。", noMachinesHint(empty))
    }

    @Test
    fun settingsOffersAddingOnlyOnTheAuthority() {
        show(withMachine.copy(isAuthority = true)) { Column { MachinesGroup(it, 0) } }
        ui.onNodeWithText("添加设备").performClick()
        assertEquals(1, paired)
        ui.onNodeWithText(addDeviceHint(empty)).assertDoesNotExist()
    }

    @Test
    fun settingsOnAnotherClientSaysWhereToAdd() {
        show(withMachine) { Column { MachinesGroup(it, 0) } }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(addDeviceHint(empty)).assertExists()
    }

    @Test
    fun settingsOnAnotherClientWithNoComputerSaysSo() {
        show(empty) { Column { MachinesGroup(it, 0) } }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(noMachinesHint(empty)).assertExists()
    }

    @Test
    fun homeEmptyStateKeepsItsButtonOnlyOnTheAuthority() {
        show(empty.copy(isAuthority = true)) { EmptyAccount(it) }
        ui.onNodeWithText("添加设备").performClick()
        assertEquals(1, paired)
        ui.onNodeWithText(noMachinesHint(empty)).assertDoesNotExist()
    }

    @Test
    fun homeEmptyStateOnAnotherClientSaysWhereToAdd() {
        show(empty) { EmptyAccount(it) }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(noMachinesHint(empty)).assertExists()
    }

    @Test
    fun machineMetadataCannotGrantManagementEntries() {
        val machineClient = DeviceView(remote.account.deviceId,
            io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_MACHINE, 0,
            io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder().setPlatform("android").build())
        show(empty.copy(devices = empty.devices + (machineClient.id to machineClient))) {
            Column { MachinesGroup(it, 0) }
        }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(noMachinesHint(empty)).assertExists()
    }

    @Test
    fun devicesPageNamesItsEntryAsSettingsDoes() {
        show(withMachine.copy(isAuthority = true)) { DevicesScreen() }
        ui.onNodeWithText("添加电脑或客户端").assertDoesNotExist()
        ui.onNodeWithText("添加设备").performScrollTo().performClick()
        assertEquals(1, paired)
        ui.onNodeWithText(addDeviceHint(empty)).assertDoesNotExist()
    }

    @Test
    fun devicesPageOnAnotherClientSaysWhereToAdd() {
        show(withMachine) { DevicesScreen() }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(addDeviceHint(empty)).performScrollTo().assertExists()
    }
}
