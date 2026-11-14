package io.github.lewin671.remoteagent.core.transport

/** Only RelayPath is installed in phase 0b. */
internal class Router(val path: Path) {
    fun status(machineId: String) = path.status(machineId)
}
