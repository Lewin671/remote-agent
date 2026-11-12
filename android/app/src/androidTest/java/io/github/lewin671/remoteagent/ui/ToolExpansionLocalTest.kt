package io.github.lewin671.remoteagent.ui

import android.graphics.Rect
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.runtime.CompositionLocalProvider
import androidx.test.core.app.ActivityScenario
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assume.assumeTrue
import org.junit.Test
import java.io.File

/** Opt-in measurements with the platform frame clock, native touches and production SessionScreen.
 * No Compose test rule: its virtual clock would distort long presses and gfxinfo timings. */
class ToolExpansionLocalTest {
    @Test fun measureExpansionAndScroll() {
        val args = InstrumentationRegistry.getArguments()
        assumeTrue(args.getString("toolLocal") == "true")
        val inst = InstrumentationRegistry.getInstrumentation()
        val service = inst.uiAutomation.serviceInfo
        service.flags = service.flags or android.accessibilityservice.AccessibilityServiceInfo.FLAG_RETRIEVE_INTERACTIVE_WINDOWS or android.accessibilityservice.AccessibilityServiceInfo.FLAG_REPORT_VIEW_IDS or android.accessibilityservice.AccessibilityServiceInfo.FLAG_INCLUDE_NOT_IMPORTANT_VIEWS
        inst.uiAutomation.serviceInfo = service
        val ctx = inst.targetContext
        check(ctx.packageName.endsWith(".queuetest"))
        val output = File(ctx.getExternalFilesDir(null), "tool-native").apply { mkdirs() }
        val phase = args.getString("phase", "baseline")
        val session = Session.newBuilder().setId("tool-benchmark").setCwd("/example/project")
            .setTitle("Tool expansion benchmark").setAgent(Agent.AGENT_CODEX).setStatus(SessionStatus.SESSION_STATUS_IDLE).build()
        val machine = MachineView("machine", true, 0, MachineMeta.newBuilder().setHostname("Test computer").setProtocolVersion(PROTOCOL_VERSION).build())
        val events = mutableListOf<Event>()
        fun event(block: Event.Builder.() -> Unit) { events += Event.newBuilder().setSessionId(session.id).setSeq(events.size + 1L).apply(block).build() }
        val command = (1..24).joinToString("\n") { "printf 'long command line $it: measure expansion and layout'" }
        repeat(80) { turn ->
            event { userMessage = UserMessage.newBuilder().setText("Benchmark turn $turn").build() }
            event { turnStarted = TurnStarted.getDefaultInstance() }
            event { agentMessage = AgentMessage.newBuilder().setText("Checking tool group $turn.").build() }
            repeat(6) { call -> event { toolCall = ToolCall.newBuilder().setCallId("group-$turn-$call").setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("group command $call").setInput(command).setStatus("completed").build() } }
            event { agentMessage = AgentMessage.newBuilder().setText("Checking the individual command.").build() }
            event { toolCall = ToolCall.newBuilder().setCallId("single-$turn").setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("benchmark single command").setInput(command).setStatus("completed").build() }
            event { turnEnded = TurnEnded.newBuilder().setDurationMs(1000).build() }
        }
        val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "benchmark-client"), Api.defaultHttp())
        remote.showForTest(RemoteState(conn = ConnState.ONLINE, link = Link.ONLINE, loaded = true, synced = true,
            machines = mapOf("machine" to machine), sessions = mapOf(session.id to SessionView(session.id, "machine", session))), events = mapOf(session.id to events),
            live = mapOf(session.id to LiveSnapshot.newBuilder().setSession(session).build()))
        val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
        fun shell(s: String): String = inst.uiAutomation.executeShellCommand(s).let { fd -> android.os.ParcelFileDescriptor.AutoCloseInputStream(fd).bufferedReader().use { it.readText() } }
        fun find(text: String): Rect? {
            fun scan(n: android.view.accessibility.AccessibilityNodeInfo): Rect? {
                var found: Rect? = null
                if (n.text?.toString() == text && n.isVisibleToUser) found = Rect().also(n::getBoundsInScreen)
                for (i in 0 until n.childCount) n.getChild(i)?.let { scan(it)?.let { rect -> found = rect } }
                return found
            }
            return inst.uiAutomation.rootInActiveWindow?.let(::scan)
        }
        fun tap(text: String) {
            val rect = requireNotNull(find(text)) { "missing $text" }
            shell("input tap ${rect.centerX()} ${rect.centerY()}")
        }
        fun capture(name: String) { File(output, "$phase-$name.png").outputStream().use { requireNotNull(inst.uiAutomation.takeScreenshot()).compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it) } }
        val scenario = ActivityScenario.launch(ComponentActivity::class.java)
        try {
            scenario.onActivity { activity ->
                activity.enableEdgeToEdge()
                activity.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalRemote provides remote, LocalNav provides nav) { AppSafeArea { SessionScreen(session.id) } } } }
            }
            Thread.sleep(3500)
            File(output, "$phase-fixture.txt").writeText("events=${events.size} rows=${buildChat(events, derive(events, Agent.AGENT_CODEX), "Test computer").size}\n")
            fun swipe(up: Boolean) { shell("input swipe 500 ${if (up) 1500 else 600} 500 ${if (up) 600 else 1500} 300"); Thread.sleep(200) }
            fun bottom() { repeat(5) { swipe(true) }; Thread.sleep(500) }
            bottom(); tap("benchmark single command"); Thread.sleep(400); capture("command-open"); tap("benchmark single command")
            tap("运行了 6 条命令"); Thread.sleep(400); capture("group-open"); tap("运行了 6 条命令")
            for (trial in 1..3) {
                shell("dumpsys gfxinfo ${ctx.packageName} reset")
                repeat(6) { swipe(false); swipe(true) }
                File(output, "$phase-scroll-$trial.txt").writeText(shell("dumpsys gfxinfo ${ctx.packageName} framestats"))
                bottom()
                for ((kind, title) in listOf("command" to "benchmark single command", "group" to "运行了 6 条命令")) {
                    shell("dumpsys gfxinfo ${ctx.packageName} reset")
                    repeat(10) { tap(title); Thread.sleep(400) }
                    File(output, "$phase-$kind-$trial.txt").writeText(shell("dumpsys gfxinfo ${ctx.packageName} framestats"))
                }
            }
        } finally { scenario.close(); remote.close(); ImageDrafts.forSession(session.id).clear() }
    }
}
