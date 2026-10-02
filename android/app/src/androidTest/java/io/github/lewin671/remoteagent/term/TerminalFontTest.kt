package io.github.lewin671.remoteagent.term

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Paint
import androidx.test.platform.app.InstrumentationRegistry
import com.termux.terminal.TerminalEmulator
import com.termux.terminal.TerminalOutput
import com.termux.view.TerminalRenderer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/** The terminal typeface on a real device: its fallbacks, and a screen drawn to files/terminal-font.png. */
class TerminalFontTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private val typeface = terminalTypeface(context)
    private val px = 37
    private val paint = Paint().apply { typeface = this@TerminalFontTest.typeface; textSize = px.toFloat() }
    private val cell = paint.measureText("X")

    @Test fun latinBoxLinesAndArrowsFillOneCell() {
        for (c in listOf("i", "W", "─", "│", "┼", "→", "●")) assertEquals(c, cell, paint.measureText(c), 0.01f)
    }

    /** Claude Code's mode line, tool output corner, spinners: absent from JetBrains Mono and from the emulator's fonts. */
    @Test fun symbolsComeFromTheBundledFallback() {
        if (android.os.Build.VERSION.SDK_INT < 29) return
        for (c in listOf("⏵", "⎿", "✻", "✳", "⏺", "⣿", "↳")) {
            assertTrue(c, paint.hasGlyph(c))
            assertEquals(c, px / 2f, paint.measureText(c), 1f)
        }
    }

    @Test fun chineseKeepsItsShapeInsideTwoCells() {
        val wide = paint.measureText("中")
        assertTrue("中 $wide in ${2 * cell}", wide <= 2 * cell && wide >= px - 1f)
    }

    @Test fun drawsAScreen() {
        val e = "\u001b"
        val screen = listOf(
            "> 帮我看看安卓终端的字体能不能换一个",
            "",
            "$e[32m●$e[0m $e[1mBash$e[0m(ls android/app/src/main/res)",
            "  ⎿  inter_regular.ttf",
            "     … +5 lines (ctrl+o to expand)",
            "",
            "$e[32m⏺$e[0m 后台命令已完成。中文、English 混排",
            "  ┌────────┬──────────┬────────┐",
            "  │ 字体   │ 用途     │ 等宽   │",
            "  ├────────┼──────────┼────────┤",
            "  │ Sarasa │ 终端     │ 是     │",
            "  └────────┴──────────┴────────┘",
            "  $e[31m- old line 删除$e[0m  $e[32m+ new line 新增$e[0m",
            "  → ← ↑ ↓ ✓ ✗ • … ⏵⏵ ▌█▓▒░ 😀 ① ↳",
            "  ✻ ✳ ✢ ⣾⣽⣻⢿⡿⣟⣯⣷ ❯ ◐ ◯ ☐ ☒",
            "$e[7m 反色选中 selected $e[0m $e[4m下划线$e[0m $e[2mdim 暗色$e[0m",
            "────────────────────────────────────────",
            "> 输入中文$e[7m $e[0m",
            "────────────────────────────────────────",
            "  ⏵⏵ accept edits on (shift+tab)",
        )
        val out = object : TerminalOutput() {
            override fun write(data: ByteArray, offset: Int, count: Int) {}
            override fun titleChanged(oldTitle: String?, newTitle: String?) {}
            override fun onCopyTextToClipboard(text: String?) {}
            override fun onPasteTextFromClipboard() {}
            override fun onBell() {}
            override fun onColorsChanged() {}
        }
        val cols = 40
        val renderer = TerminalRenderer(px, typeface)
        val em = TerminalEmulator(out, cols, screen.size, 1, 1, 100, null)
        val bytes = screen.joinToString("\r\n").toByteArray()
        em.append(bytes, bytes.size)
        val bmp = Bitmap.createBitmap((cols * renderer.fontWidth).toInt() + 16, screen.size * renderer.fontLineSpacing + 24, Bitmap.Config.ARGB_8888)
        Canvas(bmp).apply { drawColor(0xFF000000.toInt()); translate(8f, 0f); renderer.render(em, this, 0, -1, -1, -1, -1) }
        File(context.getExternalFilesDir(null), "terminal-font.png").outputStream().use { bmp.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
}
