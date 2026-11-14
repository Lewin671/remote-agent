package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import io.github.lewin671.remoteagent.proto.*
import kotlin.coroutines.Continuation
import kotlin.coroutines.intrinsics.suspendCoroutineUninterceptedOrReturn

@OptIn(ExperimentalCoroutinesApi::class)
class RecoveryLegTest {
    private fun get(r:Remote,n:String)=Remote::class.java.getDeclaredField(n).apply{isAccessible=true}.get(r)
    private fun set(r:Remote,n:String,v:Any?)=Remote::class.java.getDeclaredField(n).apply{isAccessible=true}.set(r,v)
    private suspend fun call(r:Remote,name:String):Unit=suspendCoroutineUninterceptedOrReturn { c ->
        Remote::class.java.getDeclaredMethod(name,Continuation::class.java).apply{isAccessible=true}.invoke(r,c)
    }
    @Test fun probeDoesNotDiscardAnIndependentHistoryPage()=runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this);val r=peer.remote();val held=CompletableDeferred<Unit>()
        try {
            r.start();runCurrent();peer.before={name,_->if(name=="GetEvents")held.await()}
            val events=r.events("short");runCurrent();assertEquals(1,peer.calls.count{it=="GetEvents"})
            set(r,"generation",(get(r,"generation") as Int)+1);set(r,"conn",ConnState.CONNECTING)
            held.complete(Unit);runCurrent()
            assertEquals(2,events.value.events.size);assertTrue(events.value.loaded)
        }finally{held.complete(Unit);r.close();runCurrent()}
    }
    @Test fun readyCatchupRemainsOwnedByItsActiveLeg()=runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this);val r=peer.remote();val held=CompletableDeferred<Unit>()
        peer.before={name,_->if(name=="ListRemoteSessions")held.await()}
        try {
            r.start();runCurrent()
            set(r,"generation",(get(r,"generation") as Int)+1)
            held.complete(Unit);runCurrent()
            assertTrue("Ready catch-up lost its preferences read",peer.calls.count{it=="GetAccountPrefs"}>0)
        }finally{held.complete(Unit);r.close();runCurrent()}
    }
    private fun retryDuringProbe(method:String)=runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this);val r=peer.remote()
        try {
            set(r,"running",true);set(r,"conn",ConnState.CONNECTING)
            peer.unavailableMethods=setOf(method)
            val read=launch {if(method=="GetAccountPrefs")call(r,"loadPrefs")else r.refresh()};runCurrent();read.join()
            val before=peer.calls.count{it==method};assertTrue(before>0)
            set(r,"generation",1);peer.unavailableMethods=emptySet();advanceTimeBy(2001);runCurrent()
            assertTrue("retry waited for an unrelated Ready",peer.calls.count{it==method}>before)
        }finally{r.close();runCurrent()}
    }
    @Test fun preferenceRetryDoesNotWaitForAnotherMainOnline()=retryDuringProbe("GetAccountPrefs")
    @Test fun listRetryDoesNotWaitForAnotherMainOnline()=retryDuringProbe("ListRemoteSessions")
    private fun reseal(rawOnly:Boolean)=runTest {
        val f=ColdOpenFixture(2);val r=ColdOpenPeer(f,this).remote()
        val raw=Command.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setGetSettings(GetSettingsRequest.getDefaultInstance()).build().toByteArray()+byteArrayOf(0xa0.toByte(),0x06,0x01)
        try {
            val initial=PreparedCommand(f.machine.id,"",newRequestId(),f.now+60000,Envelope.getDefaultInstance(),serializedCommand=raw,prior=false)
            val before=r.authorize(initial)
            val prepared=if(rawOnly) PreparedCommand(initial.machineId,initial.sessionId,initial.requestId,initial.deadlineMs,before,prior=true) else initial
            val next=Authority.veto(f.record.head,SigningKey(ByteArray(32){9}).publicKey,ByteArray(32){5},SigningKey(ByteArray(32){1}),f.now)
            r.security.install(listOf(next),listOf(null),HeadLease.sign(next,SigningKey(ByteArray(32){1}),f.now))
            val after=r.authorize(prepared,retryOfUnknown=true);val (h,plain)=r.security.openOwnCommand(after)
            assertArrayEquals(DeviceSecurity.stateHash(next),h.state);assertArrayEquals(raw,plain)
            assertEquals(DeviceSecurity.parseHeader(before.securityHeader.toByteArray()).contentEpoch,h.contentEpoch)
            assertEquals(initial.machineId,h.machine);assertEquals(initial.sessionId,h.session);assertEquals(initial.requestId,h.request);assertEquals(initial.deadlineMs.toULong(),h.deadline)
        }finally{r.close();runCurrent()}
    }
    @Test fun resealsWhenAuthorizationChangesWithoutChangingContentEpoch()=reseal(false)
    @Test fun storedEnvelopeOnlyNotificationReusesOriginalBytesWhileResealing()=reseal(true)

}
