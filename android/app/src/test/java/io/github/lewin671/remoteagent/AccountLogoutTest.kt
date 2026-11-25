package io.github.lewin671.remoteagent

import android.app.Application
import android.app.NotificationManager
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleRegistry
import androidx.lifecycle.ProcessLifecycleOwner
import com.google.firebase.messaging.RemoteMessage
import io.github.lewin671.remoteagent.core.MemorySecurityStoreV4
import io.github.lewin671.remoteagent.core.SecurityFailure
import io.github.lewin671.remoteagent.core.SecurityStoreV4
import io.github.lewin671.remoteagent.core.proto.AppRootStage
import io.github.lewin671.remoteagent.core.proto.ExitConfirmStep
import io.github.lewin671.remoteagent.core.proto.ExitIntentKind
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageResultEvent
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.NotifyKind
import io.github.lewin671.remoteagent.push.Push
import io.github.lewin671.remoteagent.push.RaMessagingService
import java.io.ByteArrayOutputStream
import java.io.DataOutputStream
import java.io.File
import java.security.MessageDigest
import java.util.Base64
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class AccountLogoutTest {
    @get:org.junit.Rule val appFixture = RaAppTestRule()

    @Test
    fun lockScreenPushPathNeverInstantiatesCoreHost() = runBlocking {
        val context = RuntimeEnvironment.getApplication()
        val app = appFixture.app
        // Simulate cold-start FCM delivery while locked (account presence marker only, no Keystore unlock).
        val marker = File(context.noBackupFilesDir, "security-v4-buckets.bin")
        marker.writeBytes(byteArrayOf(0))
        app.prefs.update { it.copy(notifyAttention = true, notifyTurnEnded = true) }
        Push.createChannels(context)
        val service = Robolectric.buildService(RaMessagingService::class.java).create().get()
        val manager = context.getSystemService(NotificationManager::class.java)
        val process = ProcessLifecycleOwner.get().lifecycle as LifecycleRegistry
        process.currentState = Lifecycle.State.CREATED
        try {
            assertFalse("CoreHost must stay lazy on cold start", app.hasCoreHostForTest)
            for (kind in listOf(NotifyKind.NOTIFY_KIND_ATTENTION, NotifyKind.NOTIFY_KIND_SECURITY)) {
                manager.cancelAll()
                val msg = RemoteMessage.Builder("synthetic-sender")
                    .setData(mapOf("session" to "sess-1", "kind" to kind.number.toString(), "event" to "ev-1"))
                    .build()
                service.onMessageReceived(msg)
                assertEquals("Push notification must be posted for $kind", 1, shadowOf(manager).allNotifications.size)
                assertFalse("Lock-screen FCM delivery ($kind) must never instantiate CoreHost", app.hasCoreHostForTest)
            }
        } finally {
            marker.delete()
            manager.cancelAll()
            service.onDestroy()
        }
    }

    private fun <T> awaitFlow(flow: StateFlow<T>, timeoutMs: Long = 5_000L, predicate: (T) -> Boolean) {
        val signal = LinkedBlockingQueue<Unit>()
        val done = AtomicBoolean(false)
        val job = CoroutineScope(Dispatchers.Default).launch {
            flow.collect { v ->
                if (predicate(v)) done.set(true)
                signal.offer(Unit)
            }
        }
        try {
            val deadline = System.nanoTime() + timeoutMs * 1_000_000L
            while (!done.get()) {
                org.robolectric.shadows.ShadowLooper.idleMainLooper()
                if (done.get()) break
                val remMs = ((deadline - System.nanoTime()) / 1_000_000L).coerceAtLeast(0L)
                check(remMs > 0L) { "Timed out waiting for flow condition after $timeoutMs ms" }
                signal.poll(minOf(remMs, 20L), TimeUnit.MILLISECONDS)
            }
        } finally {
            job.cancel()
        }
    }

    @Test
    fun lazySingletonCoreHostIsReusedAndResetsInPlaceOnExit() {
        val app = appFixture.app
        app.testSecurityStore = io.github.lewin671.remoteagent.core.MemorySecurityStoreV4()
        assertFalse(app.hasCoreHostForTest)
        val host1 = app.ensureForegroundCoreHost()
        val host2 = app.ensureForegroundCoreHost()
        assertTrue(app.hasCoreHostForTest)
        assertSame("RaApp must hold exactly one singleton CoreHost", host1, host2)

        awaitFlow(host1.appRootView) { it.stage == AppRootStage.APP_ROOT_STAGE_ONBOARDING }
        host1.exitRequest(ExitIntentKind.EXIT_INTENT_KIND_SETUP_RESET, "https://relay.example.com")
        awaitFlow(host1.exitConfirmView) { it.step == ExitConfirmStep.EXIT_CONFIRM_STEP_AWAITING_CONFIRM }
        host1.exitConfirm(true)
        awaitFlow(host1.exitConfirmView) { it.step == ExitConfirmStep.EXIT_CONFIRM_STEP_CLOSED }
        assertSame("Exit must reset CoreHost in place without replacing it", host1, app.ensureForegroundCoreHost())
        assertEquals(AppRootStage.APP_ROOT_STAGE_ONBOARDING, host1.appRootView.value.stage)
    }

    private fun hexBytes(s: String): ByteArray = ByteArray(s.length / 2) { i -> s.substring(i * 2, i * 2 + 2).toInt(16).toByte() }

    private fun seedValidReadyAccountStore(): MemorySecurityStoreV4 {
        val vectorFile = listOf(
            File("../internal/deviceauth/testdata/domain-v3.json"),
            File("../../internal/deviceauth/testdata/domain-v3.json"),
        ).first { it.exists() }
        val v = Json.parseToJsonElement(vectorFile.readText()).jsonObject
        val genesisRaw = Base64.getDecoder().decode(v["genesis"]!!.jsonObject["state"]!!.jsonPrimitive.content)
        val addedRaw = Base64.getDecoder().decode(v["added"]!!.jsonObject["state"]!!.jsonPrimitive.content)
        val leaseBytes = Base64.getDecoder().decode(v["lease"]!!.jsonPrimitive.content)
        val domain = hexBytes(v["domain"]!!.jsonPrimitive.content)
        val genesisHash = hexBytes(v["genesis"]!!.jsonObject["hash"]!!.jsonPrimitive.content)
        val accountId = v["account"]!!.jsonPrimitive.content
        val phoneSeed = hexBytes(v["phone_seed"]!!.jsonPrimitive.content)
        val phoneX = hexBytes(v["phone_x"]!!.jsonPrimitive.content)
        val phoneEdPub = Corebind.ed25519PublicKey(phoneSeed)
        val digest = MessageDigest.getInstance("SHA-256").digest(phoneEdPub)
        val phoneId = digest.take(16).joinToString("") { "%02x".format(it) }
        val bos = ByteArrayOutputStream()
        DataOutputStream(bos).use { s ->
            fun lp(b: ByteArray) { s.writeInt(b.size); s.write(b) }
            fun ls(str: String) = lp(str.toByteArray(Charsets.UTF_8))
            s.write("RA4REC\u0000".toByteArray(Charsets.US_ASCII))
            s.writeInt(4)
            ls(accountId)
            s.write(domain)
            s.write(genesisHash)
            ls(phoneId)
            s.writeByte(1)
            ls("http://127.0.0.1:1")
            lp(ByteArray(0))
            s.writeInt(2)
            lp(genesisRaw); lp(ByteArray(0))
            lp(addedRaw); lp(ByteArray(0))
            lp(leaseBytes)
            s.writeLong(1L)
            s.writeInt(0)
            s.writeByte(0)
            s.writeByte(0)
        }
        return MemorySecurityStoreV4().apply {
            seedTestRecord(phoneSeed, phoneEdPub, phoneX, Corebind.x25519PublicKey(phoneX), bos.toByteArray())
        }
    }

    private fun <T> runAwaitingMainLooper(timeoutMs: Long = 5_000L, block: suspend () -> T): T {
        val resultQueue = LinkedBlockingQueue<Result<T>>()
        val job = CoroutineScope(Dispatchers.Default).launch {
            resultQueue.offer(runCatching { block() })
        }
        try {
            val deadline = System.nanoTime() + timeoutMs * 1_000_000L
            while (true) {
                org.robolectric.shadows.ShadowLooper.idleMainLooper()
                val res = resultQueue.poll(10L, TimeUnit.MILLISECONDS)
                if (res != null) return res.getOrThrow()
                check(System.nanoTime() < deadline) { "Timed out awaiting suspend block after $timeoutMs ms" }
            }
        } finally {
            job.cancel()
        }
    }

    @Test
    fun backgroundApprovalLockScreenNeverPollutesAppRootAndRecoversAfterUnlock() {
        val app = appFixture.app
        val backing = seedValidReadyAccountStore()
        val storeLocked = AtomicBoolean(true)
        app.testSecurityStore = object : SecurityStoreV4 by backing {
            override fun executeStorage(effect: SecurityStorageEffect): SecurityStorageResultEvent {
                if (storeLocked.get()) throw SecurityFailure("storage")
                return backing.executeStorage(effect)
            }
        }
        try {
            // 1. Keyguard locked: background work throws WITHOUT instantiating CoreHost or touching Keystore.
            app.testDeviceLocked = { true }
            try {
                runAwaitingMainLooper { app.ensureRemoteForBackgroundWork() }
                fail("expected locked device to throw")
            } catch (_: IllegalStateException) {
            }
            assertFalse("Locked background approval must never instantiate CoreHost", app.hasCoreHostForTest)

            // 2. Race where Keystore still throws storage error during background bootstrap -> BOOTSTRAP_ERROR (canReset=false).
            app.testDeviceLocked = { false }
            try {
                runAwaitingMainLooper { app.ensureRemoteForBackgroundWork() }
                fail("expected locked storage error to throw")
            } catch (_: IllegalStateException) {
            }
            val host = app.ensureCoreHost()
            assertEquals(AppRootStage.APP_ROOT_STAGE_BOOTSTRAP_ERROR, host.appRootView.value.stage)
            assertFalse("Storage bootstrap error must not offer destructive reset", host.appRootView.value.bootstrapError.canReset)

            // 3. Unlock -> Worker retries ensureRemoteForBackgroundWork() -> automatically retries bootstrap and succeeds!
            storeLocked.set(false)
            val remote = runAwaitingMainLooper { app.ensureRemoteForBackgroundWork() }
            assertNotNull("Worker retry after unlock must succeed and return Remote", remote)
            assertEquals(AppRootStage.APP_ROOT_STAGE_READY, host.appRootView.value.stage)
        } finally {
            app.testDeviceLocked = null
        }
    }

    @Test
    fun foregroundOpenRecoversFromBackgroundStorageBootstrapError() {
        val app = appFixture.app
        val backing = seedValidReadyAccountStore()
        val storeLocked = AtomicBoolean(true)
        app.testSecurityStore = object : SecurityStoreV4 by backing {
            override fun executeStorage(effect: SecurityStorageEffect): SecurityStorageResultEvent {
                if (storeLocked.get()) throw SecurityFailure("storage")
                return backing.executeStorage(effect)
            }
        }
        app.testDeviceLocked = { false }
        val process = ProcessLifecycleOwner.get().lifecycle as LifecycleRegistry
        process.currentState = Lifecycle.State.CREATED
        try {
            // Background attempt hits STORAGE bootstrap error while locked.
            try {
                runAwaitingMainLooper { app.ensureRemoteForBackgroundWork() }
                fail("expected storage failure")
            } catch (_: IllegalStateException) {
            }
            val host = app.ensureCoreHost()
            assertEquals(AppRootStage.APP_ROOT_STAGE_BOOTSTRAP_ERROR, host.appRootView.value.stage)

            // Unlock and open the app in foreground -> onForegroundStart() triggers appRootRetryBootstrap -> READY (never stuck).
            storeLocked.set(false)
            app.onForegroundStart()
            awaitFlow(host.appRootView) { it.stage == AppRootStage.APP_ROOT_STAGE_READY }
            awaitFlow(app.remote) { it != null }
            assertEquals(AppRootStage.APP_ROOT_STAGE_READY, host.appRootView.value.stage)
            assertNotNull(app.remote.value)
        } finally {
            app.testDeviceLocked = null
            process.currentState = Lifecycle.State.CREATED
        }
    }
}
