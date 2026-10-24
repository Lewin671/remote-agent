package io.github.lewin671.remoteagent.data

import android.app.Application
import android.content.Context
import android.content.ContextWrapper
import android.database.sqlite.SQLiteDatabase
import android.os.Looper
import androidx.room.Room
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.RemoteSession
import io.github.lewin671.remoteagent.proto.StoredEvent
import io.github.lewin671.remoteagent.RaApp
import com.google.protobuf.ByteString
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.StandardTestDispatcher
import java.io.File
import java.util.concurrent.Executor
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class RoomCacheTest {
    private val databases = mutableListOf<CacheDb>()
    private val caches = mutableListOf<RoomCache>()
    private val access get() = EventAccess(RuntimeEnvironment.getApplication())
    private var now = 0L
    private fun TestScope.database(): CacheDb = Room.inMemoryDatabaseBuilder(
        RuntimeEnvironment.getApplication(), CacheDb::class.java,
    ).setQueryCoroutineContext(StandardTestDispatcher(testScheduler))
        .allowMainThreadQueries().build().also { databases += it }

    private fun TestScope.cache(dao: CacheDao, budget: Long, reclaim: suspend () -> Unit = {}): RoomCache =
        RoomCache(dao, access, parentScope = backgroundScope, budget = budget, clock = { now }, reclaim = reclaim)
            .also { caches += it }

    @Test fun removingAComputerClearsItsSignedScheduleSnapshotOnly() = runTest {
        val c=cache(database().dao(),1024)
        c.putBlob("schedule:removed",byteArrayOf(1));c.putBlob("schedule:retained",byteArrayOf(2))
        c.removeMachine("removed")
        assertNull(c.blob("schedule:removed"));assertArrayEquals(byteArrayOf(2),c.blob("schedule:retained"))
    }

    private fun event(seq: Long) = StoredEvent.newBuilder().setSeq(seq)
        .setEnvelope(Envelope.newBuilder().setCiphertext(ByteString.copyFrom(ByteArray(64) { 7 }))).build()
    private val bytes get() = event(1).serializedSize.toLong()
    private suspend fun RoomCache.seed(sid: String, openedAt: Long) {
        now = openedAt
        events(sid)
        putSession(RemoteSession.newBuilder().setSessionId(sid).setMachineId("machine").build())
        putEvents(sid, listOf(event(1), event(2)))
    }
    private fun TestScope.trim() { advanceTimeBy(1000); runCurrent() }

    private fun existingDatabase(version: Int = 3): SQLiteDatabase {
        val context = RuntimeEnvironment.getApplication()
        context.deleteDatabase(CacheDb.NAME)
        val schema = JSONObject(File(System.getProperty("cacheSchemas"),
            "io.github.lewin671.remoteagent.data.CacheDb/$version.json").readText()).getJSONObject("database")
        return context.openOrCreateDatabase(CacheDb.NAME, 0, null).apply {
            execSQL("PRAGMA auto_vacuum=NONE")
            execSQL("VACUUM")
            val entities = schema.getJSONArray("entities")
            for (i in 0 until entities.length()) {
                val entity = entities.getJSONObject(i)
                execSQL(entity.getString("createSql").replace("\${TABLE_NAME}", entity.getString("tableName")))
            }
            val setup = schema.getJSONArray("setupQueries")
            for (i in 0 until setup.length()) execSQL(setup.getString(i))
            this.version = version
            // Match Room's default journal mode on this device.
            assertTrue(enableWriteAheadLogging())
        }
    }

    private fun SQLiteDatabase.putEvents(sid: String) {
        for (seq in 1L..2L) execSQL("INSERT INTO events VALUES (?, ?, ?)", arrayOf(sid, seq, event(seq).toByteArray()))
    }

    @After fun cleanup() {
        caches.forEach { it.close() }
        access.clear()
        databases.forEach { it.close() }
        RuntimeEnvironment.getApplication().deleteDatabase(CacheDb.NAME)
    }

    @Test fun budgetEvictsWholeLeastRecentlyOpenedSessionsAndCountsDuplicatesOnce() = runTest {
        val dao = database().dao()
        val cache = cache(dao, 4 * bytes)
        cache.seed("a", 10)
        cache.seed("b", 20)
        cache.seed("c", 30)
        now = 40
        // Appending is not opening. Duplicate deliveries must not inflate the budget.
        cache.putEvents("a", listOf(event(2), event(3)))
        cache.putEvents("b", listOf(event(1), event(2)))
        trim()
        assertTrue(dao.events("a").isEmpty())
        assertEquals(listOf(1L, 2L), dao.events("b").map { it.seq })
        assertEquals(listOf(1L, 2L), dao.events("c").map { it.seq })
        assertEquals(4 * bytes, dao.eventSizes().sumOf { it.bytes })
        assertEquals(3, cache.sessions().size) // Eviction must not remove the listing.
        assertNull(dao.eventSizes().find { it.sessionId == "a" })
    }

    @Test fun reopeningInMemoryProtectsAnOversizedCurrentSessionUntilItCloses() = runTest {
        val dao = database().dao()
        val cache = cache(dao, bytes)
        cache.seed("current", 10)
        cache.seed("other", 20)
        now = 30
        cache.setSessionOpen("current", true)
        trim()
        assertEquals(2, dao.events("current").size)
        assertEquals(30L, access.times()["current"])
        assertTrue(dao.events("other").isEmpty())
        cache.setSessionOpen("current", false)
        trim()
        assertTrue(dao.events("current").isEmpty())
        assertEquals(0L, dao.eventSizes().sumOf { it.bytes })
    }

    @Test fun accessOrderSurvivesCacheRestartAndListingUpdates() = runTest {
        val dao = database().dao()
        val old = cache(dao, Long.MAX_VALUE)
        old.seed("a", 10)
        old.seed("b", 20)
        now = 30
        old.events("a")
        old.putSession(RemoteSession.newBuilder().setSessionId("a").setMachineId("machine").build())
        old.close()
        cache(dao, 2 * bytes) // Startup must trim without any append.
        trim()
        assertEquals(2, dao.events("a").size)
        assertTrue(dao.events("b").isEmpty())
    }

    @Test fun trimPrunesOnlyAccessTimesWithNoRetainedSessionOrEvents() = runTest {
        val dao = database().dao()
        val cache = cache(dao, Long.MAX_VALUE)
        cache.seed("listed", 10)
        cache.dropEvents("listed")
        cache.seed("unlisted", 20)
        cache.unlistSession("unlisted")
        now = 30
        cache.events("events-only")
        cache.putEvents("events-only", listOf(event(1)))
        cache.events("gone")
        cache.setSessionOpen("current", true)
        trim()
        assertEquals(setOf("listed", "unlisted", "events-only", "current"), access.times().keys)
        cache.setSessionOpen("current", false)
        cache.dropEvents("events-only")
        trim()
        assertEquals(setOf("listed", "unlisted"), access.times().keys)
    }

    @Test fun losingAccessTimesOnlyChangesEvictionOrder() = runTest {
        val dao = database().dao()
        val old = cache(dao, Long.MAX_VALUE)
        old.seed("a", 20)
        old.seed("b", 10)
        old.close()
        access.clear()
        assertEquals(listOf(event(1), event(2)), dao.events("a").map { StoredEvent.parseFrom(it.data) })
        assertEquals(4 * bytes, dao.eventSizes().sumOf { it.bytes })
        cache(dao, 2 * bytes)
        trim()
        // Missing timestamps tie deterministically by session ID.
        assertTrue(dao.events("a").isEmpty())
        assertEquals(listOf(event(1), event(2)), dao.events("b").map { StoredEvent.parseFrom(it.data) })
    }

    @Test fun evictedSessionWithNewTailRefetchesFromAContiguousPrefix() = runTest {
        val dao = database().dao()
        val cache = cache(dao, 2 * bytes)
        cache.seed("a", 10)
        dao.trimEvents(0, emptySet(), emptyMap())
        assertTrue(cache.events("a").isEmpty())
        // The engine still holding a in memory can append after eviction.
        cache.putEvents("a", listOf(event(3), event(4)))
        assertTrue(cache.events("a").isEmpty())
        // Relay pages refill from zero, preserving the already cached tail.
        cache.putEvents("a", listOf(event(1)))
        assertEquals(listOf(1L), cache.events("a").map { it.seq })
        cache.putEvents("a", listOf(event(2), event(3), event(4)))
        assertEquals((1L..4L).toList(), cache.events("a").map { it.seq })
        assertEquals(4 * bytes, dao.eventSizes().find { it.sessionId == "a" }!!.bytes)
    }

    @Test fun appendsDuringTheCoalescingDelayNeedOnlyOneTrim() = runTest {
        val dao = database().dao()
        var passes = 0
        val observed = object : CacheDao by dao {
            override suspend fun trimEvents(budget: Long, open: Set<String>, openedAt: Map<String, Long>): Boolean {
                passes++
                return dao.trimEvents(budget, open, openedAt)
            }
        }
        val cache = cache(observed, 2 * bytes)
        runCurrent() // Startup trim is waiting for the coalescing delay.
        cache.seed("a", 10)
        cache.seed("b", 20)
        trim()
        assertEquals(1, passes)
        trim()
        assertEquals(1, passes)
    }

    @Test fun trimsAreCoalescedAndAnAppendDuringATrimGetsAnotherPass() = runTest {
        val dao = database().dao()
        val entered = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        var passes = 0
        val observed = object : CacheDao by dao {
            override suspend fun trimEvents(budget: Long, open: Set<String>, openedAt: Map<String, Long>): Boolean {
                passes++
                if (passes == 1) { entered.complete(Unit); release.await() }
                return dao.trimEvents(budget, open, openedAt)
            }
        }
        val cache = cache(observed, 2 * bytes)
        cache.seed("a", 10)
        cache.seed("b", 20)
        assertEquals(0, passes)
        advanceTimeBy(1000)
        entered.await()
        val append = launch { cache.putEvents("b", listOf(event(3))) }
        runCurrent()
        assertFalse(append.isCompleted)
        release.complete(Unit)
        append.join()
        assertEquals(1, passes)
        trim()
        assertEquals(2, passes)
        assertTrue(dao.events("b").isEmpty())
        assertEquals(0L, dao.eventSizes().sumOf { it.bytes })
    }

    @Test fun deletingSessionsOrMachinesClearsAccessAndCloseCancelsMaintenance() = runTest {
        val dao = database().dao()
        val cache = cache(dao, 0)
        cache.seed("a", 10)
        cache.seed("b", 20)
        cache.removeSession("a")
        assertFalse(access.times().containsKey("a"))
        cache.removeMachine("machine")
        assertTrue(dao.eventSizes().isEmpty())
        assertTrue(access.times().isEmpty())
        cache.seed("kept", 30)
        cache.close()
        trim()
        assertEquals(2, dao.events("kept").size)
    }

    @Test fun existingVersionThreeCacheIsUsedAsIsWithoutVacuumOnOpen() = runTest {
        val context = RuntimeEnvironment.getApplication()
        val machine = byteArrayOf(1, 2, 3)
        val blob = byteArrayOf(4, 5, 6)
        val session = RemoteSession.newBuilder().setSessionId("old").setMachineId("machine").build()
        val unlisted = session.toBuilder().setSessionId("unlisted").build()
        var pages = 0
        var free = 0
        existingDatabase().use { db ->
            db.execSQL("INSERT INTO machines VALUES (?, ?)", arrayOf("machine", machine))
            db.execSQL("INSERT INTO sessions VALUES (?, ?, ?, ?)", arrayOf("old", "machine", session.toByteArray(), 1))
            db.execSQL("INSERT INTO sessions VALUES (?, ?, ?, ?)", arrayOf("unlisted", "machine", unlisted.toByteArray(), 0))
            db.execSQL("INSERT INTO blobs VALUES (?, ?)", arrayOf("preferences", blob))
            db.putEvents("old")
            db.putEvents("unlisted")
            db.execSQL("INSERT INTO events VALUES (?, ?, ?)", arrayOf("deleted", 1, ByteArray(64 * 1024)))
            db.execSQL("DELETE FROM events WHERE sessionId = 'deleted'")
            pages = db.rawQuery("PRAGMA page_count", null).use { it.moveToFirst(); it.getInt(0) }
            free = db.rawQuery("PRAGMA freelist_count", null).use { it.moveToFirst(); it.getInt(0) }
            assertTrue(free > 0)
        }
        val db = CacheDb.open(context).also { databases += it }
        val dao = db.dao()
        for (sid in listOf("old", "unlisted")) {
            val rows = dao.events(sid)
            assertEquals(listOf(1L, 2L), rows.map { it.seq })
            rows.forEach { assertArrayEquals(event(it.seq).toByteArray(), it.data) }
        }
        assertArrayEquals(machine, dao.machines().single().data)
        assertArrayEquals(blob, dao.blob("preferences")!!.data)
        assertArrayEquals(session.toByteArray(), dao.sessions().single().data)
        assertEquals(listOf("old", "unlisted"), dao.sessionsOf("machine").sorted())
        val sqlite = db.openHelper.writableDatabase
        sqlite.query("SELECT data, listed FROM sessions WHERE id = 'unlisted'").use {
            assertTrue(it.moveToFirst())
            assertArrayEquals(unlisted.toByteArray(), it.getBlob(0))
            assertEquals(0, it.getInt(1))
        }
        assertEquals(3, sqlite.version)
        sqlite.query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'android_metadata'").use {
            val tables = mutableSetOf<String>()
            while (it.moveToNext()) tables += it.getString(0)
            assertEquals(setOf("machines", "sessions", "events", "blobs", "room_master_table"), tables)
        }
        val cache = cache(dao, Long.MAX_VALUE)
        assertEquals(listOf(event(1), event(2)), cache.events("old"))
        assertEquals(2 * bytes, dao.eventSizes().first { it.sessionId == "old" }.bytes)
        assertEquals(0, sqlite.query("PRAGMA auto_vacuum").use { it.moveToFirst(); it.getInt(0) })
        assertEquals(pages, sqlite.query("PRAGMA page_count").use { it.moveToFirst(); it.getInt(0) })
        assertEquals(free, sqlite.query("PRAGMA freelist_count").use { it.moveToFirst(); it.getInt(0) })
    }

    @Test fun backgroundMaintenanceVacuumsOnceAfterEvictionAndReclaimsPages() = runTest {
        val context = RuntimeEnvironment.getApplication()
        existingDatabase().use { db ->
            db.execSQL("INSERT INTO events VALUES (?, ?, ?)", arrayOf("large", 1, ByteArray(64 * 1024)))
            db.execSQL("INSERT INTO events VALUES (?, ?, ?)", arrayOf("retained", 1, event(1).toByteArray()))
        }
        val vacuums = AtomicInteger()
        val onMain = AtomicBoolean()
        val db = Room.databaseBuilder(context, CacheDb::class.java, CacheDb.NAME)
            .setQueryCallback({ sql, _ ->
                if (sql == "VACUUM") {
                    vacuums.incrementAndGet()
                    onMain.set(Looper.myLooper() == Looper.getMainLooper())
                }
            }, Executor { it.run() }).build().also { databases += it }
        val sqlite = db.openHelper.writableDatabase
        fun pages() = sqlite.query("PRAGMA page_count").use { it.moveToFirst(); it.getInt(0) }
        val before = pages()
        val finished = Channel<Unit>(Channel.UNLIMITED)
        val cache = cache(db.dao(), bytes) {
            db.reclaimSpace()
            finished.trySend(Unit)
        }
        assertEquals(0, vacuums.get())
        trim()
        finished.receive()
        assertEquals(1, vacuums.get())
        assertFalse("VACUUM must run off the main thread", onMain.get())
        assertTrue("evicted pages should be returned to the filesystem", pages() < before)
        assertEquals(1, sqlite.query("PRAGMA auto_vacuum").use { it.moveToFirst(); it.getInt(0) })
        assertEquals(listOf(event(1)), cache.events("retained"))
        cache.setSessionOpen("retained", true)
        cache.putEvents("later", listOf(event(1)))
        trim()
        finished.receive()
        assertTrue(db.dao().events("later").isEmpty())
        assertEquals(1, vacuums.get()) // FULL persists; later evictions need no further rewrite.
    }

    @Test fun maintenanceWaitsForEvictionAndDoesNotHoldTheCacheReadLock() = runTest {
        val dao = database().dao()
        val entered = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        val cache = cache(dao, 2 * bytes) {
            entered.complete(Unit)
            release.await()
        }
        cache.seed("a", 10)
        trim()
        assertFalse("an under-budget cache needs no rewrite", entered.isCompleted)
        cache.seed("b", 20)
        trim()
        assertTrue(entered.isCompleted)
        try {
            val read = async { cache.events("b") }
            runCurrent()
            assertTrue("cache reads must not wait for maintenance", read.isCompleted)
            assertEquals(listOf(event(1), event(2)), read.await())
        } finally { release.complete(Unit) }
    }

    @Test fun maintenanceFailureIsNonFatalAndCanRetryAfterAnotherEviction() = runTest {
        val dao = database().dao()
        var attempts = 0
        val cache = cache(dao, 2 * bytes) {
            if (++attempts == 1) error("unavailable")
        }
        cache.seed("a", 10)
        cache.seed("b", 20)
        trim()
        assertEquals(1, attempts)
        assertEquals(listOf(event(1), event(2)), cache.events("b"))
        cache.seed("c", 30)
        trim()
        assertEquals(2, attempts)
        assertTrue(dao.events("b").isEmpty())
        assertEquals(listOf(event(1), event(2)), cache.events("c"))
    }

    @Test fun appendingToExistingEventsCountsBlobBytesWithoutDoubleCountingDuplicates() = runTest {
        val dao = database().dao()
        dao.put(listOf(EventRow("old", 1, event(1).toByteArray()), EventRow("old", 2, event(2).toByteArray())))
        val cache = cache(dao, Long.MAX_VALUE)
        cache.putEvents("old", listOf(event(2), event(3)))
        assertEquals(3 * bytes, dao.eventSizes().first { it.sessionId == "old" }.bytes)
        assertFalse(access.times().containsKey("old"))
        assertEquals(listOf(1L, 2L, 3L), dao.events("old").map { it.seq })
    }

    @Test fun openingExistingEventsProtectsThemFromEviction() = runTest {
        val dao = database().dao()
        dao.put(listOf(EventRow("old", 1, event(1).toByteArray()), EventRow("old", 2, event(2).toByteArray())))
        val cache = cache(dao, bytes)
        now = 30
        cache.setSessionOpen("old", true)
        assertEquals(2 * bytes, dao.eventSizes().first { it.sessionId == "old" }.bytes)
        assertEquals(30L, access.times()["old"])
        trim()
        assertEquals(2, dao.events("old").size)
        cache.setSessionOpen("old", false)
        trim()
        assertTrue(dao.events("old").isEmpty())
    }

    @Test fun startupTrimIncludesExistingSessionsWithoutAccessTimes() = runTest {
        val context = RuntimeEnvironment.getApplication()
        existingDatabase().use { db ->
            db.putEvents("legacy-a")
            db.putEvents("legacy-b")
        }
        val dao = CacheDb.open(context).also { databases += it }.dao()
        val trimmed = Channel<Unit>(Channel.UNLIMITED)
        val observed = object : CacheDao by dao {
            override suspend fun trimEvents(budget: Long, open: Set<String>, openedAt: Map<String, Long>): Boolean {
                val evicted = dao.trimEvents(budget, open, openedAt)
                trimmed.trySend(Unit)
                return evicted
            }
        }
        dao.put(listOf(EventRow("opened", 1, event(1).toByteArray()), EventRow("opened", 2, event(2).toByteArray())))
        access.opened("opened", 20)
        cache(observed, 4 * bytes)
        trim()
        trimmed.receive() // The file-backed database runs on Room's I/O dispatcher.
        assertTrue(dao.events("legacy-a").isEmpty())
        assertEquals(2, dao.events("legacy-b").size)
        assertEquals(2, dao.events("opened").size)
        assertEquals(4 * bytes, dao.eventSizes().sumOf { it.bytes })
        assertFalse(access.times().containsKey("legacy-b"))
    }

    @Test fun unsupportedOlderVersionUsesMemoryAndLeavesTheFileBytesUnchanged() = runTest {
        val context = RuntimeEnvironment.getApplication()
        existingDatabase(2).use { it.putEvents("old") }
        val file = context.getDatabasePath(CacheDb.NAME)
        val original = file.readBytes()
        val app = RaApp()
        ContextWrapper::class.java.getDeclaredMethod("attachBaseContext", Context::class.java)
            .apply { isAccessible = true }.invoke(app, context)
        val db = app.db.also { databases += it }
        assertSame(db, app.db)
        assertTrue("open must validate eagerly", db.isOpen)
        val cache = cache(db.dao(), Long.MAX_VALUE)
        assertTrue(cache.events("old").isEmpty())
        cache.seed("temporary", 20)
        assertEquals(listOf(event(1), event(2)), cache.events("temporary"))
        assertArrayEquals(original, file.readBytes())
        cache.close()
        backgroundScope.coroutineContext[kotlinx.coroutines.Job]!!.children.toList().forEach { it.join() }
        db.close()
        assertArrayEquals(original, file.readBytes())
        val next = CacheDb.open(context).also { databases += it }
        assertTrue(next.dao().events("temporary").isEmpty())
        next.close()
        assertArrayEquals(original, file.readBytes())
    }

    @Test fun healthyCacheOpensEagerlyAndRemainsPersistent() = runTest {
        val context = RuntimeEnvironment.getApplication()
        val db = CacheDb.open(context).also { databases += it }
        assertTrue(db.isOpen)
        assertEquals(1, db.openHelper.writableDatabase.query("PRAGMA auto_vacuum").use { it.moveToFirst(); it.getInt(0) })
        assertTrue("fresh caches should use Room's default WAL", db.openHelper.writableDatabase.isWriteAheadLoggingEnabled)
        db.dao().put(BlobRow("retained", byteArrayOf(1, 2, 3)))
        db.close()
        val reopened = CacheDb.open(context).also { databases += it }
        assertTrue(reopened.isOpen)
        assertArrayEquals(byteArrayOf(1, 2, 3), reopened.dao().blob("retained")!!.data)
    }

    @Test fun downgradeUsesMemoryAndLeavesTheFileBytesUnchanged() = runTest {
        val context = RuntimeEnvironment.getApplication()
        existingDatabase().use { it.putEvents("old"); it.version = 5 }
        val file = context.getDatabasePath(CacheDb.NAME)
        val original = file.readBytes()
        val db = CacheDb.open(context).also { databases += it }
        assertTrue(db.isOpen)
        db.dao().put(BlobRow("temporary", byteArrayOf(1)))
        assertArrayEquals(byteArrayOf(1), db.dao().blob("temporary")!!.data)
        db.close()
        assertArrayEquals(original, file.readBytes())
    }

    @Test fun schemaValidationFailureUsesMemoryAndLeavesTheFileBytesUnchanged() = runTest {
        val context = RuntimeEnvironment.getApplication()
        existingDatabase().use { it.putEvents("old"); it.execSQL("UPDATE room_master_table SET identity_hash = 'invalid'") }
        val file = context.getDatabasePath(CacheDb.NAME)
        val original = file.readBytes()
        val db = CacheDb.open(context).also { databases += it }
        assertTrue(db.isOpen)
        db.dao().put(BlobRow("temporary", byteArrayOf(1)))
        assertArrayEquals(byteArrayOf(1), db.dao().blob("temporary")!!.data)
        db.close()
        assertArrayEquals(original, file.readBytes())
    }
}
