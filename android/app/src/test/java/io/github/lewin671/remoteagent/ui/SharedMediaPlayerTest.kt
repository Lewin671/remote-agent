package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.Context
import android.media.AudioManager
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
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
import java.io.File
import java.nio.file.Files
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
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
    var plays = 0
    var pauses = 0
    override fun positionMs() = position
    override fun play() { plays++; playing = true }
    override fun pause() { pauses++; playing = false }
    override fun seekTo(ms: Int) { seeks += ms; position = ms }
    override fun attachSurface(surface: Surface?) = Unit
    override fun release() = Unit
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

    private fun page(engine: PlayerEngine, name: String, video: Boolean, theme: ThemeMode = ThemeMode.LIGHT) {
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
                    )
                }
            }
        }
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
    fun aFileTheFrameworkCannotPlayIsAFailureNotACrash() {
        val engine = MediaPlayerEngine(context, File(tempDir, "never-registered.mkv"), video = true)
        shadowOf(Looper.getMainLooper()).idle()
        assertTrue(engine.failed)
        assertFalse(engine.playing)
        engine.release()
    }
}
