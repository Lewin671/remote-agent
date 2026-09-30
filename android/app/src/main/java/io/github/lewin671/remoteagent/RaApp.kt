package io.github.lewin671.remoteagent

import android.app.Application
import android.app.NotificationManager
import android.net.ConnectivityManager
import android.net.Network
import android.util.Log
import androidx.lifecycle.DefaultLifecycleObserver
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.ProcessLifecycleOwner
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Keys
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.data.CacheDb
import io.github.lewin671.remoteagent.data.Prefs
import io.github.lewin671.remoteagent.data.RoomCache
import io.github.lewin671.remoteagent.data.Vault
import io.github.lewin671.remoteagent.push.Push
import io.github.lewin671.remoteagent.ui.AccountMemory
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient

/**
 * The app's single graph: the account, the sync engine and the cache.
 * The main stream is only kept while the app is in the foreground
 * (architecture §15); in the background notifications come by push.
 */
class RaApp : Application() {
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    val vault: Vault by lazy { Vault(this) }
    val prefs: Prefs by lazy { Prefs(this) }
    val db: CacheDb by lazy { CacheDb.open(this) }
    val http: OkHttpClient by lazy { Api.defaultHttp() }
    val pendingStore by lazy { io.github.lewin671.remoteagent.data.PendingStore(this) }
    val imageEnvelopes by lazy { io.github.lewin671.remoteagent.data.ImageEnvelopes(this) }

    private val _remote = MutableStateFlow<Remote?>(null)
    val remote: StateFlow<Remote?> = _remote.asStateFlow()
    private val _loaded = MutableStateFlow(false)
    /** The stored account has been read, or there is none. */
    val loaded: StateFlow<Boolean> = _loaded.asStateFlow()
    private val _accountError = MutableStateFlow("")
    /** Why the stored account could not be read, e.g. the phone was locked. */
    val accountError: StateFlow<String> = _accountError.asStateFlow()
    private var foreground = false
    private var loading = false

    override fun onCreate() {
        super.onCreate()
        instance = this
        // Kept light: a push may start the process just to show a notification.
        Push.createChannels(this)
        // Images shared last time have reached the other app by now.
        scope.launch(Dispatchers.IO) { java.io.File(cacheDir, io.github.lewin671.remoteagent.ui.SHARED_DIR).deleteRecursively() }
        ProcessLifecycleOwner.get().lifecycle.addObserver(object : DefaultLifecycleObserver {
            override fun onStart(owner: LifecycleOwner) {
                foreground = true
                loadAccount()
                _remote.value?.let { r ->
                    r.start()
                    scope.launch { Push.register(this@RaApp, r) }
                }
            }

            override fun onStop(owner: LifecycleOwner) {
                foreground = false
                _remote.value?.stop()
            }
        })
        getSystemService(ConnectivityManager::class.java).registerDefaultNetworkCallback(object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                _remote.value?.reconnectNow()
            }
        })
    }

    /**
     * Reads the account, only in the foreground (architecture §15): a process
     * a push started while the phone was locked could not open it, and does
     * not need the root key to show a notification. A failure, such as a
     * phone locked again meanwhile, is retried at the next start.
     */
    fun loadAccount() {
        if (_remote.value != null || loading) return
        loading = true
        scope.launch {
            val result = withContext(Dispatchers.IO) { runCatching { if (vault.hasAccount()) vault.load() else null } }
            loading = false
            result.onSuccess { acct ->
                _accountError.value = ""
                if (acct != null) use(acct)
                _loaded.value = true
            }.onFailure { e ->
                Log.w(TAG, "stored account does not open: ${e.javaClass.simpleName}")
                _accountError.value = "读不出这台手机保存的账户（${e.javaClass.simpleName}）。解锁手机后重试"
            }
        }
    }

    private fun use(acct: Account) {
        // Drafts and the like are kept per account, and only in memory.
        AccountMemory.use("${acct.server} ${acct.deviceId}")
        val r = Remote(acct, http, RoomCache(db.dao(), imageEnvelopes), log = { Log.i(TAG, it) })
        _remote.value = r
        // Only in the foreground: a process started for a push just shows it.
        if (foreground) {
            r.start()
            scope.launch { Push.register(this@RaApp, r) }
        }
    }

    /** Stores a newly joined account and starts syncing. */
    suspend fun joined(acct: Account) {
        withContext(Dispatchers.IO) { vault.save(acct, Keys.derive(acct.root).notify) }
        use(acct)
    }

    /** Forgets the account, the cache and the keys on this device. */
    suspend fun logout() {
        _remote.value?.close()
        _remote.value = null
        AccountMemory.clear()
        // Notifications may show the old account's approvals in full.
        getSystemService(NotificationManager::class.java).cancelAll()
        withContext(Dispatchers.IO) {
            vault.clear()
            db.clearAllTables()
            imageEnvelopes.clear()
            pendingStore.clearAll()
            prefs.clear()
        }
    }

    companion object {
        const val TAG = "remote-agent"
        lateinit var instance: RaApp
            private set

        /** The app's scope, or null outside the app (JVM tests). */
        fun scopeOrNull(): CoroutineScope? = if (::instance.isInitialized) instance.scope else null
    }
}
