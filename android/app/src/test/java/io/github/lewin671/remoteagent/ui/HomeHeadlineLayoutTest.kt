package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Text
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.core.app.ApplicationProvider
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.HeadlineKind
import io.github.lewin671.remoteagent.core.proto.HomeHeadlineView
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.core.proto.TextArg
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
        val headline = mutableStateOf(
            HomeHeadlineView.newBuilder()
                .setKind(HeadlineKind.HEADLINE_KIND_ERROR)
                .setTitle(CoreText.newBuilder().setKey(UiMessages.connectionConnecting().key))
                .setSub(CoreText.newBuilder().setKey(UiMessages.connectionPreviousBelow().key))
                .build(),
        )
        ui.setContent { RaTheme(ThemeMode.DARK) { Column { Headline(headline.value) {}; Text("anchor") } } }
        val top = ui.onNodeWithText("anchor").getUnclippedBoundsInRoot().top
        ui.runOnIdle { headline.value = headline.value.toBuilder().clearSub().build() }
        assertEquals(top, ui.onNodeWithText("anchor").getUnclippedBoundsInRoot().top)
        ui.runOnIdle {
            headline.value = headline.value.toBuilder().setSub(
                CoreText.newBuilder()
                    .setKey("home.idle_count")
                    .addArgs(TextArg.newBuilder().setName("count").setIntValue(1)),
            ).build()
        }
        assertEquals(top, ui.onNodeWithText("anchor").getUnclippedBoundsInRoot().top)
    }

    @Test fun highlightsStructuredWaitingCountInZhAndEnWithoutStringSearch() {
        val ctx = ApplicationProvider.getApplicationContext<Application>()
        val style = SpanStyle(color = Color(0xFFFF6600))
        val text = CoreText.newBuilder()
            .setKey("home.waiting_count")
            .addArgs(TextArg.newBuilder().setName("count").setIntValue(3))
            .build()

        val zh = resolveHighlightedCoreText(ctx, text, "count", style, "zh-CN")
        assertEquals(resolveCoreText(ctx, text, "zh-CN"), zh.text)
        assertEquals(1, zh.spanStyles.size)
        assertEquals("3", zh.text.substring(zh.spanStyles[0].start, zh.spanStyles[0].end))
        assertEquals(style.color, zh.spanStyles[0].item.color)

        val en = resolveHighlightedCoreText(ctx, text, "count", style, "en")
        assertEquals(resolveCoreText(ctx, text, "en"), en.text)
        assertEquals(1, en.spanStyles.size)
        assertEquals("3", en.text.substring(en.spanStyles[0].start, en.spanStyles[0].end))
        assertEquals(style.color, en.spanStyles[0].item.color)
    }
}
