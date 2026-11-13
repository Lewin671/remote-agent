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
import io.github.lewin671.remoteagent.proto.SharedFile
import java.io.File
import kotlin.math.roundToInt
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

private object SharedFileSpec {
    val CardWidth = 296.dp
    val CardRadius = 20.dp
    val TileSize = 44.dp
    val TileRadius = 11.dp
    val PillRadius = 15.dp
    val MenuWidth = 250.dp
    val MenuRadius = 14.dp
    val OverlayGap = 10.dp

    // Scrim and opaque composited menu/lift surfaces aligned with draft.html light and dark tokens.
    val Scrim = Color(0x2E000000)

    fun controlFill(dark: Boolean): Color = if (dark) Color(0x57787880) else Color(0x2E787880)
    fun liftSurface(dark: Boolean): Color = if (dark) Color(0xFF1C1C1E) else Color.White
    fun menuSurface(dark: Boolean): Color = if (dark) Color(0xFF28282A) else Color(0xFFF6F6F8)
    fun menuDestructiveGap(dark: Boolean): Color = if (dark) Color(0x38787880) else Color(0x1A787880)
    fun darkHairline(dark: Boolean): Color? = if (dark) Color(0x1AFFFFFF) else null
    fun floatShadowColor(dark: Boolean): Color = Color.Black.copy(alpha = if (dark) 0.60f else 0.22f)
}

internal val SharedCardWidth = SharedFileSpec.CardWidth
internal val SharedTileSize = SharedFileSpec.TileSize
private val SharedSubtitleStyle = T.s13.copy(fontFeatureSettings = "tnum")

private fun Modifier.sharedFloatingShadow(
    cornerRadius: Dp,
    dark: Boolean,
): Modifier = drawBehind {
    val radiusPx = cornerRadius.toPx()
    val blurPx = 28.dp.toPx()
    val offsetYPx = 14.dp.toPx()
    val shadowColor = SharedFileSpec.floatShadowColor(dark).toArgb()
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
    val anchorTopInRootPx: Float,
    val anchorLeftInRootPx: Float,
    val uiState: SharedFileUiState,
    val thumbnail: ImageBitmap?,
)

internal data class SharedFileImagePreview(
    val file: SharedFile,
    val localFile: File,
)

internal class SharedFileHostState(
    val manager: SharedFileManager,
    val sessionId: String,
    initialMachineName: String,
    initialMachineOnline: Boolean,
    initialSessionExited: Boolean,
) {
    var machineName by mutableStateOf(initialMachineName)
    var machineOnline by mutableStateOf(initialMachineOnline)
    var sessionExited by mutableStateOf(initialSessionExited)
    var menuTarget by mutableStateOf<SharedFileMenuRequest?>(null)
    var imagePreview by mutableStateOf<SharedFileImagePreview?>(null)
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

    LaunchedEffect(manager, sessionId) {
        manager.store.completions.collect { comp ->
            if (comp.sessionId != sessionId) return@collect
            Motion.tap(view)
            view.announceForAccessibility(copy(UiMessages.sharedFileA11YDownloadedAnnounce(name = comp.fileName)))
            if (comp.userInitiated) {
                val sf = SharedFile.newBuilder()
                    .setFileId(comp.fileId)
                    .setName(comp.fileName)
                    .setSizeBytes(comp.localFile.length())
                    .build()
                openSharedFile(context, sf, comp.localFile, liveHost)
            }
        }
    }

    var rootCoords by remember { mutableStateOf<LayoutCoordinates?>(null) }

    CompositionLocalProvider(LocalSharedFileHost provides liveHost) {
        Box(modifier.onGloballyPositioned { rootCoords = it }) {
            val menuOpen = liveHost.menuTarget != null
            Box(
                Modifier
                    .fillMaxSize()
                    .then(
                        if (menuOpen && Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                            Modifier.blur(14.dp)
                        } else {
                            Modifier
                        },
                    ),
            ) {
                content()
            }

            liveHost.menuTarget?.let { target ->
                val tr = states["$sessionId:${target.file.fileId}"] ?: SharedFileTransferState.Idle
                val freshState = resolveSharedFileUiState(target.file, tr, machineOnline, sessionExited)
                if (freshState.visual == CardVisualState.CLEANED_UP) {
                    liveHost.menuTarget = null
                } else {
                    SharedFileContextMenuOverlay(
                        request = target.copy(uiState = freshState, machineName = machineName),
                        rootHeightPx = rootCoords?.size?.height ?: 0,
                        onDismiss = { liveHost.menuTarget = null },
                        onAction = { action ->
                            liveHost.menuTarget = null
                            handleMenuAction(
                                action = action,
                                file = target.file,
                                uiState = freshState,
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
    uiState: SharedFileUiState,
    sessionId: String,
    manager: SharedFileManager,
    context: Context,
    host: SharedFileHostState,
    scope: kotlinx.coroutines.CoroutineScope,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String,
) {
    when (action) {
        SharedMenuAction.DOWNLOAD, SharedMenuAction.RESUME -> manager.startOrResume(sessionId, file, userInitiated = true)
        SharedMenuAction.PAUSE -> manager.pause(sessionId, file)
        SharedMenuAction.CANCEL -> manager.cancel(sessionId, file)
        SharedMenuAction.SAVE_TO_ALBUM -> {
            val local = uiState.localFile ?: return
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
            val local = uiState.localFile ?: return
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
            val local = uiState.localFile ?: return
            manager.shareFile(context, local, file.name)
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
    val kind = classifySharedFile(file.name)
    if (kind == SharedFileKind.IMAGE) {
        host?.imagePreview = SharedFileImagePreview(file, localFile)
        return
    }
    val opened = host?.manager?.openExternal(context, localFile, file.name) ?: false
    if (!opened) {
        host?.unopenableTarget = SharedFileImagePreview(file, localFile)
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

            val handlePrimaryClick: () -> Unit = {
                when (uiState.visual) {
                    CardVisualState.AVAILABLE, CardVisualState.PAUSED, CardVisualState.FAILED ->
                        manager.startOrResume(sessionId, file, userInitiated = true)
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
                    if (uiState.visual != CardVisualState.CLEANED_UP && host != null) {
                        val rect = cardCoords?.boundsInRoot()
                        host.menuTarget = SharedFileMenuRequest(
                            file = file,
                            machineName = machineName,
                            anchorTopInRootPx = rect?.top ?: 0f,
                            anchorLeftInRootPx = rect?.left ?: 0f,
                            uiState = uiState,
                            thumbnail = thumb,
                        )
                    }
                },
                modifier = Modifier.onGloballyPositioned { cardCoords = it },
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
                .sharedFloatingShadow(SharedFileSpec.CardRadius, p.dark)
                .then(
                    if (hairlineColor != null) {
                        Modifier.border(
                            width = 0.5.dp,
                            color = hairlineColor,
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

@Composable
private fun SharedFileTile(
    kind: SharedFileKind,
    visualState: CardVisualState,
    thumbnail: ImageBitmap?,
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
        modifier = Modifier
            .size(SharedFileSpec.TileSize)
            .clip(RoundedCornerShape(SharedFileSpec.TileRadius))
            .alpha(if (muted) 0.55f else 1f)
            .background(
                Brush.linearGradient(
                    colors = listOf(startColor, endColor),
                    start = Offset(0.33f * 132f, 0f),
                    end = Offset(0.67f * 132f, 132f),
                ),
            ),
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
            modifier = Modifier.size(22.dp),
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
                            radius = size.minDimension * 0.60f,
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

@Composable
private fun MiddleEllipsizedFileName(
    name: String,
    color: Color,
) {
    val style = T.s15b.copy(
        lineHeight = 20.sp,
        letterSpacing = (-0.1).sp,
    )
    val textMeasurer = rememberTextMeasurer()
    BoxWithConstraints(modifier = Modifier.fillMaxWidth()) {
        val maxWidthPx = constraints.maxWidth
        val display = remember(name, style, maxWidthPx) {
            fitMiddleEllipsizedName(name, style, maxWidthPx, textMeasurer)
        }
        Text(
            text = display,
            style = style,
            color = color,
            maxLines = 1,
            softWrap = false,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

private fun fitMiddleEllipsizedName(
    name: String,
    style: TextStyle,
    maxWidthPx: Int,
    textMeasurer: TextMeasurer,
): String {
    if (name.isEmpty() || maxWidthPx <= 0) return name
    val full = textMeasurer.measure(
        text = name,
        style = style,
        maxLines = 1,
        softWrap = false,
    )
    if (!full.hasVisualOverflow && full.size.width <= maxWidthPx) return name

    val (head, tail) = splitMiddleTruncateName(name)
    if (tail.isEmpty() || head.isEmpty()) return name

    var lo = 0
    var hi = head.length
    var best = "…$tail"
    while (lo <= hi) {
        var mid = (lo + hi) ushr 1
        if (mid > 0 && mid < head.length && Character.isHighSurrogate(head[mid - 1])) {
            mid--
        }
        val candidate = "${head.substring(0, mid)}…$tail"
        val m = textMeasurer.measure(
            text = candidate,
            style = style,
            maxLines = 1,
            softWrap = false,
        )
        if (!m.hasVisualOverflow && m.size.width <= maxWidthPx) {
            best = candidate
            lo = mid + 1
        } else {
            hi = mid - 1
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
    Text(
        text = text,
        style = SharedSubtitleStyle,
        color = color,
        maxLines = 1,
        overflow = TextOverflow.Ellipsis,
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

@Composable
internal fun SharedFileContextMenuOverlay(
    request: SharedFileMenuRequest,
    rootHeightPx: Int,
    onDismiss: () -> Unit,
    onAction: (SharedMenuAction) -> Unit,
) {
    val copy = uiText()
    val p = pal
    val density = LocalDensity.current
    val kind = classifySharedFile(request.file.name)
    val items = remember(request.uiState.visual, kind) {
        buildMenuEntries(request.uiState.visual, kind)
    }

    // Compute overlay vertical position so the lifted card + 10dp gap + menu fits inside viewport.
    val menuEstimatedHeightPx = with(density) {
        (56 + items.sumOf { if (it.isDestructiveGap) 52 else 44 } + 8).dp.roundToPx()
    }
    val cardHeightPx = with(density) { 64.dp.roundToPx() }
    val gapPx = with(density) { 10.dp.roundToPx() }
    val marginPx = with(density) { 16.dp.roundToPx() }
    val defaultTop = if (request.anchorTopInRootPx > 0f) request.anchorTopInRootPx.roundToInt() else with(density) { 80.dp.roundToPx() }
    val maxTop = if (rootHeightPx > 0) {
        (rootHeightPx - cardHeightPx - gapPx - menuEstimatedHeightPx - marginPx).coerceAtLeast(marginPx)
    } else defaultTop
    val topPx = defaultTop.coerceIn(marginPx, maxTop)
    val startPx = if (request.anchorLeftInRootPx > 0f) request.anchorLeftInRootPx.roundToInt() else with(density) { ChatSide.roundToPx() }

    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(SharedFileSpec.Scrim)
            .clickable(
                interactionSource = remember { MutableInteractionSource() },
                indication = null,
                onClick = onDismiss,
            ),
    ) {
        Column(
            modifier = Modifier.offset { IntOffset(x = startPx, y = topPx) },
            verticalArrangement = Arrangement.spacedBy(SharedFileSpec.OverlayGap),
        ) {
            SharedFileCard(
                file = request.file,
                uiState = request.uiState,
                thumbnail = request.thumbnail,
                lifted = true,
                onClick = onDismiss,
                onButtonClick = onDismiss,
                onLongClick = {},
            )

            SharedFileContextMenuCard(
                path = displaySharedPath(request.file.path, request.file.name),
                machineName = request.machineName,
                entries = items,
                onAction = onAction,
                copy = copy,
            )
        }
    }
}

private data class MenuEntry(
    val action: SharedMenuAction,
    val label: @Composable () -> String,
    val icon: ImageVector,
    val destructive: Boolean = false,
    val isDestructiveGap: Boolean = false,
)

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
            add(MenuEntry(SharedMenuAction.DELETE_LOCAL, { uiText(UiMessages.sharedFileMenuDeleteLocal()) }, SharedFileIcons.Trash, destructive = true, isDestructiveGap = true))
        }
        CardVisualState.MACHINE_OFFLINE, CardVisualState.CLEANED_UP -> emptyList()
    }

@Composable
private fun SharedFileContextMenuCard(
    path: String,
    machineName: String,
    entries: List<MenuEntry>,
    onAction: (SharedMenuAction) -> Unit,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String,
) {
    val p = pal
    val menuBg = SharedFileSpec.menuSurface(p.dark)
    val gapBg = SharedFileSpec.menuDestructiveGap(p.dark)
    val hairlineColor = SharedFileSpec.darkHairline(p.dark)

    Column(
        modifier = Modifier
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
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(start = 16.dp, end = 16.dp, top = 11.dp, bottom = 10.dp),
        ) {
            Text(
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
                Spacer(Modifier.height(3.dp))
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
            } else {
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
