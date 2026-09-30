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
            val pinned = File(dir, "outbox").apply { mkdirs() }
            File(pinned, ".keep").apply { writeText(""); setLastModified(1) }; pinned.setLastModified(1)
            val fresh = File(dir, "fresh").apply { mkdirs(); setLastModified(ImageDiskPolicy.DRAFT_TTL) }
            assertEquals(1, ImageDiskPolicy.expireDrafts(dir, ImageDiskPolicy.DRAFT_TTL + 1))
            assertFalse(expired.exists()); assertTrue(pinned.exists()); assertTrue(fresh.exists())
        } finally { dir.deleteRecursively() }
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
