package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
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
    @get:org.junit.Rule val batchCResources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
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

    private val addHintText get() = messageTextForTest(UiMessages.securityAddOnAuthority("Desk Browser"))
    private val noMachinesHintText get() = messageTextForTest(UiMessages.homeAddViaManager("Desk Browser"))

    private fun settingsVm(canAdd: Boolean, hasMachine: Boolean): io.github.lewin671.remoteagent.core.proto.SettingsViewModel =
        io.github.lewin671.remoteagent.core.proto.SettingsViewModel.newBuilder().apply {
            canAddDevice = canAdd
            addDeviceHint = io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral(addHintText).build()
            emptyMachinesText = io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral(noMachinesHintText).build()
            if (hasMachine) {
                addMachines(
                    io.github.lewin671.remoteagent.core.proto.MachineListCardView.newBuilder()
                        .setMachineId(machine)
                        .setName(machine)
                        .setOnline(true)
                        .setMember(true),
                )
            }
        }.build()

    private fun devicesVm(canAdd: Boolean): io.github.lewin671.remoteagent.core.proto.DevicesViewModel =
        io.github.lewin671.remoteagent.core.proto.DevicesViewModel.newBuilder().apply {
            isManager = canAdd
            canAddDevice = canAdd
            addDeviceHint = io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral(addHintText).build()
            addComputers(
                io.github.lewin671.remoteagent.core.proto.MachineListCardView.newBuilder()
                    .setMachineId(machine)
                    .setName(machine)
                    .setOnline(true)
                    .setMember(true),
            )
        }.build()

    private fun show(state: RemoteState, content: @androidx.compose.runtime.Composable () -> Unit) {
        remote.showForTest(state)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) { content() }
            }
        }
    }

    @Test
    fun settingsOffersAddingOnlyOnTheAuthority() {
        show(withMachine.copy(isAuthority = true)) {
            Column { MachinesGroup(settingsVm(canAdd = true, hasMachine = true), onPair = { nav.pair() }) }
        }
        ui.onNodeWithText("添加设备").performClick()
        assertEquals(1, paired)
        ui.onNodeWithText(addHintText).assertDoesNotExist()
    }

    @Test
    fun settingsOnAnotherClientSaysWhereToAdd() {
        show(withMachine) {
            Column { MachinesGroup(settingsVm(canAdd = false, hasMachine = true), onPair = { nav.pair() }) }
        }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(addHintText).assertExists()
    }

    @Test
    fun settingsOnAnotherClientWithNoComputerSaysSo() {
        show(empty) {
            Column { MachinesGroup(settingsVm(canAdd = false, hasMachine = false), onPair = { nav.pair() }) }
        }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(noMachinesHintText).assertExists()
    }

    @Test
    fun homeEmptyStateKeepsItsButtonOnlyOnTheAuthority() {
        val emptyView = io.github.lewin671.remoteagent.core.proto.HomeEmptyView.newBuilder()
            .setKind(io.github.lewin671.remoteagent.core.proto.HomeEmptyKind.HOME_EMPTY_KIND_NO_MACHINES)
            .setMessage(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral(addHintText))
            .setCanAddDevice(true)
            .build()
        show(empty.copy(isAuthority = true)) { EmptyHomeView(emptyView, onPair = { nav.pair() }, onRetry = {}) }
        ui.onNodeWithText("添加设备").performClick()
        assertEquals(1, paired)
        ui.onNodeWithText(noMachinesHintText).assertDoesNotExist()
    }

    @Test
    fun homeEmptyStateOnAnotherClientSaysWhereToAdd() {
        val memberView = io.github.lewin671.remoteagent.core.proto.HomeEmptyView.newBuilder()
            .setKind(io.github.lewin671.remoteagent.core.proto.HomeEmptyKind.HOME_EMPTY_KIND_NO_MACHINES_MEMBER)
            .setMessage(io.github.lewin671.remoteagent.core.proto.Text.newBuilder().setLiteral(noMachinesHintText))
            .build()
        show(empty) { EmptyHomeView(memberView, onPair = { nav.pair() }, onRetry = {}) }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(noMachinesHintText).assertExists()
    }

    @Test
    fun machineMetadataCannotGrantManagementEntries() {
        val machineClient = DeviceView(remote.account.deviceId,
            io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_MACHINE, 0,
            io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder().setPlatform("android").build())
        show(empty.copy(devices = empty.devices + (machineClient.id to machineClient))) {
            val vm by remote.settingsView.collectAsState()
            Column { MachinesGroup(vm, onPair = { nav.pair() }) }
        }
        ui.waitUntil(3000) { !remote.settingsView.value.isManager && remote.settingsView.value.authorityName == "Desk Browser" }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(addHintText).assertExists()
    }

    @Test
    fun devicesPageNamesItsEntryAsSettingsDoes() {
        show(withMachine.copy(isAuthority = true)) {
            DevicesScreenContent(devicesVm(canAdd = true), onPair = { nav.pair() })
        }
        ui.onNodeWithText("添加电脑或客户端").assertDoesNotExist()
        ui.onNodeWithText("添加设备").performScrollTo().performClick()
        assertEquals(1, paired)
        ui.onNodeWithText(addHintText).assertDoesNotExist()
    }

    @Test
    fun devicesPageOnAnotherClientSaysWhereToAdd() {
        show(withMachine) {
            DevicesScreenContent(devicesVm(canAdd = false), onPair = { nav.pair() })
        }
        ui.onNodeWithText("添加设备").assertDoesNotExist()
        ui.onNodeWithText(addHintText).performScrollTo().assertExists()
    }
}
