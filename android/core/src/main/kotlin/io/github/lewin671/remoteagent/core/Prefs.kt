// The account's project preferences (architecture §8, 项目列表偏好):
// pinned and hidden projects, sealed with the account-prefs key and
// replaced on the server only if its revision is still the one a change is
// based on. A conflict redoes the one operation on the newer object, so
// what other clients changed meanwhile stays.
package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.AccountPrefsObject
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.AgentSettings
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

    /**
     * A change to one project's settings (architecture §13). It names only
     * what changes, so redone on a newer object the rest stays as another
     * client left it.
     */
    data class SetSettings(
        override val path: String,
        /** Stored when the project has no settings yet, or with [moved]. */
        val machineId: String,
        /** The user chose another machine: every CLI starts over from that machine's defaults. */
        val moved: Boolean = false,
        /** The CLI a new session just used, or null to keep the one remembered. */
        val agent: Agent? = null,
        /** The CLIs' sets to store, each replacing that CLI's entry. */
        val sets: List<AgentSettings> = emptyList(),
        /** A start: [sets] only fill in a CLI that has no entry yet. */
        val keepExisting: Boolean = false,
    ) : PrefsOp
}

/** The CLIs a project keeps a set of settings for. */
val settingsAgents: List<Agent> get() = AGENTS.map { it.agent }

/** [op] applied to a project's stored settings, or to none. */
fun applySettings(stored: ProjectSettings?, op: PrefsOp.SetSettings, now: Long): ProjectSettings {
    val b = stored?.toBuilder() ?: ProjectSettings.newBuilder().setPath(op.path).setMachineId(op.machineId)
    val sets = LinkedHashMap<Agent, AgentSettings>()
    for (s in b.agentsList) sets[s.agent] = s
    if (op.moved) {
        b.setMachineId(op.machineId)
        // Models are per machine: an empty entry means that machine's defaults.
        for (a in settingsAgents) sets[a] = AgentSettings.newBuilder().setAgent(a).build()
    }
    if (op.agent != null) b.setAgent(op.agent)
    for (s in op.sets) if (!op.keepExisting || s.agent !in sets) sets[s.agent] = s
    return b.clearAgents().addAllAgents(sets.values.sortedBy { it.agentValue }).setUpdatedMs(now).build()
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
                val entries = base.settingsList.filter { it.path != op.path } + applySettings(base.settingsList.firstOrNull { it.path == op.path }, op, now)
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

class PrefsConflictException : MessageException(UiMessages.errorsAndroidOtherClientsKeptChangingProjectPreferencesCouldNotSave().messageText())

fun newerPrefs(have: PrefsObject?, got: PrefsObject): PrefsObject = if (have == null || got.revision >= have.revision) got else have
