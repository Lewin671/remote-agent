package io.github.lewin671.remoteagent.data

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.AttachmentIoExecutor
import io.github.lewin671.remoteagent.core.AttachmentKeyUnavailableException
import io.github.lewin671.remoteagent.core.AttachmentPlatformIo
import io.github.lewin671.remoteagent.core.HashedFileOutcome
import io.github.lewin671.remoteagent.core.PreparedImageOutcome
import io.github.lewin671.remoteagent.core.ReadChunkOutcome
import io.github.lewin671.remoteagent.core.proto.AttachmentHashFileEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentPrepareImageEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentReadChunkEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageBucket
import io.github.lewin671.remoteagent.core.proto.DraftStorageListEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageWriteEffect
import java.io.File
import java.nio.file.Files
import java.security.KeyStoreException
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AttachmentManagerTest {
    private class FakeKeyStoreBackend : AndroidKeyStoreBackend {
        private val keys = HashMap<String, SecretKey>()
        var failGetWith: Throwable? = null
        var generateCount = 0

        override fun getKey(alias: String): SecretKey? {
            failGetWith?.let { throw it }
            return keys[alias]
        }

        override fun generateKey(alias: String): SecretKey {
            generateCount++
            val kg = KeyGenerator.getInstance("AES").apply { init(256) }
            return kg.generateKey().also { keys[alias] = it }
        }

        override fun deleteKey(alias: String) {
            keys.remove(alias)
        }
    }

    private val noopIo = object : AttachmentPlatformIo {
        override fun prepareImage(eff: AttachmentPrepareImageEffect, storeBlob: (String, ByteArray) -> String) =
            PreparedImageOutcome(ok = false)
        override fun hashFile(eff: AttachmentHashFileEffect) = HashedFileOutcome(ok = false)
        override fun readFileChunk(eff: AttachmentReadChunkEffect) = ReadChunkOutcome(ok = false)
    }

    @Test
    fun keystoreFailureDoesNotDeleteDraftsOrOverwriteKeyBinAndRecoversNextStart() {
        val dir = Files.createTempDirectory("ra-ks-m4-").toFile()
        try {
            val fakeKs = FakeKeyStoreBackend()
            val provider1 = AndroidAttachmentKeyProvider(dir, fakeKs)

            // 1. Fresh install: no key.bin and no alias -> loadExistingKey returns null (not an error).
            assertNull(provider1.loadExistingKey())

            // 2. Write encrypted draft + outbox + blob using ensureKey().
            val exec1 = AttachmentIoExecutor(rootDir = dir, keyProvider = provider1, platformIo = noopIo)
            try {
                val wr1 = exec1.executeWrite(
                    DraftStorageWriteEffect.newBuilder()
                        .setOpId(1L)
                        .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                        .setKey("session:s-m4")
                        .setData(ByteString.copyFromUtf8("keep-my-draft"))
                        .build(),
                )
                assertTrue(wr1.ok)
                val wr2 = exec1.executeWrite(
                    DraftStorageWriteEffect.newBuilder()
                        .setOpId(2L)
                        .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_OUTBOX)
                        .setKey("out:s-m4:req-1")
                        .setData(ByteString.copyFromUtf8("keep-my-outbox"))
                        .build(),
                )
                assertTrue(wr2.ok)
            } finally {
                exec1.close()
            }
            val keyFile = File(dir, "key.bin")
            assertTrue(keyFile.isFile)
            val originalKeyBin = keyFile.readBytes()
            assertEquals(1, fakeKs.generateCount)

            // 3. Cold restart with transient Keystore failure:
            //    - loadExistingKey() throws AttachmentKeyUnavailableException (NOT null)
            //    - ensureKey() throws AttachmentKeyUnavailableException and NEVER overwrites key.bin
            //    - executeList() throws instead of marking records corrupt
            fakeKs.failGetWith = KeyStoreException("keystore daemon unavailable")
            val provider2 = AndroidAttachmentKeyProvider(dir, fakeKs)
            val exec2 = AttachmentIoExecutor(rootDir = dir, keyProvider = provider2, platformIo = noopIo)
            try {
                assertTrue(runCatching { provider2.loadExistingKey() }.exceptionOrNull() is AttachmentKeyUnavailableException)
                assertTrue(runCatching { provider2.ensureKey() }.exceptionOrNull() is AttachmentKeyUnavailableException)
                assertTrue(
                    runCatching { exec2.executeList(DraftStorageListEffect.newBuilder().setOpId(3L).build()) }
                        .exceptionOrNull() is AttachmentKeyUnavailableException,
                )
                assertArrayEquals("key.bin must not be overwritten on Keystore failure", originalKeyBin, keyFile.readBytes())
                assertEquals("must not generate replacement Keystore key", 1, fakeKs.generateCount)
            } finally {
                exec2.close()
            }

            // 4. Next startup after Keystore recovers -> all drafts and outbox records restore cleanly!
            fakeKs.failGetWith = null
            val provider3 = AndroidAttachmentKeyProvider(dir, fakeKs)
            val exec3 = AttachmentIoExecutor(rootDir = dir, keyProvider = provider3, platformIo = noopIo)
            try {
                val listed = exec3.executeList(DraftStorageListEffect.newBuilder().setOpId(4L).build())
                assertEquals(2, listed.entriesCount)
                assertTrue(listed.entriesList.none { it.corrupt })
                val byKey = listed.entriesList.associate { it.key to it.data.toStringUtf8() }
                assertEquals("keep-my-draft", byKey["session:s-m4"])
                assertEquals("keep-my-outbox", byKey["out:s-m4:req-1"])
            } finally {
                exec3.close()
            }
        } finally {
            dir.deleteRecursively()
        }
    }
}
