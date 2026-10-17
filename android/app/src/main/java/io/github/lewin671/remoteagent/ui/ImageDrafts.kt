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

/** Sealed files by folder and name: the pending-image store, or memory in tests. */
interface SealedFiles {
    fun <T> transaction(work: () -> T): T = synchronized(this) { work() }
    fun put(folder: String, name: String, data: ByteArray)
    fun get(folder: String, name: String): ByteArray?
    fun remove(folder: String, name: String)
    fun clearFolder(folder: String)
}

/**
 * One image picked for a message. It is processed on the phone, then sent
 * to the machine the message goes to; changing machines sends it again.
 * The processed bytes stay (encrypted on disk) until the CLI has the
 * message, so nothing has to be picked again.
 */
class PendingImage(val id: String, val uri: String?) {
    sealed interface State {
        data object Processing : State
        data class Uploading(val progress: Float) : State
        data object Ready : State
        data class Failed(val error: String) : State
    }

    var processed by mutableStateOf<Processed?>(null)
    /** Old bytes may stay for recovery, but cannot be uploaded for the new mode. */
    internal var stale by mutableStateOf(false)
    val needsProcessing: Boolean get() = processed == null || stale
    var thumb by mutableStateOf<ImageBitmap?>(null)
    var state by mutableStateOf<State>(State.Processing)

    /** The machine it goes to (the latest asked for) and the image ID the daemon gave it there. */
    var machineId: String? = null
    var imageId: String? = null
    var expiresMs by mutableStateOf(0L)
        internal set
    var transferId: String = newRequestId()
    internal var job: Job? = null

    fun readyFor(machine: String, now: Long = System.currentTimeMillis()) =
        !needsProcessing && state == State.Ready && machineId == machine && imageId != null && expiresMs > now

    internal fun uploaded(result: io.github.lewin671.remoteagent.core.UploadedImage) {
        imageId = result.imageId
        expiresMs = result.expiresMs
        state = State.Ready
    }

    /** Lets go of the bytes, the thumbnail and the picker's grant. */
    fun release() {
        job?.cancel()
        processed = null
        thumb = null
        uri?.let { u -> runCatching { RaApp.instance.contentResolver.releasePersistableUriPermission(Uri.parse(u), Intent.FLAG_GRANT_READ_URI_PERMISSION) } }
    }
}

/**
 * Whether [img] must (again) be sent to [machineId]: not when it is there
 * or already on its way there. An upload to another machine does not
 * count: the latest target wins and the old upload is dropped.
 */
fun needsUpload(img: PendingImage, machineId: String): Boolean =
    !img.needsProcessing && !img.readyFor(machineId) && !(img.state is PendingImage.State.Uploading && img.machineId == machineId)

/** Whether a machine takes images, and if not, why, in words. */
fun uploadBlocked(m: MachineView?, online: Boolean, name: String): String? {
    val caps = capabilities(m)
    return when {
        m == null -> "先选一台电脑"
        caps == null -> "还没读到 $name 的信息，稍后才能添加图片"
        // The daemon's folder has spaces in its path, which the CLIs do not take.
        !caps.imageUpload -> "$name 上 remote-agent 的目录路径带空格，不能发图片"
        !m.online || !online -> "$name 离线，连上后才能添加图片"
        else -> null
    }
}

/** The most images a message may carry to this machine: six, or fewer if the daemon says so. */
fun maxImages(m: MachineView?): Int = capabilities(m)?.maxImagesPerMessage?.takeIf { it in 1 until MAX_IMAGES } ?: MAX_IMAGES

private const val ORDER = "order"

/** Writes images under [folder]. Which of the folder's images belong to it, and in what order, is [saveOrder]'s to say. */
internal fun saveImages(files: SealedFiles, folder: String, images: List<PendingImage>, original: Boolean) {
    for (img in images) {
        val p = img.processed ?: continue
        files.put(folder, img.id + ".img", p.bytes)
        val meta = listOf(p.format.number, p.width, p.height, if (original) 1 else 0, img.uri.orEmpty(), p.note).joinToString("\n")
        files.put(folder, img.id + ".meta", meta.toByteArray())
    }
}

/**
 * Writes the order file: the whole selection as picked, so a restore keeps
 * it. Written once per save, after the images it lists, so that it never
 * names fewer than are kept.
 */
internal fun saveOrder(files: SealedFiles, folder: String, images: List<PendingImage>, original: Boolean) {
    files.put(folder, ORDER, (listOf(if (original) "1" else "0") + images.map { it.id }).joinToString("\n").toByteArray())
}

/** Images kept under [folder], in the order they were picked, and whether they were originals. */
internal fun loadImages(files: SealedFiles, folder: String, thumb: (ByteArray) -> ImageBitmap? = { null }): Pair<List<PendingImage>, Boolean> {
    val order = files.get(folder, ORDER)?.toString(Charsets.UTF_8)?.split("\n") ?: return emptyList<PendingImage>() to false
    val original = order.firstOrNull() == "1"
    val out = mutableListOf<PendingImage>()
    for (id in order.drop(1).filter { it.isNotEmpty() }) {
        val meta = files.get(folder, "$id.meta")?.toString(Charsets.UTF_8)?.split("\n") ?: continue
        val bytes = files.get(folder, "$id.img") ?: continue
        if (meta.size < 6) continue
        val img = PendingImage(id, meta[4].takeIf { it.isNotEmpty() })
        img.processed = Processed(bytes, ImageFormat.forNumber(meta[0].toInt()) ?: ImageFormat.IMAGE_FORMAT_JPEG, meta[1].toInt(), meta[2].toInt(), meta[5])
        img.stale = (meta[3] == "1") != original
        img.thumb = thumb(bytes)
        // Uploads are the daemon's for a while only: send them again.
        img.state = PendingImage.State.Failed(if (img.stale) "需要按当前模式重新处理" else "需要重新传到电脑")
        out += img
    }
    return out to original
}

/** The images of one draft (a session's message box or a project's new-session card). */
class ImageDraft(
    val key: String,
    private val files: SealedFiles = RaApp.instance.pendingStore.scoped(),
    /**
     * Where saves and removals run, one at a time for this draft, so that an
     * older snapshot never lands after a newer one nor after the removal of
     * what it wrote.
     */
    private val saves: kotlinx.coroutines.CoroutineDispatcher = Dispatchers.IO.limitedParallelism(1),
    /** What they run in; without one (tests) they run in place. */
    private val scope: kotlinx.coroutines.CoroutineScope? = RaApp.scopeOrNull(),
) {
    val images = mutableStateListOf<PendingImage>()
    var original by mutableStateOf(false)
        private set

    /** Said once after a pick or a put back: how many did not fit. */
    var notice by mutableStateOf("")

    fun add(context: Context, picked: List<Uri>, max: Int, remote: Remote, machine: () -> String?) {
        val (taken, left) = takePicked(images.size, picked, max)
        notice = if (left > 0) "一条消息最多 $max 张，多选的 $left 张没有加入" else ""
        for (uri in taken) {
            // Kept readable for "原图" later; the picker grant may not last.
            runCatching { context.contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION) }
            val img = PendingImage(newRequestId(), uri.toString())
            images += img
            process(context, img, remote, machine)
        }
        persistOrder()
    }

    /**
     * Saves the draft as it is when the save runs: every prepared image and
     * the order of the whole selection. Saves run one at a time, so an
     * older snapshot never lands after a newer one.
     */
    private fun persistOrder() = enqueue { saveNow() }

    /** Runs [work] after the saves and removals asked for before it. */
    private fun enqueue(work: () -> Unit) {
        scope?.launch(saves) { work() } ?: work()
    }

    // The three below are read and changed under `marks`, by the main thread and by saves.
    private val marks = Any()

    /** Images whose current bytes are on disk; an image prepared again is written again. */
    private val saved = HashSet<String>()

    /** Images being prepared again: `processed` still holds what they were before. */
    private val stale = HashSet<String>()

    /** How often each image was prepared again or taken out: a save that began before does not answer for it. */
    private val turns = HashMap<String, Int>()

    /** What waits for images to be on disk here or gone from the draft ([whenKept]); the main thread's. */
    private val waiting = ArrayList<Pair<List<String>, () -> Unit>>()

    private fun saveNow() {
        runCatching {
            val now = images.toList()
            val fresh = synchronized(marks) { now.filter { it.id !in saved && it.id !in stale && it.processed != null }.map { it to (turns[it.id] ?: 0) } }
            saveImages(files, key, fresh.map { it.first }, original)
            saveOrder(files, key, now, original)
            synchronized(marks) { for ((img, turn) in fresh) if ((turns[img.id] ?: 0) == turn) saved += img.id }
        }.onFailure {
            notice = "这张图未保存到手机：${errorText(it)}。当前仍可发送，关闭应用后可能丢失"
            android.util.Log.w("remote-agent", "pending images not kept: ${it.javaClass.simpleName}")
        }
        scope?.launch { settle() } ?: settle()
    }

    /**
     * Calls [then] once none of the images [ids] needs a copy kept
     * elsewhere: each is on disk here, or gone from the draft. A save that
     * fails keeps it waiting for the next one.
     */
    fun whenKept(ids: List<String>, then: () -> Unit) {
        waiting += ids to then
        settle()
    }

    private fun settle() {
        val ready = waiting.filter { (ids, _) ->
            ids.all { id ->
                // One without bytes has nothing a copy elsewhere could hold either.
                val img = images.firstOrNull { it.id == id }
                img?.processed == null || synchronized(marks) { id in saved }
            }
        }
        waiting.removeAll(ready.toSet())
        for ((_, then) in ready) then()
    }

    /** An image is being prepared again, or is taken out: its bytes on disk are not its bytes any more. */
    private fun unmark(img: PendingImage, preparing: Boolean) = synchronized(marks) {
        saved -= img.id
        if (preparing) stale += img.id else stale -= img.id
        turns[img.id] = (turns[img.id] ?: 0) + 1
    }

    /** An image is being prepared again: its bytes, in memory and on disk, are the old ones until [prepared]. */
    internal fun invalidate(img: PendingImage) {
        img.stale = true
        unmark(img, preparing = true)
    }

    /** An image has the bytes it was prepared for: they are saved. */
    internal fun prepared(img: PendingImage) {
        img.stale = false
        synchronized(marks) { stale -= img.id }
        persistOrder()
    }

    private fun process(context: Context, img: PendingImage, remote: Remote, machine: () -> String?) {
        img.job?.cancel()
        invalidate(img)
        img.state = PendingImage.State.Processing
        img.machineId = null
        img.imageId = null
        img.transferId = newRequestId()
        img.job = RaApp.instance.scope.launch {
            try {
                val p = withContext(Dispatchers.Default) { processImage(context, Uri.parse(img.uri!!), original) }
                img.processed = p
                img.thumb = withContext(Dispatchers.Default) { thumbnail(p.bytes) }
                // Kept for a reclaimed process; sending does not depend on it.
                prepared(img)
                machine()?.let { upload(remote, img, it) } ?: run { img.state = PendingImage.State.Failed("先选一台电脑") }
            } catch (e: kotlin.coroutines.cancellation.CancellationException) {
                throw e
            } catch (e: Exception) {
                img.state = PendingImage.State.Failed(errorText(e))
            }
        }
    }

    /**
     * Sends the image to [machineId] unless it is there or on its way there.
     * An upload to another machine is dropped: the latest target wins.
     */
    fun upload(remote: Remote, img: PendingImage, machineId: String) {
        val p = img.processed ?: return
        if (!needsUpload(img, machineId)) return
        img.job?.cancel()
        if (img.machineId != machineId || (img.imageId != null && img.expiresMs <= System.currentTimeMillis())) {
            img.transferId = newRequestId()
            img.imageId = null
        }
        img.machineId = machineId
        img.expiresMs = 0
        img.state = PendingImage.State.Uploading(0f)
        img.job = RaApp.instance.scope.launch {
            try {
                val id = try {
                    uploadImage(remote.uploadChannel(machineId), img.transferId, p.bytes) { done, total ->
                        if (img.machineId == machineId) img.state = PendingImage.State.Uploading(if (total > 0) done.toFloat() / total else 0f)
                    }
                } catch (e: CommandException) {
                    // The transfer expired on the daemon: start a new one.
                    if (e.code != "not_found") throw e
                    img.transferId = newRequestId()
                    uploadImage(remote.uploadChannel(machineId), img.transferId, p.bytes)
                }
                if (img.machineId == machineId) {
                    img.uploaded(id)
                }
            } catch (e: kotlin.coroutines.cancellation.CancellationException) {
                throw e
            } catch (e: Exception) {
                if (img.machineId == machineId) img.state = PendingImage.State.Failed(errorText(e))
            }
        }
    }

    /** Makes sure every image is on [machineId], e.g. after the new-session card changed machines. */
    fun sendAllTo(remote: Remote, machineId: String) {
        for (img in images) if (!img.needsProcessing && !img.readyFor(machineId) && img.state !is PendingImage.State.Processing) upload(remote, img, machineId)
    }

    fun retry(context: Context, img: PendingImage, remote: Remote, machine: () -> String?) {
        if (img.needsProcessing && img.uri != null) process(context, img, remote, machine)
        else machine()?.let { upload(remote, img, it) }
    }

    /** Originals or compressed, for this message: every image is prepared again from what was picked. */
    fun setOriginal(context: Context, v: Boolean, remote: Remote, machine: () -> String?) {
        if (v == original) return
        original = v
        for (img in images) if (img.uri != null) process(context, img, remote, machine)
        persistOrder()
    }

    fun remove(img: PendingImage) {
        images.remove(img)
        forget(img)
        img.release()
        persistOrder()
        settle()
    }

    /** Takes an image's files away, after any save of it that is still on its way. */
    private fun forget(img: PendingImage) {
        unmark(img, preparing = false)
        enqueue {
            runCatching {
                files.remove(key, img.id + ".img")
                files.remove(key, img.id + ".meta")
            }
        }
    }

    fun ready(machineId: String?) = machineId != null && images.isNotEmpty() && images.all { it.readyFor(machineId) }

    fun ids(machineId: String): List<String> = images.mapNotNull { if (it.readyFor(machineId)) it.imageId else null }

    val totalBytes: Long get() = images.sumOf { it.processed?.bytes?.size?.toLong() ?: 0L }

    /**
     * Takes [taken] out of the draft once a message carries them; images
     * picked since stay. 原图 goes back to unticked: it is for one message.
     * The caller keeps the images (see [Outbox]) until the CLI has them.
     */
    fun detach(taken: List<PendingImage>) {
        images.removeAll(taken.toSet())
        for (img in taken) forget(img)
        if (images.isEmpty()) original = false
        persistOrder()
        settle()
    }

    /**
     * Puts images back, e.g. from a message that was not submitted. Nothing
     * is dropped: when they do not fit with what is here already, nothing
     * is put back and [notice] says why. Returns whether they went in.
     */
    fun attach(back: List<PendingImage>, max: Int, wereOriginal: Boolean = original): Boolean {
        if (images.size + back.size > max) {
            notice = "输入框里已有 ${images.size} 张图，放回这 ${back.size} 张会超过 $max 张。先移除一些再放回"
            return false
        }
        if (images.isEmpty()) original = wereOriginal
        for (img in back) {
            img.imageId = null
            img.machineId = null
            img.state = PendingImage.State.Failed("需要重新传到电脑")
            images += img
        }
        persistOrder()
        return true
    }

    /** Forgets every image, e.g. when the draft is dropped. */
    fun clear() {
        for (img in images.toList()) remove(img)
        notice = ""
        original = false
        enqueue { runCatching { files.clearFolder(key) } }
    }

    /** Images kept from before the process was reclaimed, in the order they were picked; they go to the machine again. */
    internal fun restore(thumb: (ByteArray) -> ImageBitmap? = ::thumbnail) {
        val (list, orig) = loadImages(files, key, thumb)
        original = orig
        images += list
        synchronized(marks) { saved += list.map { it.id } }
    }
}

internal fun thumbnail(bytes: ByteArray): ImageBitmap? {
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
