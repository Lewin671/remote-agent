package io.github.lewin671.remoteagent.data

import io.github.lewin671.remoteagent.ui.MemoryFiles
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.launch
import org.junit.Assert.*
import org.junit.Test

class FileScopesTest {
    @Test fun aQueuedSaveCannotWriteAfterItsAccountLeaves() {
        val files = MemoryFiles()
        val scopes = FileScopes(files)
        val old = scopes.scoped()
        val thread = java.util.concurrent.Executors.newSingleThreadExecutor()
        val held = java.util.concurrent.CountDownLatch(1)
        val dispatcher = thread.asCoroutineDispatcher()
        try {
            thread.execute { held.await() }
            val result = kotlinx.coroutines.CompletableDeferred<Result<Unit>>()
            CoroutineScope(dispatcher).launch { result.complete(runCatching { old.put("same-project", "old-image", byteArrayOf(1)) }) }
            scopes.invalidate()
            files.data.clear()
            val next = scopes.scoped()
            next.put("same-project", "next", byteArrayOf(2))
            held.countDown()
            thread.submit {}.get(5, java.util.concurrent.TimeUnit.SECONDS)
            assertTrue(kotlinx.coroutines.runBlocking { result.await() }.isFailure)
            assertEquals(setOf("same-project/next"), files.data.keys)
            assertTrue(runCatching { old.clearFolder("same-project") }.isFailure)
            assertArrayEquals(byteArrayOf(2), next.get("same-project", "next"))
        } finally { held.countDown(); dispatcher.close() }
    }

    @Test fun aScopeInvalidationWaitsForAWritingTransaction() {
        val files = MemoryFiles()
        val scopes = FileScopes(files)
        val scope = scopes.scoped()
        val entered = java.util.concurrent.CountDownLatch(1)
        val release = java.util.concurrent.CountDownLatch(1)
        val pool = java.util.concurrent.Executors.newFixedThreadPool(2)
        try {
            val writer = pool.submit { scope.transaction { entered.countDown(); release.await(); scope.put("p", "a", byteArrayOf(1)) } }
            assertTrue(entered.await(5, java.util.concurrent.TimeUnit.SECONDS))
            val erase = pool.submit { scopes.invalidate(); files.clearFolder("p") }
            release.countDown()
            writer.get(5, java.util.concurrent.TimeUnit.SECONDS)
            erase.get(5, java.util.concurrent.TimeUnit.SECONDS)
            assertTrue(files.data.isEmpty())
            assertTrue(runCatching { scope.put("p", "a", byteArrayOf(1)) }.isFailure)
        } finally { release.countDown(); pool.shutdownNow() }
    }
}
