package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.transport.Recovery
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class ForwardLifetimeTest {
    private fun runLifetime(repeatedReady:Boolean)=runTest {
        val fixture=ColdOpenFixture(2);val peer=ColdOpenPeer(fixture,this);val remote=peer.remote()
        val recovery=Remote::class.java.getDeclaredField("recovery").apply{isAccessible=true}.get(remote) as Recovery
        val requests=mutableListOf<ForwardRequest>()
        peer.before={name,request->if(name=="Forward") {
            requests+=ForwardRequest.parseFrom(request.message.clone().readByteArray());awaitCancellation()
        }}
        remote.security.sync() // Seed the authenticated relay epoch without starting the main loop.
        val prepared=remote.prepare(fixture.machine.id,"",Command.newBuilder().setGetSettings(GetSettingsRequest.getDefaultInstance()).build())
        var finishedAt:Long?=null
        val pending=async { try {runCatching {remote.forward(prepared)}} finally {finishedAt=testScheduler.currentTime} }
        try {
            runCurrent();assertEquals(1,requests.size)
            if(repeatedReady) repeat(70) {
                advanceTimeBy(10_000);runCurrent()
                if(!pending.isCompleted) {recovery.ready();runCurrent()}
            } else {advanceTimeBy(60_000+RESULT_RETENTION_MS+1);runCurrent()}
            assertTrue("Forward outlived its original result recovery window",pending.isCompleted)
            val error=pending.await().exceptionOrNull() as? CommandException
            assertEquals(Outcome.UNKNOWN,error?.outcome)
            assertTrue(finishedAt!!<=60_000+RESULT_RETENTION_MS)
            if(repeatedReady) assertTrue("path changes consumed the ordinary retry budget",requests.size>10)
            else assertEquals(1,requests.size)
            assertTrue(requests.all{it.command==requests.first().command})
            assertEquals(requests.size,requests.map{it.attemptId}.distinct().size)
        }finally{pending.cancel();remote.close();runCurrent()}
    }
    @Test fun repeatedReadyCannotExtendTheOriginalForwardLifetime()=runLifetime(true)
    @Test fun aBlackholedDeliveryExpiresWithoutAnotherReady()=runLifetime(false)
}
