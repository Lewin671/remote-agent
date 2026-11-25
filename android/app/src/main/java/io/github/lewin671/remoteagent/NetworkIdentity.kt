package io.github.lewin671.remoteagent

import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.net.Inet6Address
import java.net.InetAddress

private val NUMERIC_IP_REGEX = Regex("""^[0-9a-fA-F:.]+$""")

/**
 * Normalizes a LinkAddress string for network-switch detection:
 * - Drops link-local addresses (fe80::/10, 169.254.0.0/16) that do not determine internet routing.
 * - Normalizes global/ULA IPv6 addresses to their network prefix so RFC 4941 temporary privacy
 *   address rotation on the same subnet does not trigger spurious WS/Direct teardowns.
 */
internal fun normalizeLinkAddress(raw: String): String? {
    val slash = raw.indexOf('/')
    val hostPart = (if (slash >= 0) raw.substring(0, slash) else raw).substringBefore('%').trim()
    val prefixPart = if (slash >= 0) raw.substring(slash + 1).trim().toIntOrNull() else null
    if (hostPart.isEmpty() || !NUMERIC_IP_REGEX.matches(hostPart)) return raw
    val addr = runCatching { InetAddress.getByName(hostPart) }.getOrNull() ?: return raw
    if (addr.isLinkLocalAddress) return null
    if (addr is Inet6Address && prefixPart != null && prefixPart in 0..128) {
        val bytes = addr.address
        val fullBytes = prefixPart / 8
        val remBits = prefixPart % 8
        if (fullBytes < bytes.size) {
            if (remBits > 0) {
                val mask = (0xff shl (8 - remBits)) and 0xff
                bytes[fullBytes] = (bytes[fullBytes].toInt() and mask).toByte()
                for (i in fullBytes + 1 until bytes.size) bytes[i] = 0
            } else {
                for (i in fullBytes until bytes.size) bytes[i] = 0
            }
        }
        val prefixAddr = InetAddress.getByAddress(bytes).hostAddress ?: hostPart
        return "$prefixAddr/$prefixPart"
    }
    return raw
}

private fun isLinkLocalRoute(route: String): Boolean {
    val trimmed = route.trim().lowercase()
    return trimmed.startsWith("fe80:") || trimmed.startsWith("169.254.")
}

/** Canonical in-memory identity for a default route; never logged or persisted. */
internal fun networkIdentity(
    handle: Long?, name: String?, addresses: List<String>?, routes: List<String>?, dns: List<String>?,
    transports: List<Int>, capabilities: List<Int>,
): String = listOf(
    handle,
    name,
    addresses?.mapNotNull(::normalizeLinkAddress)?.distinct()?.sorted(),
    routes?.filterNot(::isLinkLocalRoute)?.distinct()?.sorted(),
    dns?.distinct()?.sorted(),
    transports.distinct().sorted(),
    capabilities.filterNot { it == NetworkCapabilities.NET_CAPABILITY_VALIDATED }.distinct().sorted(),
).toString()

/**
 * Both baseline attachment and callbacks describe the same observed route.
 * Excludes NET_CAPABILITY_VALIDATED because probe-state churn during Wi-Fi setup or transient
 * reachability checks is not a real route change and must not tear down WS/Direct.
 */
internal fun networkIdentity(
    network: Network?, properties: LinkProperties?, capabilities: NetworkCapabilities?,
): String = networkIdentity(
    network?.networkHandle,
    properties?.interfaceName,
    properties?.linkAddresses?.map { it.toString() },
    properties?.routes?.map { it.toString() },
    properties?.dnsServers?.mapNotNull { it.hostAddress },
    (0..7).filter { capabilities?.hasTransport(it) == true },
    listOf(
        NetworkCapabilities.NET_CAPABILITY_INTERNET,
        NetworkCapabilities.NET_CAPABILITY_NOT_METERED,
        NetworkCapabilities.NET_CAPABILITY_NOT_VPN,
    ).filter { capabilities?.hasCapability(it) == true },
)

/**
 * Coalesces rapid ConnectivityManager NetworkCallback bursts (onAvailable -> onCapabilitiesChanged ->
 * onLinkPropertiesChanged, plus IPv6 SLAAC arrival) into a single network observation while delivering
 * offline loss immediately and bounding maximum coalesce delay.
 */
internal class NetworkObservationCoalescer(
    private val scope: CoroutineScope,
    private val debounceMs: Long = 150L,
    private val maxWaitMs: Long = 500L,
    private val monotonic: () -> Long = { System.nanoTime() / 1_000_000L },
    private val emit: (signature: String, available: Boolean) -> Unit,
) {
    private val lock = Any()
    private var pendingJob: Job? = null
    private var firstPendingAtMs: Long = -1L
    private var pendingSignature: String? = null
    private var pendingAvailable: Boolean = true

    /** Cancels any pending debounced update and dispatches immediately on [scope]. */
    fun emitNow(signature: String, available: Boolean) {
        val jobToCancel: Job?
        synchronized(lock) {
            jobToCancel = pendingJob
            pendingJob = null
            firstPendingAtMs = -1L
            pendingSignature = null
        }
        jobToCancel?.cancel()
        scope.launch { emit(signature, available) }
    }

    /** Debounces bursts on [scope] up to [debounceMs], capped at [maxWaitMs] from the first burst event. */
    fun schedule(signature: String, available: Boolean) {
        val oldJob: Job?
        synchronized(lock) {
            val now = monotonic()
            if (firstPendingAtMs < 0L) firstPendingAtMs = now
            pendingSignature = signature
            pendingAvailable = available
            val remainingMax = (firstPendingAtMs + maxWaitMs - now).coerceAtLeast(0L)
            val delayMs = minOf(debounceMs, remainingMax)
            oldJob = pendingJob
            pendingJob = scope.launch {
                if (delayMs > 0L) delay(delayMs)
                val toEmit = synchronized(lock) {
                    val sig = pendingSignature ?: return@synchronized null
                    val avail = pendingAvailable
                    pendingSignature = null
                    pendingJob = null
                    firstPendingAtMs = -1L
                    sig to avail
                }
                if (toEmit != null) emit(toEmit.first, toEmit.second)
            }
        }
        oldJob?.cancel()
    }
}
