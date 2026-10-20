package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.LoginResponse
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** A bearer credential. Deliberately has no generated toString containing the secret. */
class LoginToken(val value: String, val expiresMs: Long) {
    // Refresh before a lossy reconnect can straddle expiry, matching the web client.
    fun valid(now: Long) = value.isNotEmpty() && expiresMs >= now + LOGIN_REFRESH_MARGIN_MS
}

/** Implementations must bind credentials to the account and protect them like its device seed. */
interface LoginTokenStore {
    fun loadLoginToken(account: Account): LoginToken?
    fun saveLoginToken(account: Account, token: LoginToken)
    fun clearLoginToken(account: Account)
}

internal const val LOGIN_REFRESH_MARGIN_MS = 86_400_000L

/** Serializes restoration, login and rejection so simultaneous RPCs share one login. */
internal class LoginTokens(
    private val account: Account,
    private val store: LoginTokenStore?,
    private val clock: () -> Long,
    private val login: suspend () -> LoginResponse,
) {
    private val mutex = Mutex()
    private var restored = false
    private var token: LoginToken? = null

    suspend fun current(): String = mutex.withLock {
        if (!restored) {
            token = store?.loadLoginToken(account)
            restored = true
        }
        token?.let {
            if (it.valid(clock())) return@withLock it.value
            token = null
            store?.clearLoginToken(account)
        }
        val next = login().let { LoginToken(it.token, it.expiresMs) }
        check(next.value.isNotEmpty() && next.expiresMs > clock()) { "Login returned an expired credential" }
        store?.saveLoginToken(account, next)
        token = next
        next.value
    }

    /** A response for an old connection must not discard a more recent login. */
    suspend fun discard(rejected: String? = null): Boolean = mutex.withLock {
        if (rejected != null && token?.value != rejected) return@withLock false
        token = null
        restored = true
        store?.clearLoginToken(account)
        true
    }
}
