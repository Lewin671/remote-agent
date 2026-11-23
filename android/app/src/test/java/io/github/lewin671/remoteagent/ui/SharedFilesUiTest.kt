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
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.test.core.app.ApplicationProvider
import io.github.lewin671.remoteagent.RaAppTestRule
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.SharedFileTransferState
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.SessionAgentMessageItem
import io.github.lewin671.remoteagent.core.proto.SessionSharedFileItem
import io.github.lewin671.remoteagent.core.proto.SessionSharedFilesItem
import io.github.lewin671.remoteagent.core.proto.SessionTimelineItem
import io.github.lewin671.remoteagent.core.proto.SessionToolCallItem
import io.github.lewin671.remoteagent.core.proto.SessionToolStatus
import io.github.lewin671.remoteagent.core.proto.SharedFileCardStatus
import io.github.lewin671.remoteagent.core.proto.SharedFileKind
import io.github.lewin671.remoteagent.core.proto.SharedFileMenuAction
import io.github.lewin671.remoteagent.core.proto.SharedFileMenuItemView
import io.github.lewin671.remoteagent.core.proto.SharedFilePrimaryAction
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.core.proto.TextArg
import io.github.lewin671.remoteagent.data.SharedFileManager
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.NotifyKind
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.push.Notifier
import java.io.File
import java.nio.file.Files
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class, qualifiers = "w390dp-h844dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SharedFilesUiTest {

    @get:Rule(order = 0)
    val appFixture = RaAppTestRule(preparePendingStore = true)

    @get:Rule(order = 1)
    val ui = createComposeRule()

    @get:Rule(order = 2)
    val appJobs = appFixture.stopJobs { done ->
        ui.waitUntil(5000) { ui.mainClock.advanceTimeByFrame(); ui.waitForIdle(); done() }
    }

    private val context: Application get() = ApplicationProvider.getApplicationContext()
    private val remote = Remote(Account("http://127.0.0.1:1", "c1"), Api.defaultHttp())
    private val nav = Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {})
    private lateinit var tempDir: File
    private lateinit var manager: SharedFileManager

    private fun lit(s: String): CoreText = CoreText.newBuilder().setLiteral(s).build()
    private fun key(k: String, vararg args: Pair<String, String>): CoreText = CoreText.newBuilder().setKey(k).apply {
        for ((n, v) in args) addArgs(TextArg.newBuilder().setName(n).setStrValue(v))
    }.build()

    private fun menuItem(action: SharedFileMenuAction, labelKey: String, destructive: Boolean = false): SharedFileMenuItemView =
        SharedFileMenuItemView.newBuilder()
            .setAction(action)
            .setLabel(key(labelKey))
            .setDestructive(destructive)
            .setEnabled(true)
            .build()

    private fun sharedFileItem(
        id: String,
        name: String,
        sizeBytes: Long,
        kind: SharedFileKind = when {
            name.endsWith(".mp4") -> SharedFileKind.SHARED_FILE_KIND_VIDEO
            name.endsWith(".png") -> SharedFileKind.SHARED_FILE_KIND_IMAGE
            name.endsWith(".mp3") -> SharedFileKind.SHARED_FILE_KIND_AUDIO
            name.endsWith(".pdf") -> SharedFileKind.SHARED_FILE_KIND_PDF
            name.endsWith(".zip") -> SharedFileKind.SHARED_FILE_KIND_ARCHIVE
            else -> SharedFileKind.SHARED_FILE_KIND_OTHER
        },
        status: SharedFileCardStatus = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_AVAILABLE,
        subtitle: String = "视频 · 24.3 MB",
        subtitleIsError: Boolean = false,
        progress: Double = 0.0,
        primaryAction: SharedFilePrimaryAction = when (status) {
            SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADING -> SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_PAUSE
            SharedFileCardStatus.SHARED_FILE_CARD_STATUS_PAUSED -> SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_RESUME
            SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED -> SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_OPEN
            SharedFileCardStatus.SHARED_FILE_CARD_STATUS_FAILED -> SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_RETRY
            SharedFileCardStatus.SHARED_FILE_CARD_STATUS_CLEANED -> SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_UNSPECIFIED
            else -> SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_DOWNLOAD
        },
        primaryLabel: CoreText = when (primaryAction) {
            SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_PAUSE -> key(UiMessages.sharedFileActionPause().key)
            SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_RESUME -> key(UiMessages.sharedFileActionResume().key)
            SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_RETRY -> key(UiMessages.sharedFileActionRetry().key)
            SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_OPEN -> key(UiMessages.sharedFileActionOpen().key)
            else -> key(UiMessages.sharedFileActionDownload().key)
        },
        primaryEnabled: Boolean = status != SharedFileCardStatus.SHARED_FILE_CARD_STATUS_OFFLINE &&
            status != SharedFileCardStatus.SHARED_FILE_CARD_STATUS_CLEANED,
        displayPath: String = "~/Movies/daocheng/out/$name",
        menuItems: List<SharedFileMenuItemView> = when (status) {
            SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED -> buildList {
                if (kind == SharedFileKind.SHARED_FILE_KIND_VIDEO || kind == SharedFileKind.SHARED_FILE_KIND_IMAGE) {
                    add(menuItem(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE_TO_ALBUM, UiMessages.sharedFileMenuSaveToAlbum().key))
                }
                add(menuItem(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE, UiMessages.sharedFileMenuSaveToDownloads().key))
                add(menuItem(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SHARE, UiMessages.sharedFileMenuShare().key))
                add(menuItem(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_OPEN_WITH, UiMessages.sharedFileMenuOpenWith().key))
                add(menuItem(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_DELETE_LOCAL, UiMessages.sharedFileMenuDeleteLocal().key, destructive = true))
            }
            SharedFileCardStatus.SHARED_FILE_CARD_STATUS_AVAILABLE -> listOf(
                menuItem(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_DOWNLOAD, UiMessages.sharedFileActionDownload().key),
            )
            else -> emptyList()
        },
    ): SessionSharedFileItem {
        val dot = name.lastIndexOf('.')
        val splitAt = if (dot > 3) dot - 3 else name.length
        val head = if (dot > 3) name.substring(0, splitAt) else name
        val tail = if (dot > 3) name.substring(splitAt) else ""
        return SessionSharedFileItem.newBuilder()
            .setFileId(id)
            .setDisplayName(name)
            .setNameHead(head)
            .setNameTail(tail)
            .setDisplayPath(displayPath)
            .setSizeBytes(sizeBytes)
            .setKind(kind)
            .setStatus(status)
            .setSubtitle(lit(subtitle))
            .setSubtitleIsError(subtitleIsError)
            .setSourceCaption(key(UiMessages.sharedFileMenuFromMachine(machine = "MacBook Pro").key, "machine" to "MacBook Pro"))
            .setA11YLabel(lit("$name，$subtitle"))
            .setProgress(progress)
            .setPrimaryAction(primaryAction)
            .setPrimaryLabel(primaryLabel)
            .setPrimaryEnabled(primaryEnabled)
            .setSupportsAlbumSave(kind == SharedFileKind.SHARED_FILE_KIND_VIDEO || kind == SharedFileKind.SHARED_FILE_KIND_IMAGE)
            .addAllMenuItems(menuItems)
            .build()
    }

    @Test
    fun autoOpenBelongsToThePageThatStartedTheDownload() {
        val host = SharedFileHostState(manager, "s")
        val file = sharedFileItem("f", "cover.png", 10)
        host.startDownload(file)
        assertTrue(host.consumeAutoOpen("f"))
        assertEquals(false, host.consumeAutoOpen("f"))
        host.startDownload(file)
        host.leavePage()
        assertEquals(false, host.consumeAutoOpen("f"))
        val returned = SharedFileHostState(manager, "s")
        assertEquals(false, returned.consumeAutoOpen("f"))
    }

    @Before
    fun setup() {
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
    fun goldenSharedFilesAndSentFileChipsRenderDirectlyFromCoreViewModel() {
        val vm = ScreenGoldenLoader.sessionView("session_shared_files_images", "session_initial")
        val items = vm.timeline.itemsList
        val byId = items.filter { it.kindCase == SessionTimelineItem.KindCase.SHARED_FILES }
            .flatMap { it.sharedFiles.filesList }
            .associateBy { it.fileId }
        render {
            SharedFileHost("s-media", byId, manager = manager) {
                CompositionLocalProvider(LocalChat provides ChatContext("s-media", Agent.AGENT_CODEX, "Codex")) {
                    Column(Modifier.fillMaxSize().padding(top = 12.dp)) {
                        items.forEach { TimelineItemView(it) }
                    }
                }
            }
        }
        ui.onNodeWithText("1.5 MB").assertExists()
        ui.onNodeWithText("design-notes.docx").assertExists()
    }

    @Test
    fun contextMenuRendersCoreMenuItemsAndDeletesLocalCopy() {
        val videoFile = sharedFileItem(
            id = "v1",
            name = "daocheng-final.mp4",
            sizeBytes = 24_300_000L,
            status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED,
            subtitle = "1:42 · 24.3 MB",
            displayPath = "~/Movies/daocheng/out/daocheng-final.mp4",
        )
        val pdfFile = sharedFileItem(
            id = "p1",
            name = "report.pdf",
            sizeBytes = 2_100_000L,
            status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED,
            subtitle = "PDF · 2.1 MB",
            displayPath = "~/Docs/report.pdf",
        )
        val localVideo = File(tempDir, "daocheng-final.mp4").apply { writeBytes(ByteArray(100)) }
        val localPdf = File(tempDir, "report.pdf").apply { writeBytes(ByteArray(100)) }

        manager.store.setStateForTest("s1", "v1", SharedFileTransferState.Downloaded(localVideo, durationMs = 102_000L))
        manager.store.setStateForTest("s1", "p1", SharedFileTransferState.Downloaded(localPdf))

        render {
            SharedFileHost("s1", mapOf("v1" to videoFile, "p1" to pdfFile), manager = manager) {
                SharedFilesBlock(files = listOf(videoFile, pdfFile), sessionId = "s1")
            }
        }
        ui.onNodeWithText("1:42 · 24.3 MB").performTouchInput { longClick() }
        ui.onNodeWithText("~/Movies/daocheng/out/daocheng-final.mp4").assertExists()
        ui.onNodeWithText("来自 MacBook Pro").assertExists()
        ui.onNodeWithText("存到相册").assertExists()
        ui.onNodeWithText("存到「下载」").assertExists()
        ui.onNodeWithText("分享…").assertExists()
        ui.onNodeWithText("删除本机副本").assertExists()

        ui.onNodeWithText("删除本机副本").performClick()
        ui.waitForIdle()
        assertEquals(SharedFileTransferState.Idle, manager.store.states.value["s1:v1"])

        render {
            SharedFileHost("s1", mapOf("p1" to pdfFile), manager = manager) {
                SharedFilesBlock(files = listOf(pdfFile), sessionId = "s1")
            }
        }
        ui.onNodeWithText("PDF · 2.1 MB").performTouchInput { longClick() }
        ui.onNodeWithText("存到「下载」").assertExists()
        ui.onAllNodesWithText("存到相册").assertCountEquals(0)
        val labels = listOf("存到「下载」", "分享…", "用其他应用打开", "删除本机副本")
            .map { ui.onNodeWithText(it).fetchSemanticsNode().boundsInWindow.top }
        assertEquals(labels.sorted(), labels)
    }

    @Test
    fun turnEndedNotificationRendersCoreProjectedSharedFileBody() {
        val nm = context.getSystemService(NotificationManager::class.java)
        val snm = Shadows.shadowOf(nm)
        val suffix = CoreText.newBuilder()
            .setKey("shared_file.notification_files")
            .addArgs(TextArg.newBuilder().setName("count").setIntValue(2L))
            .build()
        val display = io.github.lewin671.remoteagent.core.proto.SecurityNotifyDisplay.newBuilder()
            .setValid(true)
            .setKind(NotifyKind.NOTIFY_KIND_TURN_ENDED)
            .setSessionId("s-notify")
            .setTitle(CoreText.newBuilder().setLiteral("稻城剪辑"))
            .setBody(
                CoreText.newBuilder()
                    .setSeparatorLiteral(" ")
                    .addJoin(CoreText.newBuilder().setLiteral("成片导出好了"))
                    .addJoin(suffix),
            )
            .setSharedFileCount(2)
            .build()
        Notifier.show(
            context,
            "s-notify",
            NotifyKind.NOTIFY_KIND_TURN_ENDED,
            display,
            showContent = true,
            copy = { msg -> messageTextForTest(msg, "zh-CN") },
        )
        val posted = snm.allNotifications.last()
        assertEquals("成片导出好了 附 2 个文件", posted.extras.getString(Notification.EXTRA_TEXT))
        nm.cancelAll()
    }

    @Test
    fun fittingAndOverflowingFileNamesRenderAsSingleTextNodes() {
        val fit = sharedFileItem("fit-1", "daocheng-final.mp4", 24_300_000L)
        val overflow = sharedFileItem("ovr-1", "daocheng-final-vertical-1080-v3.mp4", 61_700_000L)
        val audio = sharedFileItem("aud-1", "voice-memo.mp3", 1_200_000L)
        render {
            SharedFileHost("s-name", listOf(fit, overflow, audio).associateBy { it.fileId }, manager = manager) {
                SharedFilesBlock(files = listOf(fit, overflow, audio), sessionId = "s-name")
            }
        }
        ui.onAllNodesWithText("daocheng-final.mp4").assertCountEquals(1)
        ui.onAllNodesWithText("voice-memo.mp3").assertCountEquals(1)

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
        val sf = sharedFileItem("v-chat", "daocheng-final.mp4", 24_300_000L)
        val items = listOf(
            SessionTimelineItem.newBuilder().setId("e1").setSeq(1)
                .setUserMessage(io.github.lewin671.remoteagent.core.proto.SessionUserMessageItem.newBuilder().setText("把剪好的成片发我手机上看看")).build(),
            SessionTimelineItem.newBuilder().setId("e2").setSeq(2)
                .setToolCall(SessionToolCallItem.newBuilder().setToolKind(ToolKind.TOOL_KIND_COMMAND).setTitle("ffmpeg -i cut.mov -c:v libx264 out/daocheng-final.mp4").setStatus(SessionToolStatus.SESSION_TOOL_STATUS_OK)).build(),
            SessionTimelineItem.newBuilder().setId("e3").setSeq(3)
                .setAgentMessage(SessionAgentMessageItem.newBuilder().setMarkdown("成片导出好了，时长 1 分 42 秒，1080p。")).build(),
            SessionTimelineItem.newBuilder().setId("e4").setSeq(4)
                .setSharedFiles(SessionSharedFilesItem.newBuilder().addFiles(sf)).build(),
            SessionTimelineItem.newBuilder().setId("e5").setSeq(5)
                .setAgentMessage(SessionAgentMessageItem.newBuilder().setMarkdown("需要我再出一版竖屏的吗？")).build(),
        )
        render(theme) {
            SharedFileHost("s-chat", mapOf(sf.fileId to sf), manager = manager) {
                CompositionLocalProvider(LocalChat provides ChatContext("s-chat", Agent.AGENT_CLAUDE, "Claude")) {
                    Column(Modifier.fillMaxSize().padding(top = 18.dp)) {
                        PageBar("稻城剪辑", {}, subtitle = "MacBook Pro · 询问")
                        Spacer(Modifier.height(10.dp))
                        items.forEach { item -> TimelineItemView(item) }
                    }
                }
            }
        }
        captureShot("android-inchat-$tag")
    }

    private fun captureSixStates(theme: ThemeMode, tag: String) {
        val f1 = sharedFileItem("s-avail", "daocheng-final.mp4", 24_300_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_AVAILABLE, subtitle = "视频 · 24.3 MB")
        val f2 = sharedFileItem("s-dl", "daocheng-final.mp4", 24_300_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADING, subtitle = "8.1 / 24.3 MB · 剩 12 秒", progress = 0.33)
        val f3 = sharedFileItem("s-done", "daocheng-final.mp4", 24_300_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED, subtitle = "1:42 · 24.3 MB")
        val f4 = sharedFileItem("s-fail", "daocheng-final.mp4", 24_300_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_FAILED, subtitle = "连接中断", subtitleIsError = true)
        val f5 = sharedFileItem("s-off", "daocheng-final.mp4", 24_300_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_OFFLINE, subtitle = "电脑离线")
        val f6 = sharedFileItem("s-gone", "daocheng-final.mp4", 24_300_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_CLEANED, subtitle = "会话已结束，文件已清理")

        render(theme) {
            Column(Modifier.fillMaxSize().padding(horizontal = ChatSide, vertical = 18.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                StateHeader("可下载")
                SharedFileCard(f1, null, false, {}, {}, {})
                StateHeader("下载中 · 进度环，点一下暂停，再点继续")
                SharedFileCard(f2, null, false, {}, {}, {})
                StateHeader("已下载 · 色块换成缩略图")
                SharedFileCard(f3, createMockThumbnailBitmap(warm = true).asImageBitmap(), false, {}, {}, {})
                StateHeader("失败")
                SharedFileCard(f4, null, false, {}, {}, {})
                StateHeader("电脑不在线")
                SharedFileCard(f5, null, false, {}, {}, {})
                StateHeader("已清理")
                SharedFileCard(f6, null, false, {}, {}, {})
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
        val fImg = sharedFileItem("m-img", "cover.png", 1_800_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED, subtitle = "图片 · 1.8 MB")
        val fVid = sharedFileItem("m-vid", "daocheng-final-vertical-1080-v3.mp4", 61_700_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADING, subtitle = "12.0 / 61.7 MB", progress = 0.2)
        val fZip = sharedFileItem("m-zip", "project.zip", 88_400_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_AVAILABLE, subtitle = "压缩包 · 88.4 MB")
        val imgFile = File(tempDir, "cover-$tag.png").apply {
            outputStream().use { createMockThumbnailBitmap(warm = false).compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        manager.store.setStateForTest("s-multi", fImg.fileId, SharedFileTransferState.Downloaded(imgFile))

        render(theme) {
            val px = with(LocalDensity.current) { SharedTileSize.roundToPx() }
            manager.putThumbnailForTest(imgFile, px, createMockThumbnailBitmap(warm = false).asImageBitmap())
            SharedFileHost("s-multi", listOf(fImg, fVid, fZip).associateBy { it.fileId }, manager = manager) {
                CompositionLocalProvider(LocalChat provides ChatContext("s-multi", Agent.AGENT_CLAUDE, "Claude")) {
                    Column(Modifier.fillMaxSize().padding(top = 18.dp)) {
                        MeBubble("封面图、成片和工程包都给我")
                        SharedFilesBlock(files = listOf(fImg, fVid, fZip), sessionId = "s-multi")
                        TimelineItemView(
                            SessionTimelineItem.newBuilder().setId("ag-multi")
                                .setAgentMessage(SessionAgentMessageItem.newBuilder().setMarkdown("三个文件都放这儿了。")).build(),
                        )
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
        val sf = sharedFileItem(
            id = id,
            name = "daocheng-final.mp4",
            sizeBytes = 24_300_000L,
            status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED,
            subtitle = "1:42 · 24.3 MB",
        )
        val doneFile = File(tempDir, "$id-$tag.mp4").apply { writeBytes(ByteArray(10)) }
        manager.store.setStateForTest(session, sf.fileId, SharedFileTransferState.Downloaded(doneFile, durationMs = 102_000L))

        render(theme) {
            val px = with(LocalDensity.current) { SharedTileSize.roundToPx() }
            manager.putThumbnailForTest(doneFile, px, createMockThumbnailBitmap(warm = true).asImageBitmap())
            Column(Modifier.fillMaxSize().padding(top = 18.dp)) {
                PageBar("稻城剪辑", {}, subtitle = "MacBook Pro · 询问")
                SharedFileHost(session, mapOf(sf.fileId to sf), modifier = Modifier.weight(1f), manager = manager) {
                    CompositionLocalProvider(LocalChat provides ChatContext(session, Agent.AGENT_CLAUDE, "Claude")) {
                        Column(Modifier.fillMaxSize().padding(top = 10.dp)) {
                            if (nearBottom) Spacer(Modifier.weight(1f))
                            MeBubble("把剪好的成片发我手机上看看")
                            TimelineItemView(
                                SessionTimelineItem.newBuilder().setId("ag-$id-1")
                                    .setAgentMessage(SessionAgentMessageItem.newBuilder().setMarkdown("成片导出好了，时长 1 分 42 秒，1080p。")).build(),
                            )
                            SharedFilesBlock(files = listOf(sf), sessionId = session)
                            if (!nearBottom) TimelineItemView(
                                SessionTimelineItem.newBuilder().setId("ag-$id-2")
                                    .setAgentMessage(SessionAgentMessageItem.newBuilder().setMarkdown("需要我再出一版竖屏的吗？")).build(),
                            )
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
        val paused = sharedFileItem("fs-p", "pixel-project.zip", 51_100_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_PAUSED, subtitle = "已暂停 · 29.1 / 51.1 MB", progress = 0.57)
        val done = sharedFileItem("fs-d", "pixel-acceptance-final-1080p-v3.mp4", 51_100_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED, subtitle = "1:42 · 51.1 MB")
        val failed = sharedFileItem("fs-f", "daocheng-final.mp4", 24_300_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_FAILED, subtitle = "没下载下来，磁盘配额已用尽", subtitleIsError = true)
        val dl = sharedFileItem("fs-dl", "pixel-project.zip", 51_100_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADING, subtitle = "8.1 / 51.1 MB · 剩 12 秒", progress = 0.16)
        render(theme) {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, 1.3f)) {
                Column(Modifier.fillMaxSize().padding(horizontal = ChatSide, vertical = 18.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    StateHeader("字号 1.3：已暂停")
                    SharedFileCard(paused, null, false, {}, {}, {})
                    StateHeader("字号 1.3：已下载，长文件名")
                    SharedFileCard(done, createMockThumbnailBitmap(warm = true).asImageBitmap(), false, {}, {}, {})
                    StateHeader("字号 1.3：失败")
                    SharedFileCard(failed, null, false, {}, {}, {})
                    StateHeader("字号 1.3：下载中")
                    SharedFileCard(dl, null, false, {}, {}, {})
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

    private fun downloadedFile(session: String, id: String, name: String, durationMs: Long? = 102_000L): SessionSharedFileItem {
        val sf = sharedFileItem(id, name, 24_300_000L, status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED, subtitle = "1:42 · 24.3 MB")
        val local = File(manager.rootDir, "$session/$id/$name").apply {
            parentFile!!.mkdirs()
            writeBytes(ByteArray(16))
        }
        manager.store.setStateForTest(session, id, SharedFileTransferState.Downloaded(local, durationMs))
        return sf
    }

    private fun renderPageWithBars(
        session: String,
        files: List<SessionSharedFileItem>,
        gapAboveDp: Int = 0,
        theme: ThemeMode = ThemeMode.LIGHT,
        fontScale: Float = 1f,
    ) {
        render(theme) {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                Column(Modifier.fillMaxSize()) {
                    Box(Modifier.fillMaxWidth().height(56.dp).background(Color(0xFFD0021B))) { Text("top bar", color = Color.White) }
                    SharedFileHost(session, files.associateBy { it.fileId }, modifier = Modifier.weight(1f), manager = manager) {
                        Column(Modifier.fillMaxSize()) {
                            Spacer(Modifier.height(gapAboveDp.dp))
                            SharedFilesBlock(files, session)
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

        val overlay = ui.onNodeWithTag(SharedFileOverlayTag).fetchSemanticsNode().boundsInWindow
        assertEquals(0f, overlay.left, 0.5f)
        assertEquals(0f, overlay.top, 0.5f)
        val windowH = windowHeightPx()
        assertEquals(390f, overlay.width, 0.5f)
        assertEquals(windowH.toFloat(), overlay.height, 1.5f)
        ui.runOnIdle {
            val page = drawPageWindow()
            val all = drawAllWindows()
            assertEquals(LightPalette.paper.toArgb(), page.getPixel(probeX, probeY))
            for ((x, y) in listOf(5 to 5, 5 to windowH - 5)) {
                assertTrue("dimmed at $x,$y", android.graphics.Color.red(all.getPixel(x, y)) < android.graphics.Color.red(page.getPixel(x, y)) ||
                    android.graphics.Color.blue(all.getPixel(x, y)) < android.graphics.Color.blue(page.getPixel(x, y)))
            }
        }

        val lifted = liftedCard().fetchSemanticsNode().boundsInWindow
        assertEquals(card.center.x, lifted.center.x, 1.5f)
        assertEquals(card.center.y, lifted.center.y, 1.5f)
        assertEquals(card.width * 1.02f, lifted.width, 2f)
        ui.onAllNodes(hasContentDescription("daocheng-final.mp4", substring = true)).assertCountEquals(2)
    }

    private fun checkOverlayWindow(theme: ThemeMode, lightIcons: Boolean) {
        val sf = downloadedFile("s-bars", "v-bars", "daocheng-final.mp4")
        renderPageWithBars("s-bars", listOf(sf), theme = theme)
        ui.runOnIdle {
            val controller = WindowInspector.getGlobalWindowViews().first().windowInsetsController!!
            val mask = android.view.WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS or android.view.WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS
            controller.setSystemBarsAppearance(if (lightIcons) mask else 0, mask)
        }
        ui.onNodeWithTag(SharedFileCardTag).performTouchInput { longClick() }
        ui.waitForIdle()
        val window = org.robolectric.shadows.ShadowDialog.getLatestDialog().window!!
        assertEquals(0, window.attributes.flags and android.view.WindowManager.LayoutParams.FLAG_DIM_BEHIND)
        assertEquals(android.graphics.Color.TRANSPARENT, window.statusBarColor)
        assertEquals(android.graphics.Color.TRANSPARENT, window.navigationBarColor)
        assertFalse(window.isStatusBarContrastEnforced)
        assertFalse(window.isNavigationBarContrastEnforced)
        val controller = androidx.core.view.WindowCompat.getInsetsController(window, window.decorView)
        assertEquals(lightIcons, controller.isAppearanceLightStatusBars)
        assertEquals(lightIcons, controller.isAppearanceLightNavigationBars)
    }

    @Test
    @Config(sdk = [30])
    fun lightOverlayHasTransparentBarsOneScrimAndPreservesDarkIcons() = checkOverlayWindow(ThemeMode.LIGHT, true)

    @Test
    fun darkOverlayHasTransparentBarsOneScrimAndPreservesLightIcons() = checkOverlayWindow(ThemeMode.DARK, false)

    @Test
    fun cardAndMenuMoveUpTogetherJustEnoughToFit() {
        val sf = downloadedFile("s-up", "v-up", "daocheng-final.mp4")
        renderPageWithBars("s-up", listOf(sf), gapAboveDp = 600)
        val card = ui.onNodeWithTag(SharedFileCardTag).fetchSemanticsNode().boundsInWindow
        ui.onNodeWithTag(SharedFileCardTag).performTouchInput { longClick() }
        ui.waitForIdle()
        val menu = ui.onNodeWithTag(SharedFileMenuTag).fetchSemanticsNode().boundsInWindow
        val lifted = liftedCard().fetchSemanticsNode().boundsInWindow
        assertEquals(windowHeightPx() - 16f, menu.bottom, 2f)
        assertTrue("card moved up: ${card.top} -> ${lifted.top}", lifted.top < card.top - 20f)
        assertEquals(card.center.x, lifted.center.x, 1.5f)
        assertEquals(10f, menu.top - lifted.bottom, 4f)
    }

    @Test
    fun scrimIsEighteenPercentOverABlurAndStrongerWithoutOne() {
        assertEquals(0.18f, SharedFileSpec.scrim(dark = false, blurred = true).alpha, 0.01f)
        assertEquals(0.18f, SharedFileSpec.scrim(dark = true, blurred = true).alpha, 0.01f)
        assertEquals(0.32f, SharedFileSpec.scrim(dark = false, blurred = false).alpha, 0.01f)
        assertEquals(0.50f, SharedFileSpec.scrim(dark = true, blurred = false).alpha, 0.01f)
    }

    private fun unmergedText(text: String, substring: Boolean = false) =
        ui.onNode(hasText(text, substring = substring), useUnmergedTree = true)

    private fun textLayoutOf(node: androidx.compose.ui.test.SemanticsNodeInteraction): androidx.compose.ui.text.TextLayoutResult {
        val out = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        node.fetchSemanticsNode().config[androidx.compose.ui.semantics.SemanticsActions.GetTextLayoutResult].action!!.invoke(out)
        return out.single()
    }

    private fun cardAt(fontScale: Float, item: SessionSharedFileItem) {
        render {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                Column(Modifier.padding(16.dp)) { SharedFileCard(item, null, false, {}, {}, {}, modifier = Modifier.testTag(SharedFileCardTag)) }
            }
        }
    }

    @Test
    fun subtitleIsNeverEllipsizedAtFontScale13AndTheControlStaysCentred() {
        val failed = sharedFileItem(
            id = "fs-1",
            name = "pixel-project.zip",
            sizeBytes = 51_100_000L,
            status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_FAILED,
            subtitle = "没下载下来，磁盘配额已用尽",
            subtitleIsError = true,
        )
        cardAt(1.0f, failed)
        val normalHeight = ui.onNodeWithTag(SharedFileCardTag).fetchSemanticsNode().boundsInWindow.height

        cardAt(1.3f, failed)
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

        val paused = sharedFileItem(
            id = "fs-1",
            name = "pixel-project.zip",
            sizeBytes = 51_100_000L,
            status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_PAUSED,
            subtitle = "已暂停 · 29.1 / 51.1 MB",
            progress = 0.57,
        )
        cardAt(1.3f, paused)
        val pausedLayout = textLayoutOf(unmergedText("已暂停 · 29.1 / 51.1 MB"))
        assertTrue(pausedLayout.lineCount <= 2)
        assertFalse(pausedLayout.hasVisualOverflow)
        assertEquals("已暂停 · 29.1 / 51.1 MB".length, pausedLayout.getLineEnd(pausedLayout.lineCount - 1, visibleEnd = true))
        val control = ui.onNode(hasContentDescription("继续下载"), useUnmergedTree = true).fetchSemanticsNode().boundsInWindow
        assertEquals(ui.onNodeWithTag(SharedFileCardTag).fetchSemanticsNode().boundsInWindow.center.y, control.center.y, 1.5f)
    }

    @Test
    fun fileNameIsOneLineUpToScale115AndTwoAbove() {
        assertEquals(1, sharedFileNameMaxLines(1.0f))
        assertEquals(1, sharedFileNameMaxLines(1.15f))
        assertEquals(2, sharedFileNameMaxLines(1.16f))
        assertEquals(2, sharedFileNameMaxLines(1.3f))

        val long = sharedFileItem("fs-2", "daocheng-final-vertical-1080-v3-with-subtitles-and-music-and-more.mp4", 61_700_000L)
        cardAt(1.0f, long)
        val oneLine = textLayoutOf(ui.onNode(hasText("…", substring = true) and hasText(".mp4", substring = true), useUnmergedTree = true))
        assertEquals(1, oneLine.lineCount)

        cardAt(1.3f, long)
        val twoLines = textLayoutOf(ui.onNode(hasText("…", substring = true) and hasText(".mp4", substring = true), useUnmergedTree = true))
        assertEquals(2, twoLines.lineCount)
        assertTrue(twoLines.layoutInput.text.text.endsWith(".mp4"))

        val medium = sharedFileItem("fs-3", "daocheng-final-v3.mp4", 61_700_000L)
        cardAt(1.3f, medium)
        unmergedText("daocheng-final-v3.mp4").assertExists()
    }

    @Test
    fun routingSendsImagesToTheViewerMediaToThePlayerAndTheRestToTheChooser() {
        for (n in listOf("a.png", "a.JPG", "a.webp", "a.gif", "a.heic")) {
            assertEquals(n, SharedFileOpenRoute.IMAGE_VIEWER, sharedFileOpenRoute(n, SharedFileKind.SHARED_FILE_KIND_IMAGE))
        }
        for (n in listOf("a.mp4", "a.MOV", "a.m4v", "a.webm", "a.mkv")) assertEquals(n, SharedFileOpenRoute.VIDEO_PLAYER, sharedFileOpenRoute(n, SharedFileKind.SHARED_FILE_KIND_VIDEO))
        for (n in listOf("a.mp3", "a.m4a", "a.wav", "a.AAC", "a.flac", "a.ogg")) assertEquals(n, SharedFileOpenRoute.AUDIO_PLAYER, sharedFileOpenRoute(n, SharedFileKind.SHARED_FILE_KIND_AUDIO))
        assertEquals(SharedFileOpenRoute.TEXT_VIEWER, sharedFileOpenRoute("a.txt", SharedFileKind.SHARED_FILE_KIND_DOC))
        assertEquals(SharedFileOpenRoute.PDF_VIEWER, sharedFileOpenRoute("a.pdf", SharedFileKind.SHARED_FILE_KIND_PDF))
        for (n in listOf("a.zip", "a.avi", "a.3gp", "a.opus", "noext", "a.mp4.zip")) assertEquals(n, SharedFileOpenRoute.SYSTEM_CHOOSER, sharedFileOpenRoute(n))
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
        val host = SharedFileHostState(manager, "s-r")
        val app = shadowOf(context)

        openSharedFile(context, sharedFileItem("r1", "clip.mp4", 5), localFor("s-r", "clip.mp4"), host)
        assertEquals(true, host.playerTarget?.video)
        assertEquals(null, host.imagePreview)
        host.playerTarget = null

        openSharedFile(context, sharedFileItem("r2", "tone.mp3", 5), localFor("s-r", "tone.mp3"), host)
        assertEquals(false, host.playerTarget?.video)
        host.playerTarget = null

        openSharedFile(context, sharedFileItem("r3", "cover.png", 5), localFor("s-r", "cover.png"), host)
        assertEquals("cover.png", host.imagePreview?.item?.displayName)
        assertEquals(null, host.playerTarget)
        host.imagePreview = null
        assertNull(app.nextStartedActivity)

        openSharedFile(context, sharedFileItem("r4", "project.zip", 5), localFor("s-r", "project.zip"), host)
        val started = app.nextStartedActivity
        assertEquals(android.content.Intent.ACTION_CHOOSER, started.action)
        assertEquals("project.zip", started.getCharSequenceExtra(android.content.Intent.EXTRA_TITLE).toString())
        assertFalse(started.getBooleanExtra(android.content.Intent.EXTRA_AUTO_LAUNCH_SINGLE_CHOICE, true))
        val inner = started.getParcelableExtra<android.content.Intent>(android.content.Intent.EXTRA_INTENT)!!
        assertEquals(android.content.Intent.ACTION_VIEW, inner.action)
        assertEquals("application/zip", inner.type)
        assertEquals(null, host.playerTarget)
    }

    @Test
    fun withoutAnyAppTheChooserIsNotShownAndTheNoticeIs() {
        val host = SharedFileHostState(manager, "s-n")
        openSharedFile(context, sharedFileItem("n1", "project.zip", 5), localFor("s-n", "project.zip"), host)
        assertNull(shadowOf(context).nextStartedActivity)
        assertEquals("project.zip", host.unopenableTarget?.item?.displayName)
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
