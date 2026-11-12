package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Text
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@GraphicsMode(GraphicsMode.Mode.NATIVE)
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class HomeHeadlineLayoutTest {
    @get:Rule val ui = createComposeRule()

    @Test fun subtitleSpaceStaysWhenConnectingChangesToAnEmptyThenIdleSubtitle() {
        val headline = mutableStateOf(HomeHeadline(HomeHeadline.Kind.Connecting,
            UiMessages.connectionConnecting(), UiMessages.connectionPreviousBelow().messageText()))
        ui.setContent { RaTheme(ThemeMode.DARK) { Column { Headline(headline.value) {}; Text("anchor") } } }
        val top = ui.onNodeWithText("anchor").getUnclippedBoundsInRoot().top
        ui.runOnIdle { headline.value = headline.value.copy(sub = "".messageText()) }
        assertEquals(top, ui.onNodeWithText("anchor").getUnclippedBoundsInRoot().top)
        ui.runOnIdle { headline.value = headline.value.copy(sub = UiMessages.homeIdleCount(1).messageText()) }
        assertEquals(top, ui.onNodeWithText("anchor").getUnclippedBoundsInRoot().top)
    }
}
