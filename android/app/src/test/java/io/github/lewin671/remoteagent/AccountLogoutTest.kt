package io.github.lewin671.remoteagent

import android.app.Application
import io.github.lewin671.remoteagent.core.Account
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class AccountLogoutTest {
    @get:org.junit.Rule val appFixture = io.github.lewin671.remoteagent.RaAppTestRule()
    @Test
    fun logoutDropsTheRetainedAccountEvenWhenKeyErasureNeedsRetry() = runBlocking {
        val app = appFixture.app
        val old = Account("https://relay.example.com", "synthetic-device")
        @Suppress("UNCHECKED_CAST")
        val statusField = RaApp::class.java.getDeclaredField("_bootstrapStatus").apply { isAccessible = true }
            .get(app) as MutableStateFlow<io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus?>
        statusField.value = io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus.newBuilder()
            .setBootstrap(io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_CREATE)
            .setServer(old.server)
            .setDeviceId(old.deviceId)
            .build()
        app.logout(pairing = true)
        assertNull(app.bootstrapStatus.value?.takeIf { it.server.isNotEmpty() || it.bootstrap == io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_CREATE })
        assertTrue(app.joinDirectly)
        assertEquals(old.server, app.lastServer)
        assertEquals(1, app.joinGeneration.value)
        app.logout()
        assertFalse(app.joinDirectly)
    }
}
