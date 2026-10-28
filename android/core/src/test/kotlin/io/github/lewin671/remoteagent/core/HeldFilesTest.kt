package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class HeldFilesTest {
    private val file = SentFile.newBuilder().setFileId("f").setName("data.csv").setSizeBytes(12).build()
    private fun origin(id: String) = Origin.newBuilder().setKind(OriginKind.ORIGIN_KIND_CLIENT).setDeviceId(id).build()
    private fun event(status: InputStatus, text: String = "", withFiles: Boolean = false, mode: SendMode = SendMode.SEND_MODE_UNSPECIFIED, sender: String? = null, images: Boolean = false) =
        Event.newBuilder().setInputState(InputState.newBuilder().setRequestId("request").setStatus(status).setText(text).setMode(mode).apply {
            if (withFiles) addFiles(file)
            if (images) addImageIds("image")
        }).apply { if (sender != null) setOrigin(origin(sender)) }.build()

    @Test fun filesOnlyHeldSnapshotRestoresPayloadOriginAndModeWithoutQueuedHistory() {
        val held = event(InputStatus.INPUT_STATUS_HELD_BY_CLI, withFiles = true, mode = SendMode.SEND_MODE_QUEUE, sender = "sender")
        val d = currentActions(derive(emptyList()), LiveSnapshot.newBuilder().addActions(held).build())
        val pending = pendingInputs(d).single()
        assertEquals("", pending.text)
        assertEquals(listOf(file), pending.files)
        assertEquals(origin("sender"), pending.origin)
        assertEquals(SendMode.SEND_MODE_QUEUE, pending.mode)
        assertTrue(pending.held)
        assertEquals(listOf(file), d.accepted["request"]!!.files)
    }

    @Test fun heldPreviewCannotReplaceKnownQueuedTextOrSenderButItsLiveModeCanChange() {
        val queued = event(InputStatus.INPUT_STATUS_QUEUED, "complete original text", withFiles = true, mode = SendMode.SEND_MODE_QUEUE, sender = "original", images = true)
        val held = event(InputStatus.INPUT_STATUS_HELD_BY_CLI, "complete…", withFiles = true, mode = SendMode.SEND_MODE_STEER, sender = "snapshot")
        val d = currentActions(derive(listOf(queued)), LiveSnapshot.newBuilder().addActions(held).build())
        val pending = pendingInputs(d).single()
        assertEquals("complete original text", pending.text)
        assertEquals(origin("original"), pending.origin)
        assertEquals(SendMode.SEND_MODE_STEER, pending.mode)
        assertEquals(listOf(file), pending.files)
        val next = currentActions(d, LiveSnapshot.newBuilder().addActions(event(InputStatus.INPUT_STATUS_HELD_BY_CLI)).build())
        assertEquals(pending, pendingInputs(next).single())
        assertEquals(listOf("image"), next.inputDetails["request"]!!.imageIdsList)
    }

    @Test fun laterStatusOnlyEventsKeepHeldAttachmentsAndFillPreviouslyMissingMetadata() {
        val held = event(InputStatus.INPUT_STATUS_HELD_BY_CLI, "held text", true, SendMode.SEND_MODE_QUEUE, "sender", images = true)
        val d = derive(listOf(held, event(InputStatus.INPUT_STATUS_HELD_BY_CLI)))
        assertEquals(listOf(file), pendingInputs(d).single().files)
        assertEquals(origin("sender"), pendingInputs(d).single().origin)
        assertEquals(SendMode.SEND_MODE_QUEUE, pendingInputs(d).single().mode)
        assertEquals(listOf("image"), d.inputDetails["request"]!!.imageIdsList)
        val prior = event(InputStatus.INPUT_STATUS_QUEUED, "full queued text")
        val filled = derive(listOf(prior, held, event(InputStatus.INPUT_STATUS_HELD_BY_CLI)))
        assertEquals("full queued text", pendingInputs(filled).single().text)
        assertEquals(listOf(file), pendingInputs(filled).single().files)
    }
}
