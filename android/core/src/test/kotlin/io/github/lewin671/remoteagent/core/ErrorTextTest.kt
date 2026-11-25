package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import com.connectrpc.Code
import com.connectrpc.ConnectException
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ErrorTextTest {
    @Test
    fun aRequestThatNeverLeftThePhoneNamesTheNetworkNotTheResolver() {
        val cause = java.net.UnknownHostException("Unable to resolve host \"example.test\": No address associated with hostname")
        val e = ConnectException(Code.UNKNOWN, cause.message, cause)
        assertEquals(messageTextForTest(UiMessages.errorsAndroidCannotFindTheServerCheckYourNetwork()), errorText(e))
        assertEquals(messageTextForTest(UiMessages.errorsAndroidCannotFindTheServerCheckYourNetwork()), errorText(cause))
        assertTrue(unresolvedHost(e))
        assertFalse(unresolvedHost(java.net.SocketTimeoutException("timeout")))
    }

    @Test
    fun loginDenialIsFriendlyAndOfflineDoesNotProveDenial() {
        val denied = ConnectException(Code.PERMISSION_DENIED, "login failed")
        assertEquals(messageTextForTest(UiMessages.errorsTheServerDoesNotRecognizeThisDeviceItMay()), errorText(denied))
        assertTrue(loginDenied(denied))
        assertFalse(loginDenied(java.net.UnknownHostException()))
        assertFalse(loginDenied(ConnectException(Code.UNAVAILABLE, "offline")))
        assertFalse(loginDenied(ConnectException(Code.NOT_FOUND, "missing route")))
        assertEquals("operation denied", errorText(ConnectException(Code.PERMISSION_DENIED, "operation denied")))
    }

    @Test
    fun aServersOwnAnswerKeepsItsText() {
        assertEquals("登录失效：token expired", errorText(ConnectException(Code.UNAUTHENTICATED, "token expired")))
    }

    @Test
    fun leaseFailureUsesCoreRenewalAdvice() {
        val advice = io.github.lewin671.remoteagent.core.proto.Text.newBuilder()
            .setKey("security.open_to_renew")
            .addArgs(
                io.github.lewin671.remoteagent.core.proto.TextArg.newBuilder()
                    .setName("value")
                    .setStrValue("Android 16 · Pixel 9"),
            )
            .build()
        val msg = errorText(SecurityFailure("lease"), advice)
        assertTrue(msg.resolve { it.key + ":" + it.args.values.joinToString(",") }.contains("Android 16 · Pixel 9"))
    }
}
