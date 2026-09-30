// The account's project preferences (architecture §8, 项目列表偏好):
// pinned and hidden projects, sealed with the account-prefs key and
// replaced on the server only if its revision is still the one a change is
// based on. A conflict redoes the one operation on the newer object, so
// what other clients changed meanwhile stays.
package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.AccountPrefsObject
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.ProjectPref
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.ProjectSettings

const val PROJECT_PREFS = "project-prefs"
const val RECENT_OPS = 64

/** One change to the project preferences. Each names the state it leads to, never a toggle, so redoing it on a newer object means the same. */
sealed interface PrefsOp {
    val path: String

    data class Pin(override val path: String) : PrefsOp
    data class Unpin(override val path: String) : PrefsOp
    data class Hide(override val path: String) : PrefsOp
    data class Unhide(override val path: String) : PrefsOp
    data class SetSettings(val settings: ProjectSettings) : PrefsOp {
        override val path: String get() = settings.path
    }
}

/**
 * [op] applied to [base]. Pinning keeps the order projects were pinned in;
 * hiding also unpins, and restoring does not pin again (the project goes
 * back among the others). [opId] is remembered so a client whose reply was
 * lost can tell its change is in.
 */
fun applyPrefsOp(base: ProjectPrefs, op: PrefsOp, opId: String, now: Long): ProjectPrefs {
    val pinned = base.pinnedList.toMutableList()
    val hidden = base.hiddenList.toMutableList()
    fun pref(p: String) = ProjectPref.newBuilder().setPath(p).setSinceMs(now).build()
    when (op) {
        is PrefsOp.Pin -> {
            hidden.removeAll { it.path == op.path }
            if (pinned.none { it.path == op.path }) pinned += pref(op.path)
        }
        is PrefsOp.Unpin -> pinned.removeAll { it.path == op.path }
        is PrefsOp.Hide -> {
            pinned.removeAll { it.path == op.path }
            if (hidden.none { it.path == op.path }) hidden += pref(op.path)
        }
        is PrefsOp.Unhide -> hidden.removeAll { it.path == op.path }
        is PrefsOp.SetSettings -> Unit
    }
    val ops = (base.recentOpsList + opId).takeLast(RECENT_OPS)
    // toBuilder keeps fields this client did not change, including unknown
    // protobuf fields written by a newer client.
    return base.toBuilder().setSchema(1).clearPinned().addAllPinned(pinned)
        .clearHidden().addAllHidden(hidden).clearRecentOps().addAllRecentOps(ops)
        .apply {
            if (op is PrefsOp.SetSettings) {
                val entries = base.settingsList.filter { it.path != op.path } + op.settings
                clearSettings().addAllSettings(entries)
            }
        }.build()
}

/** The preferences as the account has them, decrypted, at a revision. */
data class PrefsObject(val revision: Long, val prefs: ProjectPrefs) {
    val pinned: List<String> get() = prefs.pinnedList.map { it.path }
    val hidden: Set<String> get() = prefs.hiddenList.map { it.path }.toSet()

    companion object {
        val EMPTY = PrefsObject(0, ProjectPrefs.newBuilder().setSchema(1).build())
    }
}

class PrefsConflictException : Exception("项目偏好一直在被其他客户端修改，没能保存")

/** Seals and opens the prefs object; the header binds its ID and revision. */
class PrefsCodec(private val key: ByteArray, private val sender: String) {
    fun seal(revision: Long, prefs: ProjectPrefs): Envelope = Envelopes.seal(
        key,
        Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_ACCOUNT_PREFS).setRequestId(PROJECT_PREFS).setSeq(revision).setSender(sender).build(),
        prefs,
    )

    /** The object's content; its sealed revision must be the one the server says it is. */
    fun open(o: AccountPrefsObject): PrefsObject {
        if (o.revision == 0L || !o.hasEnvelope()) return PrefsObject.EMPTY
        val (h, p) = Envelopes.openMessage(key, o.envelope, Place(PayloadKind.PAYLOAD_KIND_ACCOUNT_PREFS, requestId = PROJECT_PREFS), ProjectPrefs::parseFrom)
        if (h.seq != o.revision) throw OpenException("项目偏好的修订号被改动")
        return PrefsObject(o.revision, p)
    }
}

/** The server's side of a write: applied, or the object as stored now. */
data class PutResult(val applied: Boolean, val current: AccountPrefsObject)

/**
 * Writes one operation: seal it on [base], and on a conflict redo it on
 * the object the server returned. A write whose outcome is unknown (the
 * reply was lost) is settled by reading the object back: its op ID in
 * recent_ops means it went in. Returns what the account has afterwards.
 */
suspend fun writePrefsOp(
    codec: PrefsCodec,
    base: PrefsObject,
    op: PrefsOp,
    opId: String,
    now: Long,
    put: suspend (expected: Long, env: Envelope) -> PutResult,
    get: suspend () -> AccountPrefsObject,
    tries: Int = 5,
): PrefsObject {
    var cur = base
    repeat(tries) {
        if (opId in cur.prefs.recentOpsList) return cur
        val next = applyPrefsOp(cur.prefs, op, opId, now)
        val env = codec.seal(cur.revision + 1, next)
        val r = try {
            put(cur.revision, env)
        } catch (e: Exception) {
            // Did it go in? Only the stored object can say.
            val stored = codec.open(get())
            if (opId in stored.prefs.recentOpsList) return stored
            throw e
        }
        if (r.applied) return PrefsObject(cur.revision + 1, next)
        val latest = codec.open(r.current)
        // Never go back to an older revision than the one known here.
        if (latest.revision < cur.revision) throw OpenException("服务器返回了更旧的项目偏好")
        cur = latest
    }
    throw PrefsConflictException()
}

/** Keeps the value with the higher revision: a push or reply never takes the list back. */
fun newerPrefs(have: PrefsObject?, got: PrefsObject): PrefsObject = if (have == null || got.revision >= have.revision) got else have
