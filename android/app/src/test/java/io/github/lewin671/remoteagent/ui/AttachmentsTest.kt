package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalDecision
import io.github.lewin671.remoteagent.proto.ApprovalRequested
import io.github.lewin671.remoteagent.proto.ApprovalResolved
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.ImageFormat
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.ToolStarted
import io.github.lewin671.remoteagent.proto.TurnStarted
import kotlinx.coroutines.asCoroutineDispatcher
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** Sealed files in memory, as the pending store keeps them. */
class MemoryFiles : SealedFiles {
    val data = HashMap<String, ByteArray>()

    /** Set to have every write fail, as a full disk would. */
    var full = false

    /** How many more writes there is room for; null for any number. */
    var room: Int? = null

    /** Every write that went in, in order. */
    val puts = ArrayList<Pair<String, ByteArray>>()

    @Synchronized override fun put(folder: String, name: String, data: ByteArray) {
        check(!full && room != 0) { "磁盘已满" }
        room = room?.let { it - 1 }
        this.data["$folder/$name"] = data
        puts += "$folder/$name" to data
    }
    @Synchronized override fun get(folder: String, name: String) = data["$folder/$name"]
    @Synchronized override fun remove(folder: String, name: String) {
        data.remove("$folder/$name")
    }
    @Synchronized override fun clearFolder(folder: String) {
        data.keys.removeAll { it.startsWith("$folder/") }
    }
}

class AttachmentsTest {
    private fun img(id: String): PendingImage = PendingImage(id, "content://picker/$id").apply {
        processed = Processed(byteArrayOf(1, 2, id.length.toByte()), ImageFormat.IMAGE_FORMAT_JPEG, 10, 20)
        state = PendingImage.State.Ready
        machineId = "mac"
        imageId = "img-$id"
    }

    @Test
    fun aRestoredDraftKeepsThePickOrder() {
        val files = MemoryFiles()
        // Random-looking IDs in an order no listing would give back.
        val picked = listOf(img("zz"), img("aa"), img("mm"))
        saveImages(files, "session:s", picked, original = true)
        saveOrder(files, "session:s", picked, original = true)
        val d = ImageDraft("session:s", files)
        d.restore { null }
        assertEquals(listOf("zz", "aa", "mm"), d.images.map { it.id })
        assertTrue(d.original)
    }

    @Test
    fun aSentMessagesImagesStayOnDiskUntilTheCliHasThem() {
        val files = MemoryFiles()
        val o = Outgoing("r1", "看这两张", listOf("img-a", "img-b"), listOf(img("a"), img("b")), original = false)
        OutgoingRecords(files).save("s", o, false)
        // The process is gone; a new run finds it, images in order, outcome unknown.
        val back = OutgoingRecords(files).load("s").single()
        assertEquals("r1", back.requestId)
        assertEquals("看这两张", back.text)
        assertEquals(listOf("img-a", "img-b"), back.imageIds)
        assertEquals(listOf("a", "b"), back.images.map { it.id })
        assertEquals(Outgoing.State.UNKNOWN, back.state)
        OutgoingRecords(files).remove("s", "r1")
        assertTrue(OutgoingRecords(files).load("s").isEmpty())
        assertTrue(files.data.keys.none { it.startsWith("out:s:r1/") })
    }

    @Test
    fun submittedAndCanceledMessagesAreLetGo() {
        val a = Outgoing("a", "x", images = listOf(img("1")))
        val b = Outgoing("b", "y", images = listOf(img("2")))
        val c = Outgoing("c", "z")
        val done = recorded(listOf(a, b, c), mapOf("a" to InputStatus.INPUT_STATUS_SUBMITTED, "b" to InputStatus.INPUT_STATUS_CANCELED))
        assertEquals(listOf(a, b), done)
    }

    @Test
    fun sendingTakesOnlyItsImagesAndUntickesOriginal() {
        val files = MemoryFiles()
        val d = ImageDraft("new:/p", files)
        val sent = listOf(img("a"), img("b"))
        d.attach(sent, 6, wereOriginal = true)
        assertTrue(d.original)
        val later = img("c")
        d.images += later
        d.detach(sent)
        assertEquals(listOf("c"), d.images.map { it.id })
        // Still one image: 原图 stays for it; once the tray is empty it goes back to unticked.
        d.detach(listOf(later))
        assertFalse(d.original)
    }

    @Test
    fun puttingBackNeverGoesPastTheLimitNorDropsImages() {
        val d = ImageDraft("session:s", MemoryFiles())
        d.attach((1..4).map { img("have$it") }, 6)
        val back = (1..3).map { img("back$it") }
        assertFalse(d.attach(back, 6))
        assertEquals(4, d.images.size)
        assertTrue(d.notice.contains("超过 6 张"))
        assertTrue(d.attach(back.take(2), 6))
        assertEquals(6, d.images.size)
    }

    @Test
    fun anUploadToAnotherMachineIsReplacedByTheLatestTarget() {
        val i = img("a")
        i.state = PendingImage.State.Uploading(0.5f)
        i.machineId = "old"
        assertTrue(needsUpload(i, "new"))
        assertFalse(needsUpload(i, "old"))
        i.state = PendingImage.State.Ready
        i.machineId = "new"
        assertFalse(needsUpload(i, "new"))
    }


    @Test
    fun theOrderFileAlwaysListsTheWholeSelection() {
        val files = MemoryFiles()
        val d = ImageDraft("session:s", files, kotlinx.coroutines.Dispatchers.Unconfined)
        val ready = img("a")
        val preparing = PendingImage("b", "content://picker/b")
        d.attach(listOf(ready), 6)
        d.images += preparing
        // A save that runs while b is still being prepared keeps b in the selection.
        d.detach(emptyList())
        val order = String(files.get("session:s", "order")!!).split("\n")
        assertEquals(listOf("a", "b"), order.drop(1))
    }

    @Test
    fun everyOrderWrittenListsTheImagesAlreadyKept() {
        val files = MemoryFiles()
        val d = ImageDraft("session:s", files)
        d.attach(listOf(img("a")), 6)
        // A second image is prepared: only it is written, and the order still names the first at every moment.
        d.images += img("b")
        d.detach(emptyList())
        assertEquals(listOf("0\na", "0\na\nb"), files.puts.filter { it.first == "session:s/order" }.map { String(it.second) })
        assertEquals(1, files.puts.count { it.first == "session:s/a.img" })
    }

    @Test
    fun anImageStillBeingPreparedAgainIsNotTakenForSaved() {
        val files = MemoryFiles()
        val d = ImageDraft("session:s", files)
        val (a, b) = img("a") to img("b")
        d.attach(listOf(a, b), 6)
        fun again(i: PendingImage) = Processed(byteArrayOf(9, 9, i.id[0].code.toByte()), ImageFormat.IMAGE_FORMAT_PNG, 4000, 20)
        // 原图: both are read again; the first is ready and saved while the second still is.
        d.invalidate(a)
        d.invalidate(b)
        val old = files.get("session:s", "b.img")!!
        a.processed = again(a)
        d.prepared(a)
        assertTrue(files.get("session:s", "a.img")!!.contentEquals(again(a).bytes))
        assertTrue(files.get("session:s", "b.img")!!.contentEquals(old))
        b.processed = again(b)
        d.prepared(b)
        // What a reclaimed process would find is what the draft says it sends.
        assertTrue(files.get("session:s", "b.img")!!.contentEquals(again(b).bytes))
    }

    @Test
    fun anImageRemovedBeforeItsSaveRanLeavesNoFiles() {
        val files = MemoryFiles()
        // Saves wait their turn on one thread, which is held up while the image is removed.
        val thread = java.util.concurrent.Executors.newSingleThreadExecutor()
        val saves = thread.asCoroutineDispatcher()
        val held = java.util.concurrent.CountDownLatch(1)
        thread.execute { held.await() }
        val d = ImageDraft("session:s", files, saves, kotlinx.coroutines.CoroutineScope(saves))
        val a = img("a")
        d.attach(listOf(a, img("b")), 6)
        d.remove(a)
        held.countDown()
        thread.submit {}.get()
        assertEquals(setOf("session:s/b.img", "session:s/b.meta", "session:s/order"), files.data.keys.toSet())
        assertEquals("0\nb", String(files.get("session:s", "order")!!))
        // The same for a draft dropped whole.
        d.attach(listOf(img("c")), 6)
        d.clear()
        thread.submit {}.get()
        assertTrue(files.data.isEmpty())
        thread.shutdown()
    }

    @Test
    fun aMessageThatCouldNotBeKeptWholeLeavesNothingBehind() {
        val files = MemoryFiles()
        val o = Outgoing("r1", "看这两张", listOf("img-a", "img-b"), listOf(img("a"), img("b")))
        // Room for the first image and half of the second, as near the store's limit.
        files.room = 3
        assertTrue(runCatching { OutgoingRecords(files).save("s", o, false) }.isFailure)
        assertTrue(files.data.isEmpty())
        // Everything but the index went in: the same.
        files.room = 8
        assertTrue(runCatching { OutgoingRecords(files).save("s", o, false) }.isFailure)
        assertTrue(files.data.isEmpty())
        files.room = null
        OutgoingRecords(files).save("s", o, false)
        assertEquals(listOf("r1"), OutgoingRecords(files).load("s").map { it.requestId })
    }

    @Test
    fun whatWaitsForImagesToBeKeptHearsOnceTheyAreOnDiskOrGone() {
        val files = MemoryFiles()
        val thread = java.util.concurrent.Executors.newSingleThreadExecutor()
        val saves = thread.asCoroutineDispatcher()
        val held = java.util.concurrent.CountDownLatch(1)
        thread.execute { held.await() }
        val d = ImageDraft("session:s", files, saves, kotlinx.coroutines.CoroutineScope(saves))
        val heard = java.util.concurrent.CopyOnWriteArrayList<String>()
        // The save has not run: the images are in the draft but not on disk, so their copy elsewhere must stay.
        val (a, b) = img("a") to img("b")
        d.attach(listOf(a, b), 6)
        d.whenKept(listOf("a", "b")) { heard += "both" }
        d.whenKept(listOf("a")) { heard += "a" }
        assertTrue(heard.isEmpty())
        // Removed: nothing of it is left to keep.
        d.remove(a)
        assertEquals(listOf("a"), heard.toList())
        held.countDown()
        for (i in 0 until 2000) if (heard.size < 2) Thread.sleep(1)
        assertEquals(listOf("a", "both"), heard.toList())
        assertTrue(files.get("session:s", "b.img") != null)
        // Already there: said at once.
        d.whenKept(listOf("b")) { heard += "again" }
        assertEquals(listOf("a", "both", "again"), heard.toList())
        thread.shutdown()
    }
}
