package io.github.lewin671.remoteagent.ui

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.graphics.BitmapFactory
import android.graphics.BlurMaskFilter
import android.graphics.ImageDecoder
import android.graphics.Paint
import android.graphics.RectF
import android.os.Build
import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.scaleIn
import androidx.compose.animation.scaleOut
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.displayCutout
import androidx.compose.foundation.layout.systemBars
import androidx.compose.foundation.layout.union
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.window.DialogWindowProvider
import androidx.compose.runtime.SideEffect
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.DisposableEffect
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.blur
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.positionOnScreen
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.toSize
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.drawIntoCanvas
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.boundsInRoot
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextMeasurer
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.LineHeightStyle
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.core.content.ContextCompat
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CardVisualState
import io.github.lewin671.remoteagent.core.SharedFileFailureKind
import io.github.lewin671.remoteagent.core.SharedFileKind
import io.github.lewin671.remoteagent.core.SharedFileTileStyle
import io.github.lewin671.remoteagent.core.SharedFileTransferState
import io.github.lewin671.remoteagent.core.SharedFileUiState
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.classifySharedFile
import io.github.lewin671.remoteagent.core.displaySharedPath
import io.github.lewin671.remoteagent.core.formatMediaDuration
import io.github.lewin671.remoteagent.core.formatSharedFileSize
import io.github.lewin671.remoteagent.core.formatSharedProgress
import io.github.lewin671.remoteagent.core.resolveSharedFileUiState
import io.github.lewin671.remoteagent.core.sanitizeSharedFileName
import io.github.lewin671.remoteagent.core.splitMiddleTruncateName
import io.github.lewin671.remoteagent.core.supportsAlbumSave
import io.github.lewin671.remoteagent.core.tileStyle
import io.github.lewin671.remoteagent.core.typeMessage
import io.github.lewin671.remoteagent.data.SharedFileManager
import io.github.lewin671.remoteagent.data.isSharedTextFile
import io.github.lewin671.remoteagent.data.readSharedTextFile
import io.github.lewin671.remoteagent.proto.SharedFile
import java.io.File
import kotlin.math.roundToInt
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

internal object SharedFileSpec {
    val CardWidth = 296.dp
    val CardRadius = 20.dp
    val TileSize = 44.dp
    val TileRadius = 11.dp
    val PillRadius = 15.dp
    val MenuWidth = 250.dp
    val MenuRadius = 14.dp
    val OverlayGap = 10.dp

    val LiftScale = 1.02f
    val MenuRiseDp = 8.dp
    val BlurRadius = 14.dp
    val OverlayMargin = 16.dp

    /** The dim over the whole window: 18% over a real blur, a stronger one where nothing is blurred. */
    fun scrim(dark: Boolean, blurred: Boolean): Color = when {
        blurred -> Color(0x2E000000)
        dark -> Color(0x80000000)
        else -> Color(0x52000000)
    }

    fun controlFill(dark: Boolean): Color = if (dark) Color(0x57787880) else Color(0x2E787880)
    fun liftSurface(dark: Boolean): Color = if (dark) Color(0xFF1C1C1E) else Color.White
    fun menuSurface(dark: Boolean): Color = if (dark) Color(0xFF28282A) else Color(0xFFF6F6F8)
    fun menuDestructiveGap(dark: Boolean): Color = if (dark) Color(0x38787880) else Color(0x1A787880)
    fun darkHairline(dark: Boolean): Color? = if (dark) Color(0x1AFFFFFF) else null
    fun floatShadowColor(dark: Boolean): Color = Color.Black.copy(alpha = if (dark) 0.60f else 0.22f)
}

internal const val SharedFileCardTag = "shared-file-card"
internal const val SharedFileOverlayTag = "shared-file-overlay"
internal const val SharedFileMenuTag = "shared-file-menu"
internal val SharedCardWidth = SharedFileSpec.CardWidth
internal val SharedTileSize = SharedFileSpec.TileSize
private val SharedSubtitleStyle = T.s13.copy(fontFeatureSettings = "tnum")

private fun Modifier.sharedFloatingShadow(
    cornerRadius: Dp,
    dark: Boolean,
    alpha: Float = 1f,
): Modifier = drawBehind {
    val radiusPx = cornerRadius.toPx()
    val blurPx = 28.dp.toPx()
    val offsetYPx = 14.dp.toPx()
    val base = SharedFileSpec.floatShadowColor(dark)
    val shadowColor = base.copy(alpha = base.alpha * alpha).toArgb()
    drawIntoCanvas { canvas ->
        val paint = Paint().apply {
            isAntiAlias = true
            color = shadowColor
            maskFilter = BlurMaskFilter(blurPx, BlurMaskFilter.Blur.NORMAL)
        }
        canvas.nativeCanvas.drawRoundRect(
            RectF(0f, offsetYPx, size.width, size.height + offsetYPx),
            radiusPx,
            radiusPx,
            paint,
        )
    }
}

internal data class SharedFileMenuRequest(
    val file: SharedFile,
    val machineName: String,
    /** Where the pressed card is on the screen, in pixels; the lifted card is drawn exactly there. */
    val cardBoundsOnScreen: Rect,
    val uiState: SharedFileUiState,
    val thumbnail: ImageBitmap?,
    /** Which card on the page was pressed: the same file can be on the page more than once. */
    val token: Any,
)

/** Where a downloaded file opens. */
internal enum class SharedFileOpenRoute { IMAGE_VIEWER, VIDEO_PLAYER, AUDIO_PLAYER, TEXT_VIEWER, SYSTEM_CHOOSER }

private val PlayerVideoExts = setOf("mp4", "mov", "m4v", "webm", "mkv")
private val PlayerAudioExts = setOf("mp3", "m4a", "wav", "aac", "flac", "ogg")

internal fun sharedFileOpenRoute(name: String): SharedFileOpenRoute {
    if (classifySharedFile(name) == SharedFileKind.IMAGE) return SharedFileOpenRoute.IMAGE_VIEWER
    if (isSharedTextFile(name)) return SharedFileOpenRoute.TEXT_VIEWER
    val ext = sanitizeSharedFileName(name).substringAfterLast('.', "").lowercase(java.util.Locale.ROOT)
    return when (ext) {
        in PlayerVideoExts -> SharedFileOpenRoute.VIDEO_PLAYER
        in PlayerAudioExts -> SharedFileOpenRoute.AUDIO_PLAYER
        else -> SharedFileOpenRoute.SYSTEM_CHOOSER
    }
}

internal data class SharedFileImagePreview(
    val file: SharedFile,
    val localFile: File,
)

internal data class SharedFilePlayerTarget(
    val file: SharedFile,
    val localFile: File,
    val video: Boolean,
)

internal data class SharedFileTextPreview(val file: SharedFile, val localFile: File, val text: String)

internal class SharedFileHostState(
    val manager: SharedFileManager,
    val sessionId: String,
    initialMachineName: String,
    initialMachineOnline: Boolean,
    initialSessionExited: Boolean,
) {
    private val autoOpenFiles = mutableSetOf<String>()

    fun startDownload(file: SharedFile) {
        autoOpenFiles += file.fileId
        manager.startOrResume(sessionId, file, userInitiated = true)
    }

    fun leavePage() { autoOpenFiles.clear() }

    fun consumeAutoOpen(fileId: String): Boolean = autoOpenFiles.remove(fileId)

    var machineName by mutableStateOf(initialMachineName)
    var machineOnline by mutableStateOf(initialMachineOnline)
    var sessionExited by mutableStateOf(initialSessionExited)
    var menuTarget by mutableStateOf<SharedFileMenuRequest?>(null)
    var imagePreview by mutableStateOf<SharedFileImagePreview?>(null)
    var pendingTextPreview by mutableStateOf<SharedFileImagePreview?>(null)
    var textPreview by mutableStateOf<SharedFileTextPreview?>(null)
    var playerTarget by mutableStateOf<SharedFilePlayerTarget?>(null)
    var unopenableTarget by mutableStateOf<SharedFileImagePreview?>(null)
    var pendingSaveAction by mutableStateOf<(() -> Unit)?>(null)
    var requestStoragePermission: (() -> Unit)? = null
}

internal val LocalSharedFileHost = compositionLocalOf<SharedFileHostState?> { null }

/**
 * Wraps the chat area so that completed downloads auto-open when on the same session screen,
 * long-press blurs the underlying chat, and in-app image previews or "no app" prompts work cleanly.
 */
@Composable
internal fun SharedFileHost(
    sessionId: String,
    machineName: String,
    machineOnline: Boolean,
    sessionExited: Boolean,
    modifier: Modifier = Modifier,
    manager: SharedFileManager = RaApp.instance.sharedFiles,
    content: @Composable () -> Unit,
) {
    val copy = uiText()
    val context = LocalContext.current
    val view = LocalView.current
    val scope = rememberCoroutineScope()
    val states by manager.store.states.collectAsState()

    val liveHost = remember(manager, sessionId) {
        SharedFileHostState(manager, sessionId, machineName, machineOnline, sessionExited)
    }
    liveHost.machineName = machineName
    liveHost.machineOnline = machineOnline
    liveHost.sessionExited = sessionExited

    val permLauncher = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val pending = liveHost.pendingSaveAction
        liveHost.pendingSaveAction = null
        if (granted) pending?.invoke()
    }
    liveHost.requestStoragePermission = {
        permLauncher.launch(Manifest.permission.WRITE_EXTERNAL_STORAGE)
    }

    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(liveHost, lifecycle) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_PAUSE) liveHost.leavePage()
        }
        lifecycle.addObserver(observer)
        onDispose {
            lifecycle.removeObserver(observer)
            liveHost.leavePage()
        }
    }

    LaunchedEffect(manager, sessionId) {
        manager.store.completions.collect { comp ->
            if (comp.sessionId != sessionId) return@collect
            Motion.tap(view)
            view.announceForAccessibility(copy(UiMessages.sharedFileA11YDownloadedAnnounce(name = comp.fileName)))
            if (comp.userInitiated && liveHost.consumeAutoOpen(comp.fileId)) {
                val sf = SharedFile.newBuilder()
                    .setFileId(comp.fileId)
                    .setName(comp.fileName)
                    .setSizeBytes(comp.localFile.length())
                    .build()
                openSharedFile(context, sf, comp.localFile, liveHost)
            }
        }
    }

    CompositionLocalProvider(LocalSharedFileHost provides liveHost) {
        Box(modifier) {
            content()

            liveHost.menuTarget?.let { target ->
                val tr = states["$sessionId:${target.file.fileId}"] ?: SharedFileTransferState.Idle
                val freshState = resolveSharedFileUiState(target.file, tr, machineOnline, sessionExited)
                if (freshState.visual == CardVisualState.CLEANED_UP) {
                    liveHost.menuTarget = null
                } else {
                    SharedFileContextMenuOverlay(
                        request = target.copy(uiState = freshState, machineName = machineName),
                        onDismiss = { liveHost.menuTarget = null },
                        onAction = { action ->
                            handleMenuAction(
                                action = action,
                                file = target.file,
                                localFile = freshState.localFile,
                                sessionId = sessionId,
                                manager = manager,
                                context = context,
                                host = liveHost,
                                scope = scope,
                                copy = copy,
                            )
                        },
                    )
                }
            }
        }
    }

    LaunchedEffect(liveHost.pendingTextPreview) {
        val pending = liveHost.pendingTextPreview ?: return@LaunchedEffect
        val text = withContext(Dispatchers.IO) { readSharedTextFile(pending.localFile) }
        if (text != null) {
            liveHost.textPreview = SharedFileTextPreview(pending.file, pending.localFile, text)
        } else if (!manager.openExternal(context, pending.localFile, pending.file.name)) {
            liveHost.unopenableTarget = pending
        }
        liveHost.pendingTextPreview = null
    }

    liveHost.textPreview?.let { preview ->
        SharedFileTextViewerDialog(
            fileName = preview.file.name,
            text = preview.text,
            onDismiss = { liveHost.textPreview = null },
            onAction = { action ->
                handleMenuAction(action, preview.file, preview.localFile, sessionId, manager, context, liveHost, scope, copy)
            },
        )
    }

    liveHost.imagePreview?.let { preview ->
        SharedFileImageViewerDialog(
            file = preview.file,
            localFile = preview.localFile,
            onDismiss = { liveHost.imagePreview = null },
            onSave = {
                runWithStoragePermission(context, liveHost) {
                    scope.launch {
                        val res = manager.saveToAlbum(preview.localFile, SharedFileKind.IMAGE, preview.file.name)
                        if (res.isSuccess) {
                            Toast.makeText(context, copy(UiMessages.sharedFileToastSavedToAlbum()), Toast.LENGTH_SHORT).show()
                        }
                    }
                }
            },
            onShare = { manager.shareFile(context, preview.localFile, preview.file.name) },
        )
    }

    liveHost.playerTarget?.let { target ->
        SharedMediaPlayerDialog(
            file = target.file,
            localFile = target.localFile,
            video = target.video,
            onDismiss = { liveHost.playerTarget = null },
            onAction = { action ->
                handleMenuAction(
                    action = action,
                    file = target.file,
                    localFile = target.localFile,
                    sessionId = sessionId,
                    manager = manager,
                    context = context,
                    host = liveHost,
                    scope = scope,
                    copy = copy,
                )
            },
        )
    }

    liveHost.unopenableTarget?.let { target ->
        Alert(
            title = target.file.name,
            text = copy(UiMessages.sharedFileOpenNoApp()),
            confirm = copy(UiMessages.sharedFileActionSaveToPhone()),
            onDismiss = { liveHost.unopenableTarget = null },
            onConfirm = {
                runWithStoragePermission(context, liveHost) {
                    scope.launch {
                        val res = manager.saveToDownloads(target.localFile, target.file.name)
                        if (res.isSuccess) {
                            Toast.makeText(context, copy(UiMessages.sharedFileToastSavedToDownloads()), Toast.LENGTH_SHORT).show()
                        }
                    }
                }
            },
        )
    }
}

internal enum class SharedMenuAction {
    DOWNLOAD,
    PAUSE,
    RESUME,
    CANCEL,
    SAVE_TO_ALBUM,
    SAVE_TO_DOWNLOADS,
    SHARE,
    OPEN_WITH,
    DELETE_LOCAL,
}

private fun runWithStoragePermission(
    context: Context,
    host: SharedFileHostState,
    action: () -> Unit,
) {
    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q &&
        ContextCompat.checkSelfPermission(context, Manifest.permission.WRITE_EXTERNAL_STORAGE) != PackageManager.PERMISSION_GRANTED
    ) {
        host.pendingSaveAction = action
        host.requestStoragePermission?.invoke()
    } else {
        action()
    }
}

private fun handleMenuAction(
    action: SharedMenuAction,
    file: SharedFile,
    localFile: File?,
    sessionId: String,
    manager: SharedFileManager,
    context: Context,
    host: SharedFileHostState,
    scope: kotlinx.coroutines.CoroutineScope,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String,
) {
    when (action) {
        SharedMenuAction.DOWNLOAD, SharedMenuAction.RESUME -> host.startDownload(file)
        SharedMenuAction.PAUSE -> manager.pause(sessionId, file)
        SharedMenuAction.CANCEL -> manager.cancel(sessionId, file)
        SharedMenuAction.SAVE_TO_ALBUM -> {
            val local = localFile ?: return
            val kind = classifySharedFile(file.name)
            runWithStoragePermission(context, host) {
                scope.launch {
                    val res = manager.saveToAlbum(local, kind, file.name)
                    if (res.isSuccess) {
                        Toast.makeText(context, copy(UiMessages.sharedFileToastSavedToAlbum()), Toast.LENGTH_SHORT).show()
                    }
                }
            }
        }
        SharedMenuAction.SAVE_TO_DOWNLOADS -> {
            val local = localFile ?: return
            runWithStoragePermission(context, host) {
                scope.launch {
                    val res = manager.saveToDownloads(local, file.name)
                    if (res.isSuccess) {
                        Toast.makeText(context, copy(UiMessages.sharedFileToastSavedToDownloads()), Toast.LENGTH_SHORT).show()
                    }
                }
            }
        }
        SharedMenuAction.SHARE -> {
            val local = localFile ?: return
            manager.shareFile(context, local, file.name)
        }
        SharedMenuAction.OPEN_WITH -> {
            val local = localFile ?: return
            if (!manager.openExternal(context, local, file.name)) {
                host.unopenableTarget = SharedFileImagePreview(file, local)
            }
        }
        SharedMenuAction.DELETE_LOCAL -> manager.deleteLocal(sessionId, file)
    }
}

internal fun openSharedFile(
    context: Context,
    file: SharedFile,
    localFile: File,
    host: SharedFileHostState?,
) {
    when (val route = sharedFileOpenRoute(file.name)) {
        SharedFileOpenRoute.TEXT_VIEWER -> host?.pendingTextPreview = SharedFileImagePreview(file, localFile)
        SharedFileOpenRoute.IMAGE_VIEWER -> host?.imagePreview = SharedFileImagePreview(file, localFile)
        SharedFileOpenRoute.VIDEO_PLAYER, SharedFileOpenRoute.AUDIO_PLAYER ->
            host?.playerTarget = SharedFilePlayerTarget(file, localFile, route == SharedFileOpenRoute.VIDEO_PLAYER)
        SharedFileOpenRoute.SYSTEM_CHOOSER -> {
            val opened = host?.manager?.openExternal(context, localFile, file.name) ?: false
            if (!opened) {
                host?.unopenableTarget = SharedFileImagePreview(file, localFile)
            }
        }
    }
}

/** In-chat container rendering one or more Agent-shared file cards stacked with 8dp gaps. */
@Composable
internal fun SharedFilesBlock(
    files: List<SharedFile>,
    sessionId: String,
    machineName: String,
    machineOnline: Boolean,
    sessionExited: Boolean,
    modifier: Modifier = Modifier,
) {
    val copy = uiText()
    val context = LocalContext.current
    val host = LocalSharedFileHost.current
    val manager = host?.manager ?: RaApp.instance.sharedFiles
    val transferMap by manager.store.states.collectAsState()

    Column(
        modifier = modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 4.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        for (file in files) {
            LaunchedEffect(sessionId, file.fileId) {
                withContext(Dispatchers.IO) {
                    manager.registerFile(sessionId, file)
                }
            }
            val transfer = transferMap["$sessionId:${file.fileId}"]
                ?: SharedFileTransferState.Idle
            val uiState = resolveSharedFileUiState(file, transfer, machineOnline, sessionExited)
            val kind = classifySharedFile(file.name)
            val thumbPx = with(LocalDensity.current) { SharedTileSize.roundToPx() }
            val thumb by produceState<ImageBitmap?>(null, uiState.localFile?.absolutePath, uiState.visual) {
                value = if (uiState.visual == CardVisualState.DOWNLOADED && uiState.localFile != null) {
                    manager.loadThumbnail(uiState.localFile!!, kind, thumbPx)
                } else {
                    null
                }
            }

            var cardCoords by remember { mutableStateOf<LayoutCoordinates?>(null) }
            val token = remember { Any() }
            // The pressed card is drawn lifted in the overlay: never show it twice.
            val hiddenBehindOverlay = host?.menuTarget?.token === token

            val handlePrimaryClick: () -> Unit = {
                when (uiState.visual) {
                    CardVisualState.AVAILABLE, CardVisualState.PAUSED, CardVisualState.FAILED ->
                        if (host != null) host.startDownload(file)
                        else manager.startOrResume(sessionId, file, userInitiated = true)
                    CardVisualState.DOWNLOADING -> manager.pause(sessionId, file)
                    CardVisualState.DOWNLOADED -> uiState.localFile?.let { local ->
                        openSharedFile(context, file, local, host)
                    }
                    CardVisualState.MACHINE_OFFLINE -> {
                        Toast.makeText(context, copy(UiMessages.sharedFileSubtitleOffline()), Toast.LENGTH_SHORT).show()
                    }
                    CardVisualState.CLEANED_UP -> {
                        Toast.makeText(context, copy(UiMessages.sharedFileToastCleaned()), Toast.LENGTH_SHORT).show()
                    }
                }
            }

            SharedFileCard(
                file = file,
                uiState = uiState,
                thumbnail = thumb,
                lifted = false,
                onClick = handlePrimaryClick,
                onButtonClick = handlePrimaryClick,
                onLongClick = {
                    val coords = cardCoords
                    if (uiState.visual != CardVisualState.CLEANED_UP && host != null && coords != null && coords.isAttached) {
                        host.menuTarget = SharedFileMenuRequest(
                            file = file,
                            machineName = machineName,
                            cardBoundsOnScreen = Rect(coords.positionOnScreen(), coords.size.toSize()),
                            uiState = uiState,
                            thumbnail = thumb,
                            token = token,
                        )
                    }
                },
                modifier = Modifier
                    .onGloballyPositioned { cardCoords = it }
                    .testTag(SharedFileCardTag)
                    .graphicsLayer { alpha = if (hiddenBehindOverlay) 0f else 1f },
            )
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
internal fun SharedFileCard(
    file: SharedFile,
    uiState: SharedFileUiState,
    thumbnail: ImageBitmap?,
    lifted: Boolean,
    onClick: () -> Unit,
    onButtonClick: () -> Unit,
    onLongClick: () -> Unit,
    modifier: Modifier = Modifier,
    shadowAlpha: Float = 1f,
) {
    val copy = uiText()
    val p = pal
    val kind = classifySharedFile(file.name)
    val a11yState = when (uiState.visual) {
        CardVisualState.AVAILABLE -> copy(UiMessages.sharedFileA11YStateAvailable())
        CardVisualState.DOWNLOADING -> copy(UiMessages.sharedFileA11YStateDownloading())
        CardVisualState.PAUSED -> copy(UiMessages.sharedFileA11YStatePaused())
        CardVisualState.DOWNLOADED -> copy(UiMessages.sharedFileA11YStateDownloaded())
        CardVisualState.FAILED -> sharedFileFailureSubtitle(uiState, copy)
        CardVisualState.MACHINE_OFFLINE -> copy(UiMessages.sharedFileSubtitleOffline())
        CardVisualState.CLEANED_UP -> copy(UiMessages.sharedFileSubtitleCleaned())
    }
    val cardDesc = copy(
        UiMessages.sharedFileA11YCard(
            name = file.name,
            kind = copy(kind.typeMessage()),
            size = formatSharedFileSize(file.sizeBytes),
            state = a11yState,
        ),
    )

    val bg = if (lifted) SharedFileSpec.liftSurface(p.dark) else p.bubble
    val hairlineColor = SharedFileSpec.darkHairline(p.dark)
    val floatShadowMod =
        if (lifted) {
            Modifier
                .sharedFloatingShadow(SharedFileSpec.CardRadius, p.dark, shadowAlpha)
                .then(
                    if (hairlineColor != null) {
                        Modifier.border(
                            width = 0.5.dp,
                            color = hairlineColor.copy(alpha = hairlineColor.alpha * shadowAlpha),
                            shape = RoundedCornerShape(SharedFileSpec.CardRadius),
                        )
                    } else {
                        Modifier
                    },
                )
        } else {
            Modifier
        }

    Row(
        modifier = modifier
            .widthIn(max = SharedFileSpec.CardWidth)
            .fillMaxWidth()
            .then(floatShadowMod)
            .clip(RoundedCornerShape(SharedFileSpec.CardRadius))
            .background(bg)
            .combinedClickable(
                onClick = onClick,
                onLongClick = onLongClick,
            )
            .semantics(mergeDescendants = false) {
                contentDescription = cardDesc
                role = Role.Button
            }
            .padding(PaddingValues(start = 10.dp, top = 10.dp, end = 12.dp, bottom = 10.dp)),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        SharedFileTile(
            kind = kind,
            visualState = uiState.visual,
            thumbnail = thumbnail,
        )

        Column(modifier = Modifier.weight(1f)) {
            MiddleEllipsizedFileName(
                name = file.name,
                color = when (uiState.visual) {
                    CardVisualState.MACHINE_OFFLINE -> p.ink2
                    CardVisualState.CLEANED_UP -> p.ink3
                    else -> p.ink
                },
            )
            Spacer(Modifier.height(2.dp))
            SharedFileSubtitle(
                kind = kind,
                sizeBytes = file.sizeBytes,
                uiState = uiState,
                copy = copy,
            )
        }

        if (uiState.visual != CardVisualState.CLEANED_UP) {
            SharedFileTrailingControl(
                uiState = uiState,
                onClick = onButtonClick,
                copy = copy,
            )
        }
    }
}

/** The audio tile of the card at the size the player shows it: same gradient and note, scaled. */
@Composable
internal fun SharedFileAudioArtwork(modifier: Modifier = Modifier) {
    SharedFileTile(
        kind = SharedFileKind.AUDIO,
        visualState = CardVisualState.DOWNLOADED,
        thumbnail = null,
        size = 96.dp,
        radius = 24.dp,
        modifier = modifier,
    )
}

@Composable
private fun SharedFileTile(
    kind: SharedFileKind,
    visualState: CardVisualState,
    thumbnail: ImageBitmap?,
    size: Dp = SharedFileSpec.TileSize,
    radius: Dp = SharedFileSpec.TileRadius,
    modifier: Modifier = Modifier,
) {
    val muted = visualState == CardVisualState.MACHINE_OFFLINE || visualState == CardVisualState.CLEANED_UP
    val (rawStart, rawEnd) = when (kind.tileStyle()) {
        SharedFileTileStyle.VIDEO -> Color(0xFF7D7AFF) to Color(0xFF5856D6)
        SharedFileTileStyle.IMAGE -> Color(0xFF5AC8FA) to Color(0xFF2F9BF0)
        SharedFileTileStyle.AUDIO -> Color(0xFFFFB45E) to Color(0xFFFF9500)
        SharedFileTileStyle.ARCHIVE_OR_OTHER -> Color(0xFFB0B0B8) to Color(0xFF8E8E93)
        SharedFileTileStyle.PDF_OR_DOC -> Color(0xFFFF8A80) to Color(0xFFFF453A)
    }
    val startColor = if (muted) toGrayscale(rawStart) else rawStart
    val endColor = if (muted) toGrayscale(rawEnd) else rawEnd

    val showThumb = visualState == CardVisualState.DOWNLOADED && thumbnail != null
    val thumbAlpha by animateFloatAsState(
        targetValue = if (showThumb) 1f else 0f,
        animationSpec = tween(Motion.BASE),
    )

    Box(
        modifier = modifier
            .size(size)
            .clip(RoundedCornerShape(radius))
            .alpha(if (muted) 0.55f else 1f)
            .drawBehind {
                drawRect(
                    Brush.linearGradient(
                        colors = listOf(startColor, endColor),
                        start = Offset(0.33f * this.size.width, 0f),
                        end = Offset(0.67f * this.size.width, this.size.height),
                    ),
                )
            },
        contentAlignment = Alignment.Center,
    ) {
        Icon(
            imageVector = when (kind) {
                SharedFileKind.VIDEO -> SharedFileIcons.Video
                SharedFileKind.IMAGE -> SharedFileIcons.Image
                SharedFileKind.AUDIO -> SharedFileIcons.Audio
                SharedFileKind.ARCHIVE -> SharedFileIcons.Zip
                SharedFileKind.PDF, SharedFileKind.DOC, SharedFileKind.OTHER -> SharedFileIcons.Doc
            },
            contentDescription = null,
            tint = Color.White,
            modifier = Modifier.size(size * (22f / 44f)),
        )

        if (thumbnail != null && thumbAlpha > 0f) {
            Image(
                bitmap = thumbnail,
                contentDescription = null,
                contentScale = ContentScale.Crop,
                modifier = Modifier.fillMaxSize().alpha(thumbAlpha),
            )
            if (kind == SharedFileKind.VIDEO) {
                Canvas(Modifier.fillMaxSize().alpha(thumbAlpha)) {
                    drawRect(
                        brush = Brush.radialGradient(
                            colors = listOf(Color.Black.copy(alpha = 0.28f), Color.Transparent),
                            center = center,
                            radius = this.size.minDimension * 0.60f,
                        ),
                    )
                }
                Icon(
                    imageVector = SharedFileIcons.PlayFilled,
                    contentDescription = null,
                    tint = Color.White,
                    modifier = Modifier.size(16.dp).alpha(thumbAlpha),
                )
            }
        }
    }
}

private fun toGrayscale(color: Color): Color {
    val l = (0.299f * color.red + 0.587f * color.green + 0.114f * color.blue).coerceIn(0f, 1f)
    return Color(red = l, green = l, blue = l, alpha = color.alpha)
}

/** Large system fonts give the name a second line: one line up to this scale. */
internal const val SharedFileNameOneLineMaxFontScale = 1.15f

internal fun sharedFileNameMaxLines(fontScale: Float): Int =
    if (fontScale <= SharedFileNameOneLineMaxFontScale) 1 else 2

@Composable
private fun MiddleEllipsizedFileName(
    name: String,
    color: Color,
) {
    val style = T.s15b.copy(
        lineHeight = 20.sp,
        letterSpacing = (-0.1).sp,
    )
    val maxLines = sharedFileNameMaxLines(LocalDensity.current.fontScale)
    val textMeasurer = rememberTextMeasurer()
    BoxWithConstraints(modifier = Modifier.fillMaxWidth()) {
        val maxWidthPx = constraints.maxWidth
        val display = remember(name, style, maxWidthPx, maxLines) {
            fitMiddleEllipsizedName(name, style, maxWidthPx, maxLines, textMeasurer)
        }
        Text(
            text = display,
            style = style,
            color = color,
            maxLines = maxLines,
            softWrap = maxLines > 1,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

internal fun fitMiddleEllipsizedName(
    name: String,
    style: TextStyle,
    maxWidthPx: Int,
    maxLines: Int,
    textMeasurer: TextMeasurer,
): String {
    if (name.isEmpty() || maxWidthPx <= 0) return name

    fun fits(text: String): Boolean {
        val m = textMeasurer.measure(
            text = text,
            style = style,
            maxLines = maxLines,
            softWrap = maxLines > 1,
            constraints = if (maxLines > 1) Constraints(maxWidth = maxWidthPx) else Constraints(),
        )
        return !m.hasVisualOverflow && m.size.width <= maxWidthPx
    }

    if (fits(name)) return name

    val (base, suffix) = splitMiddleTruncateName(name)
    // Both the fixed suffix and the searched prefix must end on character boundaries.
    val splitPair = base.lastOrNull()?.let(Character::isHighSurrogate) == true &&
        suffix.firstOrNull()?.let(Character::isLowSurrogate) == true
    val head = if (splitPair) base.dropLast(1) else base
    val tail = if (splitPair) base.last() + suffix else suffix
    if (tail.isEmpty() || head.isEmpty()) return name

    var lo = 0
    var hi = head.length
    var best = "…$tail"
    while (lo <= hi) {
        val midpoint = (lo + hi) ushr 1
        var mid = midpoint
        if (mid > 0 && mid < head.length && Character.isHighSurrogate(head[mid - 1])) {
            mid--
        }
        val candidate = "${head.substring(0, mid)}…$tail"
        if (fits(candidate)) {
            best = candidate
            lo = midpoint + 1
        } else {
            hi = midpoint - 1
        }
    }
    return best
}

@Composable
private fun SharedFileSubtitle(
    kind: SharedFileKind,
    sizeBytes: Long,
    uiState: SharedFileUiState,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String,
) {
    val p = pal
    val text = when (uiState.visual) {
        CardVisualState.AVAILABLE ->
            copy(UiMessages.sharedFileSubtitleKindSize(kind = copy(kind.typeMessage()), size = formatSharedFileSize(sizeBytes)))
        CardVisualState.DOWNLOADING -> {
            val (dl, total) = formatSharedProgress(uiState.downloadedBytes, uiState.totalBytes)
            val eta = uiState.etaSeconds
            if (eta != null) {
                val etaStr = if (eta >= 60L) {
                    copy(UiMessages.sharedFileEtaMinutes(((eta + 30L) / 60L).toInt()))
                } else {
                    copy(UiMessages.sharedFileEtaSeconds(eta.coerceAtLeast(1L).toInt()))
                }
                copy(UiMessages.sharedFileSubtitleDownloadingEta(downloaded = dl, total = total, eta = etaStr))
            } else {
                copy(UiMessages.sharedFileSubtitleDownloading(downloaded = dl, total = total))
            }
        }
        CardVisualState.PAUSED -> {
            val (dl, total) = formatSharedProgress(uiState.downloadedBytes, uiState.totalBytes)
            copy(UiMessages.sharedFileSubtitlePaused(downloaded = dl, total = total))
        }
        CardVisualState.DOWNLOADED -> {
            val dur = uiState.durationMs
            if ((kind == SharedFileKind.VIDEO || kind == SharedFileKind.AUDIO) && dur != null) {
                copy(UiMessages.sharedFileSubtitleMediaSize(duration = formatMediaDuration(dur), size = formatSharedFileSize(sizeBytes)))
            } else {
                copy(UiMessages.sharedFileSubtitleKindSize(kind = copy(kind.typeMessage()), size = formatSharedFileSize(sizeBytes)))
            }
        }
        CardVisualState.FAILED -> sharedFileFailureSubtitle(uiState, copy)
        CardVisualState.MACHINE_OFFLINE -> copy(UiMessages.sharedFileSubtitleOffline())
        CardVisualState.CLEANED_UP -> copy(UiMessages.sharedFileSubtitleCleaned())
    }
    val color = when (uiState.visual) {
        CardVisualState.FAILED -> p.red
        CardVisualState.CLEANED_UP -> p.ink3
        else -> p.ink2
    }
    // Never an ellipsis: a long line wraps to a second one and the card grows.
    Text(
        text = text,
        style = SharedSubtitleStyle,
        color = color,
        maxLines = 2,
        overflow = TextOverflow.Clip,
    )
}

internal fun sharedFileFailureSubtitle(
    uiState: SharedFileUiState,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String,
): String {
    val reason = when (uiState.failureKind) {
        SharedFileFailureKind.CHECKSUM_MISMATCH -> copy(UiMessages.sharedFileReasonChecksumMismatch())
        SharedFileFailureKind.DISK_FULL -> copy(UiMessages.sharedFileReasonDiskFull())
        SharedFileFailureKind.DISCONNECTED, null -> copy(UiMessages.sharedFileReasonDisconnected())
        SharedFileFailureKind.OTHER -> uiState.failureDetail.ifBlank { copy(UiMessages.sharedFileReasonDisconnected()) }
    }
    return copy(UiMessages.sharedFileSubtitleFailed(reason = reason))
}

@Composable
private fun SharedFileTrailingControl(
    uiState: SharedFileUiState,
    onClick: () -> Unit,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String,
) {
    val p = pal
    val fill2 = SharedFileSpec.controlFill(p.dark)
    val blue = p.blue

    val buttonDesc = when (uiState.visual) {
        CardVisualState.AVAILABLE, CardVisualState.MACHINE_OFFLINE -> copy(UiMessages.sharedFileActionDownload())
        CardVisualState.DOWNLOADING -> copy(UiMessages.sharedFileActionPause())
        CardVisualState.PAUSED -> copy(UiMessages.sharedFileActionResume())
        CardVisualState.DOWNLOADED -> copy(UiMessages.sharedFileActionOpen())
        CardVisualState.FAILED -> copy(UiMessages.sharedFileActionRetry())
        CardVisualState.CLEANED_UP -> ""
    }

    AnimatedContent(
        targetState = uiState.visual,
        transitionSpec = {
            (fadeIn(tween(Motion.BASE)) + scaleIn(tween(Motion.BASE), initialScale = 0.9f)) togetherWith
                (fadeOut(tween(Motion.FAST)) + scaleOut(tween(Motion.FAST), targetScale = 0.9f))
        },
        contentAlignment = Alignment.Center,
    ) { state ->
        when (state) {
            CardVisualState.AVAILABLE, CardVisualState.MACHINE_OFFLINE -> {
                val iconTint = if (state == CardVisualState.MACHINE_OFFLINE) p.ink3 else blue
                TouchTargetBox(
                    visualWidth = 30.dp,
                    description = buttonDesc,
                    onClick = onClick,
                ) {
                    Box(
                        modifier = Modifier.size(30.dp).clip(CircleShape).background(fill2),
                        contentAlignment = Alignment.Center,
                    ) {
                        Icon(
                            imageVector = SharedFileIcons.Down,
                            contentDescription = null,
                            tint = iconTint,
                            modifier = Modifier.size(15.dp),
                        )
                    }
                }
            }
            CardVisualState.DOWNLOADING, CardVisualState.PAUSED -> {
                val targetFraction =
                    if (uiState.totalBytes > 0L) {
                        (uiState.downloadedBytes.toFloat() / uiState.totalBytes.toFloat()).coerceIn(0.03f, 1f)
                    } else 0.05f
                val animFraction by animateFloatAsState(
                    targetValue = targetFraction,
                    animationSpec = tween(180, easing = LinearEasing),
                )
                TouchTargetBox(
                    visualWidth = 30.dp,
                    description = buttonDesc,
                    onClick = onClick,
                ) {
                    Box(modifier = Modifier.size(30.dp), contentAlignment = Alignment.Center) {
                        Canvas(Modifier.fillMaxSize()) {
                            val strokePx = 2.6.dp.toPx()
                            val inset = 2.dp.toPx()
                            val arcSize = androidx.compose.ui.geometry.Size(
                                size.width - inset * 2,
                                size.height - inset * 2,
                            )
                            val tl = Offset(inset, inset)
                            drawArc(
                                color = fill2,
                                startAngle = 0f,
                                sweepAngle = 360f,
                                useCenter = false,
                                topLeft = tl,
                                size = arcSize,
                                style = Stroke(width = strokePx),
                            )
                            drawArc(
                                color = blue,
                                startAngle = -90f,
                                sweepAngle = 360f * animFraction,
                                useCenter = false,
                                topLeft = tl,
                                size = arcSize,
                                style = Stroke(width = strokePx, cap = StrokeCap.Round),
                            )
                        }
                        if (state == CardVisualState.DOWNLOADING) {
                            Box(
                                modifier = Modifier
                                    .size(9.dp)
                                    .clip(RoundedCornerShape(2.5.dp))
                                    .background(blue),
                            )
                        } else {
                            Icon(
                                imageVector = SharedFileIcons.Down,
                                contentDescription = null,
                                tint = blue,
                                modifier = Modifier.size(13.dp),
                            )
                        }
                    }
                }
            }
            CardVisualState.DOWNLOADED, CardVisualState.FAILED -> {
                val label =
                    if (state == CardVisualState.DOWNLOADED) copy(UiMessages.sharedFileActionOpen())
                    else copy(UiMessages.sharedFileActionRetry())
                Box(
                    modifier = Modifier
                        .height(30.dp)
                        .clip(RoundedCornerShape(SharedFileSpec.PillRadius))
                        .background(fill2)
                        .clickable(onClick = onClick)
                        .semantics {
                            contentDescription = buttonDesc
                            role = Role.Button
                        }
                        .padding(horizontal = 14.dp),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        text = label,
                        style = T.s15b.copy(lineHeight = 20.sp),
                        color = blue,
                        maxLines = 1,
                    )
                }
            }
            CardVisualState.CLEANED_UP -> Unit
        }
    }
}

/**
 * Preserves the exact visual width of a 30dp trailing circle while guaranteeing a >= 44dp vertical
 * and horizontal accessibility hit area within the card.
 */
@Composable
private fun TouchTargetBox(
    visualWidth: androidx.compose.ui.unit.Dp,
    description: String,
    onClick: () -> Unit,
    content: @Composable () -> Unit,
) {
    Box(
        modifier = Modifier
            .size(visualWidth, 44.dp)
            .clickable(
                interactionSource = remember { MutableInteractionSource() },
                indication = null,
                onClick = onClick,
            )
            .semantics {
                contentDescription = description
                role = Role.Button
            },
        contentAlignment = Alignment.Center,
    ) {
        content()
    }
}

/**
 * The long-press menu: its own window over the whole screen (top bar, tabs and composer included),
 * so the dim and the blur reach everywhere. The lifted card is drawn exactly where the pressed card
 * is; if the menu would not fit under it, both move up together by just enough.
 */
@Composable
internal fun SharedFileContextMenuOverlay(
    request: SharedFileMenuRequest,
    onDismiss: () -> Unit,
    onAction: (SharedMenuAction) -> Unit,
) {
    val copy = uiText()
    val p = pal
    val density = LocalDensity.current
    val context = LocalContext.current
    val progress = remember { Animatable(0f) }
    var closing by remember { mutableStateOf(false) }
    // What was shown when it started closing: the exit must not change with the file's state.
    var frozen by remember { mutableStateOf<SharedFileMenuRequest?>(null) }
    val shown = frozen ?: request
    val scope = rememberCoroutineScope()
    fun close() {
        if (closing) return
        closing = true
        frozen = request
        scope.launch {
            progress.animateTo(0f, tween(Motion.BASE, easing = Motion.Accelerate))
            onDismiss()
        }
    }
    LaunchedEffect(Unit) { progress.animateTo(1f, tween(Motion.BASE, easing = Motion.Decelerate)) }

    val kind = classifySharedFile(shown.file.name)
    val items = remember(shown.uiState.visual, kind) { buildMenuEntries(shown.uiState.visual, kind) }
    val blurred = remember {
        Build.VERSION.SDK_INT >= Build.VERSION_CODES.S &&
            context.getSystemService(android.view.WindowManager::class.java)?.isCrossWindowBlurEnabled == true
    }
    val reduced = Motion.reduced
    val insets = WindowInsets.systemBars.union(WindowInsets.displayCutout)
    var rootOnScreen by remember { mutableStateOf(androidx.compose.ui.geometry.Offset.Zero) }

    val parentView = LocalView.current
    Dialog(
        onDismissRequest = { close() },
        properties = DialogProperties(
            dismissOnBackPress = true,
            dismissOnClickOutside = false,
            usePlatformDefaultWidth = false,
            decorFitsSystemWindows = false,
        ),
    ) {
        val view = LocalView.current
        val blurPx = with(density) { SharedFileSpec.BlurRadius.toPx() }
        val windowProgress = progress.value
        SideEffect {
            (view.parent as? DialogWindowProvider)?.window?.let { window ->
                // Only our animated scrim dims the page, including its system-bar regions.
                window.clearFlags(android.view.WindowManager.LayoutParams.FLAG_DIM_BEHIND)
                window.statusBarColor = android.graphics.Color.TRANSPARENT
                window.navigationBarColor = android.graphics.Color.TRANSPARENT
                if (Build.VERSION.SDK_INT >= 29) {
                    window.isStatusBarContrastEnforced = false
                    window.isNavigationBarContrastEnforced = false
                }
                val appearance = if (Build.VERSION.SDK_INT >= 30) {
                    parentView.windowInsetsController?.systemBarsAppearance ?: 0
                } else 0
                androidx.core.view.WindowCompat.getInsetsController(window, view).apply {
                    isAppearanceLightStatusBars = if (Build.VERSION.SDK_INT >= 30) {
                        appearance and android.view.WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS != 0
                    } else parentView.rootView.systemUiVisibility and android.view.View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR != 0
                    isAppearanceLightNavigationBars = if (Build.VERSION.SDK_INT >= 30) {
                        appearance and android.view.WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS != 0
                    } else parentView.rootView.systemUiVisibility and android.view.View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR != 0
                }
                if (blurred) {
                    window.addFlags(android.view.WindowManager.LayoutParams.FLAG_BLUR_BEHIND)
                    val attrs = window.attributes
                    attrs.blurBehindRadius = (blurPx * windowProgress).roundToInt()
                    window.attributes = attrs
                }
            }
        }

        val scrim = SharedFileSpec.scrim(p.dark, blurred)
        Layout(
            content = {
                SharedFileCard(
                    file = shown.file,
                    uiState = shown.uiState,
                    thumbnail = shown.thumbnail,
                    lifted = true,
                    onClick = { close() },
                    onButtonClick = { close() },
                    onLongClick = {},
                    modifier = Modifier.graphicsLayer {
                        val scale = if (reduced) 1f else 1f + (SharedFileSpec.LiftScale - 1f) * progress.value
                        scaleX = scale
                        scaleY = scale
                    },
                    shadowAlpha = progress.value,
                )
                SharedFileContextMenuCard(
                    path = displaySharedPath(shown.file.path, shown.file.name),
                    machineName = shown.machineName,
                    entries = items,
                    onAction = { action ->
                        onAction(action)
                        close()
                    },
                    copy = copy,
                    modifier = Modifier.testTag(SharedFileMenuTag).graphicsLayer {
                        alpha = progress.value
                        translationY = if (reduced) 0f else (1f - progress.value) * SharedFileSpec.MenuRiseDp.toPx()
                    },
                )
            },
            modifier = Modifier
                .fillMaxSize()
                .testTag(SharedFileOverlayTag)
                .onGloballyPositioned { rootOnScreen = it.positionOnScreen() }
                .background(scrim.copy(alpha = scrim.alpha * progress.value))
                .pointerInput(Unit) { detectTapGestures { close() } },
        ) { measurables, constraints ->
            val card = shown.cardBoundsOnScreen
            val cardW = card.width.roundToInt().coerceIn(0, constraints.maxWidth)
            val cardP = measurables[0].measure(Constraints.fixedWidth(cardW))
            val menuP = measurables[1].measure(Constraints(maxWidth = constraints.maxWidth))
            val gap = SharedFileSpec.OverlayGap.roundToPx()
            val margin = SharedFileSpec.OverlayMargin.roundToPx()
            val total = cardP.height + gap + menuP.height
            val top = insets.getTop(this) + margin
            val bottom = constraints.maxHeight - insets.getBottom(this) - margin
            // Where the card is, in this window.
            val x = (card.left - rootOnScreen.x).roundToInt()
            var y = (card.top - rootOnScreen.y).roundToInt()
            if (y + total > bottom) y = bottom - total
            if (y < top) y = top
            val menuX = minOf(x, constraints.maxWidth - menuP.width - margin).coerceAtLeast(0)
            layout(constraints.maxWidth, constraints.maxHeight) {
                cardP.place(x, y)
                menuP.place(menuX, y + cardP.height + gap)
            }
        }
    }
}

internal data class MenuEntry(
    val action: SharedMenuAction,
    val label: @Composable () -> String,
    val icon: ImageVector,
    val destructive: Boolean = false,
    val isDestructiveGap: Boolean = false,
)

/** What the player's ⋯ menu offers: the same actions as a downloaded card's menu, minus delete. */
internal fun playerMenuEntries(video: Boolean): List<MenuEntry> = buildList {
    if (video) add(MenuEntry(SharedMenuAction.SAVE_TO_ALBUM, { uiText(UiMessages.sharedFileMenuSaveToAlbum()) }, SharedFileIcons.Image))
    add(MenuEntry(SharedMenuAction.SAVE_TO_DOWNLOADS, { uiText(UiMessages.sharedFileMenuSaveToDownloads()) }, SharedFileIcons.Save))
    add(MenuEntry(SharedMenuAction.SHARE, { uiText(UiMessages.sharedFileMenuShare()) }, SharedFileIcons.Share))
    add(MenuEntry(SharedMenuAction.OPEN_WITH, { uiText(UiMessages.sharedFileMenuOpenWith()) }, SharedFileIcons.OpenWith))
}

private fun buildMenuEntries(
    visual: CardVisualState,
    kind: SharedFileKind,
): List<MenuEntry> =
    when (visual) {
        CardVisualState.AVAILABLE, CardVisualState.FAILED -> listOf(
            MenuEntry(SharedMenuAction.DOWNLOAD, { uiText(UiMessages.sharedFileActionDownload()) }, SharedFileIcons.Down),
        )
        CardVisualState.DOWNLOADING -> listOf(
            MenuEntry(SharedMenuAction.PAUSE, { uiText(UiMessages.sharedFileActionPause()) }, SharedFileIcons.Pause),
            MenuEntry(SharedMenuAction.CANCEL, { uiText(UiMessages.sharedFileActionCancelDownload()) }, SharedFileIcons.Close, destructive = true, isDestructiveGap = true),
        )
        CardVisualState.PAUSED -> listOf(
            MenuEntry(SharedMenuAction.RESUME, { uiText(UiMessages.sharedFileActionResume()) }, SharedFileIcons.Down),
            MenuEntry(SharedMenuAction.CANCEL, { uiText(UiMessages.sharedFileActionCancelDownload()) }, SharedFileIcons.Close, destructive = true, isDestructiveGap = true),
        )
        CardVisualState.DOWNLOADED -> buildList {
            if (kind.supportsAlbumSave()) {
                add(MenuEntry(SharedMenuAction.SAVE_TO_ALBUM, { uiText(UiMessages.sharedFileMenuSaveToAlbum()) }, SharedFileIcons.Image))
            }
            add(MenuEntry(SharedMenuAction.SAVE_TO_DOWNLOADS, { uiText(UiMessages.sharedFileMenuSaveToDownloads()) }, SharedFileIcons.Save))
            add(MenuEntry(SharedMenuAction.SHARE, { uiText(UiMessages.sharedFileMenuShare()) }, SharedFileIcons.Share))
            add(MenuEntry(SharedMenuAction.OPEN_WITH, { uiText(UiMessages.sharedFileMenuOpenWith()) }, SharedFileIcons.OpenWith))
            add(MenuEntry(SharedMenuAction.DELETE_LOCAL, { uiText(UiMessages.sharedFileMenuDeleteLocal()) }, SharedFileIcons.Trash, destructive = true, isDestructiveGap = true))
        }
        CardVisualState.MACHINE_OFFLINE, CardVisualState.CLEANED_UP -> emptyList()
    }

@Composable
internal fun SharedFileContextMenuCard(
    path: String,
    machineName: String,
    entries: List<MenuEntry>,
    onAction: (SharedMenuAction) -> Unit,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String,
    modifier: Modifier = Modifier,
    palette: Palette = pal,
) {
    val p = palette
    val menuBg = SharedFileSpec.menuSurface(p.dark)
    val gapBg = SharedFileSpec.menuDestructiveGap(p.dark)
    val hairlineColor = SharedFileSpec.darkHairline(p.dark)

    val hasHeader = path.isNotEmpty() || machineName.isNotEmpty()
    Column(
        modifier = modifier
            .width(SharedFileSpec.MenuWidth)
            .sharedFloatingShadow(SharedFileSpec.MenuRadius, p.dark)
            .then(
                if (hairlineColor != null) {
                    Modifier.border(
                        width = 0.5.dp,
                        color = hairlineColor,
                        shape = RoundedCornerShape(SharedFileSpec.MenuRadius),
                    )
                } else {
                    Modifier
                },
            )
            .clip(RoundedCornerShape(SharedFileSpec.MenuRadius))
            .background(menuBg)
            .clickable(
                interactionSource = remember { MutableInteractionSource() },
                indication = null,
                onClick = {},
            ),
    ) {
        if (hasHeader) Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(start = 16.dp, end = 16.dp, top = 11.dp, bottom = 10.dp),
        ) {
            if (path.isNotEmpty()) Text(
                text = path,
                style = TextStyle(
                    fontFamily = MonoFamily,
                    fontSize = 11.5.sp,
                    lineHeight = (11.5f * 1.45f).sp,
                    lineHeightStyle = LineHeightStyle(LineHeightStyle.Alignment.Center, LineHeightStyle.Trim.None),
                ),
                color = p.ink2,
            )
            if (machineName.isNotEmpty()) {
                if (path.isNotEmpty()) Spacer(Modifier.height(3.dp))
                Text(
                    text = copy(UiMessages.sharedFileMenuFromMachine(machine = machineName)),
                    style = T.s12.copy(lineHeight = 16.sp),
                    color = p.ink3,
                )
            }
        }

        entries.forEachIndexed { index, entry ->
            if (entry.isDestructiveGap) {
                Box(Modifier.fillMaxWidth().height(8.dp).background(gapBg))
            } else if (hasHeader || index > 0) {
                Box(Modifier.fillMaxWidth().height(0.5.dp).background(p.sep))
            }
            val itemColor = if (entry.destructive) p.red else p.ink
            val labelText = entry.label()
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .height(44.dp)
                    .clickable { onAction(entry.action) }
                    .padding(horizontal = 16.dp)
                    .semantics {
                        contentDescription = labelText
                        role = Role.Button
                    },
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    text = labelText,
                    style = T.s17,
                    color = itemColor,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f),
                )
                Icon(
                    imageVector = entry.icon,
                    contentDescription = null,
                    tint = itemColor,
                    modifier = Modifier.size(19.dp),
                )
            }
        }
    }
}

@Composable
private fun SharedFileImageViewerDialog(
    file: SharedFile,
    localFile: File,
    onDismiss: () -> Unit,
    onSave: () -> Unit,
    onShare: () -> Unit,
) {
    val copy = uiText()
    val bitmap by produceState<ImageBitmap?>(null, localFile.absolutePath) {
        value = withContext(Dispatchers.IO) {
            runCatching {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                    ImageDecoder.decodeBitmap(ImageDecoder.createSource(localFile)).asImageBitmap()
                } else {
                    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
                    BitmapFactory.decodeFile(localFile.absolutePath, bounds)
                    var sample = 1
                    while (maxOf(bounds.outWidth, bounds.outHeight) / sample > 2048) sample *= 2
                    BitmapFactory.decodeFile(
                        localFile.absolutePath,
                        BitmapFactory.Options().apply { inSampleSize = sample },
                    )?.asImageBitmap()
                }
            }.getOrNull()
        }
    }

    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        PagedImageViewer(
            count = 1,
            start = 0,
            onClose = onDismiss,
            picture = {
                Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    bitmap?.let { ViewerBitmap(it, sanitizeSharedFileName(file.name)) }
                }
            },
            info = {
                Text(sanitizeSharedFileName(file.name), color = Color.White, style = T.s15b)
                Text(formatSharedFileSize(file.sizeBytes), color = Color.White.copy(alpha = 0.8f), style = T.s13)
                Row {
                    TextButton(onClick = onSave) {
                        Text(copy(UiMessages.sharedFileMenuSaveToAlbum()), color = Color(0xFF9EC1FF))
                    }
                    TextButton(onClick = onShare) {
                        Text(copy(UiMessages.sharedFileMenuShare()), color = Color(0xFF9EC1FF))
                    }
                }
            },
        )
    }
}
