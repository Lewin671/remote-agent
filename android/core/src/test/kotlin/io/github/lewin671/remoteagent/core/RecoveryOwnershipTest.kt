package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertTrue
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class RecoveryOwnershipTest {
    @get:org.junit.Rule val hangGuard = hangGuard()
    @Test fun closingSecurityRetiresItsOwnedHTTPRound() = runTest {
        val fixture = ColdOpenFixture(2)
        val started = CompletableDeferred<Unit>()
        val stopped = CompletableDeferred<Unit>()
        val security = DeviceSecurityClient(
            TestSecurityStore(fixture.record),
            fixture.phone.id,
            SigningKey(ByteArray(32) { 1 }),
            clock = { fixture.now },
        ) {
            started.complete(Unit)
            try { awaitCancellation() } finally { stopped.complete(Unit) }
        }
        val caller = async { runCatching { security.sync() } }
        started.await()
        security.close()
        runCurrent()
        assertTrue("closed security owner retained HTTP", stopped.isCompleted)
        caller.cancelAndJoin()
    }
}
