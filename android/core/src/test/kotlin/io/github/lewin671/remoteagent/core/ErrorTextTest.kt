package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ErrorTextTest {
    @Test
    fun aRequestThatNeverLeftThePhoneNamesTheNetworkNotTheResolver() {
        val cause = java.net.UnknownHostException("Unable to resolve host \"example.test\": No address associated with hostname")
        assertEquals(messageTextForTest(UiMessages.errorsAndroidCannotFindTheServerCheckYourNetwork()), errorText(cause))
        assertTrue(unresolvedHost(cause))
        assertFalse(unresolvedHost(java.net.SocketTimeoutException("timeout")))
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
