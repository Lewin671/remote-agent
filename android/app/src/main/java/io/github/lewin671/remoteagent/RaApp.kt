package io.github.lewin671.remoteagent

import android.app.Application
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
    lateinit var vault: Vault
    lateinit var prefs: Prefs
    lateinit var db: CacheDb
    val http: OkHttpClient by lazy { Api.defaultHttp() }

    private val _remote = MutableStateFlow<Remote?>(null)
    val remote: StateFlow<Remote?> = _remote.asStateFlow()
    private val _loaded = MutableStateFlow(false)
    /** The stored account has been read. */
    val loaded: StateFlow<Boolean> = _loaded.asStateFlow()
    private var foreground = false

    override fun onCreate() {
        super.onCreate()
        instance = this
        vault = Vault(this)
        prefs = Prefs(this)
        db = CacheDb.open(this)
        Push.createChannels(this)
        scope.launch {
            val acct = withContext(Dispatchers.IO) {
                try {
                    vault.load()
                } catch (e: Exception) {
                    Log.w(TAG, "stored account does not open: ${e.javaClass.simpleName}")
                    null
                }
            }
            if (acct != null) use(acct)
            _loaded.value = true
        }
        ProcessLifecycleOwner.get().lifecycle.addObserver(object : DefaultLifecycleObserver {
            override fun onStart(owner: LifecycleOwner) {
                foreground = true
                _remote.value?.start()
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

    private fun use(acct: Account) {
        val r = Remote(acct, http, RoomCache(db.dao()), log = { Log.i(TAG, it) })
        _remote.value = r
        if (foreground) r.start()
        scope.launch { Push.register(this@RaApp, r) }
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
        withContext(Dispatchers.IO) {
            vault.clear()
            db.clearAllTables()
            prefs.clear()
        }
    }

    companion object {
        const val TAG = "remote-agent"
        lateinit var instance: RaApp
            private set
    }
}
