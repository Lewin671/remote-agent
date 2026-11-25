// Account identity, HTTP defaults, and error display helpers (relay RPCs run through Core + SecurityExecutor).
package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import java.io.IOException
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import okhttp3.Interceptor
import okhttp3.OkHttpClient
import okhttp3.RequestBody
import okhttp3.Response

class SecurityFailure(val code: String, message: String = code) : Exception(message)

/**
 * Identity of an enrolled account on this device. All private keys remain inside the host
 * [SecurityStoreV4] boundary and never cross into app memory via [Account].
 */
data class Account(
    val server: String,
    val deviceId: String,
    val accountId: String = "",
) {
    val localKey: ByteArray
        get() = accountId.ifEmpty { deviceId }.toByteArray(Charsets.UTF_8)

    override fun toString(): String = "Account(server=$server, device=$deviceId)"
}

/** Shared HTTP client defaults (retained as [Api.defaultHttp] for RaApp & tests). */
object Api {
    /** An HTTP client with the timeouts the protocol expects: Forward waits up to 30 s. */
    fun defaultHttp(): OkHttpClient =
        OkHttpClient.Builder()
            .retryOnConnectionFailure(false)
            .followRedirects(false)
            .followSslRedirects(false)
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(45, TimeUnit.SECONDS)
            .writeTimeout(30, TimeUnit.SECONDS)
            .pingInterval(0, TimeUnit.SECONDS)
            .build()
}

/** A daemon's answer, or a failure to get one. [code] is a Connect code name. */
enum class CommandFailureKind { RAW, CONNECTION_DROPPED, REPLY_UNVERIFIED, UNKNOWN, COMPUTER_OFFLINE, IMAGE_MISSING, EXECUTED_RESULT_UNAVAILABLE, IDENTITY_CONFLICT }

class CommandException(
    val code: String,
    message: String,
    val fromDaemon: Boolean = false,
    val failureKind: CommandFailureKind = CommandFailureKind.RAW,
    override val imageKind: io.github.lewin671.remoteagent.core.proto.ImageProblemKind? = null,
) : Exception(message), MessageFailure {
    override var display: MessageText = when (code) {
        "disk_full" -> UiMessages.machineStatusDiskFull().messageText()
        "storage_exhausted" -> UiMessages.machineStatusStorageExhausted().messageText()
        else -> message.messageText()
    }
        private set

    constructor(
        code: String,
        message: MessageText,
        fromDaemon: Boolean = false,
        failureKind: CommandFailureKind = CommandFailureKind.RAW,
        imageKind: io.github.lewin671.remoteagent.core.proto.ImageProblemKind? = null,
    ) : this(code, "command_failure", fromDaemon, failureKind, imageKind) {
        display = message
    }

    companion object {
        const val UNKNOWN = "unknown_outcome"
        const val UNAVAILABLE = "unavailable"
        const val EXPIRED = "expired"
    }
}

/** Short text for an error, without content. */
fun errorText(e: Throwable, leaseRenewalAdvice: io.github.lewin671.remoteagent.core.proto.Text? = null): MessageText = localized { copy ->
    when (e) {
        is MessageFailure -> e.display.resolve(copy)
        is SecurityFailure -> when (e.code) {
            "unavailable" -> copy(UiMessages.errorsWaitingForTheOwningComputerToUpdateTheCurrentRemote())
            "invalid", "untrusted" -> copy(UiMessages.errorsInvalidCiphertextSignatureKeyOrContentVerificationFailed())
            "conflict" -> copy(UiMessages.errorsMembershipChainConflictSecurityOperationsStoppedVerifyDevicesAnd())
            "lease" -> {
                val advice = if (leaseRenewalAdvice != null && (leaseRenewalAdvice.key.isNotEmpty() || leaseRenewalAdvice.literal.isNotEmpty() || leaseRenewalAdvice.joinCount > 0)) {
                    coreTextToMessageText(leaseRenewalAdvice).resolve(copy)
                } else {
                    copy(UiMessages.securityAndroidRenewOnAuthority())
                }
                copy(UiMessages.errorsAndroidRemoteControlAuthorizationExpiredCannotSaveChangesControlSessionsOr(advice))
            }
            "clock" -> copy(UiMessages.errorsAndroidThePhoneClockIsAheadOfTheServerMembershipState())
            else -> e.message ?: copy(UiMessages.errorsAndroidSecurityStateUnavailable())
        }
        is ConnectException -> networkText(e.exception, copy) ?: when (e.code) {
            Code.UNAVAILABLE -> e.message?.let { copy(UiMessages.errorsAndroidServerUnavailableReason(it)) } ?: copy(UiMessages.connectionNoServer())
            Code.PERMISSION_DENIED -> if (hasRelayErrorReason(e, io.github.lewin671.remoteagent.proto.RelayErrorReason.RELAY_ERROR_REASON_LOGIN_FAILED)) {
                copy(UiMessages.errorsTheServerDoesNotRecognizeThisDeviceItMay())
            } else {
                e.message ?: e.code.codeName
            }
            Code.UNAUTHENTICATED -> copy(UiMessages.errorsAndroidLoginExpired(e.message.orEmpty()))
            else -> e.message ?: e.code.codeName
        }
        else -> networkText(e, copy) ?: e.message ?: e.javaClass.simpleName
    }
}

/**
 * The server's address did not resolve: on a phone, the network is down whatever the status bar
 * shows.
 */
fun unresolvedHost(e: Throwable): Boolean =
    ((e as? ConnectException)?.exception ?: e) is java.net.UnknownHostException

private fun networkText(e: Throwable?, copy: MessageResolver): String? = when (e) {
    is java.net.UnknownHostException -> copy(UiMessages.errorsAndroidCannotFindTheServerCheckYourNetwork())
    is java.net.ConnectException -> copy(UiMessages.connectionNoServer())
    is java.net.SocketTimeoutException -> copy(UiMessages.errorsConnectionTimedOut())
    is javax.net.ssl.SSLException -> copy(UiMessages.errorsAndroidSecureConnectionFailed(e.message.orEmpty()))
    else -> null
}

private fun hasRelayErrorReason(
    e: ConnectException,
    reason: io.github.lewin671.remoteagent.proto.RelayErrorReason,
): Boolean = e.details.any { d ->
    (d.type == "remoteagent.v1.RelayErrorDetail" || d.type.endsWith("/remoteagent.v1.RelayErrorDetail")) &&
        runCatching {
            io.github.lewin671.remoteagent.proto.RelayErrorDetail.parseFrom(d.payload.toByteArray()).reason == reason
        }.getOrDefault(false)
}

/** Only meaningful for an error returned by a login attempt. */
fun loginDenied(error: Throwable): Boolean =
    error is ConnectException && error.code == Code.PERMISSION_DENIED

internal const val RELAY_EPOCH_HEADER = "Remote-Agent-Relay-Epoch"

/** Outermost interceptor: inner interceptors/follow-ups share the same body guard. */
internal object OneShotRPCBody : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val request = chain.request()
        val body = request.body ?: return chain.proceed(request)
        val written = AtomicBoolean()
        val once = object : RequestBody() {
            override fun contentType() = body.contentType()
            override fun contentLength() = body.contentLength()
            override fun isOneShot() = true
            override fun isDuplex() = body.isDuplex()
            override fun writeTo(sink: okio.BufferedSink) {
                if (!written.compareAndSet(false, true)) throw IOException("request_body_replayed")
                body.writeTo(sink)
            }
        }
        return chain.proceed(request.newBuilder().method(request.method, once).build())
    }
}
