package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ErrorTextTest {
    @Test
    fun aRequestThatNeverLeftThePhoneNamesTheNetworkNotTheResolver() {
        val cause = java.net.UnknownHostException("Unable to resolve host \"example.test\": No address associated with hostname")
        val e = ConnectException(Code.UNKNOWN, cause.message, cause)
        assertEquals("找不到服务器，检查一下网络", errorText(e))
        assertEquals("找不到服务器，检查一下网络", errorText(cause))
        assertTrue(unresolvedHost(e))
        assertTrue(detailText(e).startsWith("UnknownHostException: Unable to resolve host"))
        assertFalse(unresolvedHost(java.net.SocketTimeoutException("timeout")))
    }

    @Test
    fun aServersOwnAnswerKeepsItsText() {
        assertEquals("登录失效：token expired", errorText(ConnectException(Code.UNAUTHENTICATED, "token expired")))
    }
}
