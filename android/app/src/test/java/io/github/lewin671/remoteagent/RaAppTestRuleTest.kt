package io.github.lewin671.remoteagent

import android.app.Application
import io.github.lewin671.remoteagent.proto.ImageFormat
import io.github.lewin671.remoteagent.ui.ImageDraft
import io.github.lewin671.remoteagent.ui.MemoryFiles
import io.github.lewin671.remoteagent.ui.PendingImage
import io.github.lewin671.remoteagent.ui.Processed
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.Description
import org.junit.runner.RunWith
import org.junit.runners.model.Statement
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class RaAppTestRuleTest {
    @Test fun aFailedAppTestReleasesBothScopesBeforeStandaloneDraftWrites() {
        assertNull(RaApp.scopeOrNull())
        val rule = RaAppTestRule()
        val failure = AssertionError("fixture failure")
        lateinit var main: Job
        lateinit var pairing: Job
        val body = object : Statement() {
            override fun evaluate() {
                assertSame(rule.app.scope, RaApp.scopeOrNull())
                main = rule.app.scope.coroutineContext[Job]!!
                pairing = (RaApp::class.java.getDeclaredField("pairingScope").apply { isAccessible = true }
                    .get(rule.app) as CoroutineScope).coroutineContext[Job]!!
                throw failure
            }
        }
        try {
            rule.apply(body, Description.createTestDescription(javaClass, "failing app owner")).evaluate()
            fail("Expected the original failure")
        } catch (actual: AssertionError) {
            assertSame(failure, actual)
        }
        assertNull(RaApp.scopeOrNull())
        assertTrue(main.isCancelled)
        assertTrue(pairing.isCancelled)

        // This is the previously order-dependent transition: fake app -> in-memory disk test.
        val files = MemoryFiles()
        val image = PendingImage("a", null, retainSource = false).apply {
            processed = Processed(byteArrayOf(1), ImageFormat.IMAGE_FORMAT_PNG, 1, 1)
            state = PendingImage.State.Ready
        }
        assertTrue(ImageDraft("session:fixture", files).attach(listOf(image), 6))
        assertEquals("0\na", files.get("session:fixture", "order")!!.toString(Charsets.UTF_8))
    }

    @Test fun nestedAppFixturesRestoreThePreviousOwnerWithoutCancelingIt() {
        assertNull(RaApp.scopeOrNull())
        AttachedRaApp(RuntimeEnvironment.getApplication()).use { outer ->
            val owner = outer.app.scope
            AttachedRaApp(RuntimeEnvironment.getApplication()).use { inner ->
                assertSame(inner.app, RaApp.instance)
                assertNotSame(owner, RaApp.scopeOrNull())
            }
            assertSame(outer.app, RaApp.instance)
            assertSame(owner, RaApp.scopeOrNull())
            assertTrue(owner.coroutineContext[Job]!!.isActive)
        }
        assertNull(RaApp.scopeOrNull())
    }
}
