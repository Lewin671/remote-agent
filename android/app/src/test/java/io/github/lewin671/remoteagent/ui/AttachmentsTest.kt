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
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** Sealed files in memory, as the pending store keeps them. */
class MemoryFiles : SealedFiles {
    val data = HashMap<String, ByteArray>()
    override fun put(folder: String, name: String, data: ByteArray) {
        this.data["$folder/$name"] = data
    }
    override fun get(folder: String, name: String) = data["$folder/$name"]
    override fun remove(folder: String, name: String) {
        data.remove("$folder/$name")
    }
    override fun clearFolder(folder: String) {
        data.keys.removeAll { it.startsWith("$folder/") }
    }
    override fun names(folder: String) = data.keys.filter { it.startsWith("$folder/") }.map { it.removePrefix("$folder/") }
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
    fun onlyMessagesTheCliRecordedAreLetGo() {
        val a = Outgoing("a", "x", images = listOf(img("1")))
        val b = Outgoing("b", "y", images = listOf(img("2")))
        val c = Outgoing("c", "z")
        val done = recorded(listOf(a, b, c), mapOf("a" to InputStatus.INPUT_STATUS_SUBMITTED, "b" to InputStatus.INPUT_STATUS_CANCELED))
        assertEquals(listOf(a), done)
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
    fun aDraftSavedBeforeTheOrderFileIsRestored() {
        val files = MemoryFiles()
        saveImages(files, "session:s", listOf(img("x"), img("y")), original = false)
        files.remove("session:s", "order")
        val d = ImageDraft("session:s", files, kotlinx.coroutines.Dispatchers.Unconfined)
        d.restore { null }
        assertEquals(setOf("x", "y"), d.images.map { it.id }.toSet())
        // And it has an order file now.
        assertTrue(files.get("session:s", "order") != null)
    }
}
