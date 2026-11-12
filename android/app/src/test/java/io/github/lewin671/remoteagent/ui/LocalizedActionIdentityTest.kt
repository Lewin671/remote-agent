package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.*
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = android.app.Application::class)
class LocalizedActionIdentityTest {
    @get:Rule val resources = SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()
    @Test fun changingLocaleWhileConfirmingDoesNotRequireASecondConsentForTheSameRisk() {
        var locale by mutableStateOf("zh-CN")
        var exits = 0
        ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalMessageLocale provides locale) {
            Column { AccountExitAction(RemoteState(), { false }, { exits++ }) }
        } } }
        ui.onNodeWithText(messageTextForTest(UiMessages.settingsExitAndDeleteLocalKeys())).performClick()
        ui.runOnIdle { locale = "en" }
        ui.onNodeWithText(messageTextForTest(UiMessages.securityExit(), "en")).performClick()
        ui.runOnIdle { assertEquals(1, exits) }
    }
    @Test fun twoActionsWithTheSameTranslationStillDispatchTheirOwnCallbacks() {
        var locale by mutableStateOf("zh-CN")
        var restored = 0
        var retried = 0
        ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalMessageLocale provides locale) {
            val label = if (locale == "en") "Action" else "操作"
            MeBubble("Original message", caption = "Original status", animated = true,
                links = listOf(CaptionLink(CaptionAction.RESTORE, label) { restored++ }, CaptionLink(CaptionAction.RETRY, label) { retried++ }))
        } } }
        ui.onAllNodesWithText("操作").onLast().performClick()
        ui.runOnIdle { assertEquals(0, restored); assertEquals(1, retried); locale = "en" }
        ui.mainClock.advanceTimeBy(500)
        ui.onAllNodesWithText("Action").onFirst().performClick()
        ui.runOnIdle { assertEquals(1, restored); assertEquals(1, retried) }
    }
}
