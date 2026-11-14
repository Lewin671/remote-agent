package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

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
