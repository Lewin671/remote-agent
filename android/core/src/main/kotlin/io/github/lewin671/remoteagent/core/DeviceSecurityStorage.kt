package io.github.lewin671.remoteagent.core

/** Missing state is never replaced from root possession or a relay directory. */
internal object MissingDeviceSecurityStorage : DeviceSecurity.Storage {
    private fun missing(): Nothing = throw DeviceSecurity.Failure("storage")

    override fun readSecurity(): DeviceSecurity.Record = missing()

    override fun initializeSecurity(record: DeviceSecurity.Record) = missing()

    override fun reserveSecurity(expectedState: ByteArray): DeviceSecurity.Record = missing()

    override fun advanceSecurity(
        expectedState: ByteArray,
        next: DeviceSecurity.State,
    ): DeviceSecurity.Record = missing()

    override fun readSecurityHistory(epoch: ULong): DeviceSecurity.Historical? = missing()

    override fun cacheSecurityHistory(history: DeviceSecurity.Historical) = missing()

    override fun readRemovalOperations(): ByteArray? = missing()

    override fun writeRemovalOperations(data: ByteArray) = missing()
}
