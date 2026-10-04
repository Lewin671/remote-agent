package io.github.lewin671.remoteagent.data

import android.content.Context
import androidx.room.Dao
import androidx.room.Database
import androidx.room.Entity
import androidx.room.Insert
import androidx.room.OnConflictStrategy
import androidx.room.Query
import androidx.room.Room
import androidx.room.RoomDatabase
import androidx.room.Transaction
import io.github.lewin671.remoteagent.core.CipherCache
import io.github.lewin671.remoteagent.proto.Machine
import io.github.lewin671.remoteagent.proto.RemoteSession
import io.github.lewin671.remoteagent.proto.StoredEvent

// The cache holds what the server sent, as it was sent: routing fields and
// envelopes. Nothing decrypted is written to disk.

@Entity(tableName = "machines", primaryKeys = ["id"])
class MachineRow(val id: String, val data: ByteArray)

@Entity(tableName = "sessions", primaryKeys = ["id"])
class SessionRow(val id: String, val machineId: String, val data: ByteArray)

@Entity(tableName = "events", primaryKeys = ["sessionId", "seq"])
class EventRow(val sessionId: String, val seq: Long, val data: ByteArray)

/** A sealed object kept by name, e.g. the account's project preferences as the server sent them. */
@Entity(tableName = "blobs", primaryKeys = ["name"])
class BlobRow(val name: String, val data: ByteArray)

@Dao
interface CacheDao {
    @Query("SELECT * FROM blobs WHERE name = :name") suspend fun blob(name: String): BlobRow?
    @Insert(onConflict = OnConflictStrategy.REPLACE) suspend fun put(b: BlobRow)
    @Query("SELECT * FROM machines") suspend fun machines(): List<MachineRow>
    @Query("SELECT * FROM sessions") suspend fun sessions(): List<SessionRow>
    @Query("SELECT * FROM events WHERE sessionId = :sid ORDER BY seq") suspend fun events(sid: String): List<EventRow>
    @Insert(onConflict = OnConflictStrategy.REPLACE) suspend fun put(m: MachineRow)
    @Insert(onConflict = OnConflictStrategy.REPLACE) suspend fun put(s: SessionRow)
    @Insert(onConflict = OnConflictStrategy.IGNORE) suspend fun put(e: List<EventRow>)
    @Query("DELETE FROM machines WHERE id = :id") suspend fun deleteMachine(id: String)
    @Query("DELETE FROM sessions WHERE id = :id") suspend fun deleteSession(id: String)
    @Query("DELETE FROM events WHERE sessionId = :sid") suspend fun deleteEvents(sid: String)
    @Query("SELECT id FROM sessions WHERE machineId = :mid") suspend fun sessionsOf(mid: String): List<String>

    // The sessions table holds the listing's first page (architecture §8
    // 「会话列表分页」); events are kept for any session that was opened, and
    // go only with a session known not to exist any more.
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

@Database(entities = [MachineRow::class, SessionRow::class, EventRow::class, BlobRow::class], version = 2)
abstract class CacheDb : RoomDatabase() {
    abstract fun dao(): CacheDao

    companion object {
        const val NAME = "cache.db"

        fun open(context: Context): CacheDb = Room.databaseBuilder(context, CacheDb::class.java, NAME)
            .fallbackToDestructiveMigration(true)
            .build()
    }
}

class RoomCache(private val dao: CacheDao, private val images: ImageEnvelopes? = null) : CipherCache {
    override suspend fun image(key: String) = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { images?.get(key) }
    override suspend fun putImage(key: String, envelope: ByteArray) = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { images?.put(key, envelope) ?: Unit }
    override suspend fun machines() = dao.machines().map { Machine.parseFrom(it.data) }
    override suspend fun sessions() = dao.sessions().map { RemoteSession.parseFrom(it.data) }
    override suspend fun events(sessionId: String) = dao.events(sessionId).map { StoredEvent.parseFrom(it.data) }
    override suspend fun putMachine(m: Machine) = dao.put(MachineRow(m.id, m.toByteArray()))
    override suspend fun putSession(s: RemoteSession) = dao.put(SessionRow(s.sessionId, s.machineId, s.toByteArray()))
    override suspend fun putEvents(sessionId: String, events: List<StoredEvent>) =
        dao.put(events.map { EventRow(sessionId, it.seq, it.toByteArray()) })
    override suspend fun removeMachine(id: String) = dao.removeMachine(id)
    override suspend fun removeSession(id: String) = dao.removeSession(id)
    override suspend fun unlistSession(id: String) = dao.deleteSession(id)
    override suspend fun blob(name: String) = dao.blob(name)?.data
    override suspend fun putBlob(name: String, data: ByteArray) = dao.put(BlobRow(name, data))
}
