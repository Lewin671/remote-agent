package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

/** Fixed copy must remain readable beside metadata/actions at phone widths. */
@RunWith(ParameterizedRobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class, qualifiers = "w390dp-h844dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class EnglishLayoutTest(private val locale: String, private val width: Int) {
    companion object {
        @JvmStatic @ParameterizedRobolectricTestRunner.Parameters(name = "{0}-{1}dp")
        fun cases() = listOf("zh-CN", "en").flatMap { locale -> listOf(320, 390).map { arrayOf<Any>(locale, it) } }
    }
    @get:Rule val ui = createComposeRule()
    private fun text(message: UiMessage) = messageTextForTest(message, locale)
    private var scale by mutableStateOf(1f)
    private var dark by mutableStateOf(false)
    private fun show(content: @Composable () -> Unit) = ui.setContent {
        val density = LocalDensity.current
        RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
            CompositionLocalProvider(LocalMessageLocale provides locale,
                LocalDensity provides Density(density.density, scale)) {
                Column(Modifier.width(width.dp)) { content() }
            }
        }
    }
    private fun readable(value: String): TextLayoutResult {
        val layouts = mutableListOf<TextLayoutResult>()
        ui.onNodeWithText(value, useUnmergedTree = true).performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(layouts) }
        return layouts.single().also {
            // Native font metrics can exceed integer layout height by a fraction of a pixel.
            assertTrue("Clipped vertically: $value", it.getLineBottom(it.lineCount - 1) <= it.size.height + 1f)
            for (line in 0 until it.lineCount) assertTrue("Clipped horizontally: $value", it.getLineRight(line) - it.getLineLeft(line) <= it.size.width + 1f)
            for (line in 0 until it.lineCount) assertFalse("Ellipsized: $value", it.isLineEllipsized(line))
        }
    }
    @Test fun cardTitlesMetadataAndActionsStayReadable() {
        val kinds = listOf(UiMessages.listStateAndroidRequestCommand(), UiMessages.listStatePlan(), UiMessages.listStateAgentAsking("Claude Code"))
        var kind by mutableStateOf(kinds.first())
        val details = text(UiMessages.dockMore(2))
        val collapse = text(UiMessages.markdownCollapse())
        val expand = text(UiMessages.markdownExpand())
        show { DarkCard {
            DarkCardHead(uiText(kind), details, trailing = {
                if (kind == kinds[1]) CardLink(expand, {})
                CardLink(collapse, {}, up = true)
            })
        } }
        for (night in listOf(false, true)) for (font in listOf(1f, 1.5f, 2f)) for (current in kinds) {
            ui.runOnIdle { kind = current; scale = font; dark = night }
            readable(text(current)); readable(details); readable(collapse)
            if (current == kinds[1]) readable(expand)
            val title = ui.onNodeWithText(text(current), useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val meta = ui.onNodeWithText(details, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            assertTrue("Header overlaps", title.right <= meta.left || title.bottom <= meta.top)
        }
    }
    @Test fun recoveryTitleWrapsWithoutMovingIntoBackButton() {
        val title = text(UiMessages.securityRecoverManagementWithARecoveryCode())
        show { PageBar(title, {}, wrapTitle = true) }
        for (font in listOf(1f, 2f)) {
            ui.runOnIdle { scale = font }
            readable(title)
            val back = ui.onNodeWithContentDescription(text(UiMessages.commonBack())).fetchSemanticsNode().boundsInRoot
            val heading = ui.onNodeWithText(title).fetchSemanticsNode().boundsInRoot
            assertTrue(heading.left >= back.right + 10f)
        }
    }
    @Test fun sheetTitleHasRoomBesideCancel() {
        val title = text(UiMessages.scheduleScheduleMessagesToThisConversation())
        val cancel = text(UiMessages.commonCancel())
        show { SheetHead(title, cancel, {}) }
        for (font in listOf(1f, 2f)) {
            ui.runOnIdle { scale = font }
            readable(title); readable(cancel)
            val heading = ui.onNodeWithText(title).fetchSemanticsNode().boundsInRoot
            val action = ui.onNodeWithText(cancel).fetchSemanticsNode().boundsInRoot
            assertTrue(action.left >= heading.right + 24f)
        }
    }
}
