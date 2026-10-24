package io.github.lewin671.remoteagent.data

import android.content.Context
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map

private val Context.store by preferencesDataStore("prefs")

enum class ThemeMode { SYSTEM, LIGHT, DARK }

data class Settings(
    val theme: ThemeMode = ThemeMode.SYSTEM,
    /** Push when a session waits for the user (approvals, questions, plans, popups). */
    val notifyAttention: Boolean = true,
    /** Push when a turn ends. */
    val notifyTurnEnded: Boolean = false,
    val notifyScheduled: Boolean = true,
    /** Show approval content in notifications and allow deciding there. Off by default. */
    val showContent: Boolean = false,
    /** Web client address used in pairing QR codes; empty means derived from the server. */
    val webOrigin: String = "",
    /** The version code of a release the user put off: the home page stops mentioning it. */
    val skippedUpdate: Long = 0,
)

/** Preferences; none of them are secret or content. */
class Prefs(private val context: Context) {
    private val theme = stringPreferencesKey("theme")
    private val attention = booleanPreferencesKey("notify_attention")
    private val turnEnded = booleanPreferencesKey("notify_turn_ended")
    private val scheduled = booleanPreferencesKey("notify_scheduled")
    private val content = booleanPreferencesKey("notify_show_content")
    private val web = stringPreferencesKey("web_origin")
    private val skipped = androidx.datastore.preferences.core.longPreferencesKey("skipped_update")

    val settings: Flow<Settings> = context.store.data.map { p ->
        Settings(
            theme = p[theme]?.let { runCatching { ThemeMode.valueOf(it) }.getOrNull() } ?: ThemeMode.SYSTEM,
            notifyAttention = p[attention] ?: true,
            notifyTurnEnded = p[turnEnded] ?: false,
            notifyScheduled = p[scheduled] ?: true,
            showContent = p[content] ?: false,
            webOrigin = p[web] ?: "",
            skippedUpdate = p[skipped] ?: 0,
        )
    }

    suspend fun update(f: (Settings) -> Settings) {
        context.store.edit { p ->
            val cur = Settings(
                theme = p[theme]?.let { runCatching { ThemeMode.valueOf(it) }.getOrNull() } ?: ThemeMode.SYSTEM,
                notifyAttention = p[attention] ?: true,
                notifyTurnEnded = p[turnEnded] ?: false,
            notifyScheduled = p[scheduled] ?: true,
                showContent = p[content] ?: false,
                webOrigin = p[web] ?: "",
            skippedUpdate = p[skipped] ?: 0,
            )
            val n = f(cur)
            p[theme] = n.theme.name
            p[attention] = n.notifyAttention
            p[turnEnded] = n.notifyTurnEnded
            p[scheduled] = n.notifyScheduled
            p[content] = n.showContent
            p[web] = n.webOrigin
            p[skipped] = n.skippedUpdate
        }
    }

    suspend fun clear() {
        context.store.edit { it.clear() }
    }
}
