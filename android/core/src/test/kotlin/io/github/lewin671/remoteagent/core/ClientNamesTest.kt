package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class ClientNamesTest {
    private fun client(id: String, platform: String, description: String, name: String = "") =
        DeviceView(id, DeviceKind.DEVICE_KIND_CLIENT, 0, DeviceMeta.newBuilder().setPlatform(platform).setDescription(description).setName(name).build())

    @Test fun namesUsePlatformAndExistingDescription() {
        val samples = listOf(
            Triple("web", "Chrome on macOS", "Chrome · macOS"), Triple("web", "Chrome", "Chrome"), Triple("web", "", "浏览器"),
            Triple("android", "Android 16 · Pixel 9", "Android · Pixel 9"), Triple("android", "Android 16", "Android"),
            Triple("ios", "iOS 27 · iPhone", "iPhone"), Triple("ios", "iPadOS 27 · iPad", "iPad"), Triple("ios", "", "iOS"),
        )
        samples.forEach { (platform, description, expected) -> assertEquals(expected, clientName(client("a8f2", platform, description), "a8f2")) }
        assertEquals("客户端", clientName(null, "a8f2"))
    }

    @Test fun collisionsCompareEveryClientsDefaultEvenIfNamed() {
        val first = client("aaaaa8f2", "web", "Chrome on macOS")
        val second = client("bbbb1234", "web", "Chrome on macOS", "办公浏览器")
        val phone = client("cccc5678", "android", "Android 16 · Pixel 9")
        val all = listOf(first, second, phone)
        assertEquals("Chrome · macOS · a8f2", clientName(first, first.id, all))
        assertEquals("办公浏览器", clientName(second, second.id, all))
        assertEquals("Chrome · macOS · 1234", defaultClientName(second, second.id, all))
        assertEquals("Android · Pixel 9", clientName(phone, phone.id, all))
        assertEquals("Chrome · macOS", clientName(first, first.id, listOf(first, phone)))
    }
}
