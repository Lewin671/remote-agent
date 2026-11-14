package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.transport.NetworkRound
import io.github.lewin671.remoteagent.proto.GetSecurityStatesResponse
import io.github.lewin671.remoteagent.proto.LoginResponse
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import org.junit.Assert.*
import org.junit.Test

class NetworkRoundTest {
    @Test fun lateRetirementCannotCancelANewGenerationOwner() = runBlocking {
        val release=CompletableDeferred<Unit>()
        lateinit var caller: Deferred<Int>
        lateinit var round: NetworkRound
        round=NetworkRound(Mutex()) {
            caller=async(start=CoroutineStart.UNDISPATCHED) {round.run {release.await();42}}
        }
        round.changed()
        try {assertTrue("retirement canceled its successor",caller.isActive)}
        finally {release.complete(Unit)}
        assertEquals(42,caller.await())
    }

    @Test fun obsoleteLoginDoesNotHoldTheNextNetworkOrCancelItsCaller() = runBlocking {
        val entered=CompletableDeferred<Unit>()
        var calls=0
        val tokens=LoginTokens(Account("https://example.com",byteArrayOf(),ByteArray(32),"fixture"),null,{1000}) {
            if (++calls==1) {entered.complete(Unit);awaitCancellation()}
            LoginResponse.newBuilder().setToken("fresh").setExpiresMs(1000+2*LOGIN_REFRESH_MARGIN_MS).build()
        }
        val caller=async {tokens.current()}
        entered.await();tokens.networkChanged()
        val result=withTimeoutOrNull(1000){caller.await()}
        assertEquals("fresh",result);assertEquals(2,calls)
    }
    @Test fun securityRoundIsReplacedWithoutStoppingItsOwner() = runBlocking {
        val fixture=ColdOpenFixture(2)
        val entered=CompletableDeferred<Unit>()
        var calls=0
        val security=DeviceSecurityClient(TestSecurityStore(fixture.record),fixture.phone.id,SigningKey(ByteArray(32){1}),clock={fixture.now}) {
            if (++calls==1) {entered.complete(Unit);awaitCancellation()}
            GetSecurityStatesResponse.newBuilder().setHeadEpoch(fixture.record.head.core.epoch.toLong()).build()
        }
        try {
            val caller=async {security.sync()}
            entered.await();security.networkChanged()
            val record=withTimeoutOrNull(1000){caller.await()}
            assertNotNull(record);assertEquals(2,calls)
        } finally {security.close()}
    }
    @Test fun explicitCallerCancellationDoesNotRestartNetworkWork() = runBlocking {
        val round=NetworkRound(Mutex());val entered=CompletableDeferred<Unit>()
        var calls=0
        val caller=launch {round.run {calls++;entered.complete(Unit);awaitCancellation()}}
        entered.await();caller.cancelAndJoin()
        assertEquals(1,calls);assertTrue(caller.isCancelled)
    }
}
