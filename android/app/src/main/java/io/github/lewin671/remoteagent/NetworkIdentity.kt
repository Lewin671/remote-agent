package io.github.lewin671.remoteagent

/** Canonical in-memory identity for a default route; never logged or persisted. */
internal fun networkIdentity(
    handle: Long?, name: String?, addresses: List<String>?, routes: List<String>?, dns: List<String>?,
    transports: List<Int>, capabilities: List<Int>,
): String = listOf(handle, name, addresses?.sorted(), routes?.sorted(), dns?.sorted(), transports.sorted(), capabilities.sorted()).toString()


/** Both baseline attachment and callbacks describe the same observed route. */
internal fun networkIdentity(
    network: android.net.Network?, properties: android.net.LinkProperties?, capabilities: android.net.NetworkCapabilities?,
): String = networkIdentity(
    network?.networkHandle, properties?.interfaceName,
    properties?.linkAddresses?.map { it.toString() }, properties?.routes?.map { it.toString() },
    properties?.dnsServers?.mapNotNull { it.hostAddress },
    (0..7).filter { capabilities?.hasTransport(it) == true },
    listOf(android.net.NetworkCapabilities.NET_CAPABILITY_INTERNET, android.net.NetworkCapabilities.NET_CAPABILITY_VALIDATED,
        android.net.NetworkCapabilities.NET_CAPABILITY_NOT_METERED, android.net.NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
        .filter { capabilities?.hasCapability(it) == true },
)
