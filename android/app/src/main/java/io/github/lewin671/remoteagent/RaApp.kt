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
import io.github.lewin671.remoteagent.core.value
import io.github.lewin671.remoteagent.data.CacheDb
import io.github.lewin671.remoteagent.data.Prefs
import io.github.lewin671.remoteagent.data.RoomCache
import io.github.lewin671.remoteagent.data.Vault
import io.github.lewin671.remoteagent.data.everyStep
import io.github.lewin671.remoteagent.data.finishAccountChange
import io.github.lewin671.remoteagent.push.Push
import io.github.lewin671.remoteagent.ui.AccountMemory
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient

/**
 * The app's single graph: the account, the sync engine and the cache. The main stream is only kept
 * while the app is in the foreground (architecture §15); in the background notifications come by
 * push.
 */
class RaApp : Application() {
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)

    // What BuildConfig would hold, read from the manifest and resources:
    // see the note on buildConfig in app/build.gradle.kts.
    val versionName: String by lazy {
        packageManager.getPackageInfo(packageName, 0).versionName.orEmpty()
    }
    val buildCommit: String
        get() = getString(R.string.build_commit)

    val debuggable: Boolean
        get() = applicationInfo.flags and android.content.pm.ApplicationInfo.FLAG_DEBUGGABLE != 0

    val vault: Vault by lazy { Vault(this) }
    val prefs: Prefs by lazy { Prefs(this) }
    val db: CacheDb by lazy { CacheDb.open(this) }
    val http: OkHttpClient by lazy { Api.defaultHttp() }
    val pendingStore by lazy { io.github.lewin671.remoteagent.data.PendingStore(this) }
    val imageEnvelopes by lazy { io.github.lewin671.remoteagent.data.ImageEnvelopes(this) }

    private val _remote = MutableStateFlow<Remote?>(null)
    val remote: StateFlow<Remote?> = _remote.asStateFlow()
    private val _securitySetup = MutableStateFlow<Account?>(null)
    val securitySetup: StateFlow<Account?> = _securitySetup.asStateFlow()
    private val _loaded = MutableStateFlow(false)
    /** The stored account has been read, or there is none. */
    val loaded: StateFlow<Boolean> = _loaded.asStateFlow()
    private val _pendingCreation = MutableStateFlow(false)
    val pendingCreation: StateFlow<Boolean> = _pendingCreation.asStateFlow()
    private val _accountError = MutableStateFlow("")
    /** Why the stored account could not be read, e.g. the phone was locked. */
    val accountError: StateFlow<String> = _accountError.asStateFlow()
    private var foreground = false
    private var loading = false
    @Volatile private var erasePending = false
    private var erasing = false
    val acceptsPush: Boolean
        get() = !erasePending && vault.hasAccount()

    // Reading the stored account, storing a joined one and erasing on logout
    // go one at a time: a read beside an erase would find the account being
    // removed and put it back in use, and an erase beside a join would take
    // the new account's keys.
    private val accountLock = Mutex()
    // The device has a default network, by the system's word.
    @Volatile private var online = true

    override fun onCreate() {
        super.onCreate()
        instance = this
        // Kept light: a push may start the process just to show a notification.
        Push.createChannels(this)
        // Images shared last time have reached the other app by now.
        scope.launch(Dispatchers.IO) {
            java.io.File(cacheDir, io.github.lewin671.remoteagent.ui.SHARED_DIR).deleteRecursively()
            pendingStore.expire()
        }
        ProcessLifecycleOwner.get()
            .lifecycle
            .addObserver(
                object : DefaultLifecycleObserver {
                    override fun onStart(owner: LifecycleOwner) {
                        foreground = true
                        loadAccount()
                        io.github.lewin671.remoteagent.update.Updater.check(this@RaApp)
                        _remote.value?.let { r ->
                            r.start()
                            r.renewLeaseIfNeeded()
                            Push.register(this@RaApp, r)
                        }
                    }

                    override fun onStop(owner: LifecycleOwner) {
                        foreground = false
                        Push.stop()
                        _remote.value?.stop()
                    }
                },
            )
        // Without a network at start no callback comes.
        online = getSystemService(ConnectivityManager::class.java).activeNetwork != null
        getSystemService(ConnectivityManager::class.java)
            .registerDefaultNetworkCallback(
                object : ConnectivityManager.NetworkCallback() {
                    override fun onAvailable(network: Network) {
                        online = true
                        _remote.value?.setNetwork(true)
                        _remote.value?.reconnectNow()
                        scope.launch { registerPush() }
                    }

                    // Only when no other network takes over as the default.
                    override fun onLost(network: Network) {
                        online = false
                        _remote.value?.setNetwork(false)
                    }
                },
            )
    }

    /**
     * A background token callback registers once with an already loaded account; it never loads
     * keys.
     */
    fun registerPush(token: String? = null) {
        if (foreground || token != null)
            _remote.value?.let { Push.register(this, it, token, autoRetry = foreground) }
    }

    /**
     * Reads the account, only in the foreground (architecture §15): a process a push started while
     * the phone was locked could not open it, and does not need the root key to show a
     * notification. A failure, such as a phone locked again meanwhile, is retried at the next
     * start.
     */
    fun discardAccount() {
        scope.launch { logout() }
    }

    fun loadAccount() {
        if (erasePending) {
            if (!erasing) scope.launch { logout() }
            return
        }
        if (_remote.value != null || _securitySetup.value != null || loading) return
        loading = true
        scope.launch {
            accountLock.withLock {
                if (erasePending) {
                    loading = false
                    return@withLock
                }
                val result =
                    withContext(Dispatchers.IO) {
                        runCatching {
                            // A crash after confirmed local adoption is already a success;
                            // do not gate its cleanup on another network registration probe.
                            vault.loadCreationOperation()?.let { draft ->
                                if (vault.hasAccount() && vault.hasSecurityState()) {
                                    val installed = vault.readSecurity()
                                    val adoptedAccount = vault.load()
                                    if (
                                        adoptedAccount != null &&
                                            installed.includesInstallation(draft.record)
                                    ) {
                                        io.github.lewin671.remoteagent.core
                                            .reconcileCompletedAdoption(
                                                adoptedAccount,
                                                installed,
                                                vault,
                                            )
                                    }
                                }
                            }
                            vault.loadCreationOperation()?.let { draft ->
                                io.github.lewin671.remoteagent.core.retryCreationRegistration(
                                    draft,
                                    http,
                                    vault::saveCreationOperation,
                                )
                            }
                            vault.loadRegistrationIntent()?.let { intent ->
                                intent.resume(http) { acct, record ->
                                    vault.initializeSecurity(record)
                                    vault.save(
                                        acct,
                                        io.github.lewin671.remoteagent.core.DeviceSecurity
                                            .NotifyRecord
                                            .fromRecord(record)
                                            .bytes(),
                                    )
                                }
                                vault.clearRegistrationIntent(intent)
                            }
                            val acct = if (vault.hasAccount()) vault.load() else null
                            val ready =
                                if (acct != null && vault.hasSecurityState()) {
                                    val record = vault.readSecurity()
                                    val accountId =
                                        acct.accountId.ifEmpty {
                                            if (acct.hasRoot)
                                                io.github.lewin671.remoteagent.core.deviceId(
                                                    io.github.lewin671.remoteagent.core.Keys.derive(
                                                            acct.root,
                                                        )
                                                        .account
                                                        .publicKey,
                                                )
                                            else ""
                                        }
                                    if (
                                        record.memberID != acct.deviceId ||
                                            (accountId.isNotEmpty() &&
                                                record.pin.account != accountId)
                                    )
                                        throw io.github.lewin671.remoteagent.core.DeviceSecurity
                                            .Failure("untrusted")
                                    io.github.lewin671.remoteagent.core.reconcileCompletedAdoption(
                                        acct,
                                        record,
                                        vault,
                                    )
                                    true
                                } else false
                            _pendingCreation.value = vault.loadCreationOperation() != null
                            acct to (ready && !_pendingCreation.value)
                        }
                    }
                loading = false
                result
                    .onSuccess { (acct, secure) ->
                        _accountError.value = ""
                        // Joined while this waited for its turn: that account is the one in use.
                        if (acct != null && _remote.value == null) {
                            if (secure) use(acct)
                            else {
                                _securitySetup.value = acct
                                lastServer = acct.server
                            }
                        }
                        _loaded.value = true
                    }
                    .onFailure { e ->
                        Log.w(TAG, "stored account does not open: ${e.javaClass.simpleName}")
                        _accountError.value =
                            io.github.lewin671.remoteagent.core.errorText(e) +
                                "。原请求和密钥已保留，可重试；若批准已过期或被拒绝，请退出并重新配对或创建。"
                    }
            }
        }
    }

    private fun use(acct: Account) {
        _pendingCreation.value = false
        _securitySetup.value = null
        // Drafts and the like are kept per account, and only in memory.
        AccountMemory.use("${acct.server} ${acct.deviceId}")
        val r =
            Remote(
                acct,
                http,
                RoomCache(db.dao(), imageEnvelopes),
                securityStorage = vault,
                log = { Log.i(TAG, it) },
            )
        io.github.lewin671.remoteagent.ui.ViewerImages.use(r)
        r.setNetwork(online)
        Push.open()
        _remote.value = r
        // Only in the foreground: a process started for a push just shows it.
        if (foreground) {
            r.start()
            r.renewLeaseIfNeeded()
            Push.register(this@RaApp, r)
        }
    }

    /** Stores a newly created account and its genesis, then starts syncing. */
    suspend fun created(
        acct: Account,
        record: io.github.lewin671.remoteagent.core.DeviceSecurity.Record,
    ) = adopt(acct, record)

    /** Stores a recovered account and the published chain, then starts syncing. */
    suspend fun recovered(
        acct: Account,
        record: io.github.lewin671.remoteagent.core.DeviceSecurity.Record,
    ) = adopt(acct, record)

    private suspend fun adopt(
        acct: Account,
        record: io.github.lewin671.remoteagent.core.DeviceSecurity.Record,
    ) = accountLock.withLock {
        withContext(Dispatchers.IO) {
            vault.clear(preserveSecurityOperation = true)
            vault.initializeSecurity(record)
            vault.save(
                acct,
                io.github.lewin671.remoteagent.core.DeviceSecurity.NotifyRecord.fromRecord(
                        record,
                    )
                    .bytes(),
            )
            vault.clearPendingSecurityOperation()
        }
        _loaded.value = true
        use(acct)
    }

    suspend fun persistPairing(
        acct: Account,
        record: io.github.lewin671.remoteagent.core.DeviceSecurity.Record,
    ) = accountLock.withLock {
        withContext(Dispatchers.IO) {
            vault.initializeSecurity(record)
            vault.save(
                acct,
                io.github.lewin671.remoteagent.core.DeviceSecurity.NotifyRecord.fromRecord(
                        record,
                    )
                    .bytes(),
            )
        }
    }

    /** Stores a newly joined account and starts syncing. */
    suspend fun joined(acct: Account) = accountLock.withLock {
        withContext(Dispatchers.IO) {
            vault.save(
                acct,
                io.github.lewin671.remoteagent.core.DeviceSecurity.NotifyRecord.fromRecord(
                        vault.readSecurity(),
                    )
                    .bytes(),
            )
        }
        use(acct)
    }

    /** The server of the account just left, to fill in when joining again. Only in memory. */
    var lastServer = ""

    /**
     * Forgets the account, the cache and the keys on this device. The keys go first; once they are
     * gone every other store is cleared whichever of them fails, so one that does not go cannot
     * leave the next account the images that waited to be sent.
     */
    suspend fun logout() = finishAccountChange {
        if (erasing) return@finishAccountChange
        erasing = true
        try {
            accountLock.withLock {
                erasePending = true
                _loaded.value = false
                _accountError.value = ""
                val leaving = _remote.value
                leaving?.let { lastServer = it.account.server }
                // Before anything here suspends: a network callback or a return
                // to the foreground meanwhile must not find the account, start
                // it and register it for push again.
                _remote.value = null
                Push.close()
                pendingStore.invalidate()
                AccountMemory.clear()
                io.github.lewin671.remoteagent.ui.HomeScopes.clear(this@RaApp)
                getSystemService(NotificationManager::class.java).cancelAll()
                leaving?.let { Push.unregister(it) }
                leaving?.close()
                try {
                    withContext(Dispatchers.IO) {
                        vault.clear()
                        everyStep(
                            { db.clearAllTables() },
                            { imageEnvelopes.clear() },
                            { pendingStore.clearAll() },
                            { prefs.clear() },
                        )
                    }
                    erasePending = false
                    _pendingCreation.value = false
                    _loaded.value = true
                } catch (e: Exception) {
                    Log.w(TAG, "account erase failed: ${e.javaClass.simpleName}")
                    _accountError.value = "没能清除这台手机上的账户和缓存，请重试"
                }
            }
        } finally {
            erasing = false
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
