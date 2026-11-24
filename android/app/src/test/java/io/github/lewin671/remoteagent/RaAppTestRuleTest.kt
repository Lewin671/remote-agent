package io.github.lewin671.remoteagent

import android.app.Application
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
    @Test fun aFailedAppTestReleasesBothScopes() {
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
