package io.github.lewin671.remoteagent.core

/** Missing state is never replaced from root possession or a relay directory. */
internal object MissingDeviceSecurityStorage : DeviceSecurity.Storage {
    private fun missing(): Nothing = throw DeviceSecurity.Failure("storage")

    override fun readSecurity(): DeviceSecurity.Record = missing()

    override fun initializeSecurity(record: DeviceSecurity.Record) = missing()

    override fun installSecurity(
        expectedState: ByteArray,
        states: List<DeviceSecurity.State>,
        proofs: List<ByteArray?>,
        lease: HeadLease?,
    ): DeviceSecurity.Record = missing()

    override fun touchSecurity(expectedState: ByteArray, floor: ULong): DeviceSecurity.Record =
        missing()

    override fun cacheSecurityKey(expectedState: ByteArray, epoch: ULong, secret: ByteArray) =
        missing()
}
