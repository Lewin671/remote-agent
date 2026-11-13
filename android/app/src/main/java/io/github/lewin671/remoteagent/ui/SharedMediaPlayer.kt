package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.graphics.SurfaceTexture
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.media.MediaPlayer
import android.view.Surface
import android.view.TextureView
import androidx.activity.compose.BackHandler
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.displayCutout
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.systemBars
import androidx.compose.foundation.layout.union
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.ProgressBarRangeInfo
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.progressBarRangeInfo
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.setProgress
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.DialogWindowProvider
import androidx.core.view.WindowCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.formatMediaDuration
import io.github.lewin671.remoteagent.core.sanitizeSharedFileName
import io.github.lewin671.remoteagent.proto.SharedFile
import java.io.File
import kotlin.math.roundToInt
import kotlinx.coroutines.delay

internal const val SharedPlayerPageTag = "shared-player-page"

/** Sizes, colours and timings of the in-app player; the card's own are in [SharedFileSpec]. */
internal object SharedPlayerSpec {
    val TopShadeHeight = 96.dp
    val BottomShadeHeight = 140.dp
    val TopShadeColor = Color.Black.copy(alpha = 0.35f)
    val BottomShadeColor = Color.Black.copy(alpha = 0.45f)
    val SideMargin = 16.dp
    val TrackHeight = 3.dp
    val ThumbRest = 12.dp
    val ThumbDragging = 16.dp
    val ScrubberTouchHeight = 32.dp
    val PlayButton = 56.dp
    val PlayIcon = 28.dp
    val ControlsBottomPad = 12.dp
    val MenuTopPad = 8.dp + 44.dp + 6.dp
    val ArtworkToName = 20.dp
    val NameToScrubber = 28.dp
    val ScrubberToButton = 12.dp
    val TrackColor = Color.White.copy(alpha = 0.30f)
    val ProgressColor = Color.White
    val TimeColor = Color.White.copy(alpha = 0.70f)
    const val AutoHideMs = 3_000L
    const val PollMs = 250L
}

/**
 * What the player page needs from a media player. The page watches [playing], [failed] and the
 * sizes as state; the position is asked for. The real one is [MediaPlayerEngine]; tests use a fake,
 * so the page's behaviour can be checked without a codec.
 */
internal interface PlayerEngine {
    val playing: Boolean
    val failed: Boolean
    val durationMs: Int
    val videoWidth: Int
    val videoHeight: Int
    fun positionMs(): Int
    fun play()
    fun pause()
    fun seekTo(ms: Int)
    fun attachSurface(surface: Surface?)
    fun release()
}

/**
 * The framework's [MediaPlayer] (no media library in the project). It starts by itself once
 * prepared, holds audio focus only while playing, and pauses when another app takes the focus.
 */
internal class MediaPlayerEngine(
    context: Context,
    file: File,
    video: Boolean,
) : PlayerEngine {
    private val audioManager = context.getSystemService(AudioManager::class.java)
    private val attributes = AudioAttributes.Builder()
        .setUsage(AudioAttributes.USAGE_MEDIA)
        .setContentType(if (video) AudioAttributes.CONTENT_TYPE_MOVIE else AudioAttributes.CONTENT_TYPE_MUSIC)
        .build()
    private val focusRequest = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN)
        .setAudioAttributes(attributes)
        .setOnAudioFocusChangeListener { change ->
            if (change == AudioManager.AUDIOFOCUS_LOSS || change == AudioManager.AUDIOFOCUS_LOSS_TRANSIENT) pause()
        }
        .build()
    private var player: MediaPlayer? = MediaPlayer()
    private var prepared = false
    private var ended = false
    private var wantPlay = true
    private var focused = false

    override var playing by mutableStateOf(false)
        private set
    override var failed by mutableStateOf(false)
        private set
    override var durationMs by mutableIntStateOf(0)
        private set
    override var videoWidth by mutableIntStateOf(0)
        private set
    override var videoHeight by mutableIntStateOf(0)
        private set

    init {
        val mp = player!!
        mp.setAudioAttributes(attributes)
        mp.setOnPreparedListener {
            prepared = true
            durationMs = it.duration.coerceAtLeast(0)
            videoWidth = it.videoWidth
            videoHeight = it.videoHeight
            if (wantPlay) play()
        }
        mp.setOnVideoSizeChangedListener { _, w, h ->
            videoWidth = w
            videoHeight = h
        }
        mp.setOnCompletionListener {
            ended = true
            playing = false
            abandonFocus()
        }
        mp.setOnErrorListener { _, _, _ ->
            failed = true
            playing = false
            abandonFocus()
            true
        }
        try {
            mp.setDataSource(file.absolutePath)
            mp.prepareAsync()
        } catch (_: Exception) {
            failed = true
        }
    }

    override fun positionMs(): Int {
        val mp = player ?: return 0
        if (!prepared) return 0
        return if (ended) durationMs else runCatching { mp.currentPosition }.getOrDefault(0)
    }

    override fun play() {
        val mp = player ?: return
        wantPlay = true
        if (!prepared || failed) return
        if (ended) {
            mp.seekTo(0)
            ended = false
        }
        if (!requestFocus()) return
        runCatching { mp.start() }.onSuccess { playing = true }.onFailure { failed = true }
    }

    override fun pause() {
        wantPlay = false
        val mp = player ?: return
        if (playing) runCatching { mp.pause() }
        playing = false
        abandonFocus()
    }

    override fun seekTo(ms: Int) {
        val mp = player ?: return
        if (!prepared) return
        ended = false
        mp.seekTo(ms.toLong(), MediaPlayer.SEEK_CLOSEST)
    }

    override fun attachSurface(surface: Surface?) {
        runCatching { player?.setSurface(surface) }
    }

    override fun release() {
        abandonFocus()
        playing = false
        player?.let { runCatching { it.release() } }
        player = null
    }

    private fun requestFocus(): Boolean {
        if (focused) return true
        focused = audioManager.requestAudioFocus(focusRequest) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED
        return focused
    }

    private fun abandonFocus() {
        if (!focused) return
        focused = false
        audioManager.abandonAudioFocusRequest(focusRequest)
    }
}

/** The player as its own full-screen window, like the image viewer. */
@Composable
internal fun SharedMediaPlayerDialog(
    file: SharedFile,
    localFile: File,
    video: Boolean,
    onDismiss: () -> Unit,
    onAction: (SharedMenuAction) -> Unit,
) {
    val context = LocalContext.current
    val engine = remember(localFile) { MediaPlayerEngine(context.applicationContext, localFile, video) }
    DisposableEffect(engine) { onDispose { engine.release() } }
    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(
            usePlatformDefaultWidth = false,
            dismissOnBackPress = false,
            decorFitsSystemWindows = false,
        ),
    ) {
        val view = LocalView.current
        SideEffect {
            (view.parent as? DialogWindowProvider)?.window?.let { window ->
                // Draw the black page and video shades through the system-bar areas too.
                window.statusBarColor = android.graphics.Color.TRANSPARENT
                window.navigationBarColor = android.graphics.Color.TRANSPARENT
                if (android.os.Build.VERSION.SDK_INT >= 29) {
                    window.isStatusBarContrastEnforced = false
                    window.isNavigationBarContrastEnforced = false
                }
                WindowCompat.getInsetsController(window, view).apply {
                    isAppearanceLightStatusBars = false
                    isAppearanceLightNavigationBars = false
                }
            }
        }
        SharedMediaPlayerPage(
            engine = engine,
            fileName = sanitizeSharedFileName(file.name),
            video = video,
            onClose = onDismiss,
            onAction = onAction,
        )
    }
}

@Composable
private fun PlayerTextureView(engine: PlayerEngine, modifier: Modifier) {
    AndroidView(
        modifier = modifier,
        factory = { context ->
            TextureView(context).apply {
                surfaceTextureListener = object : TextureView.SurfaceTextureListener {
                    private var surface: Surface? = null

                    override fun onSurfaceTextureAvailable(st: SurfaceTexture, width: Int, height: Int) {
                        surface = Surface(st).also { engine.attachSurface(it) }
                    }

                    override fun onSurfaceTextureSizeChanged(st: SurfaceTexture, width: Int, height: Int) = Unit

                    override fun onSurfaceTextureDestroyed(st: SurfaceTexture): Boolean {
                        engine.attachSurface(null)
                        surface?.release()
                        surface = null
                        return true
                    }

                    override fun onSurfaceTextureUpdated(st: SurfaceTexture) = Unit
                }
            }
        },
    )
}

/**
 * The player page: black, the image viewer's close button, a "more" menu, the picture (video, aspect-fit)
 * or the audio tile with the name, a scrubber with both times, and a play/pause button.
 * [videoSurface] draws the video; tests put a still there.
 */
@Composable
internal fun SharedMediaPlayerPage(
    engine: PlayerEngine,
    fileName: String,
    video: Boolean,
    onClose: () -> Unit,
    onAction: (SharedMenuAction) -> Unit,
    videoSurface: @Composable (Modifier) -> Unit = { PlayerTextureView(engine, it) },
) {
    val copy = uiText()
    val playing = engine.playing
    val failed = engine.failed

    // Playback stops with the app going to the background.
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(lifecycle, engine) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_STOP) engine.pause()
        }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer) }
    }
    val view = LocalView.current
    DisposableEffect(playing) {
        view.keepScreenOn = playing
        onDispose { view.keepScreenOn = false }
    }

    var position by remember { mutableIntStateOf(0) }
    LaunchedEffect(engine, playing) {
        position = engine.positionMs()
        while (playing) {
            delay(SharedPlayerSpec.PollMs)
            position = engine.positionMs()
        }
    }

    var controlsVisible by remember { mutableStateOf(true) }
    var interaction by remember { mutableIntStateOf(0) }
    var scrubbing by remember { mutableStateOf(false) }
    var menuOpen by remember { mutableStateOf(false) }

    // Video controls leave 3 s after playback starts or after the last touch; paused, they stay.
    LaunchedEffect(playing) { if (playing) controlsVisible = true }
    LaunchedEffect(video, playing, controlsVisible, interaction, scrubbing, menuOpen) {
        if (video && playing && controlsVisible && !scrubbing && !menuOpen) {
            delay(SharedPlayerSpec.AutoHideMs)
            controlsVisible = false
        }
    }
    val chromeVisible = failed || !video || !playing || controlsVisible
    val chromeAlpha by animateFloatAsState(
        targetValue = if (chromeVisible) 1f else 0f,
        animationSpec = Motion.fadeInSpec(),
    )

    BackHandler { if (menuOpen) menuOpen = false else onClose() }

    fun togglePlay() {
        if (playing) engine.pause() else engine.play()
        interaction++
    }

    fun seek(ms: Int) {
        engine.seekTo(ms)
        position = ms
        interaction++
    }

    Box(
        Modifier
            .fillMaxSize()
            .testTag(SharedPlayerPageTag)
            .background(Color.Black)
            .pointerInput(video, failed) {
                detectTapGestures {
                    if (video && !failed) {
                        controlsVisible = !chromeVisible
                        interaction++
                    }
                }
            },
    ) {
        when {
            failed -> PlayerErrorState(onOpenWith = { onAction(SharedMenuAction.OPEN_WITH) }, modifier = Modifier.align(Alignment.Center))
            video -> Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                val ratio = if (engine.videoWidth > 0 && engine.videoHeight > 0) engine.videoWidth.toFloat() / engine.videoHeight else 16f / 9f
                videoSurface(Modifier.aspectRatio(ratio))
            }
            else -> PlayerAudioPage(
                fileName = fileName,
                playing = playing,
                positionMs = position,
                durationMs = engine.durationMs,
                onSeek = ::seek,
                onScrubbing = { scrubbing = it },
                onToggle = ::togglePlay,
                modifier = Modifier.align(Alignment.Center),
            )
        }

        if (video && !failed && chromeAlpha > 0f) {
            Box(Modifier.fillMaxSize().alpha(chromeAlpha)) {
                Box(
                    Modifier.fillMaxWidth().height(SharedPlayerSpec.TopShadeHeight)
                        .align(Alignment.TopCenter).testTag("player-top-shade")
                        .background(Brush.verticalGradient(listOf(SharedPlayerSpec.TopShadeColor, Color.Transparent))),
                )
                Box(
                    Modifier.fillMaxWidth().height(SharedPlayerSpec.BottomShadeHeight)
                        .align(Alignment.BottomCenter).testTag("player-bottom-shade")
                        .background(Brush.verticalGradient(listOf(Color.Transparent, SharedPlayerSpec.BottomShadeColor))),
                )
                PlayerPlayButton(playing, ::togglePlay, Modifier.align(Alignment.Center))
                PlayerScrubber(
                    positionMs = position,
                    durationMs = engine.durationMs,
                    onSeek = ::seek,
                    onScrubbing = { scrubbing = it },
                    modifier = Modifier
                        .align(Alignment.BottomCenter)
                        .windowInsetsPadding(WindowInsets.navigationBars.union(WindowInsets.displayCutout))
                        .padding(start = SharedPlayerSpec.SideMargin, end = SharedPlayerSpec.SideMargin, bottom = SharedPlayerSpec.ControlsBottomPad),
                )
            }
        }

        if (chromeAlpha > 0f) {
            Row(
                Modifier.fillMaxWidth()
                    .windowInsetsPadding(WindowInsets.systemBars.union(WindowInsets.displayCutout))
                    .padding(8.dp).alpha(chromeAlpha),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                GlassButton(Lucide.X, copy(UiMessages.commonClose()), onClose, dark = true)
                Spacer(Modifier.weight(1f))
                GlassButton(Lucide.Ellipsis, copy(UiMessages.commonAndroidMore()), { menuOpen = true; interaction++ }, dark = true)
            }
        }

        if (menuOpen) {
            Box(Modifier.fillMaxSize().pointerInput(Unit) { detectTapGestures { menuOpen = false; interaction++ } })
            SharedFileContextMenuCard(
                path = "",
                machineName = "",
                entries = playerMenuEntries(video),
                onAction = { action ->
                    menuOpen = false
                    onAction(action)
                },
                copy = copy,
                modifier = Modifier
                    .align(Alignment.TopEnd)
                    .windowInsetsPadding(WindowInsets.systemBars.union(WindowInsets.displayCutout))
                    .padding(top = SharedPlayerSpec.MenuTopPad, end = 8.dp),
            )
        }
    }
}

@Composable
private fun PlayerAudioPage(
    fileName: String,
    playing: Boolean,
    positionMs: Int,
    durationMs: Int,
    onSeek: (Int) -> Unit,
    onScrubbing: (Boolean) -> Unit,
    onToggle: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier.fillMaxWidth().padding(horizontal = SharedPlayerSpec.SideMargin),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        SharedFileAudioArtwork()
        Spacer(Modifier.height(SharedPlayerSpec.ArtworkToName))
        Text(
            text = fileName,
            style = T.s17b,
            color = Color.White,
            textAlign = TextAlign.Center,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
        )
        Spacer(Modifier.height(SharedPlayerSpec.NameToScrubber))
        PlayerScrubber(positionMs, durationMs, onSeek, onScrubbing)
        Spacer(Modifier.height(SharedPlayerSpec.ScrubberToButton))
        PlayerPlayButton(playing, onToggle)
    }
}

@Composable
private fun PlayerErrorState(onOpenWith: () -> Unit, modifier: Modifier = Modifier) {
    val copy = uiText()
    Column(modifier.padding(horizontal = 32.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        Text(
            copy(UiMessages.sharedFilePlayerError()),
            style = T.s15,
            color = SharedPlayerSpec.TimeColor,
            textAlign = TextAlign.Center,
        )
        Spacer(Modifier.height(16.dp))
        Box(
            Modifier
                .height(44.dp)
                .clip(Radii.Pill)
                .background(SharedFileSpec.controlFill(dark = true))
                .clickable(role = Role.Button, onClick = onOpenWith)
                .padding(horizontal = 20.dp),
            contentAlignment = Alignment.Center,
        ) {
            Text(copy(UiMessages.sharedFileMenuOpenWith()), style = T.s15b, color = Color.White, maxLines = 1)
        }
    }
}

@Composable
internal fun PlayerPlayButton(playing: Boolean, onToggle: () -> Unit, modifier: Modifier = Modifier) {
    val copy = uiText()
    val label = if (playing) copy(UiMessages.sharedFilePlayerPause()) else copy(UiMessages.sharedFilePlayerPlay())
    Box(
        modifier
            .size(SharedPlayerSpec.PlayButton)
            .glass(pal, CircleShape, dark = true)
            .clickable(role = Role.Button, onClick = onToggle)
            .semantics { contentDescription = label },
        contentAlignment = Alignment.Center,
    ) {
        Icon(
            imageVector = if (playing) SharedFileIcons.Pause else SharedFileIcons.PlayFilled,
            contentDescription = null,
            tint = Color.White,
            modifier = Modifier.size(SharedPlayerSpec.PlayIcon),
        )
    }
}

/** The minus sign (U+2212) before the remaining time: a symbol, not a word. */
private const val MINUS = "\u2212"

private val PlayerTimeStyle = T.s13.copy(fontFeatureSettings = "tnum")

/**
 * A thin track with a round thumb, the elapsed time under its left end and the remaining time under
 * its right. Tap or drag to seek (the seek happens when the finger lifts); a screen reader gets it as a slider.
 */
@Composable
internal fun PlayerScrubber(
    positionMs: Int,
    durationMs: Int,
    onSeek: (Int) -> Unit,
    onScrubbing: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
) {
    val dur = durationMs.coerceAtLeast(0)
    var dragFraction by remember { mutableStateOf<Float?>(null) }
    val fraction = dragFraction ?: if (dur > 0) (positionMs.toFloat() / dur).coerceIn(0f, 1f) else 0f
    val shownMs = (fraction * dur).roundToInt()
    val thumb by animateDpAsState(
        targetValue = if (dragFraction != null) SharedPlayerSpec.ThumbDragging else SharedPlayerSpec.ThumbRest,
        animationSpec = tween(Motion.FAST),
    )
    val elapsed = formatMediaDuration(shownMs.toLong())
    val total = formatMediaDuration(dur.toLong())

    Column(modifier.fillMaxWidth()) {
        Box(
            Modifier
                .fillMaxWidth()
                .height(SharedPlayerSpec.ScrubberTouchHeight)
                .semantics(mergeDescendants = true) {
                    stateDescription = "$elapsed / $total"
                    progressBarRangeInfo = ProgressBarRangeInfo(shownMs.toFloat(), 0f..dur.toFloat().coerceAtLeast(1f))
                    setProgress { value ->
                        if (dur <= 0) false else {
                            onSeek(value.roundToInt().coerceIn(0, dur))
                            true
                        }
                    }
                }
                .pointerInput(dur) {
                    detectTapGestures { o ->
                        if (dur > 0) onSeek((o.x / size.width * dur).roundToInt().coerceIn(0, dur))
                    }
                }
                .pointerInput(dur) {
                    detectHorizontalDragGestures(
                        onDragStart = { o ->
                            dragFraction = (o.x / size.width).coerceIn(0f, 1f)
                            onScrubbing(true)
                        },
                        onDragEnd = {
                            dragFraction?.let { if (dur > 0) onSeek((it * dur).roundToInt()) }
                            dragFraction = null
                            onScrubbing(false)
                        },
                        onDragCancel = {
                            dragFraction = null
                            onScrubbing(false)
                        },
                        onHorizontalDrag = { change, delta ->
                            change.consume()
                            dragFraction = ((dragFraction ?: 0f) + delta / size.width).coerceIn(0f, 1f)
                        },
                    )
                }
                .drawBehind {
                    val trackH = SharedPlayerSpec.TrackHeight.toPx()
                    val cy = size.height / 2f
                    val corner = CornerRadius(trackH / 2f)
                    drawRoundRect(SharedPlayerSpec.TrackColor, Offset(0f, cy - trackH / 2f), Size(size.width, trackH), corner)
                    if (fraction > 0f) {
                        drawRoundRect(SharedPlayerSpec.ProgressColor, Offset(0f, cy - trackH / 2f), Size(size.width * fraction, trackH), corner)
                    }
                    drawCircle(SharedPlayerSpec.ProgressColor, radius = thumb.toPx() / 2f, center = Offset(size.width * fraction, cy))
                },
        )
        Row(Modifier.fillMaxWidth()) {
            Text(elapsed, style = PlayerTimeStyle, color = SharedPlayerSpec.TimeColor, maxLines = 1)
            Spacer(Modifier.weight(1f))
            Text(MINUS + formatMediaDuration((dur - shownMs).toLong()), style = PlayerTimeStyle, color = SharedPlayerSpec.TimeColor, maxLines = 1)
        }
    }
}
