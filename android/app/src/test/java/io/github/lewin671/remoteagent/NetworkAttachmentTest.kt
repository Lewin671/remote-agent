package io.github.lewin671.remoteagent

import io.github.lewin671.remoteagent.core.*
import kotlinx.coroutines.test.*
import kotlinx.coroutines.*
import com.connectrpc.StreamResult
import com.connectrpc.http.*
import io.github.lewin671.remoteagent.proto.*
import com.google.protobuf.ByteString
import okio.Buffer
import org.junit.Assert.*
import org.junit.Test

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk=[35], application=android.app.Application::class)
class NetworkAttachmentTest {
    @get:org.junit.Rule val fixture=RaAppTestRule()
    private fun field(r:Remote,name:String)=Remote::class.java.getDeclaredField(name).apply{isAccessible=true}.get(r)
    @Test fun networkObservedBeforeAccountLoadingSeedsEachEngine()=runTest {
        val app=fixture.app
        app.observeNetwork("wifi",true)
        var challenges=0
        val transport=object:HTTPClientInterface {
            override fun unary(request:UnaryHTTPRequest,onResult:(HTTPResponse)->Unit):()->Unit {
                val job=backgroundScope.launch {
                    val method=request.methodSpec.path.substringAfterLast('/')
                    if(method=="GetChallenge" && ++challenges==1) awaitCancellation()
                    val response=if(method=="GetChallenge") GetChallengeResponse.newBuilder().setChallenge(ByteString.copyFrom(ByteArray(32))).build()
                        else LoginResponse.newBuilder().setToken("fresh-token").setExpiresMs(System.currentTimeMillis()+3*86_400_000L).build()
                    onResult(HTTPResponse(trailers=emptyMap(),status=200,headers=mapOf("content-type" to listOf("application/proto")),message=Buffer().write(response.toByteArray())))
                }
                return {job.cancel()}
            }
            override fun stream(request:HTTPRequest,duplex:Boolean,onResult:suspend (StreamResult<Buffer>)->Unit):Stream=error("unused stream")
        }
        val r=Remote(Account("https://example.com",byteArrayOf(),ByteArray(32){1},"fixture-device"),Api.defaultHttp(),dispatcher=StandardTestDispatcher(testScheduler),rpcTransport=transport)
        val login=async(start=CoroutineStart.LAZY){r.currentToken()}
        try {
            app.attachNetwork(r);runCurrent()
            assertEquals(0L,field(r,"networkGeneration"))
            login.start();runCurrent();assertEquals(1,challenges)
            // A real change before any Ready retires the blocked cold login.
            r.networkChanged("cellular",true);runCurrent()
            assertTrue("cold login survived the network switch",login.isCompleted)
            assertEquals(1L,field(r,"networkGeneration"))
            assertEquals("fresh-token",login.await());assertEquals(2,challenges)
        }finally{login.cancel();r.close();runCurrent()}
    }
}
