package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import io.github.lewin671.remoteagent.proto.Agent
import org.junit.Test

class QueueTest {
    @Test
    fun failureTextNamesTheAgentForUnrecognizedImages() {
        assertEquals("Claude Code 没认出图片", failureText("image_not_recognized", Agent.AGENT_CLAUDE, copy = ::messageTextForTest))
    }
}

class OriginTest {
    @Test
    fun clientsFormatNamesDetailsUsageAndSortOrder() {
        val meta = { name: String, platform: String, description: String -> io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder().setName(name).setPlatform(platform).setDescription(description).build() }
        val zone = java.time.ZoneId.of("UTC")
        val at = { y: Int, m: Int, d: Int, h: Int, min: Int -> java.time.LocalDateTime.of(y, m, d, h, min).atZone(zone).toInstant().toEpochMilli() }
        val now = at(2026, 10, 3, 12, 0)
        val phone = DeviceView("aaaab71c", io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT, 0,
            io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder().setPlatform("android").build())
        val browser = phone.copy(id = "dddd8a5f", createdMs = at(2026, 9, 27, 0, 0), meta = meta("", "web", "Chrome on macOS"))
        assertEquals("Chrome · macOS", clientName(browser, browser.id, copy = ::messageTextForTest))
        assertEquals(messageTextForTest(UiMessages.securityBrowser()), clientName(browser.copy(meta = meta("", "web", "")), browser.id, copy = ::messageTextForTest))
        val pixel = browser.copy(createdMs = at(2025, 12, 1, 0, 0), meta = meta("我的手机", "android", "Android 16 · Pixel 9"))
        assertEquals("我的手机", clientName(pixel, pixel.id, copy = ::messageTextForTest))
        assertEquals("Android · Pixel 9", defaultClientName(pixel, pixel.id, copy = ::messageTextForTest))
        val iphone = pixel.copy(meta = meta("", "ios", "iOS 27.0 · iPhone"))
        assertEquals("iPhone", defaultClientName(iphone, iphone.id, copy = ::messageTextForTest))
        assertEquals("iOS", clientName(pixel.copy(meta = meta("", "ios", "")), pixel.id, copy = ::messageTextForTest))
    }
}
