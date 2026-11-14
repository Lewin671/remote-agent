package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.transport.NetworkRound
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import java.net.SocketTimeoutException

@OptIn(ExperimentalCoroutinesApi::class)
class RecoveryLifetimeTest {
    private fun field(r:Remote,name:String):Any?=Remote::class.java.getDeclaredField(name).apply{isAccessible=true}.get(r)
    private fun resumeRetires(method:String)=runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this)
        val r=peer.remote()
        var requests=0;val first=CompletableDeferred<Unit>();val second=CompletableDeferred<Unit>()
        try {
            r.start();runCurrent()
            if(method=="GetChallenge") (field(r,"loginTokens") as LoginTokens).discard()
            peer.before={name,_->if(name==method){when(++requests){1->{first.complete(Unit);awaitCancellation()};2->{second.complete(Unit);awaitCancellation()}}}}
            val caller=async{if(method=="GetChallenge")r.currentToken() else r.security.sync()}
            first.await();r.stop();runCurrent();second.await()
            r.start();runCurrent()
            assertTrue("resume kept the paused HTTP round",caller.isCompleted)
            caller.await();assertEquals(if(method=="GetChallenge")3 else 4,requests)
        }finally{r.close();runCurrent()}
    }
    @Test fun resumeRetiresPausedLogin()=resumeRetires("GetChallenge")
    @Test fun resumeRetiresPausedSecurityPull()=resumeRetires("GetSecurityStates")
    @Test fun canceledColdStartStillCountsAsAResume()=runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this);val r=peer.remote(tokens=null)
        peer.before={name,_->if(name=="GetChallenge")awaitCancellation()}
        try {r.start();runCurrent();r.stop();runCurrent();val before=field(r,"networkGeneration") as Long
            peer.before={_,_->};r.start();runCurrent();assertTrue((field(r,"networkGeneration") as Long)>before)
        }finally{r.close();runCurrent()}
    }
    @Test fun directoryDeadlineReleasesTheNextRead()=runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this);val r=peer.remote();var calls=0
        try {r.start();runCurrent()
            peer.before={name,_->if(name=="ListDevices" && ++calls==1)awaitCancellation()}
            val first=async{r.refreshDevices()};runCurrent();advanceTimeBy(3001);runCurrent()
            assertTrue("directory owner has no deadline",first.isCompleted);first.await()
            assertNull(field(r,"directoryLoad"));r.refreshDevices();assertEquals(2,calls)
        }finally{r.close();runCurrent()}
    }
    @Test fun theNetworkRoundOwnsItsDeadlineAndReleasesTheMutex()=runTest {
        val round=NetworkRound(Mutex());round.changed()
        val first=async{runCatching{round.run<Int>{awaitCancellation()}}}
        runCurrent();advanceTimeBy(3001);runCurrent()
        assertTrue("round has no independent deadline",first.isCompleted)
        assertTrue(first.await().exceptionOrNull() is SocketTimeoutException)
        assertEquals(7,round.run{7})
    }
}
