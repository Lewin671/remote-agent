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
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.assertCountEquals
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
            val view = WindowInspector.getGlobalWindowViews().last()
            val bitmap = Bitmap.createBitmap(view.width, view.height, Bitmap.Config.ARGB_8888)
            view.draw(Canvas(bitmap))
            File(outDir, "$name.png").apply { parentFile!!.mkdirs() }.outputStream().use {
                assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
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
    fun screenshotsMatchDraftLightAndDark() {
        for (theme in listOf(ThemeMode.LIGHT, ThemeMode.DARK)) {
            val tag = if (theme == ThemeMode.DARK) "dark" else "light"
            captureInChat(theme, tag)
            captureSixStates(theme, tag)
            captureMultiFiles(theme, tag)
            captureLongPress(theme, tag)
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

    private fun captureLongPress(theme: ThemeMode, tag: String) {
        val sf = sharedFile("v-ctx", "daocheng-final.mp4", 24_300_000L, "/Users/alex/Movies/daocheng/out/daocheng-final.mp4")
        val doneFile = File(tempDir, "ctx-$tag.mp4").apply { writeBytes(ByteArray(10)) }
        manager.store.setStateForTest("s-ctx", sf.fileId, SharedFileTransferState.Downloaded(doneFile, durationMs = 102_000L))

        render(theme) {
            val px = with(LocalDensity.current) { SharedTileSize.roundToPx() }
            manager.putThumbnailForTest(doneFile, px, createMockThumbnailBitmap(warm = true).asImageBitmap())
            SharedFileHost("s-ctx", "MacBook Pro", machineOnline = true, sessionExited = false, manager = manager) {
                CompositionLocalProvider(LocalChat provides ChatContext("s-ctx", Agent.AGENT_CLAUDE, derive(emptyList()), 0L, machineName = "MacBook Pro", machineOnline = true, sessionExited = false)) {
                    Column(Modifier.fillMaxSize().padding(top = 18.dp)) {
                        MeBubble("把剪好的成片发我手机上看看")
                        ChatItemView(ChatItem.Agent("ag-ctx-1", "成片导出好了，时长 1 分 42 秒，1080p。"), "Claude")
                        SharedFilesBlock(
                            files = listOf(sf),
                            sessionId = "s-ctx",
                            machineName = "MacBook Pro",
                            machineOnline = true,
                            sessionExited = false,
                        )
                        ChatItemView(ChatItem.Agent("ag-ctx-2", "需要我再出一版竖屏的吗？"), "Claude")
                    }
                }
            }
        }
        ui.onNodeWithText("1:42 · 24.3 MB").performTouchInput { longClick() }
        captureShot("android-longpress-$tag")
    }
}
