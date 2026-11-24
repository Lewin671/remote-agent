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
}
