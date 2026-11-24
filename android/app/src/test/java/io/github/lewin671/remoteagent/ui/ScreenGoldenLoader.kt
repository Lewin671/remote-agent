package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.proto.*
import java.io.File

/**
 * Parses `testdata/screens/<scenario>.golden.pb` binary protobuf snapshots directly
 * into the generated lite protobuf ViewModels so Robolectric UI tests render
 * without any hand-written JSON mirror.
 */
object ScreenGoldenLoader {
    private fun dir(): File {
        val prop = System.getProperty("screenGoldens")
        val f = if (!prop.isNullOrEmpty()) File(prop) else File("../testdata/screens")
        check(f.isDirectory) { "screenGoldens directory not found: ${f.absolutePath}" }
        return f
    }

    fun namedViews(scenario: String): Map<String, ViewModel> {
        val file = File(dir(), "$scenario.golden.pb")
        val parsed = ScreenGoldenFile.parseFrom(file.readBytes())
        val out = LinkedHashMap<String, ViewModel>()
        for (step in parsed.stepsList) {
            for (view in step.viewsList) {
                check(out.put(view.name, view.viewModel) == null) {
                    "Duplicate golden view '${view.name}' in $scenario.golden.pb"
                }
            }
        }
        return out
    }

    private fun requireView(scenario: String, viewName: String): ViewModel =
        namedViews(scenario)[viewName]
            ?: error("Missing golden view '$viewName' in $scenario.golden.pb")

    fun homeView(scenario: String, viewName: String): HomeViewModel =
        requireView(scenario, viewName).home

    fun projectView(scenario: String, viewName: String): ProjectViewModel =
        requireView(scenario, viewName).project

    fun newSessionView(scenario: String, viewName: String): NewSessionViewModel =
        requireView(scenario, viewName).newSession

    fun machineStatusView(scenario: String, viewName: String): MachineStatusViewModel =
        requireView(scenario, viewName).machineStatus

    fun quotaView(scenario: String, viewName: String): QuotaViewModel =
        requireView(scenario, viewName).quota

    fun settingsView(scenario: String, viewName: String): SettingsViewModel =
        requireView(scenario, viewName).settings

    fun machineSettingsView(scenario: String, viewName: String): MachineSettingsViewModel =
        requireView(scenario, viewName).machineSettings

    fun devicesView(scenario: String, viewName: String): DevicesViewModel =
        requireView(scenario, viewName).devices

    fun clientDetailView(scenario: String, viewName: String): ClientDetailViewModel =
        requireView(scenario, viewName).clientDetail

    fun sessionView(scenario: String, viewName: String): SessionViewModel =
        requireView(scenario, viewName).session

    fun schedulesView(scenario: String, viewName: String): SchedulesViewModel =
        requireView(scenario, viewName).schedules

    fun scheduleDetailView(scenario: String, viewName: String): ScheduleDetailViewModel =
        requireView(scenario, viewName).scheduleDetail

    fun scheduleFormView(scenario: String, viewName: String): ScheduleFormViewModel =
        requireView(scenario, viewName).scheduleForm

    fun scheduleSessionStripView(scenario: String, viewName: String): ScheduleSessionStripViewModel =
        requireView(scenario, viewName).scheduleSessionStrip
}
