package io.github.lewin671.remoteagent.ui

import android.graphics.Rect
import android.os.Bundle
import android.os.ParcelFileDescriptor
import android.view.accessibility.AccessibilityNodeInfo
import androidx.test.core.app.ActivityScenario
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.MainActivity
import org.junit.Assume.assumeTrue
import org.junit.Test
import java.io.File

/** Opt-in native touches on a live local session; deliberately uses the platform clock. */
class QueueInteractionLocalTest {
    @Test fun measureDraftLongPresses() {
        val args = InstrumentationRegistry.getArguments()
        assumeTrue(args.getString("queueLocal") == "true")
        val inst = InstrumentationRegistry.getInstrumentation()
        val service = inst.uiAutomation.serviceInfo
        service.flags = service.flags or android.accessibilityservice.AccessibilityServiceInfo.FLAG_RETRIEVE_INTERACTIVE_WINDOWS or android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS or android.accessibilityservice.AccessibilityServiceInfo.FLAG_INCLUDE_NOT_IMPORTANT_VIEWS
        inst.uiAutomation.serviceInfo = service
        val ctx = inst.targetContext
        check(ctx.packageName.endsWith(".queuetest"))
        val output = File(ctx.getExternalFilesDir(null), "queue-native").apply { mkdirs() }
        val phase = args.getString("phase", "baseline")
        val log = File(output, "$phase.txt").apply { writeText("") }
        fun all(): List<AccessibilityNodeInfo> {
            fun nodes(n: AccessibilityNodeInfo): List<AccessibilityNodeInfo> = listOf(n) + (0 until n.childCount).flatMap { n.getChild(it)?.let(::nodes).orEmpty() }
            return inst.uiAutomation.windows.flatMap { it.root?.let(::nodes).orEmpty() }
        }
        fun find(text: String) = all().firstOrNull { it.text?.toString() == text || it.contentDescription?.toString() == text }
        fun bounds(n: AccessibilityNodeInfo) = Rect().also(n::getBoundsInScreen)
        fun shell(s: String) = inst.uiAutomation.executeShellCommand(s).let { ParcelFileDescriptor.AutoCloseInputStream(it).bufferedReader().use { r -> r.readText() } }
        fun tap(n: AccessibilityNodeInfo) { val r = bounds(n); shell("input tap ${r.centerX()} ${r.centerY()}") }
        fun capture(name: String) { File(output, "$phase-$name.png").outputStream().use { requireNotNull(inst.uiAutomation.takeScreenshot()).compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it) } }
        val scenario = ActivityScenario.launch(MainActivity::class.java)
        try {
            Thread.sleep(2500)
            repeat(30) { if (all().none { it.isEditable || it.text?.toString() == "Queue interaction check" }) Thread.sleep(500) }
            capture("start")
            if (all().none { it.isEditable }) { tap(requireNotNull(find("Queue interaction check"))); Thread.sleep(1500) }
            for ((kind, draft) in listOf("short" to "Queue short check", "long" to "Long queue draft sentence. ".repeat(14), "multiline" to (1..8).joinToString("\n") { "Line $it queue draft" })) {
                for (keyboard in listOf(true, false)) {
                    if (args.getString("queueKind") != null && args.getString("queueKind") != kind) continue
                    val field = all().first { it.isEditable }
                    field.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, Bundle().apply { putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, draft) })
                    Thread.sleep(350); tap(all().first { it.isEditable }); Thread.sleep(900)
                    if (keyboard) repeat(4) {
                        if (!shell("dumpsys input_method").contains("mInputShown=true")) { tap(all().first { it.isEditable }); Thread.sleep(650) }
                    }
                    if (!keyboard) { shell("input keyevent 4"); Thread.sleep(650) }
                    check(shell("dumpsys input_method").contains("mInputShown=true") == keyboard) { "keyboard state did not settle" }
                    for (edge in listOf(false, true)) repeat(3) { trial ->
                        val rect = bounds(requireNotNull(find("发送")))
                        val x = rect.centerX() + if (edge) (21 * ctx.resources.displayMetrics.density).toInt() else 0
                        // Background shell keeps DOWN held while the midpoint screenshot is taken.
                        val fd = inst.uiAutomation.executeShellCommand("input swipe $x ${rect.centerY()} $x ${rect.centerY()} 1000")
                        Thread.sleep(650)
                        if (trial == 0) capture("$kind-ime$keyboard-edge$edge-held")
                        ParcelFileDescriptor.AutoCloseInputStream(fd).bufferedReader().use { it.readText() }
                        Thread.sleep(250)
                        val menu = find("这一轮结束后再发") != null
                        val ime = shell("dumpsys input_method").contains("mInputShown=true")
                        log.appendText("$kind ime=$keyboard actualIme=$ime edge=$edge trial=$trial menu=$menu bounds=$rect\n")
                        if (trial == 0) capture("$kind-ime$keyboard-edge$edge-released")
                        if (menu) { shell("input keyevent 4"); Thread.sleep(250) }
                    }
                }
            }
            all().first { it.isEditable }.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, Bundle().apply { putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, "") })
        } finally { scenario.close() }
    }
}
