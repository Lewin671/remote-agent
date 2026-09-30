package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.content.Intent
import android.graphics.BitmapFactory
import android.net.Uri
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.MAX_IMAGES
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.capabilities
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.newRequestId
import io.github.lewin671.remoteagent.core.takePicked
import io.github.lewin671.remoteagent.core.uploadImage
import io.github.lewin671.remoteagent.proto.ImageFormat
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * One image picked for a message. It is processed on the phone, then sent
 * to the machine the message goes to; changing machines sends it again.
 * The processed bytes stay (encrypted on disk) until the message is sent,
 * so nothing has to be picked again.
 */
class PendingImage(val id: String, val uri: Uri?) {
    sealed interface State {
        data object Processing : State
        data class Uploading(val progress: Float) : State
        data object Ready : State
        data class Failed(val error: String) : State
    }

    var processed by mutableStateOf<Processed?>(null)
    var thumb by mutableStateOf<ImageBitmap?>(null)
    var state by mutableStateOf<State>(State.Processing)

    /** The machine it was sent to and the image ID the daemon gave it there. */
    var machineId: String? = null
    var imageId: String? = null
    var transferId: String = newRequestId()
    internal var job: Job? = null

    fun readyFor(machine: String) = state == State.Ready && machineId == machine && imageId != null
}

/** Whether a machine takes images, and if not, why, in words. */
fun uploadBlocked(m: MachineView?, online: Boolean, name: String): String? {
    val caps = capabilities(m)
    return when {
        m == null -> "先选一台电脑"
        caps == null || !caps.imageUpload -> "$name 上的 remote-agent 版本不支持发图片，更新后才能添加"
        !m.online || !online -> "$name 离线，连上后才能添加图片"
        else -> null
    }
}

/** The most images a message may carry to this machine: six, or fewer if the daemon says so. */
fun maxImages(m: MachineView?): Int = capabilities(m)?.maxImagesPerMessage?.takeIf { it in 1 until MAX_IMAGES } ?: MAX_IMAGES

/** The images of one draft (a session's message box or a project's new-session card). */
class ImageDraft(val key: String) {
    val images = mutableStateListOf<PendingImage>()
    var original by mutableStateOf(false)
        private set

    /** Said once after a pick: how many did not fit. */
    var notice by mutableStateOf("")

    private val store get() = RaApp.instance.pendingStore

    fun add(context: Context, picked: List<Uri>, max: Int, remote: Remote, machine: () -> String?) {
        val (taken, left) = takePicked(images.size, picked, max)
        notice = if (left > 0) "一条消息最多 $max 张，多选的 $left 张没有加入" else ""
        for (uri in taken) {
            // Kept readable for "原图" later; the picker grant may not last.
            runCatching { context.contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION) }
            val img = PendingImage(newRequestId(), uri)
            images += img
            process(context, img, remote, machine)
        }
    }

    private fun process(context: Context, img: PendingImage, remote: Remote, machine: () -> String?) {
        img.job?.cancel()
        img.state = PendingImage.State.Processing
        img.machineId = null
        img.imageId = null
        img.transferId = newRequestId()
        img.job = RaApp.instance.scope.launch {
            try {
                val p = withContext(Dispatchers.Default) { processImage(context, img.uri!!, original) }
                img.processed = p
                img.thumb = withContext(Dispatchers.Default) { thumbnail(p.bytes) }
                withContext(Dispatchers.IO) { save(img, p) }
                machine()?.let { upload(remote, img, it) } ?: run { img.state = PendingImage.State.Failed("先选一台电脑") }
            } catch (e: kotlin.coroutines.cancellation.CancellationException) {
                throw e
            } catch (e: Exception) {
                img.state = PendingImage.State.Failed(errorText(e))
            }
        }
    }

    /** Sends the image to [machineId] unless it is there already. */
    fun upload(remote: Remote, img: PendingImage, machineId: String) {
        val p = img.processed ?: return
        if (img.readyFor(machineId) || img.state is PendingImage.State.Uploading) return
        if (img.machineId != machineId) {
            img.transferId = newRequestId()
            img.imageId = null
        }
        img.machineId = machineId
        img.state = PendingImage.State.Uploading(0f)
        img.job = RaApp.instance.scope.launch {
            try {
                val id = try {
                    uploadImage(remote.uploadChannel(machineId), img.transferId, p.bytes) { done, total ->
                        img.state = PendingImage.State.Uploading(if (total > 0) done.toFloat() / total else 0f)
                    }
                } catch (e: CommandException) {
                    // The transfer expired on the daemon: start a new one.
                    if (e.code != "not_found") throw e
                    img.transferId = newRequestId()
                    uploadImage(remote.uploadChannel(machineId), img.transferId, p.bytes)
                }
                if (img.machineId == machineId) {
                    img.imageId = id
                    img.state = PendingImage.State.Ready
                }
            } catch (e: kotlin.coroutines.cancellation.CancellationException) {
                throw e
            } catch (e: Exception) {
                img.state = PendingImage.State.Failed(errorText(e))
            }
        }
    }

    /** Makes sure every image is on [machineId], e.g. after the new-session card changed machines. */
    fun sendAllTo(remote: Remote, machineId: String) {
        for (img in images) if (img.processed != null && !img.readyFor(machineId) && img.state !is PendingImage.State.Processing) upload(remote, img, machineId)
    }

    fun retry(context: Context, img: PendingImage, remote: Remote, machine: () -> String?) {
        if (img.processed == null && img.uri != null) process(context, img, remote, machine)
        else machine()?.let { upload(remote, img, it) }
    }

    /** Originals or compressed, for the whole message: every image is prepared again from what was picked. */
    fun setOriginal(context: Context, v: Boolean, remote: Remote, machine: () -> String?) {
        if (v == original) return
        original = v
        for (img in images) if (img.uri != null) process(context, img, remote, machine)
    }

    fun remove(img: PendingImage) {
        img.job?.cancel()
        images.remove(img)
        store.remove(key, img.id + ".img")
        store.remove(key, img.id + ".meta")
        img.uri?.let { u -> runCatching { RaApp.instance.contentResolver.releasePersistableUriPermission(u, Intent.FLAG_GRANT_READ_URI_PERMISSION) } }
    }

    fun ready(machineId: String?) = machineId != null && images.all { it.readyFor(machineId) }

    fun ids(machineId: String): List<String> = images.mapNotNull { if (it.readyFor(machineId)) it.imageId else null }

    val totalBytes: Long get() = images.sumOf { it.processed?.bytes?.size?.toLong() ?: 0L }

    /** Forgets the images once their message was accepted. */
    fun clear() {
        for (img in images.toList()) remove(img)
        notice = ""
        original = false
        store.clearDraft(key)
    }

    private fun save(img: PendingImage, p: Processed) {
        store.put(key, img.id + ".img", p.bytes)
        val meta = listOf(p.format.number, p.width, p.height, if (original) 1 else 0, img.uri?.toString().orEmpty(), p.note).joinToString("\n")
        store.put(key, img.id + ".meta", meta.toByteArray())
    }

    /** Images kept from before the process was reclaimed; they go to the machine again. */
    internal fun restore() {
        val ids = store.names(key).filter { it.endsWith(".meta") }.map { it.removeSuffix(".meta") }
        for (id in ids) {
            val meta = store.get(key, "$id.meta")?.toString(Charsets.UTF_8)?.split("\n") ?: continue
            val bytes = store.get(key, "$id.img") ?: continue
            if (meta.size < 6) continue
            val img = PendingImage(id, meta[4].takeIf { it.isNotEmpty() }?.let(Uri::parse))
            img.processed = Processed(bytes, ImageFormat.forNumber(meta[0].toInt()) ?: ImageFormat.IMAGE_FORMAT_JPEG, meta[1].toInt(), meta[2].toInt(), meta[5])
            img.thumb = thumbnail(bytes)
            img.state = PendingImage.State.Failed("需要重新传到电脑")
            if (meta[3] == "1") original = true
            images += img
        }
    }
}

private fun thumbnail(bytes: ByteArray): ImageBitmap? {
    val b = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(bytes, 0, bytes.size, b)
    var s = 1
    while (maxOf(b.outWidth, b.outHeight) / (s * 2) >= 256) s *= 2
    return BitmapFactory.decodeByteArray(bytes, 0, bytes.size, BitmapFactory.Options().apply { inSampleSize = s })?.asImageBitmap()
}

/** The drafts' images for this run, restored from the encrypted store when first used. */
object ImageDrafts {
    private val drafts = HashMap<String, ImageDraft>()

    fun of(key: String): ImageDraft = drafts.getOrPut(key) { ImageDraft(key).also { runCatching { it.restore() } } }

    fun forSession(sid: String) = of("session:$sid")
    fun forProject(key: String) = of("new:$key")

    fun clear() {
        drafts.values.forEach { d -> d.images.forEach { it.job?.cancel() } }
        drafts.clear()
    }
}
