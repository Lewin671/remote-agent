package io.github.lewin671.remoteagent

/** Canonical in-memory identity for a default route; never logged or persisted. */
internal fun networkIdentity(
    handle: Long?, name: String?, addresses: List<String>?, routes: List<String>?, dns: List<String>?,
    transports: List<Int>, capabilities: List<Int>,
): String = listOf(handle, name, addresses?.sorted(), routes?.sorted(), dns?.sorted(), transports.sorted(), capabilities.sorted()).toString()
