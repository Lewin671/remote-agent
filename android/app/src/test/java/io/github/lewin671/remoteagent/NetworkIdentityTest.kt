package io.github.lewin671.remoteagent

import android.net.LinkProperties
import android.net.NetworkCapabilities
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowNetworkCapabilities

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = android.app.Application::class)
class NetworkIdentityTest {
    @Test fun addressRouteAndCapabilityChangesAreNotHiddenByTheSameHandle() {
        fun snapshot(address: String = "192.0.2.1/24", route: String = "default", caps: List<Int> = listOf(1, 2)) =
            networkIdentity(7, "wlan0", listOf(address), listOf(route), listOf("192.0.2.53"), listOf(1), caps)
        assertEquals(snapshot(), snapshot(caps = listOf(2, 1)))
        assertNotEquals(snapshot(), snapshot(address = "192.0.2.2/24"))
        assertNotEquals(snapshot(), snapshot(route = "another-default"))
        assertNotEquals(snapshot(), snapshot(caps = listOf(1)))
    }

    @Test fun validatedCapabilityChurnDoesNotChangeNetworkIdentity() {
        val props = LinkProperties().apply { interfaceName = "wlan0" }
        val unvalidated = ShadowNetworkCapabilities.newInstance().also { caps ->
            org.robolectric.Shadows.shadowOf(caps).apply {
                addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)
                addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            }
        }
        val validated = ShadowNetworkCapabilities.newInstance().also { caps ->
            org.robolectric.Shadows.shadowOf(caps).apply {
                addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)
                addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                addCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)
            }
        }
        assertEquals(
            "NET_CAPABILITY_VALIDATED flicker must not change networkIdentity",
            networkIdentity(null, props, unvalidated),
            networkIdentity(null, props, validated),
        )
    }

    @Test fun ipv6PrivacyRotationAndLinkLocalChurnDoNotChangeNetworkIdentity() {
        val initial = networkIdentity(
            handle = 7L,
            name = "wlan0",
            addresses = listOf(
                "192.0.2.10/24",
                "fe80::1111:2222:3333:4444/64",
                "2001:db8:abcd:1234:aaaa:bbbb:cccc:dddd/64",
            ),
            routes = listOf("0.0.0.0/0 -> 192.0.2.1 wlan0", "fe80::/64 -> :: wlan0"),
            dns = listOf("192.0.2.53"),
            transports = listOf(NetworkCapabilities.TRANSPORT_WIFI),
            capabilities = listOf(NetworkCapabilities.NET_CAPABILITY_INTERNET, NetworkCapabilities.NET_CAPABILITY_NOT_VPN),
        )
        val rotated = networkIdentity(
            handle = 7L,
            name = "wlan0",
            addresses = listOf(
                "192.0.2.10/24",
                "fe80::9999:8888:7777:6666/64",
                "2001:db8:abcd:1234:1111:2222:3333:4444/64",
                "2001:db8:abcd:1234:5555:6666:7777:8888/64",
            ),
            routes = listOf("0.0.0.0/0 -> 192.0.2.1 wlan0"),
            dns = listOf("192.0.2.53"),
            transports = listOf(NetworkCapabilities.TRANSPORT_WIFI),
            capabilities = listOf(NetworkCapabilities.NET_CAPABILITY_INTERNET, NetworkCapabilities.NET_CAPABILITY_NOT_VPN),
        )
        val differentPrefix = networkIdentity(
            handle = 7L,
            name = "wlan0",
            addresses = listOf(
                "192.0.2.10/24",
                "2001:db8:9999:0001:1111:2222:3333:4444/64",
            ),
            routes = listOf("0.0.0.0/0 -> 192.0.2.1 wlan0"),
            dns = listOf("192.0.2.53"),
            transports = listOf(NetworkCapabilities.TRANSPORT_WIFI),
            capabilities = listOf(NetworkCapabilities.NET_CAPABILITY_INTERNET, NetworkCapabilities.NET_CAPABILITY_NOT_VPN),
        )
        assertEquals("IPv6 privacy rotation and link-local churn must keep same identity", initial, rotated)
        assertNotEquals("Different IPv6 prefix must change identity", initial, differentPrefix)
    }

    @Test fun vpnToggleStillChangesNetworkIdentity() {
        val props = LinkProperties().apply { interfaceName = "wlan0" }
        val withoutVpn = ShadowNetworkCapabilities.newInstance().also { caps ->
            org.robolectric.Shadows.shadowOf(caps).apply {
                addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            }
        }
        val withVpn = ShadowNetworkCapabilities.newInstance().also { caps ->
            org.robolectric.Shadows.shadowOf(caps).apply {
                addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                addTransportType(NetworkCapabilities.TRANSPORT_VPN)
                addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                removeCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            }
        }
        assertNotEquals(networkIdentity(null, props, withoutVpn), networkIdentity(null, props, withVpn))
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test fun coalescerMergesWifiSetupBurstAndEmitsOfflineImmediately() = runTest {
        val emitted = ArrayList<Pair<String, Boolean>>()
        val coalescer = NetworkObservationCoalescer(
            scope = this,
            debounceMs = 150L,
            maxWaitMs = 500L,
            monotonic = { testScheduler.currentTime },
        ) { sig, avail -> emitted.add(sig to avail) }

        // Simulate Wi-Fi reconnect burst: onAvailable -> onCaps -> onLinkProps(v4) -> onLinkProps(v4+v6).
        coalescer.schedule("net2-empty", true)
        advanceTimeBy(5)
        coalescer.schedule("net2-v4", true)
        advanceTimeBy(110)
        coalescer.schedule("net2-v4-v6", true)
        advanceTimeBy(149)
        runCurrent()
        assertTrue("burst should not emit before debounce settles", emitted.isEmpty())

        advanceTimeBy(1)
        runCurrent()
        assertEquals(listOf("net2-v4-v6" to true), emitted)

        // Offline onLost must cancel any pending debounced update and deliver immediately.
        coalescer.schedule("net3-partial", true)
        advanceTimeBy(50)
        coalescer.emitNow("offline", false)
        runCurrent()
        assertEquals(listOf("net2-v4-v6" to true, "offline" to false), emitted)
        advanceTimeBy(500)
        runCurrent()
        assertEquals("cancelled pending update must not emit after offline", 2, emitted.size)
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test fun coalescerCapsMaximumWaitUnderContinuousBurst() = runTest {
        val emitted = ArrayList<Pair<String, Boolean>>()
        val coalescer = NetworkObservationCoalescer(
            scope = this,
            debounceMs = 150L,
            maxWaitMs = 400L,
            monotonic = { testScheduler.currentTime },
        ) { sig, avail -> emitted.add(sig to avail) }

        coalescer.schedule("s0", true)
        repeat(4) { idx ->
            advanceTimeBy(100)
            coalescer.schedule("s${idx + 1}", true)
        }
        runCurrent()
        assertEquals("must emit once maxWaitMs elapses", listOf("s4" to true), emitted)
    }
}
