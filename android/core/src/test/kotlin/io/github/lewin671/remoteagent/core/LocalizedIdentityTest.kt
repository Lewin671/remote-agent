package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class LocalizedIdentityTest {
    @Test fun translatedFallbackNamesCannotCreateFalseDeviceCollisions() {
        fun client(id: String, platform: String) = DeviceView(id, DeviceKind.DEVICE_KIND_CLIENT, 0, DeviceMeta.newBuilder().setPlatform(platform).build())
        val browser = client("browser-a001", "web")
        val generic = client("generic-b002", "unknown-platform")
        val otherBrowser = client("browser-c003", "web")
        for (locale in listOf("zh-CN", "en")) {
            val same = if (locale == "en") "Same" else "相同"
            val resolver: MessageResolver = { same }
            assertEquals(same, defaultClientName(browser, browser.id, listOf(browser, generic), resolver))
            assertEquals(same, defaultClientName(generic, generic.id, listOf(browser, generic), resolver))
            assertEquals("$same · a001", defaultClientName(browser, browser.id, listOf(browser, otherBrowser), resolver))
        }
    }
    @Test fun diagnosticDeduplicationPreservesTheOldVisibleDetailsWithoutReadingUiCopy() {
        val cases = listOf(
            Exception("raw detail"), IllegalStateException("raw detail"), java.io.IOException(),
            java.net.UnknownHostException("raw resolver failure"), javax.net.ssl.SSLException("raw certificate failure"),
            ConnectException(Code.UNKNOWN, "raw reply", Exception("raw reply")),
            ConnectException(Code.UNAUTHENTICATED, "raw token failure"),
            MessageException(UiMessages.errorsAndroidCannotVerifyTheCurrentRequest().messageText()),
        )
        for (locale in listOf("zh-CN", "en")) for (error in cases) {
            val rawDetail = detailText(error)
            val primary = errorText(error).resolve { messageTextForTest(it, locale) }
            val before = if (rawDetail == primary) "" else rawDetail
            assertEquals(error.javaClass.simpleName + locale, before, connectionErrorDetail(error))
        }
    }
}
