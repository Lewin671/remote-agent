package io.github.lewin671.remoteagent.core

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
}
