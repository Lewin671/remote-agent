package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.RegisterDeviceRequest
import okhttp3.OkHttpClient

/** Exact authority approval and private bootstrap, protected by the foreground vault. */
class RegistrationIntent(
    val account: Account,
    val record: DeviceSecurity.Record,
    val request: RegisterDeviceRequest,
) {
    override fun toString() = "RegistrationIntent"

    fun bytes(): ByteArray = operationMaterial { s ->
        s.writeInt(1)
        s.writeUTF(account.server)
        s.blob(account.root)
        s.blob(account.deviceSeed)
        s.writeUTF(account.deviceId)
        s.writeUTF(account.accountId)
        s.blob(account.accountPublicKey)
        s.blob(record.bytes())
        s.blob(request.toByteArray())
    }

    suspend fun resume(
        http: OkHttpClient,
        persist: suspend (Account, DeviceSecurity.Record) -> Unit,
    ): Account {
        persist(account, record)
        val result = Api(account.server, http).auth.registerDevice(request, emptyMap()).value()
        require(result.deviceId == account.deviceId)
        return account
    }

    companion object {
        fun parse(raw: ByteArray): RegistrationIntent =
            readOperationMaterial(raw) { s ->
                val account =
                    Account(s.readUTF(), s.blob(), s.blob(), s.readUTF(), s.readUTF(), s.blob())
                val record = DeviceSecurity.Record.parse(s.blob())
                val request = RegisterDeviceRequest.parseFrom(s.blob())
                require(
                    account.deviceId == deviceId(SigningKey(account.deviceSeed).publicKey) &&
                        record.memberID == account.deviceId &&
                        record.pin.account == account.accountId,
                )
                RegistrationIntent(account, record, request)
            }
    }
}
