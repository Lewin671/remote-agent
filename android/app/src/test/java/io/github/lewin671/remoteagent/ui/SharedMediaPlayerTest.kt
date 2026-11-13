package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.Context
import android.media.AudioManager
import android.media.MediaPlayer
import android.os.Looper
import android.view.Surface
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.compositeOver
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.assertHeightIsEqualTo
import androidx.compose.ui.unit.dp
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.click
import androidx.compose.ui.test.swipe
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.LifecycleRegistry
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.test.core.app.ApplicationProvider
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.proto.SharedFile
import org.robolectric.shadows.ShadowDialog
import java.io.File
import java.nio.file.Files
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Implementation
import org.robolectric.annotation.Implements
import org.robolectric.util.ReflectionHelpers
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.shadows.ShadowMediaPlayer
import org.robolectric.shadows.util.DataSource

/** A player that plays nothing: the page's own behaviour (timers, labels, seeking) needs no codec. */
internal class FakePlayerEngine(duration: Int = 102_000, video: Boolean = true) : PlayerEngine {
    override var playing by mutableStateOf(false)
    override var failed by mutableStateOf(false)
    override var durationMs by mutableIntStateOf(duration)
    override var videoWidth by mutableIntStateOf(if (video) 1920 else 0)
    override var videoHeight by mutableIntStateOf(if (video) 1080 else 0)
    var position = 0
    val seeks = mutableListOf<Int>()
    val surfaces = mutableListOf<Surface?>()
    var plays = 0
    var pauses = 0
    override fun positionMs() = position
    override fun play() { plays++; playing = true }
    override fun pause() { pauses++; playing = false }
    override fun seekTo(ms: Int) { seeks += ms; position = ms }
    override fun attachSurface(surface: Surface?) { surfaces += surface }
    override fun release() = Unit
}

/** Model the extractor omitting an unsupported video track while retaining its audio. */
@Implements(MediaPlayer::class)
class TrackPlayerShadow : ShadowMediaPlayer() {
    var videoTrack = true

    @Implementation
    fun getTrackInfo(): Array<MediaPlayer.TrackInfo> {
        val type = if (videoTrack) MediaPlayer.TrackInfo.MEDIA_TRACK_TYPE_VIDEO else MediaPlayer.TrackInfo.MEDIA_TRACK_TYPE_AUDIO
        return arrayOf(ReflectionHelpers.newInstance(MediaPlayer.TrackInfo::class.java).also {
            ReflectionHelpers.setField(it, "mTrackType", type)
        })
    }
}

private class TestLifecycleOwner : LifecycleOwner {
    private val registry = LifecycleRegistry.createUnsafe(this)
    override val lifecycle: Lifecycle get() = registry
    fun move(state: Lifecycle.State) { registry.currentState = state }
}

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class, qualifiers = "w390dp-h844dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SharedMediaPlayerTest {
    @get:Rule
    val ui = createComposeRule()

    private val owner = TestLifecycleOwner()
    private val actions = mutableListOf<SharedMenuAction>()
    private var closed = 0
    private lateinit var tempDir: File

    @Before
    fun setup() {
        tempDir = Files.createTempDirectory("player-test").toFile()
        owner.move(Lifecycle.State.RESUMED)
    }

    @After
    fun teardown() {
        tempDir.deleteRecursively()
    }

    private fun page(engine: PlayerEngine, name: String, video: Boolean, theme: ThemeMode = ThemeMode.LIGHT, safeInsets: androidx.compose.foundation.layout.WindowInsets = androidx.compose.foundation.layout.WindowInsets(0, 0, 0, 0)) {
        ui.setContent {
            RaTheme(theme) {
                CompositionLocalProvider(
                    LocalMessageLocale provides "zh-CN",
                    LocalLifecycleOwner provides owner,
                ) {
                    SharedMediaPlayerPage(
                        engine = engine,
                        fileName = name,
                        video = video,
                        onClose = { closed++ },
                        onAction = { actions += it },
                        videoSurface = { m -> Box(m.background(Color(0xFF2F4858))) },
                        safeInsets = safeInsets,
                    )
                }
            }
        }
    }

    @Test
    fun playerWindowDoesNotCoverThePageWithSystemBarScrims() {
        val file = File(tempDir, "clip.mp4").apply { writeBytes(byteArrayOf(0)) }
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                SharedMediaPlayerDialog(
                    SharedFile.newBuilder().setName(file.name).build(), file, video = true,
                    onDismiss = {}, onAction = {},
                )
            }
        }
        ui.waitForIdle()
        val window = ShadowDialog.getLatestDialog().window!!
        assertEquals(android.graphics.Color.TRANSPARENT, window.statusBarColor)
        assertEquals(android.graphics.Color.TRANSPARENT, window.navigationBarColor)
        assertFalse(window.isStatusBarContrastEnforced)
        assertFalse(window.isNavigationBarContrastEnforced)
        val controller = androidx.core.view.WindowCompat.getInsetsController(window, window.decorView)
        assertFalse(controller.isAppearanceLightStatusBars)
        assertFalse(controller.isAppearanceLightNavigationBars)
        assertEquals(androidx.core.view.WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE, controller.systemBarsBehavior)
        assertEquals(android.view.WindowManager.LayoutParams.LAYOUT_IN_DISPLAY_CUTOUT_MODE_SHORT_EDGES, window.attributes.layoutInDisplayCutoutMode)
        val requested: Int = ReflectionHelpers.getField(window.insetsController, "mRequestedVisibleTypes")
        assertEquals(0, requested and android.view.WindowInsets.Type.systemBars())
    }


    @Test
    @Config(sdk = [30])
    fun audioWindowIsImmersiveAndDismissalPreservesTheActivityBars() {
        val file = File(tempDir, "tone.mp3").apply { writeBytes(byteArrayOf(0)) }
        var visible by mutableStateOf(true)
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                if (visible) SharedMediaPlayerDialog(
                    SharedFile.newBuilder().setName(file.name).build(), file, video = false,
                    onDismiss = { visible = false }, onAction = {},
                )
            }
        }
        ui.waitForIdle()
        val parent = android.view.inspector.WindowInspector.getGlobalWindowViews().first().windowInsetsController!!
        val mask = android.view.WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS
        ui.runOnIdle { parent.setSystemBarsAppearance(mask, mask) }
        val parentStatus = ReflectionHelpers.callInstanceMethod<Boolean>(parent, "isRequestedVisible", ReflectionHelpers.ClassParameter.from(Int::class.javaPrimitiveType, 0))
        val parentNavigation = ReflectionHelpers.callInstanceMethod<Boolean>(parent, "isRequestedVisible", ReflectionHelpers.ClassParameter.from(Int::class.javaPrimitiveType, 1))
        val window = ShadowDialog.getLatestDialog().window!!
        assertFalse(ReflectionHelpers.callInstanceMethod<Boolean>(window.insetsController, "isRequestedVisible", ReflectionHelpers.ClassParameter.from(Int::class.javaPrimitiveType, 0)))
        assertFalse(ReflectionHelpers.callInstanceMethod<Boolean>(window.insetsController, "isRequestedVisible", ReflectionHelpers.ClassParameter.from(Int::class.javaPrimitiveType, 1)))
        ui.runOnIdle { visible = false }
        ui.waitForIdle()
        assertEquals(mask, parent.systemBarsAppearance and mask)
        assertEquals(parentStatus, ReflectionHelpers.callInstanceMethod<Boolean>(parent, "isRequestedVisible", ReflectionHelpers.ClassParameter.from(Int::class.javaPrimitiveType, 0)))
        assertEquals(parentNavigation, ReflectionHelpers.callInstanceMethod<Boolean>(parent, "isRequestedVisible", ReflectionHelpers.ClassParameter.from(Int::class.javaPrimitiveType, 1)))
    }

    @Test
    fun rotatingReappliesImmersiveWindowState() {
        val file = File(tempDir, "clip.mp4").apply { writeBytes(byteArrayOf(0)) }
        var orientation by mutableIntStateOf(android.content.res.Configuration.ORIENTATION_PORTRAIT)
        ui.setContent {
            val configuration = android.content.res.Configuration(androidx.compose.ui.platform.LocalConfiguration.current).apply {
                this.orientation = orientation
            }
            CompositionLocalProvider(androidx.compose.ui.platform.LocalConfiguration provides configuration) {
                RaTheme(ThemeMode.LIGHT) {
                    SharedMediaPlayerDialog(
                        SharedFile.newBuilder().setName(file.name).build(), file, video = true,
                        onDismiss = {}, onAction = {},
                    )
                }
            }
        }
        ui.waitForIdle()
        val window = ShadowDialog.getLatestDialog().window!!
        ui.runOnIdle {
            // Model the platform changing window state while rotating the existing dialog.
            androidx.core.view.WindowCompat.getInsetsController(window, window.decorView)
                .show(androidx.core.view.WindowInsetsCompat.Type.systemBars())
            orientation = android.content.res.Configuration.ORIENTATION_LANDSCAPE
        }
        ui.waitForIdle()
        val rotatedWindow = ShadowDialog.getLatestDialog().window!!
        assertTrue(window !== rotatedWindow)
        val requested: Int = ReflectionHelpers.getField(rotatedWindow.insetsController, "mRequestedVisibleTypes")
        assertEquals(0, requested and android.view.WindowInsets.Type.systemBars())
    }

    private fun checkSafeArea() {
        page(FakePlayerEngine(), "clip.mp4", video = true,
            safeInsets = androidx.compose.foundation.layout.WindowInsets(left = 30, top = 24, right = 10, bottom = 8))
        val root = ui.onNodeWithTag(SharedPlayerPageTag).fetchSemanticsNode().boundsInRoot
        val close = ui.onNodeWithContentDescription("关闭").fetchSemanticsNode().boundsInRoot
        val more = ui.onNodeWithContentDescription("更多").fetchSemanticsNode().boundsInRoot
        val elapsed = ui.onNodeWithText("0:00").fetchSemanticsNode().boundsInRoot
        assertEquals(root.left + 30 + 16, close.left, 0.5f)
        assertEquals(root.top + 24 + 12, close.top, 0.5f)
        assertEquals(root.right - 10 - 16, more.right, 0.5f)
        assertEquals(root.bottom - 8 - 16, elapsed.bottom, 0.5f)
        assertEquals(root.left + 30 + 16, elapsed.left, 0.5f)
        val topShade = ui.onNodeWithTag("player-top-shade").fetchSemanticsNode().boundsInRoot
        val bottomShade = ui.onNodeWithTag("player-bottom-shade").fetchSemanticsNode().boundsInRoot
        assertEquals(root.top, topShade.top, 0.5f)
        assertEquals(root.bottom, bottomShade.bottom, 0.5f)
    }

    @Test
    fun portraitControlsUseCutoutSafetyAndExactScreenMargins() = checkSafeArea()

    @Test
    @Config(qualifiers = "w844dp-h390dp-mdpi")
    fun landscapeControlsAvoidTheSideCutout() = checkSafeArea()

    @Test
    fun lightThemePlayerMenuUsesTheDarkMenuSurfaceTextDividerAndIcons() {
        page(FakePlayerEngine(), "clip.mp4", video = true)
        ui.onNodeWithContentDescription("更多").performClick()
        val label = ui.onNodeWithText("存到相册")
        val layouts = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        label.performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(layouts) }
        assertEquals(DarkPalette.ink, layouts.single().layoutInput.style.color)
        ui.runOnIdle {
            val view = android.view.inspector.WindowInspector.getGlobalWindowViews().first()
            val bitmap = android.graphics.Bitmap.createBitmap(view.width, view.height, android.graphics.Bitmap.Config.ARGB_8888)
            view.draw(android.graphics.Canvas(bitmap))
            val row = label.fetchSemanticsNode().boundsInRoot
            val menu = ui.onNodeWithTag("player-menu").fetchSemanticsNode().boundsInRoot
            assertEquals(SharedFileSpec.menuSurface(true).toArgb(), bitmap.getPixel(menu.center.x.toInt(), menu.top.toInt() + 5))
            val y = (row.center.y + 22).toInt()
            assertEquals(DarkPalette.sep.compositeOver(SharedFileSpec.menuSurface(true)).toArgb(), bitmap.getPixel(menu.center.x.toInt(), y))
            // The right-hand icon uses the same solid ink as the label.
            val ink = DarkPalette.ink.toArgb()
            assertTrue((view.width - 60 until view.width - 30).any { x ->
                (row.top.toInt() until row.bottom.toInt()).any { yy -> bitmap.getPixel(x, yy) == ink }
            })
            bitmap.recycle()
        }
    }

    @Test
    fun videoShadesHaveSpecifiedHeightsAndFollowControlVisibility() {
        ui.mainClock.autoAdvance = false
        val engine = FakePlayerEngine().apply { playing = true }
        page(engine, "bright.mp4", video = true)
        ui.onNodeWithTag("player-top-shade").assertHeightIsEqualTo(96.dp)
        ui.onNodeWithTag("player-bottom-shade").assertHeightIsEqualTo(140.dp)
        assertEquals(0.35f, SharedPlayerSpec.TopShadeColor.alpha, 0.005f)
        assertEquals(0.45f, SharedPlayerSpec.BottomShadeColor.alpha, 0.005f)
        ui.mainClock.advanceTimeBy(3_500)
        ui.onNodeWithTag("player-top-shade").assertDoesNotExist()
        ui.onNodeWithTag("player-bottom-shade").assertDoesNotExist()
        ui.runOnUiThread { engine.pause() }
        ui.mainClock.advanceTimeBy(300)
        ui.onNodeWithTag("player-top-shade").assertExists()
        ui.onNodeWithTag("player-bottom-shade").assertExists()
    }

    @Test
    fun videoControlsLeaveThreeSecondsAfterPlaybackStartsAndStayWhilePaused() {
        ui.mainClock.autoAdvance = false
        val engine = FakePlayerEngine().apply { playing = true }
        page(engine, "clip.mp4", video = true)
        ui.mainClock.advanceTimeBy(2_900)
        ui.onNodeWithContentDescription("暂停").assertExists()
        ui.onNodeWithContentDescription("关闭").assertExists()
        ui.mainClock.advanceTimeBy(600)
        ui.onNodeWithContentDescription("暂停").assertDoesNotExist()
        ui.onNodeWithContentDescription("关闭").assertDoesNotExist()
        ui.onNodeWithContentDescription("更多").assertDoesNotExist()

        // A tap brings them back, and they leave again 3 s after the last touch.
        ui.onNodeWithTag(SharedPlayerPageTag).performTouchInput { click(Offset(8f, 400f)) }
        ui.mainClock.advanceTimeBy(300)
        ui.onNodeWithContentDescription("暂停").assertExists()
        ui.mainClock.advanceTimeBy(2_500)
        ui.onNodeWithContentDescription("暂停").assertExists()
        ui.mainClock.advanceTimeBy(900)
        ui.onNodeWithContentDescription("暂停").assertDoesNotExist()

        // Paused, they stay however long it takes.
        ui.runOnUiThread { engine.pause() }
        ui.mainClock.advanceTimeBy(20_000)
        ui.onNodeWithContentDescription("播放").assertExists()
        ui.onNodeWithContentDescription("关闭").assertExists()
    }

    @Test
    fun touchingTheControlsRestartsTheThreeSeconds() {
        ui.mainClock.autoAdvance = false
        val engine = FakePlayerEngine().apply { playing = true }
        page(engine, "clip.mp4", video = true)
        ui.mainClock.advanceTimeBy(2_000)
        ui.onNode(SemanticsMatcher.keyIsDefined(SemanticsProperties.ProgressBarRangeInfo))
            .performSemanticsAction(SemanticsActions.SetProgress) { it(51_000f) }
        assertEquals(listOf(51_000), engine.seeks)
        ui.mainClock.advanceTimeBy(2_900)
        ui.onNodeWithContentDescription("暂停").assertExists()
        ui.mainClock.advanceTimeBy(600)
        ui.onNodeWithContentDescription("暂停").assertDoesNotExist()
    }

    @Test
    fun audioControlsNeverLeaveAndTheNameShowsUnderTheTile() {
        ui.mainClock.autoAdvance = false
        val engine = FakePlayerEngine(duration = 12_000, video = false).apply { playing = true }
        page(engine, "voice-memo.mp3", video = false)
        ui.mainClock.advanceTimeBy(30_000)
        ui.onNodeWithTag("player-top-shade").assertDoesNotExist()
        ui.onNodeWithTag("player-bottom-shade").assertDoesNotExist()
        ui.onNodeWithText("voice-memo.mp3").assertExists()
        ui.onNodeWithContentDescription("暂停").assertExists()
        ui.onNodeWithContentDescription("关闭").assertExists()
        // A tap on the page does not hide anything for audio.
        ui.onNodeWithTag(SharedPlayerPageTag).performTouchInput { click(Offset(8f, 400f)) }
        ui.mainClock.advanceTimeBy(1_000)
        ui.onNodeWithContentDescription("暂停").assertExists()
    }

    @Test
    fun theAppGoingToTheBackgroundPausesPlayback() {
        val engine = FakePlayerEngine().apply { playing = true }
        page(engine, "clip.mp4", video = true)
        ui.waitForIdle()
        assertEquals(0, engine.pauses)
        ui.runOnUiThread { owner.move(Lifecycle.State.STARTED) } // paused only: still on screen
        ui.waitForIdle()
        assertTrue(engine.playing)
        ui.runOnUiThread { owner.move(Lifecycle.State.CREATED) } // stopped: in the background
        ui.waitForIdle()
        assertFalse(engine.playing)
        assertEquals(1, engine.pauses)
        ui.onNodeWithContentDescription("播放").assertExists()
    }

    @Test
    fun playButtonTogglesAndIsLabelledByWhatItWillDo() {
        val engine = FakePlayerEngine()
        page(engine, "clip.mp4", video = true)
        ui.onNodeWithContentDescription("播放").performClick()
        ui.waitForIdle()
        assertTrue(engine.playing)
        ui.onNodeWithContentDescription("暂停").performClick()
        ui.waitForIdle()
        assertFalse(engine.playing)
        assertEquals(1, engine.plays)
        assertEquals(1, engine.pauses)
    }

    @Test
    fun scrubberIsASliderAndSeeksByTapAndDrag() {
        val engine = FakePlayerEngine().apply { position = 12_000 }
        page(engine, "clip.mp4", video = true)
        ui.waitForIdle()
        val slider = ui.onNode(SemanticsMatcher.keyIsDefined(SemanticsProperties.ProgressBarRangeInfo))
        val info = slider.fetchSemanticsNode().config[SemanticsProperties.ProgressBarRangeInfo]
        assertEquals(12_000f, info.current, 1f)
        assertEquals(102_000f, info.range.endInclusive, 1f)
        assertEquals("0:12 / 1:42", slider.fetchSemanticsNode().config[SemanticsProperties.StateDescription])
        ui.onNodeWithText("0:12").assertExists()
        ui.onNodeWithText("−1:30").assertExists()

        slider.performTouchInput { click(Offset(width * 0.5f, centerY)) }
        ui.waitForIdle()
        assertTrue("seek ${engine.seeks.last()}", kotlin.math.abs(engine.seeks.last() - 51_000) < 1_500)

        slider.performTouchInput { swipe(Offset(width * 0.25f, centerY), Offset(width * 0.75f, centerY), 300) }
        ui.waitForIdle()
        val drag = engine.seeks.last()
        assertTrue("seek $drag", drag in 50_000..90_000)
    }

    @Test
    fun anUnplayableFileShowsTheErrorStateWithOpenInAnotherApp() {
        val engine = FakePlayerEngine()
        page(engine, "clip.mp4", video = true)
        ui.runOnUiThread { engine.failed = true }
        ui.waitForIdle()
        ui.onNodeWithText("播放不了这个文件").assertExists()
        ui.onAllNodesWithContentDescription("播放").assertCountEquals(0)
        ui.onNodeWithText("用其他应用打开").performClick()
        assertEquals(listOf(SharedMenuAction.OPEN_WITH), actions)
        ui.onNodeWithContentDescription("关闭").performClick()
        assertEquals(1, closed)
    }

    @Test
    fun moreMenuOffersAlbumOnlyForVideo() {
        page(FakePlayerEngine(), "clip.mp4", video = true)
        ui.onNodeWithContentDescription("更多").performClick()
        ui.onNodeWithText("存到相册").assertExists()
        ui.onNodeWithText("存到「下载」").assertExists()
        ui.onNodeWithText("分享…").assertExists()
        ui.onNodeWithText("用其他应用打开").performClick()
        assertEquals(listOf(SharedMenuAction.OPEN_WITH), actions)
        ui.onAllNodesWithText("存到相册").assertCountEquals(0)
    }

    @Test
    fun moreMenuOfAudioHasNoAlbumEntry() {
        page(FakePlayerEngine(video = false), "tone.mp3", video = false)
        ui.onNodeWithContentDescription("更多").performClick()
        ui.onAllNodesWithText("存到相册").assertCountEquals(0)
        ui.onNodeWithText("存到「下载」").assertExists()
        ui.onNodeWithText("用其他应用打开").assertExists()
    }

    // ---- the real engine on the framework's MediaPlayer (shadowed: no codec, so no frames) ----

    private val context: Context get() = ApplicationProvider.getApplicationContext()

    private fun audioFile(name: String): File = File(tempDir, name).apply { writeBytes(ByteArray(64)) }

    @Test
    fun engineStartsByItselfHoldsAudioFocusOnlyWhilePlayingAndReleasesIt() {
        val file = audioFile("a.mp3")
        ShadowMediaPlayer.addMediaInfo(DataSource.toDataSource(file.absolutePath), ShadowMediaPlayer.MediaInfo(5_000, 0))
        val audio = context.getSystemService(AudioManager::class.java)
        val engine = MediaPlayerEngine(context, file, video = false)
        shadowOf(Looper.getMainLooper()).idle()
        assertFalse(engine.failed)
        assertEquals(5_000, engine.durationMs)
        assertTrue(engine.playing)
        assertNotNull(shadowOf(audio).lastAudioFocusRequest)

        engine.pause()
        assertFalse(engine.playing)
        assertNotNull(shadowOf(audio).lastAbandonedAudioFocusRequest)
        engine.release()
    }

    @Test
    fun engineStopsWhenAnotherAppTakesTheAudioFocus() {
        val file = audioFile("b.mp3")
        ShadowMediaPlayer.addMediaInfo(DataSource.toDataSource(file.absolutePath), ShadowMediaPlayer.MediaInfo(5_000, 0))
        val audio = context.getSystemService(AudioManager::class.java)
        val engine = MediaPlayerEngine(context, file, video = false)
        shadowOf(Looper.getMainLooper()).idle()
        assertTrue(engine.playing)
        val request = shadowOf(audio).lastAudioFocusRequest!!.audioFocusRequest
        val listener = android.media.AudioFocusRequest::class.java.getDeclaredMethod("getOnAudioFocusChangeListener")
            .apply { isAccessible = true }.invoke(request) as AudioManager.OnAudioFocusChangeListener
        listener.onAudioFocusChange(AudioManager.AUDIOFOCUS_LOSS)
        assertFalse(engine.playing)
        engine.release()
    }

    @Test
    fun enginePausedBeforeItIsReadyDoesNotStartLater() {
        val file = audioFile("c.mp3")
        ShadowMediaPlayer.addMediaInfo(DataSource.toDataSource(file.absolutePath), ShadowMediaPlayer.MediaInfo(5_000, 0))
        val engine = MediaPlayerEngine(context, file, video = false)
        engine.pause()
        shadowOf(Looper.getMainLooper()).idle()
        assertFalse(engine.playing)
        assertEquals(5_000, engine.durationMs)
        engine.release()
    }

    @Test
    @Config(shadows = [TrackPlayerShadow::class])
    fun videoWithOnlyADecodableAudioTrackFailsBeforePlayingOrTakingFocus() {
        val file = audioFile("unsupported.mkv")
        ShadowMediaPlayer.addMediaInfo(DataSource.toDataSource(file.absolutePath), ShadowMediaPlayer.MediaInfo(7_000, 0))
        ShadowMediaPlayer.setCreateListener { _, shadow -> (shadow as TrackPlayerShadow).videoTrack = false }
        val engine = MediaPlayerEngine(context, file, video = true)
        shadowOf(Looper.getMainLooper()).idle()
        assertTrue(engine.failed)
        assertFalse(engine.playing)
        assertNull(shadowOf(context.getSystemService(AudioManager::class.java)).lastAudioFocusRequest)
        engine.release()
    }

    @Test
    @Config(shadows = [TrackPlayerShadow::class])
    fun aVideoTrackMayPrepareBeforeItsSizeIsKnownButFailureToPlayItStopsAudio() {
        val file = audioFile("video.mkv")
        ShadowMediaPlayer.addMediaInfo(DataSource.toDataSource(file.absolutePath), ShadowMediaPlayer.MediaInfo(7_000, 0))
        lateinit var player: ShadowMediaPlayer
        ShadowMediaPlayer.setCreateListener { _, shadow -> player = shadow }
        val engine = MediaPlayerEngine(context, file, video = true)
        shadowOf(Looper.getMainLooper()).idle()
        assertFalse(engine.failed)
        assertTrue(engine.playing)
        assertEquals(0, engine.videoWidth) // Unknown size alone is not a failure.
        player.invokeInfoListener(MediaPlayer.MEDIA_INFO_VIDEO_NOT_PLAYING, 0)
        assertTrue(engine.failed)
        assertFalse(engine.playing)
        assertNotNull(shadowOf(context.getSystemService(AudioManager::class.java)).lastAbandonedAudioFocusRequest)
        engine.release()
    }

    @Test
    fun returningWindowFocusReattachesTheRetainedTextureSurfaceAndDestructionDetachesIt() {
        val engine = FakePlayerEngine()
        val view = PlayerVideoTextureView(context, engine)
        val texture = android.graphics.SurfaceTexture(0)
        view.onSurfaceTextureAvailable(texture, 640, 360)
        val surface = engine.surfaces.single()
        assertNotNull(surface)
        view.onWindowFocusChanged(false)
        assertEquals(1, engine.surfaces.size)
        view.onWindowFocusChanged(true)
        assertEquals(listOf(surface, surface), engine.surfaces)
        view.onSurfaceTextureDestroyed(texture)
        assertNull(engine.surfaces.last())
        texture.release()
    }

    @Test
    @Config(shadows = [TrackPlayerShadow::class])
    fun replacingAPausedVideoSurfaceRedrawsItsFrameWithoutPlayingOrLosingTheEndState() {
        val file = audioFile("surface.mp4")
        ShadowMediaPlayer.addMediaInfo(DataSource.toDataSource(file.absolutePath), ShadowMediaPlayer.MediaInfo(5_000, 0))
        lateinit var player: ShadowMediaPlayer
        ShadowMediaPlayer.setCreateListener { _, shadow -> player = shadow }
        val engine = MediaPlayerEngine(context, file, video = true)
        shadowOf(Looper.getMainLooper()).idle()
        player.setCurrentPosition(2_400)
        engine.pause()
        engine.attachSurface(null)
        assertEquals(-1, player.pendingSeek)
        val texture = android.graphics.SurfaceTexture(0)
        val surface = Surface(texture)
        try {
            engine.attachSurface(surface)
            assertEquals(2_400, player.pendingSeek)
            assertFalse(engine.playing)
            shadowOf(Looper.getMainLooper()).idle()
            assertEquals(2_400, engine.positionMs())

            player.invokeCompletionListener()
            assertEquals(5_000, engine.positionMs())
            engine.attachSurface(null)
            engine.attachSurface(surface)
            assertEquals(4_900, player.pendingSeek)
            assertEquals(5_000, engine.positionMs()) // The replay button still means start over.
            assertFalse(engine.playing)
        } finally {
            surface.release()
            texture.release()
            engine.release()
        }
    }

    @Test
    fun aFileTheFrameworkCannotPlayIsAFailureNotACrash() {
        val engine = MediaPlayerEngine(context, File(tempDir, "never-registered.mkv"), video = true)
        shadowOf(Looper.getMainLooper()).idle()
        assertTrue(engine.failed)
        assertFalse(engine.playing)
        engine.release()
    }
}
