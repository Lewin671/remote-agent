package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class NetworkBaselineTest {
    private fun field(r:Remote,name:String)=Remote::class.java.getDeclaredField(name).apply{isAccessible=true}.get(r)
    @Test fun firstObservationEvenAfterReadyOnlyRecordsTheBaseline()=runTest {
        val remote=ColdOpenPeer(ColdOpenFixture(2),this).remote()
        try {
            remote.start();runCurrent();assertEquals(ConnState.ONLINE,remote.state.value.conn)
            val generation=field(remote,"networkGeneration") as Long;val leg=field(remote,"ws")
            remote.networkChanged("first-wifi",true);runCurrent()
            assertEquals(generation,field(remote,"networkGeneration"));assertSame(leg,field(remote,"ws"))
            remote.networkChanged("cellular",true);runCurrent()
            assertEquals(generation+1,field(remote,"networkGeneration"))
        }finally{remote.close();runCurrent()}
    }
}
