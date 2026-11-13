package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.app.Notification
import android.app.NotificationManager
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.LinearGradient
import android.graphics.Paint
import android.graphics.Shader
import android.view.inspector.WindowInspector
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.Density
import androidx.compose.ui.graphics.toArgb
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.robolectric.Shadows.shadowOf
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.test.core.app.ApplicationProvider
import io.github.lewin671.remoteagent.RaAppTestRule
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Link
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.PROTOCOL_VERSION
import io.github.lewin671.remoteagent.core.PrefsObject
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.SharedFileFailureKind
import io.github.lewin671.remoteagent.core.SharedFileTransferState
import io.github.lewin671.remoteagent.core.UiMessage
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.data.SharedFileManager
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.DeviceMeta
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.MachineCapabilities
import io.github.lewin671.remoteagent.proto.MachineMeta
import io.github.lewin671.remoteagent.proto.NotifyContent
import io.github.lewin671.remoteagent.proto.NotifyKind
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.SharedFile
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.AgentMessage
import io.github.lewin671.remoteagent.proto.UserMessage
import io.github.lewin671.remoteagent.push.Notifier
import java.io.File
import java.nio.file.Files
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class, qualifiers = "w390dp-h844dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SharedFilesUiTest {

    @get:Rule(order = 0)
    val appFixture = RaAppTestRule()

    @get:Rule(order = 1)
    val ui = createComposeRule()

    private val context: Application get() = ApplicationProvider.getApplicationContext()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "c1"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private lateinit var tempDir: File
    private lateinit var manager: SharedFileManager

    @Test
    fun autoOpenBelongsToThePageThatStartedTheDownload() {
        val host = SharedFileHostState(manager, "s", "Test computer", true, false)
        val file = sharedFile("f", "cover.png", 10)
        host.startDownload(file)
        assertTrue(host.consumeAutoOpen("f"))
        assertEquals(false, host.consumeAutoOpen("f"))
        host.startDownload(file)
        host.leavePage()
        assertEquals(false, host.consumeAutoOpen("f"))
        val returned = SharedFileHostState(manager, "s", "Test computer", true, false)
        assertEquals(false, returned.consumeAutoOpen("f"))
    }

    private fun sharedFile(
        id: String,
        name: String,
        sizeBytes: Long,
        path: String = "/Users/alex/Movies/daocheng/out/$name",
    ): SharedFile =
        SharedFile.newBuilder()
            .setFileId(id)
            .setName(name)
            .setSizeBytes(sizeBytes)
            .setPath(path)
            .build()

    @Before
    fun setup() {
        // FileProvider keeps the folders of the first test's cache for the whole JVM.
        (androidx.core.content.FileProvider::class.java.getDeclaredField("sCache").apply { isAccessible = true }
            .get(null) as MutableMap<*, *>).clear()
        tempDir = Files.createTempDirectory("sf-ui-test").toFile()
        manager = SharedFileManager(context) { remote }
    }

    @After
    fun teardown() {
        manager.clearAll()
        remote.close()
        tempDir.deleteRecursively()
    }

    private var contentMounted = false
    private var slotTheme by mutableStateOf(ThemeMode.LIGHT)
    private var slotLocale by mutableStateOf("zh-CN")
    private var slotBody by mutableStateOf<@Composable () -> Unit>({})

    private fun render(
        theme: ThemeMode = ThemeMode.LIGHT,
        locale: String = "zh-CN",
        content: @Composable () -> Unit,
    ) {
        slotTheme = theme
        slotLocale = locale
        slotBody = content
        if (!contentMounted) {
            contentMounted = true
            ui.setContent {
                RaTheme(slotTheme) {
                    CompositionLocalProvider(
                        LocalRemote provides remote,
                        LocalNav provides nav,
                        LocalMessageLocale provides slotLocale,
                    ) {
                        Box(Modifier.fillMaxSize().background(pal.paper)) {
                            slotBody()
                        }
                    }
                }
            }
        }
        ui.waitForIdle()
    }

    private fun captureShot(name: String) {
        ui.waitForIdle()
        val outDir = System.getenv("RA_SHARED_FILE_SHOTS") ?: return
        ui.mainClock.advanceTimeBy(350)
        ui.waitForIdle()
        ui.runOnIdle {
            val bitmap = drawAllWindows()
            File(outDir, "$name.png").apply { parentFile!!.mkdirs() }.outputStream().use {
                assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
    }

    /** The activity window with every window over it (the long-press overlay is one), as on a screen. */
    private fun drawAllWindows(): Bitmap {
        val views = WindowInspector.getGlobalWindowViews()
        val base = views.first()
        val baseAt = IntArray(2).also { base.getLocationOnScreen(it) }
        val bitmap = Bitmap.createBitmap(base.width, base.height, Bitmap.Config.ARGB_8888)
        val canvas = Canvas(bitmap)
        for (view in views) {
            val at = IntArray(2).also { view.getLocationOnScreen(it) }
            canvas.save()
            canvas.translate((at[0] - baseAt[0]).toFloat(), (at[1] - baseAt[1]).toFloat())
            view.draw(canvas)
            canvas.restore()
        }
        return bitmap
    }

    /** Only the activity window: what is on the page under any overlay. */
    private fun drawPageWindow(): Bitmap {
        val base = WindowInspector.getGlobalWindowViews().first()
        val bitmap = Bitmap.createBitmap(base.width, base.height, Bitmap.Config.ARGB_8888)
        base.draw(Canvas(bitmap))
        return bitmap
    }

    private fun createMockThumbnailBitmap(warm: Boolean): Bitmap {
        val bmp = Bitmap.createBitmap(132, 132, Bitmap.Config.ARGB_8888)
        val c = Canvas(bmp)
        val paint = Paint().apply {
            shader = if (warm) {
                LinearGradient(0f, 0f, 132f, 132f, intArrayOf(0xFFE07A38.toInt(), 0xFF8C4A2F.toInt(), 0xFF282022.toInt()), null, Shader.TileMode.CLAMP)
            } else {
                LinearGradient(0f, 0f, 132f, 132f, intArrayOf(0xFF42525E.toInt(), 0xFFA29785.toInt(), 0xFFD9C7A7.toInt()), null, Shader.TileMode.CLAMP)
            }
        }
        c.drawRect(0f, 0f, 132f, 132f, paint)
        return bmp
    }

    @Test
    fun buildChatReplacesOnlySupportedSharedFilesAndBreaksToolFold() {
        val sf = sharedFile("f1", "clip.mp4", 24_300_000L)
        val events = listOf(
            Event.newBuilder().setSeq(1).setToolCall(ToolCall.newBuilder().setTitle("ls").setStatus("completed")).build(),
            Event.newBuilder().setSeq(2).setToolCall(ToolCall.newBuilder().setTitle("pwd").setStatus("completed")).build(),
            Event.newBuilder().setSeq(3).setToolCall(ToolCall.newBuilder().setTitle("send_file").setStatus("completed").addSharedFiles(sf)).build(),
            Event.newBuilder().setSeq(4).setToolCall(ToolCall.newBuilder().setTitle("cat").setStatus("completed")).build(),
            Event.newBuilder().setSeq(5).setToolCall(ToolCall.newBuilder().setTitle("echo").setStatus("completed")).build(),
            Event.newBuilder().setSeq(6).setToolCall(ToolCall.newBuilder().setTitle("send_file").setStatus("failed")).build(),
        )
        val d = derive(events)

        // When capability is true: Tools(1,2) -> SharedFiles(3) -> Tools(4,5) -> Tool(6 with 0 files).
        val supported = buildChat(events, d, "Mac", sharedFilesSupported = true)
        assertEquals(4, supported.size)
        assertTrue(supported[0] is ChatItem.Tools)
        assertEquals(listOf(1L, 2L), (supported[0] as ChatItem.Tools).callSeqs)
        assertTrue(supported[1] is ChatItem.SharedFiles)
        assertEquals(listOf(sf), (supported[1] as ChatItem.SharedFiles).files)
        assertTrue(supported[2] is ChatItem.Tools)
        assertEquals(listOf(4L, 5L), (supported[2] as ChatItem.Tools).callSeqs)
        assertTrue(supported[3] is ChatItem.Tool)

        // When capability is false: events 1..5 remain folded as normal tools, seq 6 stays failed Tool.
        val unsupported = buildChat(events, d, "Mac", sharedFilesSupported = false)
        assertEquals(2, unsupported.size)
        assertTrue(unsupported[0] is ChatItem.Tools)
        assertTrue(unsupported[1] is ChatItem.Tool)

        // ChatProjection updates cleanly when capability flips.
        val projection = ChatProjection()
        assertEquals(2, projection.update(events, d, "Mac", sharedFilesSupported = false).size)
        assertEquals(4, projection.update(events, d, "Mac", sharedFilesSupported = true).size)
    }

    @Test
    fun contextMenuEntriesMatchFileStateAndExcludedWhenCleaned() {
        val videoFile = sharedFile("v1", "daocheng-final.mp4", 24_300_000L, "/Users/alex/Movies/daocheng/out/daocheng-final.mp4")
        val pdfFile = sharedFile("p1", "report.pdf", 2_100_000L, "/Users/alex/Docs/report.pdf")
        val localVideo = File(tempDir, "daocheng-final.mp4").apply { writeBytes(ByteArray(100)) }
        val localPdf = File(tempDir, "report.pdf").apply { writeBytes(ByteArray(100)) }

        manager.store.setStateForTest("s1", "v1", SharedFileTransferState.Downloaded(localVideo, durationMs = 102_000L))
        manager.store.setStateForTest("s1", "p1", SharedFileTransferState.Downloaded(localPdf))

        render {
            SharedFileHost("s1", "MacBook Pro", machineOnline = true, sessionExited = false, manager = manager) {
                SharedFilesBlock(
                    files = listOf(videoFile, pdfFile),
                    sessionId = "s1",
                    machineName = "MacBook Pro",
                    machineOnline = true,
                    sessionExited = false,
                )
            }
        }
        // Long press downloaded video -> shows 存到相册, 存到「下载」, 分享…, 删除本机副本.
        ui.onNodeWithText("1:42 · 24.3 MB").performTouchInput { longClick() }
        ui.onNodeWithText("~/Movies/daocheng/out/daocheng-final.mp4").assertExists()
        ui.onNodeWithText("来自 MacBook Pro").assertExists()
        ui.onNodeWithText("存到相册").assertExists()
        ui.onNodeWithText("存到「下载」").assertExists()
        ui.onNodeWithText("分享…").assertExists()
        ui.onNodeWithText("删除本机副本").assertExists()

        // Dismiss by clicking deleting / or clicking backdrop? Clicking 删除本机副本 deletes it cleanly.
        ui.onNodeWithText("删除本机副本").performClick()
        ui.waitForIdle()
        // Now video returns to Available ("视频 · 24.3 MB") and long press shows "下载".
        ui.onNodeWithText("视频 · 24.3 MB").performTouchInput { longClick() }
        ui.onNodeWithText("下载").assertExists()
        ui.onAllNodesWithText("存到相册").assertCountEquals(0)
        // Dismiss backdrop by clicking lifted card.
        ui.onNodeWithText("下载").assertExists()

        // Test PDF menu (no Save to Photos).
        render {
            SharedFileHost("s1", "MacBook Pro", machineOnline = true, sessionExited = false, manager = manager) {
                SharedFilesBlock(
                    files = listOf(pdfFile),
                    sessionId = "s1",
                    machineName = "MacBook Pro",
                    machineOnline = true,
                    sessionExited = false,
                )
            }
        }
        ui.onNodeWithText("PDF · 2.1 MB").performTouchInput { longClick() }
        ui.onNodeWithText("存到「下载」").assertExists()
        ui.onAllNodesWithText("存到相册").assertCountEquals(0)
    }

    @Test
    fun turnEndedNotificationAppendsSharedFileCount() {
        val nm = context.getSystemService(NotificationManager::class.java)
        val snm = Shadows.shadowOf(nm)
        val content = NotifyContent.newBuilder()
            .setKind(NotifyKind.NOTIFY_KIND_TURN_ENDED)
            .setTitle("稻城剪辑")
            .setText("成片导出好了")
            .build()
        Notifier.show(
            context,
            "s-notify",
            NotifyKind.NOTIFY_KIND_TURN_ENDED,
            content,
            showContent = true,
            copy = { msg -> messageTextForTest(msg, "zh-CN") },
            sharedFileCount = 2,
        )
        val posted = snm.allNotifications.last()
        assertEquals("成片导出好了 · 附 2 个文件", posted.extras.getString(Notification.EXTRA_TEXT))
        nm.cancelAll()
    }

    @Test
    fun fittingAndOverflowingFileNamesRenderAsSingleTextNodes() {
        val fit = sharedFile("fit-1", "daocheng-final.mp4", 24_300_000L)
        val overflow = sharedFile("ovr-1", "daocheng-final-vertical-1080-v3.mp4", 61_700_000L)
        val audio = sharedFile("aud-1", "voice-memo.mp3", 1_200_000L)
        render {
            SharedFileHost("s-name", "MacBook Pro", machineOnline = true, sessionExited = false, manager = manager) {
                SharedFilesBlock(
                    files = listOf(fit, overflow, audio),
                    sessionId = "s-name",
                    machineName = "MacBook Pro",
                    machineOnline = true,
                    sessionExited = false,
                )
            }
        }
        // Fitting name renders cleanly as a single Text node with zero middle-split seam.
        ui.onAllNodesWithText("daocheng-final.mp4").assertCountEquals(1)
        ui.onAllNodesWithText("voice-memo.mp3").assertCountEquals(1)

        // Overflowing name keeps the extension/tail intact in a single Text node with no gap after ellipsis.
        val overflowNode = ui.onNodeWithText("…", substring = true).fetchSemanticsNode()
        val renderedName = overflowNode.config[androidx.compose.ui.semantics.SemanticsProperties.Text].first().text
        assertTrue("expected trailing extension on $renderedName", renderedName.endsWith("-v3.mp4"))
        assertTrue("expected no gap after ellipsis in $renderedName", !renderedName.contains("… "))
    }

    @Test
    fun screenshotsMatchDraftLightAndDark() {
        for (theme in listOf(ThemeMode.LIGHT, ThemeMode.DARK)) {
            val tag = if (theme == ThemeMode.DARK) "dark" else "light"
            captureInChat(theme, tag)
            captureSixStates(theme, tag)
            captureMultiFiles(theme, tag)
            captureLongPress(theme, tag)
            captureLongPressNearBottom(theme, tag)
            captureFontScale(theme, tag)
            capturePlayer(theme, tag)
        }
    }

    private fun captureInChat(theme: ThemeMode, tag: String) {
        val sf = sharedFile("v-chat", "daocheng-final.mp4", 24_300_000L)
        val events = listOf(
            Event.newBuilder().setSeq(1).setUserMessage(UserMessage.newBuilder().setText("把剪好的成片发我手机上看看")).build(),
            Event.newBuilder().setSeq(2).setToolCall(ToolCall.newBuilder().setTitle("ffmpeg -i cut.mov -c:v libx264 out/daocheng-final.mp4").setStatus("completed")).build(),
            Event.newBuilder().setSeq(3).setAgentMessage(AgentMessage.newBuilder().setText("成片导出好了，时长 1 分 42 秒，1080p。")).build(),
            Event.newBuilder().setSeq(4).setToolCall(ToolCall.newBuilder().setTitle("send_file").setStatus("completed").addSharedFiles(sf)).build(),
            Event.newBuilder().setSeq(5).setAgentMessage(AgentMessage.newBuilder().setText("需要我再出一版竖屏的吗？")).build(),
        )
        val items = buildChat(events, derive(events), "MacBook Pro", sharedFilesSupported = true)
        render(theme) {
            SharedFileHost("s-chat", "MacBook Pro", machineOnline = true, sessionExited = false, manager = manager) {
                CompositionLocalProvider(LocalChat provides ChatContext("s-chat", Agent.AGENT_CLAUDE, derive(events), 0L, machineName = "MacBook Pro", machineOnline = true, sessionExited = false)) {
                    Column(Modifier.fillMaxSize().padding(top = 18.dp)) {
                        PageBar("稻城剪辑", {}, subtitle = "MacBook Pro · 询问")
                        Spacer(Modifier.height(10.dp))
                        items.forEach { item ->
                            ChatItemView(item, "Claude")
                        }
                    }
                }
            }
        }
        captureShot("android-inchat-$tag")
    }

    private fun captureSixStates(theme: ThemeMode, tag: String) {
        val f1 = sharedFile("s-avail", "daocheng-final.mp4", 24_300_000L)
        val f2 = sharedFile("s-dl", "daocheng-final.mp4", 24_300_000L)
        val f3 = sharedFile("s-done", "daocheng-final.mp4", 24_300_000L)
        val f4 = sharedFile("s-fail", "daocheng-final.mp4", 24_300_000L)
        val f5 = sharedFile("s-off", "daocheng-final.mp4", 24_300_000L)
        val f6 = sharedFile("s-gone", "daocheng-final.mp4", 24_300_000L)

        val localDone = File(tempDir, "done-$tag.mp4").apply { writeBytes(ByteArray(10)) }
        manager.store.setStateForTest("s-states", f1.fileId, SharedFileTransferState.Idle)
        manager.store.setStateForTest("s-states", f2.fileId, SharedFileTransferState.Downloading(8_100_000L, 24_300_000L, etaSeconds = 12L))
        manager.store.setStateForTest("s-states", f3.fileId, SharedFileTransferState.Downloaded(localDone, durationMs = 102_000L))
        manager.store.setStateForTest("s-states", f4.fileId, SharedFileTransferState.Failed(SharedFileFailureKind.DISCONNECTED))
        manager.store.setStateForTest("s-states", f5.fileId, SharedFileTransferState.Idle)
        manager.store.setStateForTest("s-states", f6.fileId, SharedFileTransferState.CleanedUp)

        render(theme) {
            val px = with(LocalDensity.current) { SharedTileSize.roundToPx() }
            manager.putThumbnailForTest(localDone, px, createMockThumbnailBitmap(warm = true).asImageBitmap())
            SharedFileHost("s-states", "MacBook Pro", machineOnline = true, sessionExited = false, manager = manager) {
                Column(Modifier.fillMaxSize().padding(horizontal = ChatSide, vertical = 18.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    StateHeader("可下载")
                    SharedFileCard(f1, io.github.lewin671.remoteagent.core.resolveSharedFileUiState(f1, SharedFileTransferState.Idle, true, false), null, false, {}, {}, {})
                    StateHeader("下载中 · 进度环，点一下暂停，再点继续")
                    SharedFileCard(f2, io.github.lewin671.remoteagent.core.resolveSharedFileUiState(f2, SharedFileTransferState.Downloading(8_100_000L, 24_300_000L, 12L), true, false), null, false, {}, {}, {})
                    StateHeader("已下载 · 色块换成缩略图")
                    SharedFileCard(f3, io.github.lewin671.remoteagent.core.resolveSharedFileUiState(f3, SharedFileTransferState.Downloaded(localDone, 102_000L), true, false), createMockThumbnailBitmap(warm = true).asImageBitmap(), false, {}, {}, {})
                    StateHeader("失败")
                    SharedFileCard(f4, io.github.lewin671.remoteagent.core.resolveSharedFileUiState(f4, SharedFileTransferState.Failed(SharedFileFailureKind.DISCONNECTED), true, false), null, false, {}, {}, {})
                    StateHeader("电脑不在线")
                    SharedFileCard(f5, io.github.lewin671.remoteagent.core.resolveSharedFileUiState(f5, SharedFileTransferState.Idle, false, false), null, false, {}, {}, {})
                    StateHeader("已清理")
                    SharedFileCard(f6, io.github.lewin671.remoteagent.core.resolveSharedFileUiState(f6, SharedFileTransferState.CleanedUp, true, true), null, false, {}, {}, {})
                }
            }
        }
        captureShot("android-states-$tag")
    }

    @Composable
    private fun StateHeader(label: String) {
        Text(
            text = label,
            style = TextStyle(fontSize = 11.sp),
            color = pal.ink3,
            modifier = Modifier.padding(top = 4.dp),
        )
    }

    private fun captureMultiFiles(theme: ThemeMode, tag: String) {
        val fImg = sharedFile("m-img", "cover.png", 1_800_000L)
        val fVid = sharedFile("m-vid", "daocheng-final-vertical-1080-v3.mp4", 61_700_000L)
        val fZip = sharedFile("m-zip", "project.zip", 88_400_000L)
        val imgFile = File(tempDir, "cover-$tag.png").apply {
            outputStream().use { createMockThumbnailBitmap(warm = false).compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        manager.store.setStateForTest("s-multi", fImg.fileId, SharedFileTransferState.Downloaded(imgFile))
        manager.store.setStateForTest("s-multi", fVid.fileId, SharedFileTransferState.Downloading(12_000_000L, 61_700_000L))
        manager.store.setStateForTest("s-multi", fZip.fileId, SharedFileTransferState.Idle)

        render(theme) {
            val px = with(LocalDensity.current) { SharedTileSize.roundToPx() }
            manager.putThumbnailForTest(imgFile, px, createMockThumbnailBitmap(warm = false).asImageBitmap())
            SharedFileHost("s-multi", "MacBook Pro", machineOnline = true, sessionExited = false, manager = manager) {
                CompositionLocalProvider(LocalChat provides ChatContext("s-multi", Agent.AGENT_CLAUDE, derive(emptyList()), 0L, machineName = "MacBook Pro", machineOnline = true, sessionExited = false)) {
                    Column(Modifier.fillMaxSize().padding(top = 18.dp)) {
                        MeBubble("封面图、成片和工程包都给我")
                        SharedFilesBlock(
                            files = listOf(fImg, fVid, fZip),
                            sessionId = "s-multi",
                            machineName = "MacBook Pro",
                            machineOnline = true,
                            sessionExited = false,
                        )
                        ChatItemView(ChatItem.Agent("ag-multi", "三个文件都放这儿了。"), "Claude")
                    }
                }
            }
        }
        captureShot("android-multi-$tag")
    }

    @Composable
    private fun FakeComposer() {
        Box(
            Modifier.padding(horizontal = 16.dp, vertical = 12.dp).fillMaxWidth().height(52.dp)
                .clip(androidx.compose.foundation.shape.RoundedCornerShape(26.dp)).background(pal.bubble)
                .padding(horizontal = 18.dp),
            contentAlignment = androidx.compose.ui.Alignment.CenterStart,
        ) { Text("消息", color = pal.ink3) }
    }

    private fun captureLongPress(theme: ThemeMode, tag: String, nearBottom: Boolean = false) {
        val id = if (nearBottom) "v-ctx-b" else "v-ctx"
        val session = if (nearBottom) "s-ctx-b" else "s-ctx"
        val sf = sharedFile(id, "daocheng-final.mp4", 24_300_000L, "/Users/alex/Movies/daocheng/out/daocheng-final.mp4")
        val doneFile = File(tempDir, "$id-$tag.mp4").apply { writeBytes(ByteArray(10)) }
        manager.store.setStateForTest(session, sf.fileId, SharedFileTransferState.Downloaded(doneFile, durationMs = 102_000L))

        render(theme) {
            val px = with(LocalDensity.current) { SharedTileSize.roundToPx() }
            manager.putThumbnailForTest(doneFile, px, createMockThumbnailBitmap(warm = true).asImageBitmap())
            Column(Modifier.fillMaxSize().padding(top = 18.dp)) {
                PageBar("稻城剪辑", {}, subtitle = "MacBook Pro · 询问")
                SharedFileHost(session, "MacBook Pro", machineOnline = true, sessionExited = false, modifier = Modifier.weight(1f), manager = manager) {
                    CompositionLocalProvider(LocalChat provides ChatContext(session, Agent.AGENT_CLAUDE, derive(emptyList()), 0L, machineName = "MacBook Pro", machineOnline = true, sessionExited = false)) {
                        Column(Modifier.fillMaxSize().padding(top = 10.dp)) {
                            if (nearBottom) Spacer(Modifier.weight(1f))
                            MeBubble("把剪好的成片发我手机上看看")
                            ChatItemView(ChatItem.Agent("ag-$id-1", "成片导出好了，时长 1 分 42 秒，1080p。"), "Claude")
                            SharedFilesBlock(
                                files = listOf(sf),
                                sessionId = session,
                                machineName = "MacBook Pro",
                                machineOnline = true,
                                sessionExited = false,
                            )
                            if (!nearBottom) ChatItemView(ChatItem.Agent("ag-$id-2", "需要我再出一版竖屏的吗？"), "Claude")
                        }
                    }
                }
                FakeComposer()
            }
        }
        ui.onNodeWithTag(SharedFileCardTag).performTouchInput { longClick() }
        captureShot(if (nearBottom) "android-longpress-bottom-$tag" else "android-longpress-$tag")
    }

    private fun captureLongPressNearBottom(theme: ThemeMode, tag: String) = captureLongPress(theme, tag, nearBottom = true)

    private fun captureFontScale(theme: ThemeMode, tag: String) {
        val paused = sharedFile("fs-p", "pixel-project.zip", 51_100_000L)
        val done = sharedFile("fs-d", "pixel-acceptance-final-1080p-v3.mp4", 51_100_000L)
        val failed = sharedFile("fs-f", "daocheng-final.mp4", 24_300_000L)
        val doneFile = File(tempDir, "fs-$tag.mp4").apply { writeBytes(ByteArray(10)) }
        render(theme) {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, 1.3f)) {
                Column(Modifier.fillMaxSize().padding(horizontal = ChatSide, vertical = 18.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    StateHeader("字号 1.3：已暂停")
                    SharedFileCard(paused, io.github.lewin671.remoteagent.core.resolveSharedFileUiState(paused, SharedFileTransferState.Paused(29_100_000L, 51_100_000L), true, false), null, false, {}, {}, {})
                    StateHeader("字号 1.3：已下载，长文件名")
                    SharedFileCard(done, io.github.lewin671.remoteagent.core.resolveSharedFileUiState(done, SharedFileTransferState.Downloaded(doneFile, 102_000L), true, false), createMockThumbnailBitmap(warm = true).asImageBitmap(), false, {}, {}, {})
                    StateHeader("字号 1.3：失败")
                    SharedFileCard(failed, io.github.lewin671.remoteagent.core.resolveSharedFileUiState(failed, SharedFileTransferState.Failed(SharedFileFailureKind.OTHER, "磁盘配额已用尽"), true, false), null, false, {}, {}, {})
                    StateHeader("字号 1.3：下载中")
                    SharedFileCard(paused, io.github.lewin671.remoteagent.core.resolveSharedFileUiState(paused, SharedFileTransferState.Downloading(8_100_000L, 51_100_000L, 12L), true, false), null, false, {}, {}, {})
                }
            }
        }
        captureShot("android-fontscale13-$tag")
    }

    private fun capturePlayer(theme: ThemeMode, tag: String) {
        fun still(m: Modifier): @Composable () -> Unit = {
            androidx.compose.foundation.Image(createMockThumbnailBitmap(warm = true).asImageBitmap(), null, m, contentScale = androidx.compose.ui.layout.ContentScale.Crop)
        }
        val video = FakePlayerEngine(102_000, video = true).apply { position = 42_000 }
        render(theme) {
            SharedMediaPlayerPage(video, "daocheng-final.mp4", true, {}, {}) { m -> still(m)() }
        }
        captureShot("android-player-video-$tag")
        ui.onNodeWithContentDescription("更多").performClick()
        captureShot("android-player-video-menu-$tag")

        val audio = FakePlayerEngine(12_000, video = false).apply { position = 4_000 }
        render(theme) {
            SharedMediaPlayerPage(audio, "pixel-tone-voice-memo-take-3.mp3", false, {}, {})
        }
        captureShot("android-player-audio-$tag")

        val broken = FakePlayerEngine(video = true).apply { failed = true }
        render(theme) {
            SharedMediaPlayerPage(broken, "clip.mkv", true, {}, {})
        }
        captureShot("android-player-error-$tag")
    }

    // ---- long-press overlay ----

    private fun downloadedFile(session: String, id: String, name: String, durationMs: Long? = 102_000L): SharedFile {
        val sf = sharedFile(id, name, 24_300_000L)
        val local = File(manager.rootDir, "$session/$id/$name").apply {
            parentFile!!.mkdirs()
            writeBytes(ByteArray(16))
        }
        manager.store.setStateForTest(session, id, SharedFileTransferState.Downloaded(local, durationMs))
        return sf
    }

    /** A page with a top bar and a composer around the chat area, like the session page. */
    private fun renderPageWithBars(
        session: String,
        files: List<SharedFile>,
        gapAboveDp: Int = 0,
        theme: ThemeMode = ThemeMode.LIGHT,
        fontScale: Float = 1f,
    ) {
        render(theme) {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                Column(Modifier.fillMaxSize()) {
                    Box(Modifier.fillMaxWidth().height(56.dp).background(Color(0xFFD0021B))) { Text("top bar", color = Color.White) }
                    SharedFileHost(session, "MacBook Pro", machineOnline = true, sessionExited = false, modifier = Modifier.weight(1f), manager = manager) {
                        Column(Modifier.fillMaxSize()) {
                            Spacer(Modifier.height(gapAboveDp.dp))
                            SharedFilesBlock(files, session, "MacBook Pro", machineOnline = true, sessionExited = false)
                        }
                    }
                    Box(Modifier.fillMaxWidth().height(80.dp).background(Color(0xFF0A84FF))) { Text("composer", color = Color.White) }
                }
            }
        }
    }

    private fun windowHeightPx(): Int = ui.runOnIdle { WindowInspector.getGlobalWindowViews().first().height }

    private fun liftedCard() = ui.onNode(
        hasContentDescription("daocheng-final.mp4", substring = true) and !hasTestTag(SharedFileCardTag),
    )

    @Test
    fun longPressOverlayCoversTheWholeWindowAndTheOriginalCardIsHidden() {
        val sf = downloadedFile("s-ov", "v-ov", "daocheng-final.mp4")
        renderPageWithBars("s-ov", listOf(sf), gapAboveDp = 40)
        val card = ui.onNodeWithTag(SharedFileCardTag).fetchSemanticsNode().boundsInWindow
        val probeX = (card.left + 150f).toInt()
        val probeY = (card.top + 3f).toInt()
        ui.runOnIdle { assertEquals(LightPalette.bubble.toArgb(), drawPageWindow().getPixel(probeX, probeY)) }

        ui.onNodeWithTag(SharedFileCardTag).performTouchInput { longClick() }
        ui.waitForIdle()

        // The overlay is a window of its own over everything: top bar and composer included.
        val overlay = ui.onNodeWithTag(SharedFileOverlayTag).fetchSemanticsNode().boundsInWindow
        assertEquals(0f, overlay.left, 0.5f)
        assertEquals(0f, overlay.top, 0.5f)
        val windowH = windowHeightPx()
        assertEquals(390f, overlay.width, 0.5f)
        assertEquals(windowH.toFloat(), overlay.height, 1.5f)
        ui.runOnIdle {
            val page = drawPageWindow()
            val all = drawAllWindows()
            // The pressed card is not on the page any more: its place shows the page colour.
            assertEquals(LightPalette.paper.toArgb(), page.getPixel(probeX, probeY))
            // The dim reaches the top bar and the composer.
            for ((x, y) in listOf(5 to 5, 5 to windowH - 5)) {
                assertTrue("dimmed at $x,$y", android.graphics.Color.red(all.getPixel(x, y)) < android.graphics.Color.red(page.getPixel(x, y)) ||
                    android.graphics.Color.blue(all.getPixel(x, y)) < android.graphics.Color.blue(page.getPixel(x, y)))
            }
        }

        // The lifted card sits where the original is (centred on it: it is 2% larger), never a second one.
        val lifted = liftedCard().fetchSemanticsNode().boundsInWindow
        assertEquals(card.center.x, lifted.center.x, 1.5f)
        assertEquals(card.center.y, lifted.center.y, 1.5f)
        assertEquals(card.width * 1.02f, lifted.width, 2f)
        ui.onAllNodes(hasContentDescription("daocheng-final.mp4", substring = true)).assertCountEquals(2)
    }

    @Test
    fun cardAndMenuMoveUpTogetherJustEnoughToFit() {
        val sf = downloadedFile("s-up", "v-up", "daocheng-final.mp4")
        renderPageWithBars("s-up", listOf(sf), gapAboveDp = 600)
        val card = ui.onNodeWithTag(SharedFileCardTag).fetchSemanticsNode().boundsInWindow
        ui.onNodeWithTag(SharedFileCardTag).performTouchInput { longClick() }
        ui.waitForIdle()
        val menu = ui.onNodeWithTag(SharedFileMenuTag).fetchSemanticsNode().boundsInWindow
        val lifted = liftedCard().fetchSemanticsNode().boundsInWindow
        // 16 dp from the bottom edge, exactly; the card moved up, not sideways.
        assertEquals(windowHeightPx() - 16f, menu.bottom, 2f)
        assertTrue("card moved up: ${card.top} -> ${lifted.top}", lifted.top < card.top - 20f)
        assertEquals(card.center.x, lifted.center.x, 1.5f)
        // 10 dp between the card and the menu (the card is drawn 2% larger than its slot).
        assertEquals(10f, menu.top - lifted.bottom, 4f)
    }

    @Test
    fun scrimIsEighteenPercentOverABlurAndStrongerWithoutOne() {
        assertEquals(0.18f, SharedFileSpec.scrim(dark = false, blurred = true).alpha, 0.01f)
        assertEquals(0.18f, SharedFileSpec.scrim(dark = true, blurred = true).alpha, 0.01f)
        assertEquals(0.32f, SharedFileSpec.scrim(dark = false, blurred = false).alpha, 0.01f)
        assertEquals(0.50f, SharedFileSpec.scrim(dark = true, blurred = false).alpha, 0.01f)
    }

    @Test
    fun openInAnotherAppIsInTheMenuOnlyOfADownloadedFile() {
        val done = downloadedFile("s-m1", "m-done", "report.pdf")
        val idle = sharedFile("m-idle", "report.pdf", 1_000)
        val downloading = sharedFile("m-dl", "report.pdf", 1_000)
        val paused = sharedFile("m-pa", "report.pdf", 1_000)
        val failed = sharedFile("m-fa", "report.pdf", 1_000)
        manager.store.setStateForTest("s-m", "m-idle", SharedFileTransferState.Idle)
        manager.store.setStateForTest("s-m", "m-dl", SharedFileTransferState.Downloading(100, 1_000, null))
        manager.store.setStateForTest("s-m", "m-pa", SharedFileTransferState.Paused(100, 1_000))
        manager.store.setStateForTest("s-m", "m-fa", SharedFileTransferState.Failed(SharedFileFailureKind.DISCONNECTED))
        for ((session, file) in listOf("s-m" to idle, "s-m" to downloading, "s-m" to paused, "s-m" to failed)) {
            renderPageWithBars(session, listOf(file))
            ui.onNodeWithTag(SharedFileCardTag).performTouchInput { longClick() }
            ui.waitForIdle()
            ui.onAllNodesWithText("用其他应用打开").assertCountEquals(0)
        }
        renderPageWithBars("s-m1", listOf(done))
        ui.onNodeWithTag(SharedFileCardTag).performTouchInput { longClick() }
        ui.waitForIdle()
        ui.onNodeWithText("用其他应用打开").assertExists()
        // Right after 分享…, before the separated delete.
        val labels = listOf("存到「下载」", "分享…", "用其他应用打开", "删除本机副本")
            .map { ui.onNodeWithText(it).fetchSemanticsNode().boundsInWindow.top }
        assertEquals(labels.sorted(), labels)
    }

    // ---- large font ----

    private fun unmergedText(text: String, substring: Boolean = false) =
        ui.onNode(hasText(text, substring = substring), useUnmergedTree = true)

    private fun textLayoutOf(node: androidx.compose.ui.test.SemanticsNodeInteraction): androidx.compose.ui.text.TextLayoutResult {
        val out = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        node.fetchSemanticsNode().config[androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult].action!!.invoke(out)
        return out.single()
    }

    private fun cardAt(fontScale: Float, file: SharedFile, state: SharedFileTransferState) {
        val resolved = io.github.lewin671.remoteagent.core.resolveSharedFileUiState(file, state, true, false)
        render {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                Column(Modifier.padding(16.dp)) { SharedFileCard(file, resolved, null, false, {}, {}, {}, modifier = Modifier.testTag(SharedFileCardTag)) }
            }
        }
    }

    @Test
    fun subtitleIsNeverEllipsizedAtFontScale13AndTheControlStaysCentred() {
        val sf = sharedFile("fs-1", "pixel-project.zip", 51_100_000L)
        cardAt(1.0f, sf, SharedFileTransferState.Failed(SharedFileFailureKind.OTHER, "磁盘配额已用尽"))
        val normalHeight = ui.onNodeWithTag(SharedFileCardTag).fetchSemanticsNode().boundsInWindow.height

        cardAt(1.3f, sf, SharedFileTransferState.Failed(SharedFileFailureKind.OTHER, "磁盘配额已用尽"))
        val full = "没下载下来，磁盘配额已用尽"
        val layout = textLayoutOf(unmergedText(full))
        assertEquals(full, layout.layoutInput.text.text)
        assertEquals(2, layout.lineCount)
        assertFalse(layout.hasVisualOverflow)
        assertEquals(full.length, layout.getLineEnd(layout.lineCount - 1, visibleEnd = true))
        val card = ui.onNodeWithTag(SharedFileCardTag).fetchSemanticsNode().boundsInWindow
        assertTrue("the card grew: $normalHeight -> ${card.height}", card.height > normalHeight)
        val retry = ui.onNode(hasText("重试"), useUnmergedTree = true).fetchSemanticsNode().boundsInWindow
        assertEquals(card.center.y, retry.center.y, 1.5f)

        cardAt(1.3f, sf, SharedFileTransferState.Paused(29_100_000L, 51_100_000L))
        val paused = textLayoutOf(unmergedText("已暂停 · 29.1 / 51.1 MB"))
        assertTrue(paused.lineCount <= 2)
        assertFalse(paused.hasVisualOverflow)
        assertEquals("已暂停 · 29.1 / 51.1 MB".length, paused.getLineEnd(paused.lineCount - 1, visibleEnd = true))
        val control = ui.onNode(hasContentDescription("继续下载"), useUnmergedTree = true).fetchSemanticsNode().boundsInWindow
        assertEquals(ui.onNodeWithTag(SharedFileCardTag).fetchSemanticsNode().boundsInWindow.center.y, control.center.y, 1.5f)
    }

    @Test
    fun fileNameIsOneLineUpToScale115AndTwoAbove() {
        assertEquals(1, sharedFileNameMaxLines(1.0f))
        assertEquals(1, sharedFileNameMaxLines(1.15f))
        assertEquals(2, sharedFileNameMaxLines(1.16f))
        assertEquals(2, sharedFileNameMaxLines(1.3f))

        val long = sharedFile("fs-2", "daocheng-final-vertical-1080-v3-with-subtitles-and-music-and-more.mp4", 61_700_000L)
        cardAt(1.0f, long, SharedFileTransferState.Idle)
        val oneLine = textLayoutOf(ui.onNode(hasText("…", substring = true) and hasText(".mp4", substring = true), useUnmergedTree = true))
        assertEquals(1, oneLine.lineCount)

        cardAt(1.3f, long, SharedFileTransferState.Idle)
        val twoLines = textLayoutOf(ui.onNode(hasText("…", substring = true) and hasText(".mp4", substring = true), useUnmergedTree = true))
        assertEquals(2, twoLines.lineCount)
        assertTrue(twoLines.layoutInput.text.text.endsWith(".mp4"))

        // A name that fits on two lines is shown whole.
        val medium = sharedFile("fs-3", "daocheng-final-v3.mp4", 61_700_000L)
        cardAt(1.3f, medium, SharedFileTransferState.Idle)
        unmergedText("daocheng-final-v3.mp4").assertExists()
    }

    // ---- where a file opens ----

    @Test
    fun routingSendsImagesToTheViewerMediaToThePlayerAndTheRestToTheChooser() {
        for (n in listOf("a.png", "a.JPG", "a.webp", "a.gif", "a.heic")) assertEquals(n, SharedFileOpenRoute.IMAGE_VIEWER, sharedFileOpenRoute(n))
        for (n in listOf("a.mp4", "a.MOV", "a.m4v", "a.webm", "a.mkv")) assertEquals(n, SharedFileOpenRoute.VIDEO_PLAYER, sharedFileOpenRoute(n))
        for (n in listOf("a.mp3", "a.m4a", "a.wav", "a.AAC", "a.flac", "a.ogg")) assertEquals(n, SharedFileOpenRoute.AUDIO_PLAYER, sharedFileOpenRoute(n))
        for (n in listOf("a.zip", "a.pdf", "a.txt", "a.avi", "a.3gp", "a.opus", "noext", "a.mp4.zip")) assertEquals(n, SharedFileOpenRoute.SYSTEM_CHOOSER, sharedFileOpenRoute(n))
    }

    private fun localFor(session: String, name: String): File =
        File(manager.rootDir, "$session/x/$name").apply {
            parentFile!!.mkdirs()
            writeBytes(ByteArray(8))
        }

    private fun registerViewer() {
        val pm = shadowOf(context.packageManager)
        val cn = android.content.ComponentName("com.example.viewer", "com.example.viewer.Main")
        pm.addActivityIfNotPresent(cn)
        pm.addIntentFilterForActivity(cn, android.content.IntentFilter(android.content.Intent.ACTION_VIEW).apply {
            addCategory(android.content.Intent.CATEGORY_DEFAULT)
            addDataType("*/*")
        })
    }

    @Test
    fun openingARoutedFileGoesToTheRightPlace() {
        registerViewer()
        val host = SharedFileHostState(manager, "s-r", "MacBook Pro", true, false)
        val app = shadowOf(context)

        openSharedFile(context, sharedFile("r1", "clip.mp4", 5), localFor("s-r", "clip.mp4"), host)
        assertEquals(true, host.playerTarget?.video)
        assertEquals(null, host.imagePreview)
        host.playerTarget = null

        openSharedFile(context, sharedFile("r2", "tone.mp3", 5), localFor("s-r", "tone.mp3"), host)
        assertEquals(false, host.playerTarget?.video)
        host.playerTarget = null

        openSharedFile(context, sharedFile("r3", "cover.png", 5), localFor("s-r", "cover.png"), host)
        assertEquals("cover.png", host.imagePreview?.file?.name)
        assertEquals(null, host.playerTarget)
        host.imagePreview = null
        assertNull(app.nextStartedActivity)

        // Everything else: always the system chooser, titled with the file name.
        openSharedFile(context, sharedFile("r4", "project.zip", 5), localFor("s-r", "project.zip"), host)
        val started = app.nextStartedActivity
        assertEquals(android.content.Intent.ACTION_CHOOSER, started.action)
        assertEquals("project.zip", started.getCharSequenceExtra(android.content.Intent.EXTRA_TITLE).toString())
        val inner = started.getParcelableExtra<android.content.Intent>(android.content.Intent.EXTRA_INTENT)!!
        assertEquals(android.content.Intent.ACTION_VIEW, inner.action)
        assertEquals("application/zip", inner.type)
        assertEquals(null, host.playerTarget)
    }

    @Test
    fun withoutAnyAppTheChooserIsNotShownAndTheNoticeIs() {
        val host = SharedFileHostState(manager, "s-n", "MacBook Pro", true, false)
        openSharedFile(context, sharedFile("n1", "project.zip", 5), localFor("s-n", "project.zip"), host)
        assertNull(shadowOf(context).nextStartedActivity)
        assertEquals("project.zip", host.unopenableTarget?.file?.name)
    }

    @Test
    fun openInAnotherAppAlwaysShowsTheChooserEvenForAVideo() {
        registerViewer()
        val local = localFor("s-o", "clip.mp4")
        assertTrue(manager.openExternal(context, local, "clip.mp4"))
        val started = shadowOf(context).nextStartedActivity
        assertEquals(android.content.Intent.ACTION_CHOOSER, started.action)
        assertEquals("video/mp4", started.getParcelableExtra<android.content.Intent>(android.content.Intent.EXTRA_INTENT)!!.type)
    }
}
