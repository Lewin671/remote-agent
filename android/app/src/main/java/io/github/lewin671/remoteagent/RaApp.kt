package io.github.lewin671.remoteagent

import io.github.lewin671.remoteagent.ui.sessionText

import io.github.lewin671.remoteagent.core.UiMessages

import android.app.Application
import android.app.NotificationManager
import android.net.ConnectivityManager
import android.net.Network
import android.net.LinkProperties
import android.net.NetworkCapabilities
import android.util.Log
import androidx.lifecycle.DefaultLifecycleObserver
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.ProcessLifecycleOwner
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.PairingController
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.SecurityStoreV4
import io.github.lewin671.remoteagent.core.StandaloneSecurityHost
import io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind
import io.github.lewin671.remoteagent.core.proto.SecurityExitAdvice
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus
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

    // Pairing approval outlives its page and foreground stream. The Remote still
    // gates authority signatures; waiting for an already signed delivery does not.
    private var pairingScope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val pairings = mutableMapOf<Triple<Remote, String, String>, PairingController>()

    fun pairing(remote: Remote, replacesDeviceId: String, webOrigin: String = ""): PairingController =
        pairings.getOrPut(Triple(remote, replacesDeviceId, webOrigin)) {
            PairingController(
                scope = pairingScope,
                operationScope = pairingScope,
                client = remote.securityClient,
                webOrigin = webOrigin,
                replacesDeviceId = replacesDeviceId,
                onCompleted = { scope.launch { runCatching { remote.refreshDevices() } } },
            )
        }

    // What BuildConfig would hold, read from the manifest and resources:
    // see the note on buildConfig in app/build.gradle.kts.
    val versionName: String by lazy {
        packageManager.getPackageInfo(packageName, 0).versionName.orEmpty()
    }
    val buildCommit: String
        get() = getString(R.string.build_commit)

    val debuggable: Boolean
        get() = applicationInfo.flags and android.content.pm.ApplicationInfo.FLAG_DEBUGGABLE != 0

    val vault: Vault by lazy { Vault(this, onLegacyWipe = { clearNonSettingsCaches() }) }
    val prefs: Prefs by lazy { Prefs(this) }
    internal val directPrefs by lazy { io.github.lewin671.remoteagent.transport.SharedPrefsDirectPreferenceStore(this) }
    internal val rtcFactory: io.github.lewin671.remoteagent.core.transport.RtcFactory by lazy { io.github.lewin671.remoteagent.transport.AndroidRtcFactory(this) }
    @Volatile internal var testRtcFactory: io.github.lewin671.remoteagent.core.transport.RtcFactory? = null
    @Volatile internal var testStunUrls: List<String>? = null
    val db: CacheDb by lazy { CacheDb.open(this) }
    val eventAccess by lazy { io.github.lewin671.remoteagent.data.EventAccess(this) }
    val http: OkHttpClient by lazy { Api.defaultHttp() }
    val pendingStore by lazy { io.github.lewin671.remoteagent.data.PendingStore(this) }
    val imageEnvelopes by lazy { io.github.lewin671.remoteagent.data.ImageEnvelopes(this) }
    val sharedFiles by lazy { io.github.lewin671.remoteagent.data.SharedFileManager(this) { _remote.value } }

    private val _remote = MutableStateFlow<Remote?>(null)
    val remote: StateFlow<Remote?> = _remote.asStateFlow()
    private val _bootstrapStatus = MutableStateFlow<SecurityWorkflowStatus?>(null)
    val bootstrapStatus: StateFlow<SecurityWorkflowStatus?> = _bootstrapStatus.asStateFlow()
    private val _retainedDenied = MutableStateFlow(false)
    val retainedDenied: StateFlow<Boolean> = _retainedDenied.asStateFlow()
    private val _joinGeneration = MutableStateFlow(0)
    val joinGeneration: StateFlow<Int> = _joinGeneration.asStateFlow()
    var joinDirectly = false
        private set

    internal fun updateBootstrapStatus(status: SecurityWorkflowStatus) {
        _bootstrapStatus.value = status
        if (status.server.isNotEmpty()) lastServer = status.server
    }

    internal fun setRetainedDenied(denied: Boolean) {
        _retainedDenied.value = denied
    }

    internal fun clearJoinState(pairing: Boolean) {
        _bootstrapStatus.value?.server?.takeIf { it.isNotEmpty() }?.let { lastServer = it }
        _bootstrapStatus.value = null
        _retainedDenied.value = false
        joinDirectly = pairing
        _joinGeneration.value += 1
    }

    private val _loaded = MutableStateFlow(false)
    /** The stored account has been read, or there is none. */
    val loaded: StateFlow<Boolean> = _loaded.asStateFlow()
    private val _accountError = MutableStateFlow("")
    /** Why the stored account could not be read, e.g. the phone was locked. */
    val accountError: StateFlow<String> = _accountError.asStateFlow()
    /** The session page currently visible; cleared when its activity stops or leaves it. */
    @Volatile internal var visibleSession: String? = null
        set(value) {
            field = value
            _remote.value?.coreHost?.setScheduleVisibleSession(value.orEmpty())
        }
    @Volatile private var foreground = false
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
    private var networkSignature: String? = null

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
                            r.foregroundChanged(true)
                            r.start()
                            r.renewLeaseIfNeeded()
                            Push.register(this@RaApp, r)
                        }
                    }

                    override fun onStop(owner: LifecycleOwner) {
                        foreground = false
                        Push.stop()
                        _remote.value?.let { r ->
                            r.foregroundChanged(false)
                            r.stop()
                        }
                        // Clearing waits for an in-flight security read; keep it off the main thread.
                        scope.launch(Dispatchers.IO) { vault.clearMemoryCache() }
                    }
                },
            )
        // Without a network at start no callback comes.
        val connectivity = getSystemService(ConnectivityManager::class.java)
        observeCurrentNetwork(connectivity)
        connectivity.registerDefaultNetworkCallback(object : ConnectivityManager.NetworkCallback() {
            private var current: Network? = connectivity.activeNetwork
            private var properties: LinkProperties? = current?.let(connectivity::getLinkProperties)
            private var capabilities: NetworkCapabilities? = current?.let(connectivity::getNetworkCapabilities)
            private fun changed() {
                val network = current
                val available = network != null
                // Account attachment and observations share the main actor's order.
                val signature = networkIdentity(network, properties, capabilities)
                scope.launch { observeNetwork(signature, available) }
            }
            override fun onAvailable(network: Network) {
                current = network
                properties = connectivity.getLinkProperties(network)
                capabilities = connectivity.getNetworkCapabilities(network)
                changed()
                scope.launch { registerPush() }
            }
            override fun onLinkPropertiesChanged(network: Network, linkProperties: LinkProperties) {
                if (network != current) return
                properties = linkProperties
                changed()
            }
            override fun onCapabilitiesChanged(network: Network, networkCapabilities: NetworkCapabilities) {
                if (network != current) return
                capabilities = networkCapabilities
                changed()
            }
            override fun onLost(network: Network) {
                if (network != current) return
                current = connectivity.activeNetwork?.takeUnless { it == network }
                properties = current?.let(connectivity::getLinkProperties)
                capabilities = current?.let(connectivity::getNetworkCapabilities)
                changed()
            }
        })
    }

    private fun observeCurrentNetwork(connectivity: ConnectivityManager) {
        val network = connectivity.activeNetwork
        observeNetwork(networkIdentity(network, network?.let(connectivity::getLinkProperties),
            network?.let(connectivity::getNetworkCapabilities)), network != null)
    }

    /** Called on the app's main dispatcher, including observations before account loading. */
    internal fun observeNetwork(signature: String, available: Boolean) {
        if (networkSignature == signature && online == available) return
        networkSignature = signature
        online = available
        _remote.value?.networkChanged(signature, available)
    }

    internal fun attachNetwork(remote: Remote) {
        if (networkSignature == null) observeCurrentNetwork(getSystemService(ConnectivityManager::class.java))
        remote.networkChanged(checkNotNull(networkSignature), online)
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

    private fun clearNonSettingsCaches() {
        runCatching { db.clearAllTables() }
        runCatching { eventAccess.clear() }
        runCatching { imageEnvelopes.clear() }
        runCatching { pendingStore.clearAll() }
        runCatching { sharedFiles.clearAllOnDiskAsync() }
        runCatching { directPrefs.clear() }
        runCatching { io.github.lewin671.remoteagent.ui.HomeScopes.clear(this@RaApp) }
        runCatching { getSharedPreferences("schedule-seen", MODE_PRIVATE).edit().clear().commit() }
    }

    private suspend fun runBootstrapHost(): Pair<SecurityWorkflowStatus, Boolean> {
        val host = StandaloneSecurityHost(
            store = vault,
            http = http,
            messageResolver = ::sessionText,
            allowPlainHttp = true,
            monotonic = android.os.SystemClock::elapsedRealtime,
        )
        return try {
            var st = host.client.bootstrap()
            if (st.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_REGISTRATION) {
                val resumed = host.client.resumePendingOp()
                if (resumed.hasStatus()) st = resumed.status
            }
            var denied = false
            if (st.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_EMPTY &&
                st.deviceId.isNotEmpty() && st.server.isNotEmpty()
            ) {
                denied = host.client.checkRetainedDenied()
            }
            when (st.bootstrap) {
                SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_CLOCK_SKEWED ->
                    throw IllegalStateException(sessionText(UiMessages.errorsThisDeviceSClockIsBehindTheAccountState()))
                SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_INVALID ->
                    throw IllegalStateException(sessionText(UiMessages.errorsThisDeviceSSecurityStateCouldNotBeVerified()))
                else -> {}
            }
            st to denied
        } finally {
            host.close()
        }
    }

    /**
     * Loads the stored account for background actions (notifications). Keystore unlock failure when
     * locked propagates so callers retry after unlock; empty store returns null.
     */
    suspend fun loadAccountForBackgroundWork(): Account? {
        _remote.value?.account?.let { return it }
        if (!vault.hasAccount()) return null
        val (st, _) = withContext(Dispatchers.IO) { runBootstrapHost() }
        if ((st.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_READY ||
                st.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_REMOVED ||
                st.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_CLOCK_SKEWED) &&
            st.server.isNotEmpty() && st.deviceId.isNotEmpty()
        ) {
            return Account(st.server, st.deviceId, st.accountId)
        }
        return null
    }

    fun loadAccount() {
        if (erasePending) {
            if (!erasing) scope.launch { logout(pairing = joinDirectly) }
            return
        }
        if (_remote.value != null || _loaded.value || loading) return
        loading = true
        scope.launch {
            accountLock.withLock {
                if (erasePending) {
                    loading = false
                    return@withLock
                }
                val result = withContext(Dispatchers.IO) { runCatching { runBootstrapHost() } }
                loading = false
                result
                    .onSuccess { (st, denied) ->
                        _accountError.value = ""
                        _bootstrapStatus.value = st
                        _retainedDenied.value = denied
                        if (st.server.isNotEmpty()) lastServer = st.server
                        val readyForRemote =
                            st.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_READY ||
                                st.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_REMOVED
                        if (readyForRemote &&
                            st.server.isNotEmpty() && st.deviceId.isNotEmpty() &&
                            _remote.value == null
                        ) {
                            use(Account(st.server, st.deviceId, st.accountId))
                        }
                        _loaded.value = true
                    }
                    .onFailure { e ->
                        vault.clearMemoryCache()
                        Log.w(TAG, "stored account does not open: ${e.javaClass.simpleName}")
                        _accountError.value =
                            io.github.lewin671.remoteagent.ui.displayError(e) +
                                sessionText(UiMessages.commonAndroidTheOriginalRequestAndKeysAreSavedForRetryIf())
                    }
            }
        }
    }

    private suspend fun use(acct: Account) {
        val cacheDb = withContext(Dispatchers.IO) { db }
        joinDirectly = false
        _retainedDenied.value = false
        // Local drafts and pending sends belong to this account.
        AccountMemory.use("${acct.server} ${acct.deviceId}")
        val localFiles = pendingStore.scoped()
        val accountToken = AccountMemory.token
        val r =
            Remote(
                acct,
                http,
                RoomCache(cacheDb.dao(), eventAccess, imageEnvelopes, reclaim = cacheDb::reclaimSpace),
                onSessionRemoved = { sid ->
                    withContext(Dispatchers.Main.immediate) {
                        if (accountToken === AccountMemory.token) {
                            io.github.lewin671.remoteagent.ui.ImageDrafts.forgetSession(sid)
                            io.github.lewin671.remoteagent.ui.Outbox.forgetSession(sid)
                        }
                    }
                    withContext(Dispatchers.IO) {
                        localFiles.clearFolder("session:$sid")
                        io.github.lewin671.remoteagent.ui.OutgoingRecords(localFiles).removeSession(sid)
                        sharedFiles.forgetSession(sid)
                    }
                },
                monotonic = android.os.SystemClock::elapsedRealtime,
                securityStore = object : SecurityStoreV4 by vault {
                    override fun loadLoginToken(server: String, deviceId: String) =
                        if (foreground) vault.loadLoginToken(server, deviceId) else null
                },
                scheduleSeenStore = object : io.github.lewin671.remoteagent.core.ScheduleSeenStore {
                    private fun keyFor(machineId: String): String =
                        "ra-schedule-seen:" + kotlinx.serialization.json.JsonArray(
                            listOf(acct.server, acct.deviceId, machineId).map { kotlinx.serialization.json.JsonPrimitive(it) },
                        ).toString()

                    override fun load(machineId: String): List<io.github.lewin671.remoteagent.core.proto.ScheduleSeenToken>? {
                        val raw = getSharedPreferences("schedule-seen", MODE_PRIVATE)
                            .getString(keyFor(machineId), null) ?: return null
                        return try {
                            val arr = kotlinx.serialization.json.Json.parseToJsonElement(raw) as? kotlinx.serialization.json.JsonArray
                                ?: return emptyList()
                            arr.mapNotNull { el ->
                                val pair = el as? kotlinx.serialization.json.JsonArray ?: return@mapNotNull null
                                if (pair.size < 2) return@mapNotNull null
                                val task = (pair[0] as? kotlinx.serialization.json.JsonPrimitive)?.content ?: return@mapNotNull null
                                val run = (pair[1] as? kotlinx.serialization.json.JsonPrimitive)?.content ?: return@mapNotNull null
                                io.github.lewin671.remoteagent.core.proto.ScheduleSeenToken.newBuilder()
                                    .setTaskId(task)
                                    .setRunId(run)
                                    .build()
                            }
                        } catch (_: Exception) {
                            emptyList()
                        }
                    }

                    override fun save(
                        machineId: String,
                        tokens: List<io.github.lewin671.remoteagent.core.proto.ScheduleSeenToken>,
                    ) {
                        val json = kotlinx.serialization.json.JsonArray(
                            tokens.map { tok ->
                                kotlinx.serialization.json.JsonArray(
                                    listOf(
                                        kotlinx.serialization.json.JsonPrimitive(tok.taskId),
                                        kotlinx.serialization.json.JsonPrimitive(tok.runId),
                                    ),
                                )
                            },
                        ).toString()
                        getSharedPreferences("schedule-seen", MODE_PRIVATE).edit().putString(keyFor(machineId), json).apply()
                    }

                    override fun delete(machineId: String) {
                        getSharedPreferences("schedule-seen", MODE_PRIVATE).edit().remove(keyFor(machineId)).apply()
                    }
                },
                onDismissScheduleNotifications = { runs ->
                    io.github.lewin671.remoteagent.push.Notifier.dismissScheduled(this@RaApp, runs)
                },
                log = { Log.i(TAG, it) },
                rtcFactory = testRtcFactory ?: rtcFactory,
                directPrefs = directPrefs,
                stunUrls = testStunUrls,
            )
        r.homeScopeStore = io.github.lewin671.remoteagent.ui.HomeScopes.storeAdapter(this@RaApp)
        r.uiMessageResolver = { msg -> io.github.lewin671.remoteagent.ui.sessionText(msg) }
        io.github.lewin671.remoteagent.ui.ViewerImages.use(r)
        io.github.lewin671.remoteagent.ui.SettingsExecutor.attach(this@RaApp, r)
        sharedFiles.attachRemote(r)
        attachNetwork(r)
        Push.open()
        _remote.value = r
        // Only in the foreground: a process started for a push just shows it.
        if (foreground) {
            r.start()
            r.renewLeaseIfNeeded()
            Push.register(this@RaApp, r)
        }
    }

    /** Adopts a newly enrolled account (created, recovered, or joined) persisted by Core and starts syncing. */
    suspend fun adoptBootstrappedAccount(status: SecurityWorkflowStatus? = null) = accountLock.withLock {
        val resolved = if (status != null &&
            (status.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_READY ||
                status.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_REMOVED) &&
            status.server.isNotEmpty() && status.deviceId.isNotEmpty()
        ) {
            status
        } else {
            withContext(Dispatchers.IO) { runBootstrapHost().first }
        }
        check(
            (resolved.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_READY ||
                resolved.bootstrap == SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_REMOVED) &&
                resolved.server.isNotEmpty() && resolved.deviceId.isNotEmpty(),
        ) { "Security account is not ready" }
        _bootstrapStatus.value = resolved
        _retainedDenied.value = false
        _loaded.value = true
        use(Account(resolved.server, resolved.deviceId, resolved.accountId))
    }

    /** Evaluates exit advice when no active [Remote] exists (unauthenticated/removed exit). */
    suspend fun checkExitAdvice(): SecurityExitAdvice = withContext(Dispatchers.IO) {
        val host = StandaloneSecurityHost(
            store = vault,
            http = http,
            messageResolver = ::sessionText,
            allowPlainHttp = true,
            monotonic = android.os.SystemClock::elapsedRealtime,
        )
        try {
            host.client.bootstrap()
            host.client.checkExitAdvice()
        } finally {
            host.close()
        }
    }

    /** The server of the account just left, to fill in when joining again. Only in memory. */
    var lastServer = ""

    /**
     * Forgets the account, the cache and the keys on this device. The keys go first; once they are
     * gone every other store is cleared whichever of them fails, so one that does not go cannot
     * leave the next account the images that waited to be sent.
     */
    suspend fun logout(pairing: Boolean = false) = finishAccountChange {
        if (erasing) return@finishAccountChange
        erasing = true
        try {
            accountLock.withLock {
                erasePending = true
                _loaded.value = false
                _accountError.value = ""
                clearJoinState(pairing)
                val leaving = _remote.value
                leaving?.let { lastServer = it.account.server }
                // Before anything here suspends: a network callback or a return
                // to the foreground meanwhile must not find the account, start
                // it and register it for push again.
                _remote.value = null
                pairingScope.coroutineContext[kotlinx.coroutines.Job]?.cancel()
                pairings.clear()
                pairingScope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
                Push.close()
                pendingStore.invalidate()
                AccountMemory.clear()
                io.github.lewin671.remoteagent.ui.SettingsExecutor.detach()
                io.github.lewin671.remoteagent.ui.HomeScopes.clear(this@RaApp)
                getSystemService(NotificationManager::class.java).cancelAll()
                leaving?.let { Push.unregister(it) }
                leaving?.clearAllSharedFiles()
                leaving?.close()
                try {
                    withContext(Dispatchers.IO) {
                        vault.clear()
                        everyStep(
                            { db.clearAllTables() },
                            { eventAccess.clear() },
                            { imageEnvelopes.clear() },
                            { pendingStore.clearAll() },
                            { sharedFiles.clearAllOnDisk() },
                            { contentResolver.persistedUriPermissions.forEach { permission ->
                                runCatching { contentResolver.releasePersistableUriPermission(permission.uri, android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION) }
                            } },
                            { prefs.clear() },
                            { directPrefs.clear() },
                        )
                    }
                    erasePending = false
                    _bootstrapStatus.value = null
                    _retainedDenied.value = false
                    _loaded.value = true
                } catch (e: Exception) {
                    Log.w(TAG, "account erase failed: ${e.javaClass.simpleName}")
                    _accountError.value = sessionText(UiMessages.commonAndroidCouldNotClearTheAccountAndCacheOnThisPhone())
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
