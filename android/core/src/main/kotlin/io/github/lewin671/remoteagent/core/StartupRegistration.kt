package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import io.github.lewin671.remoteagent.proto.RegisterDeviceRequest
import okhttp3.OkHttpClient

/** Startup probes a saved creation; credentials and proof solving belong to its UI. */
suspend fun retryCreationRegistration(
    draft: CreatedAccount,
    http: OkHttpClient,
    persist: (CreatedAccount) -> Unit = { throw DeviceSecurity.Failure("storage") },
) {
    if (draft.registration.isEmpty()) return
    val request =
        RegisterDeviceRequest.parseFrom(draft.registration).toBuilder().clearSignupProof().build()
    persist(draft.copy(registration = request.toByteArray()))
    try {
        val result =
            Api(draft.account.server, http).auth.registerDevice(request, emptyMap()).value()
        require(result.deviceId == draft.account.deviceId)
    } catch (e: ConnectException) {
        if (
            e.code != Code.PERMISSION_DENIED &&
                e.code != Code.NOT_FOUND &&
                e.code != Code.FAILED_PRECONDITION
        )
            throw e
    }
}
