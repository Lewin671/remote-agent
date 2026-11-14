package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import okhttp3.WebSocket

@OptIn(ExperimentalCoroutinesApi::class)
class RecoveryOwnershipTest {
    private fun field(remote: Remote, name: String): Any? = Remote::class.java.getDeclaredField(name).apply { isAccessible = true }.get(remote)
    private fun children(remote: Remote): Int = (field(remote,"scope") as CoroutineScope).coroutineContext[Job]!!.children.count { it.isActive }

    @Test fun canceledSetupCannotClearItsSuspendedSuccessor() = runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this)
        peer.before={name,_->if(name=="GetChallenge") delay(2500)}
        val remote=peer.remote(tokens=null)
        try {
            remote.networkChanged("baseline",true);runCurrent()
            remote.start();runCurrent();assertEquals(1,peer.calls.count{it=="GetChallenge"})
            remote.networkChanged("replacement",true);runCurrent()
            val successor=field(remote,"connecting") as Job?
            assertNotNull(successor);assertTrue(successor!!.isActive)
            advanceTimeBy(1001);runCurrent()
            assertSame("retired setup cleared or replaced the live owner",successor,field(remote,"connecting"))
            assertEquals(2,peer.calls.count{it=="GetChallenge"})
            advanceTimeBy(1600);runCurrent();assertEquals(ConnState.ONLINE,remote.state.value.conn)
        } finally {remote.close();runCurrent()}
    }

    @Test fun candidateIsOnlyAWaiterOfTheExistingCredentialWork() = runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this)
        peer.before={name,_->if(name=="GetChallenge") delay(2500)}
        val remote=peer.remote(tokens=null)
        try {
            val caller=async{remote.currentToken()};runCurrent()
            remote.start();runCurrent()
            assertEquals("ordinary candidate retired the RPC's login",1,peer.calls.count{it=="GetChallenge"})
            advanceTimeBy(2600);runCurrent();assertTrue(caller.await().isNotEmpty())
            assertEquals(ConnState.ONLINE,remote.state.value.conn)
        } finally {remote.close();runCurrent()}
    }

    @Test fun retiringALegReleasesItsReaderWithoutAWebSocketCloseCallback() = runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this);val remote=peer.remote()
        try {
            remote.start();runCurrent();val before=children(remote)
            repeat(4){
                remote.networkChanged("route-$it",true);runCurrent()
                advanceTimeBy(1100);runCurrent()
                assertEquals(ConnState.ONLINE,remote.state.value.conn)
                assertEquals("retired main reader retained",before,children(remote))
            }
        } finally {remote.close();runCurrent()}
    }

    @Test fun aNetworkChangeRetiresOneOffLoginBeforeTheMainLoopStarts() = runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this);var calls=0
        val entered=CompletableDeferred<Unit>();val retired=CompletableDeferred<Unit>()
        peer.before={name,_->if(name=="GetChallenge" && ++calls==1) {entered.complete(Unit);try{awaitCancellation()}finally{retired.complete(Unit)}}}
        val remote=peer.remote(tokens=null)
        remote.networkChanged("baseline",true);runCurrent()
        val caller=async {remote.currentToken()}
        try {
            entered.await();remote.networkChanged("replacement",true);runCurrent()
            assertTrue("inactive main loop left an old login alive",retired.isCompleted)
            assertTrue(caller.await().isNotEmpty());assertEquals(2,calls)
            assertNull(field(remote,"connecting"))
        } finally {caller.cancelAndJoin();remote.close();runCurrent()}
    }

    @Test fun loginRefusalRetiresMainWorkAndRetryKeepsTheExistingPollOwner() = runTest {
        val peer=ColdOpenPeer(ColdOpenFixture(2),this)
        val sockets=WebSocket.Factory {request,listener ->
            val socket=peer.newWebSocket(request,listener)
            object : WebSocket by socket { override fun close(code:Int,reason:String?)=true }
        }
        val remote=peer.remote(sockets=sockets)
        try {
            remote.start();runCurrent();val polling=field(remote,"stateJob")
            val tokens=field(remote,"loginTokens") as LoginTokens;tokens.discard()
            val entered=CompletableDeferred<Unit>();val release=CompletableDeferred<Unit>();var calls=0
            peer.denyLogin=true
            peer.before={name,_->if(name=="GetChallenge" && ++calls==1){entered.complete(Unit);release.await()}}
            val caller=async{runCatching{remote.currentToken()}}
            entered.await();remote.reconnectNow();runCurrent()
            assertNotNull(field(remote,"connecting"));release.complete(Unit);runCurrent()
            assertTrue(caller.await().isFailure)
            for(name in listOf("connecting","candidate","opening","liveness")) assertNull("retained $name",field(remote,name))
            assertTrue((field(remote,"mainReaders") as Map<*,*>).isEmpty())
            peer.denyLogin=false;remote.retryLogin();runCurrent()
            assertSame("retry overwrote an active poll owner",polling,field(remote,"stateJob"))
            advanceTimeBy(1100);runCurrent() // Wait for the same target bucket's next credit.
            assertEquals(ConnState.ONLINE,remote.state.value.conn)
        } finally {remote.close();runCurrent()}
    }

    @Test fun closingSecurityRetiresItsOwnedHTTPRound() = runTest {
        val fixture=ColdOpenFixture(2);val started=CompletableDeferred<Unit>();val stopped=CompletableDeferred<Unit>()
        val security=DeviceSecurityClient(TestSecurityStore(fixture.record),fixture.phone.id,SigningKey(ByteArray(32){1}),clock={fixture.now}) {
            started.complete(Unit);try {awaitCancellation()} finally {stopped.complete(Unit)}
        }
        val caller=async{runCatching{security.sync()}}
        started.await();security.close();runCurrent()
        assertTrue("closed security owner retained HTTP",stopped.isCompleted)
        caller.cancelAndJoin()
    }
}
