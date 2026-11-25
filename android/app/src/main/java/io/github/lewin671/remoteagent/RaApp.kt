package io.github.lewin671.remoteagent

import android.app.Application
import android.app.KeyguardManager
import android.app.NotificationManager
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.util.Log
import androidx.lifecycle.DefaultLifecycleObserver
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.ProcessLifecycleOwner
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.CoreHost
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.SecurityExecutor
import io.github.lewin671.remoteagent.core.SecurityStoreV4
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.AccountRuntimeChangedEffect
import io.github.lewin671.remoteagent.core.proto.AppRootStage
import io.github.lewin671.remoteagent.core.proto.AppRootViewModel
import io.github.lewin671.remoteagent.core.proto.ClearAccountCachesEffect
import io.github.lewin671.remoteagent.core.proto.UnregisterPushOnExitEffect
import io.github.lewin671.remoteagent.data.CacheDb
import io.github.lewin671.remoteagent.data.Prefs
import io.github.lewin671.remoteagent.data.RoomCache
import io.github.lewin671.remoteagent.data.Vault
import io.github.lewin671.remoteagent.data.everyStep
import io.github.lewin671.remoteagent.data.finishAccountChange
import io.github.lewin671.remoteagent.push.Push
import io.github.lewin671.remoteagent.ui.AccountMemory
import io.github.lewin671.remoteagent.ui.sessionText
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient

/**
 * The app's single graph: a lazy singleton [CoreHost], the account sync engine ([Remote]), and the
 * local encrypted cache. The [CoreHost] is NEVER instantiated in [onCreate] or by lock-screen FCM
 * push delivery so background notifications never touch unlock-gated Android Keystore private keys.
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

    val vault: Vault by lazy { Vault(this, onLegacyWipe = { clearNonSettingsCaches() }) }
    @Volatile internal var testSecurityStore: SecurityStoreV4? = null
    @Volatile internal var testDeviceLocked: (() -> Boolean)? = null
    private fun isDeviceLocked(): Boolean = testDeviceLocked?.invoke()
        ?: (getSystemService(KeyguardManager::class.java)?.isDeviceLocked == true)
    private fun AppRootViewModel.isStorageBootstrapError(): Boolean =
        stage == AppRootStage.APP_ROOT_STAGE_BOOTSTRAP_ERROR && !bootstrapError.canReset
    val prefs: Prefs by lazy { Prefs(this) }
    internal val directPrefs by lazy { io.github.lewin671.remoteagent.transport.SharedPrefsDirectPreferenceStore(this) }
    internal val rtcFactory: io.github.lewin671.remoteagent.core.transport.RtcFactory by lazy { io.github.lewin671.remoteagent.transport.AndroidRtcFactory(this) }
    @Volatile internal var testRtcFactory: io.github.lewin671.remoteagent.core.transport.RtcFactory? = null
    @Volatile internal var testStunUrls: List<String>? = null
    private val dbLazy = lazy { CacheDb.open(this) }
    val db: CacheDb get() = dbLazy.value
    val eventAccess by lazy { io.github.lewin671.remoteagent.data.EventAccess(this) }
    private val httpLazy = lazy { Api.defaultHttp() }
    val http: OkHttpClient get() = httpLazy.value
    val attachments by lazy { io.github.lewin671.remoteagent.data.AttachmentManager(this) { _remote.value } }
    val imageEnvelopes by lazy { io.github.lewin671.remoteagent.data.ImageEnvelopes(this) }
    val sharedFiles by lazy { io.github.lewin671.remoteagent.data.SharedFileManager(this) { _remote.value } }

    private val coreHostLock = Any()
    @Volatile private var _coreHost: CoreHost? = null
    private var coreSecurityExecutor: SecurityExecutor? = null
    @Volatile private var clientInitDispatched = false

    /** Exposed for tests verifying that lock-screen push delivery never instantiates CoreHost. */
    internal val hasCoreHostForTest: Boolean
        get() = _coreHost != null

    /** Releases the lazy singleton CoreHost, active Remote, and test resources during fixture teardown. */
    internal fun closeCoreHostForTest() {
        _remote.value?.close()
        _remote.value = null
        Push.close()
        io.github.lewin671.remoteagent.ui.SettingsExecutor.detach()
        synchronized(coreHostLock) {
            coreSecurityExecutor?.close()
            coreSecurityExecutor = null
            _coreHost?.close()
            _coreHost = null
            clientInitDispatched = false
        }
        if (dbLazy.isInitialized()) runCatching { db.close() }
        if (httpLazy.isInitialized()) runCatching {
            http.dispatcher.cancelAll()
            http.connectionPool.evictAll()
        }
    }

    private val foregroundGatedSecurityStore: SecurityStoreV4
        get() = testSecurityStore ?: object : SecurityStoreV4 by vault {
            override fun loadLoginToken(server: String, deviceId: String) =
                if (foreground) vault.loadLoginToken(server, deviceId) else null
        }

    private val _remote = MutableStateFlow<Remote?>(null)
    val remote: StateFlow<Remote?> = _remote.asStateFlow()

    /** The session page currently visible; cleared when its activity stops or leaves it. */
    @Volatile internal var visibleSession: String? = null
        set(value) {
            field = value
            _remote.value?.coreHost?.setScheduleVisibleSession(value.orEmpty())
        }
    @Volatile private var foreground = false
    internal val isForeground: Boolean
        get() = foreground
    @Volatile private var erasePending = false
    val acceptsPush: Boolean
        get() = !erasePending && vault.hasAccount()

    /** The server of the account just left, to fill in when joining again. Only in memory. */
    @Volatile var lastServer = ""

    // Serializes Remote attach/detach and exit cache clearing so network/lifecycle callbacks
    // never race with logout or account enrollment.
    private val accountLock = Mutex()
    @Volatile private var online = true
    private var networkSignature: String? = null

    /**
     * Returns the process-wide lazy singleton [CoreHost], creating it on first foreground UI entry
     * or post-unlock background approval work. Never called during [onCreate] or lock-screen FCM.
     */
    fun ensureCoreHost(): CoreHost {
        _coreHost?.let { return it }
        return synchronized(coreHostLock) {
            _coreHost?.let { return it }
            val host = CoreHost(
                clock = System::currentTimeMillis,
                monotonic = android.os.SystemClock::elapsedRealtime,
            )
            val exec = SecurityExecutor(
                store = foregroundGatedSecurityStore,
                http = http,
                emit = host::dispatch,
                parentScope = scope,
                allowPlainHttp = true,
            )
            host.resolveUiMessage = ::sessionText
            host.onDismissUpgradeNotice = { vault.dismissUpgradeNotice() }
            host.onSecurityEffect = exec::handleEffect
            host.onPersistLastServer = { eff ->
                if (eff.server.isNotEmpty()) lastServer = eff.server
            }
            host.onRequestStoragePersist = { eff ->
                host.sendStoragePersistResult(eff.opId, persisted = true)
            }
            host.onUnregisterPushOnExit = { eff ->
                scope.launch { handleUnregisterPushOnExit(host, eff) }
            }
            host.onClearAccountCaches = { eff ->
                scope.launch { handleClearAccountCaches(host, eff) }
            }
            host.onAccountRuntimeChanged = { eff ->
                scope.launch { handleAccountRuntimeChanged(host, eff) }
            }
            coreSecurityExecutor = exec
            _coreHost = host
            host
        }
    }

    /**
     * Ensures the lazy singleton [CoreHost] exists and has received its initial [ClientInitEvent].
     * Called from foreground lifecycle and Activity UI only — never from background push delivery.
     */
    fun ensureForegroundCoreHost(): CoreHost {
        val host = ensureCoreHost()
        var retryStorage = false
        synchronized(coreHostLock) {
            if (!clientInitDispatched) {
                clientInitDispatched = true
                host.clientInit(
                    platform = "android", deviceDescription = androidDeviceDescription(this),
                    inlineCrypto = true, foreground = foreground, networkAvailable = online,
                    networkSignature = networkSignature.orEmpty(), defaultServer = lastServer,
                    requestStoragePersist = false,
                )
            } else if (foreground && !isDeviceLocked() && host.appRootView.value.isStorageBootstrapError()) {
                retryStorage = true
            }
        }
        if (retryStorage) host.appRootRetryBootstrap()
        return host
    }

    internal fun onForegroundStart() {
        foreground = true
        prewarmRtcIfAccountPresent()
        val host = ensureForegroundCoreHost()
        host.sendSecurityForeground(true)
        io.github.lewin671.remoteagent.update.Updater.check(this)
        _remote.value?.let { r ->
            r.foregroundChanged(true)
            r.start()
            r.renewLeaseIfNeeded()
            Push.register(this, r)
        }
    }

    override fun onCreate() {
        super.onCreate()
        instance = this
        // Kept light: a push may start the process while locked just to show a notification.
        // Do NOT instantiate CoreHost or touch unlock-gated Vault/Keystore here.
        Push.createChannels(this)
        // Images shared last time have reached the other app by now.
        scope.launch(Dispatchers.IO) {
            java.io.File(cacheDir, io.github.lewin671.remoteagent.ui.SHARED_DIR).deleteRecursively()
        }
        ProcessLifecycleOwner.get()
            .lifecycle
            .addObserver(
                object : DefaultLifecycleObserver {
                    override fun onStart(owner: LifecycleOwner) = onForegroundStart()

                    override fun onStop(owner: LifecycleOwner) {
                        foreground = false
                        Push.stop()
                        _coreHost?.sendSecurityForeground(false)
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
        val networkCoalescer = NetworkObservationCoalescer(scope) { signature, available ->
            observeNetwork(signature, available)
        }
        connectivity.registerDefaultNetworkCallback(object : ConnectivityManager.NetworkCallback() {
            private var current: Network? = connectivity.activeNetwork
            private var properties: LinkProperties? = current?.let(connectivity::getLinkProperties)
            private var capabilities: NetworkCapabilities? = current?.let(connectivity::getNetworkCapabilities)
            private fun scheduleChanged() {
                val network = current
                val available = network != null
                // Account attachment and observations share the main actor's order.
                val signature = networkIdentity(network, properties, capabilities)
                networkCoalescer.schedule(signature, available)
            }
            override fun onAvailable(network: Network) {
                if (current != network) {
                    current = network
                    properties = null
                    capabilities = null
                }
                scheduleChanged()
                scope.launch { registerPush() }
            }
            override fun onLinkPropertiesChanged(network: Network, linkProperties: LinkProperties) {
                if (network != current) return
                properties = linkProperties
                scheduleChanged()
            }
            override fun onCapabilitiesChanged(network: Network, networkCapabilities: NetworkCapabilities) {
                if (network != current) return
                capabilities = networkCapabilities
                scheduleChanged()
            }
            override fun onLost(network: Network) {
                if (network != current) return
                val next = connectivity.activeNetwork?.takeUnless { it == network }
                current = next
                properties = null
                capabilities = null
                if (next == null) {
                    networkCoalescer.emitNow(networkIdentity(null, null, null), false)
                } else {
                    scheduleChanged()
                }
            }
        })
    }

    /**
     * Overlaps native libwebrtc (.so + PeerConnectionFactory) startup with account bootstrap and
     * Room open during foreground launch; never runs for background push wakeups or empty vaults.
     */
    internal fun prewarmRtcIfAccountPresent(foregroundOverride: Boolean? = null): kotlinx.coroutines.Job? {
        if (!(foregroundOverride ?: foreground) || erasePending) return null
        return scope.launch(Dispatchers.IO) {
            if ((foregroundOverride ?: foreground) && !erasePending && runCatching { vault.hasAccount() }.getOrDefault(false)) {
                runCatching { (testRtcFactory ?: rtcFactory).prewarm() }
            }
        }
    }

    private fun observeCurrentNetwork(connectivity: ConnectivityManager) {
        val network = connectivity.activeNetwork
        observeNetwork(
            networkIdentity(
                network,
                network?.let(connectivity::getLinkProperties),
                network?.let(connectivity::getNetworkCapabilities),
            ),
            network != null,
        )
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

    private fun clearNonSettingsCaches() {
        runCatching { db.clearAllTables() }
        runCatching { eventAccess.clear() }
        runCatching { imageEnvelopes.clear() }
        runCatching { attachments.clearAllOnDiskAsync() }
        runCatching { sharedFiles.clearAllOnDiskAsync() }
        runCatching { directPrefs.clear() }
        runCatching { io.github.lewin671.remoteagent.ui.HomeScopes.clear(this@RaApp) }
        runCatching { getSharedPreferences("schedule-seen", MODE_PRIVATE).edit().clear().commit() }
    }

    /**
     * Ensures the shared singleton [CoreHost] (initialized via [ensureForegroundCoreHost]) and
     * [Remote] are ready for post-unlock background actions (notification quick-approval).
     * Keystore unlock failure when locked propagates so callers retry after unlock; empty store
     * returns null. Never called by lock-screen push display.
     */
    suspend fun ensureRemoteForBackgroundWork(): Remote? {
        _remote.value?.let { return it }
        if (testSecurityStore == null && !vault.hasAccount()) return null
        if (isDeviceLocked()) throw IllegalStateException("device locked")
        val wasDispatched = clientInitDispatched
        val host = ensureForegroundCoreHost()
        val root = if (wasDispatched && host.appRootView.value.isStorageBootstrapError()) {
            host.retryBootstrapAndAwait()
        } else {
            host.appRootView.first {
                it.stage != AppRootStage.APP_ROOT_STAGE_LOADING &&
                    it.stage != AppRootStage.APP_ROOT_STAGE_UNSPECIFIED
            }
        }
        if (root.stage == AppRootStage.APP_ROOT_STAGE_BOOTSTRAP_ERROR) {
            throw IllegalStateException("account bootstrap failed or keystore locked")
        }
        if (root.stage != AppRootStage.APP_ROOT_STAGE_READY) return null
        return _remote.first { it != null }
    }

    private suspend fun handleAccountRuntimeChanged(host: CoreHost, eff: AccountRuntimeChangedEffect) {
        accountLock.withLock {
            if (!eff.active) {
                if (!eff.removed) {
                    val old = _remote.value
                    _remote.value = null
                    Push.close()
                    io.github.lewin671.remoteagent.ui.SettingsExecutor.detach()
                    old?.close()
                }
                return@withLock
            }
            if (eff.server.isEmpty() || eff.deviceId.isEmpty()) return@withLock
            val cur = _remote.value
            if (cur != null &&
                cur.account.server == eff.server &&
                cur.account.deviceId == eff.deviceId &&
                cur.account.accountId == eff.accountId
            ) {
                return@withLock
            }
            _remote.value = null
            io.github.lewin671.remoteagent.ui.SettingsExecutor.detach()
            cur?.close()
            use(Account(eff.server, eff.deviceId, eff.accountId), host)
        }
    }

    private suspend fun handleUnregisterPushOnExit(host: CoreHost, eff: UnregisterPushOnExitEffect) {
        erasePending = true
        Push.close()
        val cur = _remote.value
        runCatching {
            if (cur != null) {
                Push.unregister(cur)
            } else {
                val srv = eff.server.ifEmpty { lastServer }
                val tok = eff.authToken.ifEmpty {
                    if (srv.isNotEmpty()) {
                        val devId = host.appRootView.value.deviceId
                        if (devId.isNotEmpty()) {
                            withContext(Dispatchers.IO) { runCatching { vault.loadLoginToken(srv, devId)?.token }.getOrNull() }.orEmpty()
                        } else ""
                    } else ""
                }
                if (srv.isNotEmpty() && tok.isNotEmpty()) {
                    Push.unregisterStandalone(http, srv, tok)
                }
            }
        }
        host.sendPushUnregistered(eff.opId)
    }

    private suspend fun handleClearAccountCaches(host: CoreHost, eff: ClearAccountCachesEffect) = finishAccountChange {
        var err = ""
        accountLock.withLock {
            erasePending = true
            if (eff.preserveServer.isNotEmpty()) lastServer = eff.preserveServer
            val leaving = _remote.value
            leaving?.let { if (lastServer.isEmpty()) lastServer = it.account.server }
            _remote.value = null
            Push.close()
            AccountMemory.clear()
            io.github.lewin671.remoteagent.ui.SettingsExecutor.detach()
            io.github.lewin671.remoteagent.ui.HomeScopes.clear(this@RaApp)
            getSystemService(NotificationManager::class.java).cancelAll()
            leaving?.clearAllDrafts()
            leaving?.clearAllSharedFiles()
            leaving?.close()
            try {
                withContext(Dispatchers.IO) {
                    if (testSecurityStore == null) vault.clear()
                    everyStep(
                        { db.clearAllTables() },
                        { eventAccess.clear() },
                        { imageEnvelopes.clear() },
                        { attachments.clearAllOnDisk() },
                        { sharedFiles.clearAllOnDisk() },
                        {
                            contentResolver.persistedUriPermissions.forEach { permission ->
                                runCatching {
                                    contentResolver.releasePersistableUriPermission(
                                        permission.uri,
                                        android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION,
                                    )
                                }
                            }
                        },
                        { prefs.clear() },
                        { directPrefs.clear() },
                    )
                }
                erasePending = false
            } catch (e: Exception) {
                Log.w(TAG, "account erase failed: ${e.javaClass.simpleName}")
                err = sessionText(UiMessages.commonAndroidCouldNotClearTheAccountAndCacheOnThisPhone())
            }
        }
        host.sendAccountCachesCleared(eff.opId, err)
    }

    private suspend fun use(acct: Account, sharedHost: CoreHost) {
        val cacheDb = withContext(Dispatchers.IO) { db }
        // Local drafts and pending sends belong to this account.
        AccountMemory.use("${acct.server} ${acct.deviceId}")
        val r =
            Remote(
                acct,
                http,
                RoomCache(cacheDb.dao(), eventAccess, imageEnvelopes, reclaim = cacheDb::reclaimSpace),
                onSessionRemoved = { sid ->
                    withContext(Dispatchers.IO) {
                        attachments.forgetSession(sid)
                        sharedFiles.forgetSession(sid)
                    }
                },
                monotonic = android.os.SystemClock::elapsedRealtime,
                devicePlatform = "android",
                deviceDescription = androidDeviceDescription(this),
                securityStore = foregroundGatedSecurityStore,
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
                sharedCoreHost = sharedHost,
            )
        r.homeScopeStore = io.github.lewin671.remoteagent.ui.HomeScopes.storeAdapter(this@RaApp)
        r.uiMessageResolver = { msg -> io.github.lewin671.remoteagent.ui.sessionText(msg) }
        io.github.lewin671.remoteagent.ui.ViewerImages.use(r)
        io.github.lewin671.remoteagent.ui.SettingsExecutor.attach(this@RaApp, r)
        attachments.attachRemote(r)
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

    companion object {
        const val TAG = "remote-agent"
        lateinit var instance: RaApp
            private set

        /** The app's scope, or null outside the app (JVM tests). */
        fun scopeOrNull(): CoroutineScope? = if (::instance.isInitialized) instance.scope else null

        /** The app instance, or null outside the app (pure JVM tests). */
        fun instanceOrNull(): RaApp? = if (::instance.isInitialized) instance else null
    }
}
