package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.ContextWrapper
import io.github.lewin671.remoteagent.RaApp
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
class SessionMessageContextRuleTest {
    @Test fun aFailedResourceTestRestoresThePreviousContextWithoutCreatingAnApp() {
        assertNull(RaApp.scopeOrNull())
        assertNull(sessionMessageContextForTesting)
        val parent = ContextWrapper(RuntimeEnvironment.getApplication())
        val failure = AssertionError("resource fixture failure")
        sessionMessageContextForTesting = parent
        try {
            val body = object : Statement() {
                override fun evaluate() {
                    assertSame(RuntimeEnvironment.getApplication(), sessionMessageContextForTesting)
                    assertNull(RaApp.scopeOrNull())
                    throw failure
                }
            }
            try {
                SessionMessageContextRule().apply(body, Description.createTestDescription(javaClass, "failing resource owner")).evaluate()
                fail("Expected the original failure")
            } catch (actual: AssertionError) {
                assertSame(failure, actual)
            }
            assertSame(parent, sessionMessageContextForTesting)
            assertNull(RaApp.scopeOrNull())
        } finally {
            sessionMessageContextForTesting = null
        }
    }
}
