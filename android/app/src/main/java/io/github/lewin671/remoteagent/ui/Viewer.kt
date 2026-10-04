package io.github.lewin671.remoteagent.ui

import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.graphics.BitmapFactory
import android.os.Build
import android.provider.MediaStore
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.Orientation
import androidx.compose.foundation.gestures.draggable
import androidx.compose.foundation.gestures.rememberDraggableState
import androidx.compose.foundation.gestures.rememberTransformableState
import androidx.compose.foundation.gestures.transformable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.SessionImage
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.sessionImages
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.proto.ImageFormat
import io.github.lewin671.remoteagent.proto.ImageSource
import io.github.lewin671.remoteagent.proto.ImageSync
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.io.File

/** Where a display copy stands while the viewer shows an image. */
sealed interface Big {
    data object Loading : Big
    class Ready(val bitmap: ImageBitmap, val bytes: ByteArray, val format: ImageFormat) : Big
    /** The event says there is no display copy. */
    data object NotSynced : Big
    data object Expired : Big
    /** Never fetched and no connection to fetch it. */
    data object Offline : Big
    data class Failed(val why: String) : Big
    data class Corrupt(val why: String) : Big
}

/**
 * The image viewer: every image of the session, paged, from the one tapped.
 * The preview shows at once, enlarged, and the display copy replaces it once
 * downloaded and decrypted (in memory only). Pinch to zoom, swipe down or
 * back to close; save and share hand the picture to the system.
 */
@Composable
fun ImageViewer(sid: String, seq: Long, index: Int) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val events by remote.events(sid).collectAsState()
    val all = remember(events.events) { sessionImages(events.events) }
    val start = remember(all.size > 0) {
        var n = 0
        val first = all.indexOfFirst { it.seq == seq }
        if (first >= 0) n = first + index
        n.coerceIn(0, (all.size - 1).coerceAtLeast(0))
    }
    BackHandler { nav.back() }
    Box(Modifier.fillMaxSize().background(Color.Black)) {
        if (all.isEmpty()) {
            Text("读取中…", Modifier.align(Alignment.Center), color = Color.White)
            return@Box
        }
        val pager = rememberPagerState(initialPage = start) { all.size }
        var drag by remember { mutableFloatStateOf(0f) }
        HorizontalPager(
            pager,
            Modifier.fillMaxSize().graphicsLayer { translationY = drag }.draggable(
                rememberDraggableState { drag = (drag + it).coerceAtLeast(0f) },
                Orientation.Vertical,
                onDragStopped = { if (drag > 300f) nav.back() else drag = 0f },
            ),
        ) { page ->
            ViewerPage(remote, sid, all[page], state.conn == ConnState.ONLINE)
        }
        val cur = all[pager.currentPage]
        Row(Modifier.fillMaxWidth().padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = nav.back) { Ic(Lucide.X, 22.dp, Color.White, contentDescription = "关闭") }
            Text("${pager.currentPage + 1} / ${all.size}", color = Color.White, style = MaterialTheme.typography.titleSmall)
        }
        Column(Modifier.align(Alignment.BottomCenter).fillMaxWidth().background(Color(0xAA000000)).padding(horizontal = 16.dp, vertical = 10.dp)) {
            val agent = agentName(s?.agent() ?: io.github.lewin671.remoteagent.proto.Agent.AGENT_CODEX)
            val from = when (cur.ref.source) {
                ImageSource.IMAGE_SOURCE_TOOL -> "$agent 查看的图片"
                ImageSource.IMAGE_SOURCE_GENERATED -> "$agent 生成的图片"
                else -> "用户粘贴"
            }
            Text(from + if (cur.ref.path.isNotEmpty()) "  " + cur.ref.path else "", color = Color(0xFFD5DADE), style = MaterialTheme.typography.bodySmall, fontFamily = MonoFamily, maxLines = 2)
            val w = if (cur.ref.originalWidth > 0) cur.ref.originalWidth else cur.ref.width
            val h = if (cur.ref.originalHeight > 0) cur.ref.originalHeight else cur.ref.height
            Text(listOfNotNull(if (w > 0) "$w × $h" else null, s?.let { titleOf(it) }).joinToString(" · "), color = Color(0xFFB9BFC6), style = MaterialTheme.typography.bodySmall)
            ViewerActions(remote, sid, cur)
        }
    }
}

/** Images decrypted while the viewer is open, by session and image, so paging back does not fetch again. */
internal val ViewerImages = AccountCache<Big.Ready>(6)

@Composable
private fun ViewerPage(remote: Remote, sid: String, img: SessionImage, online: Boolean) {
    val ref = img.ref
    val preview = rememberPreview(ref)
    var big by remember(ref.imageId) { mutableStateOf<Big>(Big.Loading) }
    var attempt by remember { mutableIntStateOf(0) }
    LaunchedEffect(ref.imageId, attempt, online) {
        big = load(remote, sid, img, online)
    }
    var scale by remember { mutableFloatStateOf(1f) }
    var offset by remember { mutableStateOf(androidx.compose.ui.geometry.Offset.Zero) }
    val zoom = rememberTransformableState { z, pan, _ ->
        scale = (scale * z).coerceIn(1f, 6f)
        offset = if (scale == 1f) androidx.compose.ui.geometry.Offset.Zero else offset + pan
    }
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        val b = big
        val bmp = (b as? Big.Ready)?.bitmap ?: preview
        if (bmp != null) Image(
            bmp, ref.path.ifEmpty { "图片" },
            Modifier.fillMaxSize().transformable(zoom, canPan = { scale > 1f }).graphicsLayer {
                scaleX = scale; scaleY = scale; translationX = offset.x; translationY = offset.y
            },
            contentScale = ContentScale.Fit,
        )
        else Text(fileName(ref.path), color = Color.White)
        val note = when (b) {
            Big.Loading -> null
            is Big.Ready -> null
            Big.NotSynced -> if (ref.sync == ImageSync.IMAGE_SYNC_NAME_ONLY) "图片没有同步：只有文件名，原图在电脑上" else "只有预览，原图在电脑上"
            Big.Expired -> "大图已过期，只剩预览"
            Big.Offline -> "连上后加载"
            is Big.Failed -> "没能下载：${b.why} · 点按重试"
            is Big.Corrupt -> "图片校验失败：${b.why}"
        }
        if (b == Big.Loading) CircularProgressIndicator(Modifier.align(Alignment.Center).size(36.dp), color = Color.White)
        if (note != null) Text(
            note,
            Modifier.align(Alignment.TopCenter).padding(top = 72.dp).background(Color(0xCC000000), RoundedCornerShape(10.dp))
                .clickable(enabled = b is Big.Failed || b == Big.Offline) { attempt++ }.padding(horizontal = 12.dp, vertical = 8.dp),
            color = Color.White, style = MaterialTheme.typography.bodyMedium,
        )
    }
}

private suspend fun load(remote: Remote, sid: String, img: SessionImage, online: Boolean): Big {
    val ref = img.ref
    if (ref.sync != ImageSync.IMAGE_SYNC_SYNCED || ref.imageId.isEmpty()) return Big.NotSynced
    ViewerImages.get(remote, "$sid/${ref.imageId}")?.let { return it }
    return try {
        when (val r = remote.image(sid, ref.imageId)) {
            is Remote.ImageResult.Loaded -> {
                val bytes = r.payload.data.toByteArray()
                val bmp = withContext(Dispatchers.Default) { BitmapFactory.decodeByteArray(bytes, 0, bytes.size) } ?: return Big.Corrupt("解不出图片")
                Big.Ready(bmp.asImageBitmap(), bytes, r.payload.format).also { ViewerImages.put(remote, "$sid/${ref.imageId}", it) }
            }
            Remote.ImageResult.Expired -> Big.Expired
            Remote.ImageResult.Missing -> Big.NotSynced
            is Remote.ImageResult.Corrupt -> Big.Corrupt(r.why)
        }
    } catch (e: kotlin.coroutines.cancellation.CancellationException) {
        throw e
    } catch (e: Exception) {
        if (!online) Big.Offline else Big.Failed(errorText(e))
    }
}

private fun ext(f: ImageFormat) = when (f) {
    ImageFormat.IMAGE_FORMAT_PNG -> "png"
    ImageFormat.IMAGE_FORMAT_WEBP -> "webp"
    ImageFormat.IMAGE_FORMAT_GIF -> "gif"
    else -> "jpg"
}

private fun mime(f: ImageFormat) = "image/" + when (f) {
    ImageFormat.IMAGE_FORMAT_JPEG, ImageFormat.IMAGE_FORMAT_UNSPECIFIED -> "jpeg"
    else -> ext(f)
}

/** 保存 and 分享: the display copy (or the preview when that is all there is) leaves the app here, by the user's choice. */
@Composable
private fun ViewerActions(remote: Remote, sid: String, img: SessionImage) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var msg by remember(img.ref.imageId) { mutableStateOf("") }
    fun bytes(): Pair<ByteArray, ImageFormat>? {
        ViewerImages.get(remote, "$sid/${img.ref.imageId}")?.let { return it.bytes to it.format }
        val p = img.ref.preview.toByteArray()
        return if (p.isEmpty()) null else p to img.ref.previewFormat
    }
    fun doSave() {
        val b = bytes() ?: return
        scope.launch {
            msg = try {
                withContext(Dispatchers.IO) { save(context, b.first, b.second, fileName(img.ref.path)) }
                "已保存到相册"
            } catch (e: Exception) {
                "没能保存：${errorText(e)}"
            }
        }
    }
    // Android 8 and 9 need the storage permission to add to the gallery; 10 and later do not.
    val askStorage = androidx.activity.compose.rememberLauncherForActivityResult(androidx.activity.result.contract.ActivityResultContracts.RequestPermission()) { ok ->
        if (ok) doSave() else msg = "没有存储权限，没能保存"
    }
    Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
        TextButton(onClick = {
            if (needsStoragePermission(Build.VERSION.SDK_INT) &&
                androidx.core.content.ContextCompat.checkSelfPermission(context, android.Manifest.permission.WRITE_EXTERNAL_STORAGE) != android.content.pm.PackageManager.PERMISSION_GRANTED
            ) askStorage.launch(android.Manifest.permission.WRITE_EXTERNAL_STORAGE)
            else doSave()
        }) { Text("保存", color = Color(0xFF9EC1FF)) }
        TextButton(onClick = {
            val b = bytes() ?: return@TextButton
            scope.launch {
                try {
                    val uri = withContext(Dispatchers.IO) { shareFile(context, b.first, b.second) }
                    context.startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).setType(mime(b.second)).putExtra(Intent.EXTRA_STREAM, uri).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION), "分享图片"))
                } catch (e: Exception) {
                    msg = "没能分享：${errorText(e)}"
                }
            }
        }) { Text("分享", color = Color(0xFF9EC1FF)) }
        if (msg.isNotEmpty()) Text(msg, color = Color.White, style = MaterialTheme.typography.bodySmall)
    }
}

private fun save(context: Context, data: ByteArray, format: ImageFormat, name: String) {
    val base = name.substringBeforeLast('.').ifEmpty { "remote-agent" }
    val values = ContentValues().apply {
        put(MediaStore.Images.Media.DISPLAY_NAME, "$base-${System.currentTimeMillis()}.${ext(format)}")
        put(MediaStore.Images.Media.MIME_TYPE, mime(format))
        if (Build.VERSION.SDK_INT >= 29) {
            put(MediaStore.Images.Media.RELATIVE_PATH, "Pictures/remote-agent")
            put(MediaStore.Images.Media.IS_PENDING, 1)
        }
    }
    val resolver = context.contentResolver
    val uri = resolver.insert(MediaStore.Images.Media.EXTERNAL_CONTENT_URI, values) ?: throw IllegalStateException("相册不可写")
    resolver.openOutputStream(uri)?.use { it.write(data) } ?: throw IllegalStateException("相册不可写")
    if (Build.VERSION.SDK_INT >= 29) {
        values.clear()
        values.put(MediaStore.Images.Media.IS_PENDING, 0)
        resolver.update(uri, values, null, null)
    }
}

/** A file for the share sheet; shared files are deleted when the app starts again. */
private fun shareFile(context: Context, data: ByteArray, format: ImageFormat): android.net.Uri {
    val dir = File(context.cacheDir, SHARED_DIR).apply { mkdirs() }
    val f = File(dir, "image-${System.currentTimeMillis()}.${ext(format)}")
    f.writeBytes(data)
    return FileProvider.getUriForFile(context, context.packageName + ".files", f)
}

const val SHARED_DIR = "shared"

/** Saving to the gallery asks for the storage permission before Android 10 only. */
fun needsStoragePermission(sdk: Int): Boolean = sdk < 29
