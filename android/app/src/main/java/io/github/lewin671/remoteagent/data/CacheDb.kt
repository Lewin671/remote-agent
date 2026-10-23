package io.github.lewin671.remoteagent.data

import android.content.Context
import android.util.Log
import androidx.room.Dao
import androidx.room.Database
import androidx.room.Entity
import androidx.room.Insert
import androidx.room.OnConflictStrategy
import androidx.room.Query
import androidx.room.Room
import androidx.room.RoomDatabase
import androidx.room.Transaction
import androidx.sqlite.db.SupportSQLiteDatabase
import io.github.lewin671.remoteagent.core.CipherCache
import io.github.lewin671.remoteagent.proto.Machine
import io.github.lewin671.remoteagent.proto.RemoteSession
import io.github.lewin671.remoteagent.proto.StoredEvent
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

// The cache holds what the server sent, as it was sent: routing fields and
// envelopes. Nothing decrypted is written to disk.

@Entity(tableName = "machines", primaryKeys = ["id"])
class MachineRow(val id: String, val data: ByteArray)

@Entity(tableName = "sessions", primaryKeys = ["id"])
class SessionRow(val id: String, val machineId: String, val data: ByteArray, val listed: Boolean = true)

@Entity(tableName = "events", primaryKeys = ["sessionId", "seq"])
class EventRow(val sessionId: String, val seq: Long, val data: ByteArray)

class EventSize(val sessionId: String, val bytes: Long)

/** Losing these timestamps only changes eviction order, never cached ciphertext. */
class EventAccess(context: Context) {
    private val prefs = context.getSharedPreferences("event-access", Context.MODE_PRIVATE)

    fun times(): Map<String, Long> = prefs.all.mapNotNull { (sid, time) -> (time as? Long)?.let { sid to it } }.toMap()
    fun opened(sid: String, now: Long) { prefs.edit().putLong(sid, now).apply() }
    fun remove(sid: String) { prefs.edit().remove(sid).apply() }
    fun retain(sessions: Set<String>) {
        val stale = prefs.all.keys - sessions
        if (stale.isNotEmpty()) prefs.edit().apply { stale.forEach { remove(it) } }.apply()
    }
    fun clear() { prefs.edit().clear().apply() }
}

/** A sealed object kept by name, e.g. the account's project preferences as the server sent them. */
@Entity(tableName = "blobs", primaryKeys = ["name"])
class BlobRow(val name: String, val data: ByteArray)

@Dao
interface CacheDao {
    @Query("SELECT * FROM blobs WHERE name = :name") suspend fun blob(name: String): BlobRow?
    @Insert(onConflict = OnConflictStrategy.REPLACE) suspend fun put(b: BlobRow)
    @Query("SELECT * FROM machines") suspend fun machines(): List<MachineRow>
    @Query("SELECT * FROM sessions WHERE listed = 1") suspend fun sessions(): List<SessionRow>
    @Query("SELECT * FROM events WHERE sessionId = :sid ORDER BY seq") suspend fun events(sid: String): List<EventRow>
    @Insert(onConflict = OnConflictStrategy.REPLACE) suspend fun put(m: MachineRow)
    @Insert(onConflict = OnConflictStrategy.REPLACE) suspend fun put(s: SessionRow)
    @Insert(onConflict = OnConflictStrategy.IGNORE) suspend fun putIfAbsent(s: SessionRow)
    @Query("UPDATE sessions SET listed = 0 WHERE id = :id") suspend fun unlist(id: String)
    @Insert(onConflict = OnConflictStrategy.IGNORE) suspend fun put(e: List<EventRow>)
    @Query("SELECT sessionId, SUM(LENGTH(data)) AS bytes FROM events GROUP BY sessionId") suspend fun eventSizes(): List<EventSize>
    @Query("SELECT id FROM sessions UNION SELECT sessionId FROM events") suspend fun cachedSessionIds(): List<String>
    @Query("DELETE FROM machines WHERE id = :id") suspend fun deleteMachine(id: String)
    @Query("DELETE FROM sessions WHERE id = :id") suspend fun deleteSession(id: String)
    @Query("DELETE FROM events WHERE sessionId = :sid") suspend fun deleteEvents(sid: String)
    @Query("SELECT id FROM sessions WHERE machineId = :mid") suspend fun sessionsOf(mid: String): List<String>

    @Transaction
    suspend fun trimEvents(budget: Long, open: Set<String>, openedAt: Map<String, Long>) {
        // SQLite measures BLOB lengths without loading the ciphertext into memory.
        val sessions = eventSizes().sortedWith(compareBy<EventSize> { openedAt[it.sessionId] ?: Long.MIN_VALUE }.thenBy { it.sessionId })
        var total = sessions.sumOf { it.bytes }
        for (session in sessions) {
            if (total <= budget) break
            if (session.sessionId in open) continue
            deleteEvents(session.sessionId)
            total -= session.bytes
        }
    }

    // Listed sessions are the listing's first page (architecture §8). An
    // unlisted row still identifies the owner of any retained events.
    @Transaction
    suspend fun removeMachine(id: String) {
        for (sid in sessionsOf(id)) {
            deleteEvents(sid)
            deleteSession(sid)
        }
        deleteMachine(id)
    }

    @Transaction
    suspend fun removeSession(id: String) {
        deleteEvents(id)
        deleteSession(id)
    }
}

@Database(entities = [MachineRow::class, SessionRow::class, EventRow::class, BlobRow::class], version = 3)
abstract class CacheDb : RoomDatabase() {
    abstract fun dao(): CacheDao

    companion object {
        const val NAME = "cache.db"

        fun open(context: Context): CacheDb = Room.databaseBuilder(context, CacheDb::class.java, NAME)
            .fallbackToDestructiveMigration(true)
            .addCallback(object : Callback() {
                override fun onOpen(db: SupportSQLiteDatabase) {
                    // Return deleted pages to the filesystem rather than retaining a high-water mark.
                    val vacuum = db.query("PRAGMA auto_vacuum").use { it.moveToFirst(); it.getInt(0) }
                    if (vacuum != 1) {
                        db.execSQL("PRAGMA auto_vacuum=FULL")
                        db.execSQL("VACUUM")
                    }
                }
            })
            .build()
    }
}

/** The budget counts serialized ciphertext, like the web cache; SQLite adds page/index overhead. */
class RoomCache(
    private val dao: CacheDao,
    private val access: EventAccess,
    private val images: ImageEnvelopes? = null,
    parentScope: CoroutineScope = CoroutineScope(Dispatchers.IO),
    private val budget: Long = 256L * 1024 * 1024,
    private val clock: () -> Long = System::currentTimeMillis,
) : CipherCache {
    private val scope = CoroutineScope(parentScope.coroutineContext + SupervisorJob(parentScope.coroutineContext[kotlinx.coroutines.Job]))
    private val lock = Mutex()
    private val open = mutableSetOf<String>()
    private val trims = Channel<Unit>(Channel.CONFLATED)

    init {
        scope.launch {
            for (request in trims) {
                delay(1000)
                while (trims.tryReceive().isSuccess) { /* Coalesce appends during the delay. */ }
                try {
                    lock.withLock {
                        dao.trimEvents(budget, open, access.times())
                        access.retain(dao.cachedSessionIds().toSet() + open)
                    }
                } catch (e: Exception) {
                    if (e is CancellationException) throw e
                    Log.w("remote-agent", "event cache trim failed: ${e.javaClass.simpleName}")
                }
            }
        }
        trims.trySend(Unit)
    }

    override fun close() { scope.cancel() }

    override suspend fun setSessionOpen(sessionId: String, open: Boolean) = lock.withLock {
        if (open) {
            this.open.add(sessionId)
            access.opened(sessionId, clock())
        } else this.open.remove(sessionId)
        trims.trySend(Unit)
        Unit
    }

    override suspend fun image(key: String) = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { images?.get(key) }
    override suspend fun putImage(key: String, envelope: ByteArray) = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { images?.put(key, envelope) ?: Unit }
    override suspend fun machines() = dao.machines().map { Machine.parseFrom(it.data) }
    override suspend fun sessions() = dao.sessions().map { RemoteSession.parseFrom(it.data) }
    override suspend fun events(sessionId: String) = lock.withLock {
        access.opened(sessionId, clock())
        // An evicted session may still receive tail appends from its in-memory buffer.
        // Only a contiguous prefix can be restored; the relay fills everything after it.
        val rows = dao.events(sessionId)
        var count = 0
        while (count < rows.size && rows[count].seq == count + 1L) count++
        rows.subList(0, count).map { StoredEvent.parseFrom(it.data) }
    }
    override suspend fun putMachine(m: Machine) = dao.put(MachineRow(m.id, m.toByteArray()))
    override suspend fun putSession(s: RemoteSession) = dao.put(SessionRow(s.sessionId, s.machineId, s.toByteArray()))
    override suspend fun putEvents(sessionId: String, events: List<StoredEvent>) = lock.withLock {
        if (events.isNotEmpty()) {
            dao.put(events.map { EventRow(sessionId, it.seq, it.toByteArray()) })
            trims.trySend(Unit)
        }
        Unit
    }
    override suspend fun dropEvents(sessionId: String) = lock.withLock { dao.deleteEvents(sessionId) }
    override suspend fun removeMachine(id: String) = lock.withLock {
        val sessions = dao.sessionsOf(id)
        dao.removeMachine(id)
        sessions.forEach { access.remove(it) }
    }
    override suspend fun removeSession(id: String) = lock.withLock {
        dao.removeSession(id)
        access.remove(id)
    }
    override suspend fun unlistSession(id: String) = dao.unlist(id)
    override suspend fun keepOwner(s: RemoteSession) = dao.putIfAbsent(SessionRow(s.sessionId, s.machineId, s.toByteArray(), listed = false))
    override suspend fun blob(name: String) = dao.blob(name)?.data
    override suspend fun putBlob(name: String, data: ByteArray) = dao.put(BlobRow(name, data))
}
