package io.github.lewin671.remoteagent

import android.app.Application
import android.content.Context
import android.content.ContextWrapper
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
    @Test
    fun logoutDropsTheRetainedAccountEvenWhenKeyErasureNeedsRetry() = runBlocking {
        val app = RaApp()
        ContextWrapper::class.java.getDeclaredMethod("attachBaseContext", Context::class.java)
            .apply { isAccessible = true }.invoke(app, RuntimeEnvironment.getApplication())
        RaApp::class.java.getDeclaredField("instance").apply { isAccessible = true }.set(null, app)
        val old = Account("https://relay.example.com", ByteArray(32), ByteArray(32), "synthetic-device")
        @Suppress("UNCHECKED_CAST")
        val setup = RaApp::class.java.getDeclaredField("_securitySetup").apply { isAccessible = true }
            .get(app) as MutableStateFlow<Account?>
        setup.value = old
        app.logout(pairing = true)
        assertNull(app.securitySetup.value)
        assertFalse(app.pendingCreation.value)
        assertTrue(app.joinDirectly)
        assertEquals(old.server, app.lastServer)
        assertEquals(1, app.joinGeneration.value)
        app.logout()
        assertNull(app.securitySetup.value)
        assertFalse(app.joinDirectly)
    }
}
