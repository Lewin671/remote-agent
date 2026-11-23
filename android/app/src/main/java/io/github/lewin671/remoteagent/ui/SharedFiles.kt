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
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.displayCutout
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.systemBars
import androidx.compose.foundation.layout.union
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
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
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.drawIntoCanvas
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionOnScreen
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextMeasurer
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.LineHeightStyle
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.unit.toSize
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.DialogWindowProvider
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.SessionSharedFileItem
import io.github.lewin671.remoteagent.core.proto.SharedFileCardStatus
import io.github.lewin671.remoteagent.core.proto.SharedFileKind
import io.github.lewin671.remoteagent.core.proto.SharedFileMenuAction
import io.github.lewin671.remoteagent.core.proto.SharedFileMenuItemView
import io.github.lewin671.remoteagent.core.proto.SharedFilePrimaryAction
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.data.SharedFileManager
import io.github.lewin671.remoteagent.data.isSharedTextFile
import io.github.lewin671.remoteagent.data.readSharedTextFile
import java.io.File
import java.util.Locale
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
    val item: SessionSharedFileItem,
    /** Where the pressed card is on the screen, in pixels; the lifted card is drawn exactly there. */
    val cardBoundsOnScreen: Rect,
    val thumbnail: ImageBitmap?,
    /** Which card on the page was pressed: the same file can be on the page more than once. */
    val token: Any,
)

/** Where a downloaded file opens on Android. */
internal enum class SharedFileOpenRoute { IMAGE_VIEWER, VIDEO_PLAYER, AUDIO_PLAYER, TEXT_VIEWER, PDF_VIEWER, SYSTEM_CHOOSER }

private val PlayerVideoExts = setOf("mp4", "mov", "m4v", "webm", "mkv")
private val PlayerAudioExts = setOf("mp3", "m4a", "wav", "aac", "flac", "ogg")

internal fun sharedFileOpenRoute(displayName: String, kind: SharedFileKind = SharedFileKind.SHARED_FILE_KIND_UNSPECIFIED): SharedFileOpenRoute {
    if (kind == SharedFileKind.SHARED_FILE_KIND_IMAGE) return SharedFileOpenRoute.IMAGE_VIEWER
    if (kind == SharedFileKind.SHARED_FILE_KIND_PDF) return SharedFileOpenRoute.PDF_VIEWER
    if (isSharedTextFile(displayName)) return SharedFileOpenRoute.TEXT_VIEWER
    val ext = displayName.substringAfterLast('/', displayName).substringAfterLast('\\')
        .trim().substringAfterLast('.', "").lowercase(Locale.ROOT)
    return when (ext) {
        "pdf" -> SharedFileOpenRoute.PDF_VIEWER
        in PlayerVideoExts -> SharedFileOpenRoute.VIDEO_PLAYER
        in PlayerAudioExts -> SharedFileOpenRoute.AUDIO_PLAYER
        else -> SharedFileOpenRoute.SYSTEM_CHOOSER
    }
}

internal data class SharedFilePreviewTarget(
    val item: SessionSharedFileItem,
    val localFile: File,
)

internal data class SharedFilePlayerTarget(
    val item: SessionSharedFileItem,
    val localFile: File,
    val video: Boolean,
)

internal data class SharedFileTextPreview(
    val item: SessionSharedFileItem,
    val localFile: File,
    val text: String,
)

internal class SharedFileHostState(
    val manager: SharedFileManager,
    val sessionId: String,
) {
    private val autoOpenFiles = mutableSetOf<String>()

    fun startDownload(item: SessionSharedFileItem) {
        autoOpenFiles += item.fileId
        manager.startOrResume(sessionId, item, userInitiated = true)
    }

    fun leavePage() { autoOpenFiles.clear() }

    fun consumeAutoOpen(fileId: String): Boolean = autoOpenFiles.remove(fileId)

    var menuTarget by mutableStateOf<SharedFileMenuRequest?>(null)
    var imagePreview by mutableStateOf<SharedFilePreviewTarget?>(null)
    var pendingTextPreview by mutableStateOf<SharedFilePreviewTarget?>(null)
    var textPreview by mutableStateOf<SharedFileTextPreview?>(null)
    var pdfPreview by mutableStateOf<SharedFilePreviewTarget?>(null)
    var playerTarget by mutableStateOf<SharedFilePlayerTarget?>(null)
    var unopenableTarget by mutableStateOf<SharedFilePreviewTarget?>(null)
    var pendingSaveAction by mutableStateOf<(() -> Unit)?>(null)
    var requestStoragePermission: (() -> Unit)? = null
}

internal val LocalSharedFileHost = compositionLocalOf<SharedFileHostState?> { null }

/**
 * Wraps the chat area so that completed downloads auto-open when on the same session screen,
 * long-press blurs the underlying chat, and in-app viewers or "no app" prompts work cleanly.
 */
@Composable
internal fun SharedFileHost(
    sessionId: String,
    itemsById: Map<String, SessionSharedFileItem> = emptyMap(),
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
        SharedFileHostState(manager, sessionId)
    }

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
            view.announceForAccessibility(copy(UiMessages.sharedFileA11YDownloadedAnnounce(name = comp.displayName)))
            if (comp.userInitiated && liveHost.consumeAutoOpen(comp.fileId)) {
                val item = itemsById[comp.fileId] ?: SessionSharedFileItem.newBuilder()
                    .setFileId(comp.fileId)
                    .setDisplayName(comp.displayName)
                    .setName(comp.displayName)
                    .setSizeBytes(comp.sizeBytes)
                    .setKind(comp.kind)
                    .build()
                openSharedFile(context, item, comp.localFile, liveHost)
            }
        }
    }

    CompositionLocalProvider(LocalSharedFileHost provides liveHost) {
        Box(modifier) {
            content()

            liveHost.menuTarget?.let { target ->
                val freshItem = itemsById[target.item.fileId] ?: target.item
                val localFile = (states["$sessionId:${freshItem.fileId}"] as? io.github.lewin671.remoteagent.core.SharedFileTransferState.Downloaded)
                    ?.localFile?.takeIf { it.isFile }
                if (freshItem.status == SharedFileCardStatus.SHARED_FILE_CARD_STATUS_CLEANED || freshItem.menuItemsCount == 0) {
                    liveHost.menuTarget = null
                } else {
                    SharedFileContextMenuOverlay(
                        request = target.copy(item = freshItem),
                        onDismiss = { liveHost.menuTarget = null },
                        onAction = { action ->
                            handleMenuAction(
                                action = action,
                                item = freshItem,
                                localFile = localFile,
                                sessionId = sessionId,
                                manager = manager,
                                context = context,
                                host = liveHost,
                                scope = scope,
                                copy = copy,
                            )
                        },
                        onDisabledAction = { reason ->
                            if (reason.isNotEmpty()) Toast.makeText(context, reason, Toast.LENGTH_SHORT).show()
                        },
                    )
                }
            }
        }
    }

    LaunchedEffect(liveHost.pendingTextPreview) {
        val pending = liveHost.pendingTextPreview ?: return@LaunchedEffect
        val text = withContext(Dispatchers.IO) { readSharedTextFile(pending.localFile) }
        val name = pending.item.displayName.ifEmpty { pending.item.name }
        if (text != null) {
            liveHost.textPreview = SharedFileTextPreview(pending.item, pending.localFile, text)
        } else if (!manager.openExternal(context, pending.localFile, name)) {
            liveHost.unopenableTarget = pending
        }
        liveHost.pendingTextPreview = null
    }

    liveHost.textPreview?.let { preview ->
        val name = preview.item.displayName.ifEmpty { preview.item.name }
        SharedFileTextViewerDialog(
            fileName = name,
            nameHead = preview.item.nameHead,
            nameTail = preview.item.nameTail,
            text = preview.text,
            onDismiss = { liveHost.textPreview = null },
            onAction = { action ->
                handleMenuAction(action, preview.item, preview.localFile, sessionId, manager, context, liveHost, scope, copy)
            },
        )
    }

    liveHost.pdfPreview?.let { preview ->
        val name = preview.item.displayName.ifEmpty { preview.item.name }
        SharedFilePdfViewer(
            fileName = name,
            nameHead = preview.item.nameHead,
            nameTail = preview.item.nameTail,
            localFile = preview.localFile,
            onDismiss = { liveHost.pdfPreview = null },
            onUnsupported = {
                liveHost.pdfPreview = null
                if (!manager.openExternal(context, preview.localFile, name)) {
                    liveHost.unopenableTarget = preview
                }
            },
            onAction = { action ->
                handleMenuAction(action, preview.item, preview.localFile, sessionId, manager, context, liveHost, scope, copy)
            },
        )
    }

    liveHost.imagePreview?.let { preview ->
        val name = preview.item.displayName.ifEmpty { preview.item.name }
        SharedFileImageViewerDialog(
            item = preview.item,
            localFile = preview.localFile,
            onDismiss = { liveHost.imagePreview = null },
            onSave = {
                runWithStoragePermission(context, liveHost) {
                    scope.launch {
                        val res = manager.saveToAlbum(preview.localFile, SharedFileKind.SHARED_FILE_KIND_IMAGE, name)
                        if (res.isSuccess) {
                            Toast.makeText(context, copy(UiMessages.sharedFileToastSavedToAlbum()), Toast.LENGTH_SHORT).show()
                        }
                    }
                }
            },
            onShare = { manager.shareFile(context, preview.localFile, name) },
        )
    }

    liveHost.playerTarget?.let { target ->
        SharedMediaPlayerDialog(
            item = target.item,
            localFile = target.localFile,
            video = target.video,
            onDismiss = { liveHost.playerTarget = null },
            onAction = { action ->
                handleMenuAction(
                    action = action,
                    item = target.item,
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
        val name = target.item.displayName.ifEmpty { target.item.name }
        Alert(
            title = name,
            text = copy(UiMessages.sharedFileOpenNoApp()),
            confirm = copy(UiMessages.sharedFileActionSaveToPhone()),
            onDismiss = { liveHost.unopenableTarget = null },
            onConfirm = {
                runWithStoragePermission(context, liveHost) {
                    scope.launch {
                        val res = manager.saveToDownloads(target.localFile, name)
                        if (res.isSuccess) {
                            Toast.makeText(context, copy(UiMessages.sharedFileToastSavedToDownloads()), Toast.LENGTH_SHORT).show()
                        }
                    }
                }
            },
        )
    }
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
    action: SharedFileMenuAction,
    item: SessionSharedFileItem,
    localFile: File?,
    sessionId: String,
    manager: SharedFileManager,
    context: Context,
    host: SharedFileHostState,
    scope: kotlinx.coroutines.CoroutineScope,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String,
) {
    val name = item.displayName.ifEmpty { item.name }
    when (action) {
        SharedFileMenuAction.SHARED_FILE_MENU_ACTION_DOWNLOAD,
        SharedFileMenuAction.SHARED_FILE_MENU_ACTION_RESUME,
        SharedFileMenuAction.SHARED_FILE_MENU_ACTION_RETRY -> host.startDownload(item)
        SharedFileMenuAction.SHARED_FILE_MENU_ACTION_PAUSE -> manager.pause(sessionId, item)
        SharedFileMenuAction.SHARED_FILE_MENU_ACTION_CANCEL_DOWNLOAD -> manager.cancel(sessionId, item)
        SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE_TO_ALBUM -> {
            val local = localFile ?: return
            runWithStoragePermission(context, host) {
                scope.launch {
                    val res = manager.saveToAlbum(local, item.kind, name)
                    if (res.isSuccess) {
                        Toast.makeText(context, copy(UiMessages.sharedFileToastSavedToAlbum()), Toast.LENGTH_SHORT).show()
                    }
                }
            }
        }
        SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE -> {
            val local = localFile ?: return
            runWithStoragePermission(context, host) {
                scope.launch {
                    val res = manager.saveToDownloads(local, name)
                    if (res.isSuccess) {
                        Toast.makeText(context, copy(UiMessages.sharedFileToastSavedToDownloads()), Toast.LENGTH_SHORT).show()
                    }
                }
            }
        }
        SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SHARE -> {
            val local = localFile ?: return
            manager.shareFile(context, local, name)
        }
        SharedFileMenuAction.SHARED_FILE_MENU_ACTION_OPEN_WITH -> {
            val local = localFile ?: return
            if (!manager.openExternal(context, local, name)) {
                host.unopenableTarget = SharedFilePreviewTarget(item, local)
            }
        }
        SharedFileMenuAction.SHARED_FILE_MENU_ACTION_DELETE_LOCAL -> manager.deleteLocal(sessionId, item)
        else -> Unit
    }
}

internal fun openSharedFile(
    context: Context,
    item: SessionSharedFileItem,
    localFile: File,
    host: SharedFileHostState?,
) {
    val name = item.displayName.ifEmpty { item.name }
    when (val route = sharedFileOpenRoute(name, item.kind)) {
        SharedFileOpenRoute.PDF_VIEWER -> host?.pdfPreview = SharedFilePreviewTarget(item, localFile)
        SharedFileOpenRoute.TEXT_VIEWER -> host?.pendingTextPreview = SharedFilePreviewTarget(item, localFile)
        SharedFileOpenRoute.IMAGE_VIEWER -> host?.imagePreview = SharedFilePreviewTarget(item, localFile)
        SharedFileOpenRoute.VIDEO_PLAYER, SharedFileOpenRoute.AUDIO_PLAYER ->
            host?.playerTarget = SharedFilePlayerTarget(item, localFile, route == SharedFileOpenRoute.VIDEO_PLAYER)
        SharedFileOpenRoute.SYSTEM_CHOOSER -> {
            val opened = host?.manager?.openExternal(context, localFile, name) ?: false
            if (!opened) {
                host?.unopenableTarget = SharedFilePreviewTarget(item, localFile)
            }
        }
    }
}

/** In-chat container rendering one or more Agent-shared file cards stacked with 8dp gaps. */
@Composable
internal fun SharedFilesBlock(
    files: List<SessionSharedFileItem>,
    sessionId: String,
    modifier: Modifier = Modifier,
) {
    val context = LocalContext.current
    val host = LocalSharedFileHost.current
    val manager = host?.manager ?: RaApp.instance.sharedFiles
    val transferMap by manager.store.states.collectAsState()

    Column(
        modifier = modifier.fillMaxWidth().padding(horizontal = ChatSide, vertical = 4.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        for (item in files) {
            LaunchedEffect(sessionId, item.fileId) {
                withContext(Dispatchers.IO) {
                    manager.registerFile(sessionId, item)
                }
            }
            val localFile = (transferMap["$sessionId:${item.fileId}"] as? io.github.lewin671.remoteagent.core.SharedFileTransferState.Downloaded)
                ?.localFile?.takeIf { it.isFile }
            val thumbPx = with(LocalDensity.current) { SharedTileSize.roundToPx() }
            val thumb by produceState<ImageBitmap?>(null, localFile?.absolutePath, item.status, item.kind) {
                value = if (item.status == SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED && localFile != null) {
                    manager.loadThumbnail(localFile, item.kind, thumbPx)
                } else {
                    null
                }
            }

            var cardCoords by remember { mutableStateOf<LayoutCoordinates?>(null) }
            val token = remember { Any() }
            // The pressed card is drawn lifted in the overlay: never show it twice.
            val hiddenBehindOverlay = host?.menuTarget?.token === token
            val disabledReason = if (item.hasPrimaryDisabledReason()) uiText(item.primaryDisabledReason) else ""

            val handlePrimaryClick: () -> Unit = {
                if (!item.primaryEnabled) {
                    if (disabledReason.isNotEmpty()) {
                        Toast.makeText(context, disabledReason, Toast.LENGTH_SHORT).show()
                    }
                } else {
                    when (item.primaryAction) {
                        SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_DOWNLOAD,
                        SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_RESUME,
                        SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_RETRY ->
                            if (host != null) host.startDownload(item)
                            else manager.startOrResume(sessionId, item, userInitiated = true)
                        SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_PAUSE -> manager.pause(sessionId, item)
                        SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_OPEN -> localFile?.let { local ->
                            openSharedFile(context, item, local, host)
                        }
                        else -> Unit
                    }
                }
            }

            SharedFileCard(
                item = item,
                thumbnail = thumb,
                lifted = false,
                onClick = handlePrimaryClick,
                onButtonClick = handlePrimaryClick,
                onLongClick = {
                    val coords = cardCoords
                    if (item.menuItemsCount > 0 && host != null && coords != null && coords.isAttached) {
                        host.menuTarget = SharedFileMenuRequest(
                            item = item,
                            cardBoundsOnScreen = Rect(coords.positionOnScreen(), coords.size.toSize()),
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
    item: SessionSharedFileItem,
    thumbnail: ImageBitmap?,
    lifted: Boolean,
    onClick: () -> Unit,
    onButtonClick: () -> Unit,
    onLongClick: () -> Unit,
    modifier: Modifier = Modifier,
    shadowAlpha: Float = 1f,
) {
    val p = pal
    val displayName = item.displayName.ifEmpty { item.name }
    val cardDesc = if (item.hasA11YLabel()) uiText(item.a11YLabel) else displayName

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
            kind = item.kind,
            status = item.status,
            thumbnail = thumbnail,
        )

        Column(modifier = Modifier.weight(1f)) {
            MiddleEllipsizedFileName(
                name = displayName,
                head = item.nameHead,
                tail = item.nameTail,
                color = when (item.status) {
                    SharedFileCardStatus.SHARED_FILE_CARD_STATUS_OFFLINE -> p.ink2
                    SharedFileCardStatus.SHARED_FILE_CARD_STATUS_CLEANED -> p.ink3
                    else -> p.ink
                },
            )
            Spacer(Modifier.height(2.dp))
            val subtitleText = if (item.hasSubtitle()) uiText(item.subtitle) else ""
            val subtitleColor = when {
                item.subtitleIsError -> p.red
                item.status == SharedFileCardStatus.SHARED_FILE_CARD_STATUS_CLEANED -> p.ink3
                else -> p.ink2
            }
            // Never an ellipsis: a long line wraps to a second one and the card grows.
            Text(
                text = subtitleText,
                style = SharedSubtitleStyle,
                color = subtitleColor,
                maxLines = 2,
                overflow = TextOverflow.Clip,
            )
        }

        if (item.primaryAction != SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_UNSPECIFIED) {
            SharedFileTrailingControl(
                item = item,
                onClick = onButtonClick,
            )
        }
    }
}

/** The audio tile of the card at the size the player shows it: same gradient and note, scaled. */
@Composable
internal fun SharedFileAudioArtwork(modifier: Modifier = Modifier) {
    SharedFileTile(
        kind = SharedFileKind.SHARED_FILE_KIND_AUDIO,
        status = SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED,
        thumbnail = null,
        size = 96.dp,
        radius = 24.dp,
        modifier = modifier,
    )
}

@Composable
private fun SharedFileTile(
    kind: SharedFileKind,
    status: SharedFileCardStatus,
    thumbnail: ImageBitmap?,
    size: Dp = SharedFileSpec.TileSize,
    radius: Dp = SharedFileSpec.TileRadius,
    modifier: Modifier = Modifier,
) {
    val muted = status == SharedFileCardStatus.SHARED_FILE_CARD_STATUS_OFFLINE ||
        status == SharedFileCardStatus.SHARED_FILE_CARD_STATUS_CLEANED
    val (rawStart, rawEnd) = when (kind) {
        SharedFileKind.SHARED_FILE_KIND_VIDEO -> Color(0xFF7D7AFF) to Color(0xFF5856D6)
        SharedFileKind.SHARED_FILE_KIND_IMAGE -> Color(0xFF5AC8FA) to Color(0xFF2F9BF0)
        SharedFileKind.SHARED_FILE_KIND_AUDIO -> Color(0xFFFFB45E) to Color(0xFFFF9500)
        SharedFileKind.SHARED_FILE_KIND_PDF,
        SharedFileKind.SHARED_FILE_KIND_DOC -> Color(0xFFFF8A80) to Color(0xFFFF453A)
        else -> Color(0xFFB0B0B8) to Color(0xFF8E8E93)
    }
    val startColor = if (muted) toGrayscale(rawStart) else rawStart
    val endColor = if (muted) toGrayscale(rawEnd) else rawEnd

    val showThumb = status == SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED && thumbnail != null
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
                SharedFileKind.SHARED_FILE_KIND_VIDEO -> SharedFileIcons.Video
                SharedFileKind.SHARED_FILE_KIND_IMAGE -> SharedFileIcons.Image
                SharedFileKind.SHARED_FILE_KIND_AUDIO -> SharedFileIcons.Audio
                SharedFileKind.SHARED_FILE_KIND_ARCHIVE -> SharedFileIcons.Zip
                else -> SharedFileIcons.Doc
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
            if (kind == SharedFileKind.SHARED_FILE_KIND_VIDEO) {
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
    head: String,
    tail: String,
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
        val display = remember(name, head, tail, style, maxWidthPx, maxLines) {
            fitMiddleEllipsizedName(name, head, tail, style, maxWidthPx, maxLines, textMeasurer)
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

/** Fits Core's pre-split `(head, tail)` into [maxWidthPx] on [maxLines] using Compose's [TextMeasurer]. */
internal fun fitMiddleEllipsizedName(
    name: String,
    head: String,
    tail: String,
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
private fun SharedFileTrailingControl(
    item: SessionSharedFileItem,
    onClick: () -> Unit,
) {
    val p = pal
    val fill2 = SharedFileSpec.controlFill(p.dark)
    val blue = p.blue
    val buttonDesc = if (item.hasPrimaryLabel()) uiText(item.primaryLabel) else ""

    AnimatedContent(
        targetState = item.primaryAction,
        transitionSpec = {
            (fadeIn(tween(Motion.BASE)) + scaleIn(tween(Motion.BASE), initialScale = 0.9f)) togetherWith
                (fadeOut(tween(Motion.FAST)) + scaleOut(tween(Motion.FAST), targetScale = 0.9f))
        },
        contentAlignment = Alignment.Center,
    ) { action ->
        when (action) {
            SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_DOWNLOAD -> {
                val iconTint = if (!item.primaryEnabled) p.ink3 else blue
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
            SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_PAUSE,
            SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_RESUME -> {
                val targetFraction = if (item.sizeBytes > 0L) {
                    item.progress.toFloat().coerceIn(0.03f, 1f)
                } else 0.05f
                val animFraction by animateFloatAsState(
                    targetValue = targetFraction,
                    animationSpec = tween(180, easing = LinearEasing),
                )
                val ringColor = if (item.primaryEnabled) blue else p.ink3
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
                                color = ringColor,
                                startAngle = -90f,
                                sweepAngle = 360f * animFraction,
                                useCenter = false,
                                topLeft = tl,
                                size = arcSize,
                                style = Stroke(width = strokePx, cap = StrokeCap.Round),
                            )
                        }
                        if (action == SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_PAUSE) {
                            Box(
                                modifier = Modifier
                                    .size(9.dp)
                                    .clip(RoundedCornerShape(2.5.dp))
                                    .background(ringColor),
                            )
                        } else {
                            Icon(
                                imageVector = SharedFileIcons.Down,
                                contentDescription = null,
                                tint = ringColor,
                                modifier = Modifier.size(13.dp),
                            )
                        }
                    }
                }
            }
            SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_OPEN,
            SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_RETRY -> {
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
                        text = buttonDesc,
                        style = T.s15b.copy(lineHeight = 20.sp),
                        color = blue,
                        maxLines = 1,
                    )
                }
            }
            else -> Unit
        }
    }
}

/**
 * Preserves the exact visual width of a 30dp trailing circle while guaranteeing a >= 44dp vertical
 * and horizontal accessibility hit area within the card.
 */
@Composable
private fun TouchTargetBox(
    visualWidth: Dp,
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
    onAction: (SharedFileMenuAction) -> Unit,
    onDisabledAction: (String) -> Unit = {},
) {
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

    val blurred = remember {
        Build.VERSION.SDK_INT >= Build.VERSION_CODES.S &&
            context.getSystemService(android.view.WindowManager::class.java)?.isCrossWindowBlurEnabled == true
    }
    val reduced = Motion.reduced
    val insets = WindowInsets.systemBars.union(WindowInsets.displayCutout)
    var rootOnScreen by remember { mutableStateOf(Offset.Zero) }

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
        val sourceCaption = if (shown.item.hasSourceCaption()) uiText(shown.item.sourceCaption) else ""
        Layout(
            content = {
                SharedFileCard(
                    item = shown.item,
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
                    path = shown.item.displayPath,
                    sourceCaption = sourceCaption,
                    entries = shown.item.menuItemsList,
                    onAction = { action ->
                        onAction(action)
                        close()
                    },
                    onDisabledAction = onDisabledAction,
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

internal fun sharedFileMenuIcon(action: SharedFileMenuAction): ImageVector = when (action) {
    SharedFileMenuAction.SHARED_FILE_MENU_ACTION_DOWNLOAD,
    SharedFileMenuAction.SHARED_FILE_MENU_ACTION_RESUME,
    SharedFileMenuAction.SHARED_FILE_MENU_ACTION_RETRY -> SharedFileIcons.Down
    SharedFileMenuAction.SHARED_FILE_MENU_ACTION_PAUSE -> SharedFileIcons.Pause
    SharedFileMenuAction.SHARED_FILE_MENU_ACTION_CANCEL_DOWNLOAD -> SharedFileIcons.Close
    SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE_TO_ALBUM -> SharedFileIcons.Image
    SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE -> SharedFileIcons.Save
    SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SHARE -> SharedFileIcons.Share
    SharedFileMenuAction.SHARED_FILE_MENU_ACTION_OPEN_WITH -> SharedFileIcons.OpenWith
    SharedFileMenuAction.SHARED_FILE_MENU_ACTION_DELETE_LOCAL -> SharedFileIcons.Trash
    else -> SharedFileIcons.Doc
}

internal fun viewerMenuItem(action: SharedFileMenuAction, labelKey: String): SharedFileMenuItemView =
    SharedFileMenuItemView.newBuilder()
        .setAction(action)
        .setLabel(CoreText.newBuilder().setKey(labelKey))
        .setEnabled(true)
        .build()

/** What the player's ⋯ menu offers: the same actions as a downloaded card's menu, minus delete. */
internal fun playerMenuEntries(video: Boolean): List<SharedFileMenuItemView> = buildList {
    if (video) add(viewerMenuItem(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE_TO_ALBUM, UiMessages.sharedFileMenuSaveToAlbum().key))
    add(viewerMenuItem(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE, UiMessages.sharedFileMenuSaveToDownloads().key))
    add(viewerMenuItem(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SHARE, UiMessages.sharedFileMenuShare().key))
    add(viewerMenuItem(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_OPEN_WITH, UiMessages.sharedFileMenuOpenWith().key))
}

@Composable
internal fun SharedFileContextMenuCard(
    path: String,
    sourceCaption: String,
    entries: List<SharedFileMenuItemView>,
    onAction: (SharedFileMenuAction) -> Unit,
    onDisabledAction: (String) -> Unit = {},
    modifier: Modifier = Modifier,
    palette: Palette = pal,
) {
    val p = palette
    val menuBg = SharedFileSpec.menuSurface(p.dark)
    val gapBg = SharedFileSpec.menuDestructiveGap(p.dark)
    val hairlineColor = SharedFileSpec.darkHairline(p.dark)

    val hasHeader = path.isNotEmpty() || sourceCaption.isNotEmpty()
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
            if (sourceCaption.isNotEmpty()) {
                if (path.isNotEmpty()) Spacer(Modifier.height(3.dp))
                Text(
                    text = sourceCaption,
                    style = T.s12.copy(lineHeight = 16.sp),
                    color = p.ink3,
                )
            }
        }

        entries.forEachIndexed { index, entry ->
            if (entry.separated) {
                Box(Modifier.fillMaxWidth().height(8.dp).background(gapBg))
            } else if (hasHeader || index > 0) {
                Box(Modifier.fillMaxWidth().height(0.5.dp).background(p.sep))
            }
            val itemColor = when {
                !entry.enabled -> p.ink3
                entry.destructive -> p.red
                else -> p.ink
            }
            val labelText = uiText(entry.label)
            val disabledText = if (entry.hasDisabledReason()) uiText(entry.disabledReason) else ""
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .height(44.dp)
                    .clickable {
                        if (entry.enabled) onAction(entry.action)
                        else onDisabledAction(disabledText)
                    }
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
                    imageVector = sharedFileMenuIcon(entry.action),
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
    item: SessionSharedFileItem,
    localFile: File,
    onDismiss: () -> Unit,
    onSave: () -> Unit,
    onShare: () -> Unit,
) {
    val copy = uiText()
    val name = item.displayName.ifEmpty { item.name }
    val sizeLabel = if (item.hasSizeText()) uiText(item.sizeText) else ""
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
                    bitmap?.let { ViewerBitmap(it, name) }
                }
            },
            info = {
                Text(name, color = Color.White, style = T.s15b)
                if (sizeLabel.isNotEmpty()) Text(sizeLabel, color = Color.White.copy(alpha = 0.8f), style = T.s13)
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
