package io.github.lewin671.remoteagent.core

import java.io.File
import java.nio.file.Files
import org.junit.Assert.*
import org.junit.Test

class ImageDiskPolicyTest {
    @Test fun cacheEvictsOldestAndCountsOnlySuccessfulDeletes() {
        val dir = Files.createTempDirectory("images").toFile()
        try {
            val old = File(dir, "old").apply { writeBytes(ByteArray(8)); setLastModified(10) }
            val recent = File(dir, "recent").apply { writeBytes(ByteArray(8)); setLastModified(20) }
            assertEquals(8L, ImageDiskPolicy.trimCache(dir, 8))
            assertFalse(old.exists()); assertTrue(recent.exists())
        } finally { dir.deleteRecursively() }
    }

    @Test fun expiredDraftsGoButUnconfirmedMessagesStay() {
        val dir = Files.createTempDirectory("drafts").toFile()
        try {
            val expired = File(dir, "expired").apply { mkdirs() }
            File(expired, "image.meta").apply { writeText("sealed"); setLastModified(1) }; expired.setLastModified(1)
            // Named by the outbox, and with nothing in its folder to say so.
            val pinned = File(dir, "waiting").apply { mkdirs() }
            File(pinned, "image.meta").apply { writeText("sealed"); setLastModified(1) }; pinned.setLastModified(1)
            val fresh = File(dir, "fresh").apply { mkdirs(); setLastModified(ImageDiskPolicy.DRAFT_TTL) }
            assertEquals(1, ImageDiskPolicy.expireDrafts(dir, setOf("waiting"), ImageDiskPolicy.DRAFT_TTL + 1))
            assertFalse(expired.exists()); assertTrue(pinned.exists()); assertTrue(fresh.exists())
            // Once the outbox no longer names it, it expires like any draft.
            assertEquals(1, ImageDiskPolicy.expireDrafts(dir, emptySet(), ImageDiskPolicy.DRAFT_TTL + 1))
            assertFalse(pinned.exists())
        } finally { dir.deleteRecursively() }
    }

    @Test fun theOutboxIndexNamesTheMessagesThatWait() {
        assertEquals(listOf("out:s1:r1", "out:s2:r2"), ImageDiskPolicy.outboxKeys("s1\tr1\ns2\tr2\n"))
        assertEquals(emptyList<String>(), ImageDiskPolicy.outboxKeys(""))
        // A line that is not a record names nothing.
        assertEquals(listOf("out:s1:r1"), ImageDiskPolicy.outboxKeys("s1\tr1\nbroken\na\tb\tc"))
    }

    @Test fun fullDraftStoreRefusesNewBytesWithoutEviction() {
        val dir = Files.createTempDirectory("drafts").toFile()
        try {
            val old = File(dir, "old").apply { writeBytes(ByteArray(8)) }
            try { ImageDiskPolicy.requireRoom(dir, File(dir, "new"), 4, 10); fail("quota bypassed") } catch (_: IllegalArgumentException) {}
            assertTrue(old.exists())
            ImageDiskPolicy.requireRoom(dir, old, 9, 10) // replacement counts once
        } finally { dir.deleteRecursively() }
    }
}
