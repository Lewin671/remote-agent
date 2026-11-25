package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.NotifyKind

fun machineNotificationKinds(kinds: List<NotifyKind>, advertised: Boolean): List<NotifyKind> =
    kinds.filter { it != NotifyKind.NOTIFY_KIND_MACHINE_DISK } + if (advertised) listOf(NotifyKind.NOTIFY_KIND_MACHINE_DISK) else emptyList()
