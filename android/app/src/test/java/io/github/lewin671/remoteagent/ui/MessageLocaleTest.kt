package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.widget.LinearLayout
import android.widget.TextView
import android.view.View
import io.github.lewin671.remoteagent.core.MessageLocale
import io.github.lewin671.remoteagent.core.UiMessages
import org.json.JSONArray
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = android.app.Application::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MessageLocaleTest {
    private val context: Context get() = RuntimeEnvironment.getApplication()
    @Test fun sharedLanguageContract() {
        val vectors = JSONArray(File("../../i18n/locale-vectors.json").readText())
        for (i in 0 until vectors.length()) {
            val v = vectors.getJSONObject(i)
            val system = v.getJSONArray("system")
            assertEquals(v.getString("expected"), MessageLocale.resolve(v.getString("preference"), (0 until system.length()).map { system.getString(it) }))
        }
    }
    @Test fun nativeResourcePluralRulesAndStageZero() {
        assertEquals("取消", resolveUiMessage(context, UiMessages.commonCancel()))
        assertEquals("Copy", resolveUiMessage(context, UiMessages.commonCopy(), "en"))
        assertEquals("复制", resolveUiMessage(context, UiMessages.commonCopy(), "unsupported"))
        assertEquals("1 image could not be attached. Tap it for details.", resolveUiMessage(context, UiMessages.imageAttachFailed(1), "en"))
        for (count in listOf(0, 2, 1000)) {
            assertEquals("$count 张图没加上，点图片看原因", resolveUiMessage(context, UiMessages.imageAttachFailed(count)))
            assertEquals("$count images could not be attached. Tap an image for details.", resolveUiMessage(context, UiMessages.imageAttachFailed(count), "en"))
        }
        val preferences = MessagePreferences(context)
        preferences.preference = "en"
        assertEquals("en", MessagePreferences(context).preference)
        assertEquals("zh-CN", preferences.current())
    }
    @Test fun pilotResourceSnapshots() {
        val directory = System.getenv("RA_I18N_SCREENSHOT_DIR") ?: return
        for (locale in listOf("zh-CN", "en")) {
            val content = LinearLayout(context).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(32, 32, 32, 32)
                setBackgroundColor(Color.WHITE)
            }
            for (message in listOf(UiMessages.commonCancel(), UiMessages.commonCopy(), UiMessages.commonCopied(), UiMessages.commonRetry(), UiMessages.commonRemove(), UiMessages.commonSave(), UiMessages.commonDone(), UiMessages.imageAttachFailed(2))) {
                content.addView(TextView(context).apply {
                    text = resolveUiMessage(context, message, locale)
                    textSize = 22f; setTextColor(Color.BLACK); setPadding(0, 14, 0, 14)
                })
            }
            content.measure(View.MeasureSpec.makeMeasureSpec(600, View.MeasureSpec.EXACTLY), View.MeasureSpec.makeMeasureSpec(1200, View.MeasureSpec.AT_MOST))
            content.layout(0, 0, content.measuredWidth, content.measuredHeight)
            val bitmap = Bitmap.createBitmap(content.width, content.height, Bitmap.Config.ARGB_8888)
            content.draw(Canvas(bitmap))
            File(directory, "android-$locale.png").apply { parentFile?.mkdirs() }.outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
