package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionState
import io.github.lewin671.remoteagent.proto.SessionStatus
import org.junit.Test

class QueueTest {
    @Test
    fun theDaemonsReasonComesFirst() {
        val s = SessionView("s", "m", Session.newBuilder().setInputWait("draft").setAgent(Agent.AGENT_CODEX).build())
        assertEquals(messageTextForTest(UiMessages.queueAndroidDraft()), queueWhy(s, Status.RUNNING, copy = ::messageTextForTest).short)
        val r = SessionView("s", "m", Session.newBuilder().build())
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.queueAfterCurrentTurn()), queueWhy(r, Status.RUNNING, copy = ::messageTextForTest).short)
        assertEquals(messageTextForTest(UiMessages.queueAndroidTurnNote()), queueWhy(r, Status.RUNNING, copy = ::messageTextForTest).long)
    }

    @Test
    fun aCancelOutcomeSaysWhoCanceled() {
        assertEquals(CancelOutcome.CANCELED, cancelOutcome(InputStatus.INPUT_STATUS_CANCELED, canceledHere = true))
        assertEquals(CancelOutcome.CANCELED_ELSEWHERE, cancelOutcome(InputStatus.INPUT_STATUS_CANCELED, canceledHere = false))
        assertEquals(CancelOutcome.WRITTEN, cancelOutcome(InputStatus.INPUT_STATUS_SUBMITTED, canceledHere = true))
    }

    @Test
    fun failureTextNamesTheAgentForUnrecognizedImages() {
        assertEquals("Claude Code 没认出图片", failureText("image_not_recognized", Agent.AGENT_CLAUDE, copy = ::messageTextForTest))
    }
}

class OriginTest {
    private fun o(kind: io.github.lewin671.remoteagent.proto.OriginKind, dev: String = "") =
        io.github.lewin671.remoteagent.proto.Origin.newBuilder().setKind(kind).setDeviceId(dev).build()

    @Test
    fun onlySignedClientsAreNamedAndOnlyFromTheDeviceList() {
        val phone = DeviceView("aaaab71c", io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT, 0,
            io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder().setPlatform("android").build())
        val named = DeviceView("cccc92de", io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_CLIENT, 0,
            io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder().setName("我的手机").build())
        val state = RemoteState(devices = mapOf(phone.id to phone, named.id to named))
        val k = io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_CLIENT
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionOriginSelf()), originLabel(o(k, "me"), state, "me", "MacBook", copy = ::messageTextForTest))
        assertEquals("Android", originLabel(o(k, "aaaab71c"), state, "me", "MacBook", copy = ::messageTextForTest))
        assertEquals("我的手机", originLabel(o(k, "cccc92de"), state, "me", "MacBook", copy = ::messageTextForTest))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionOriginRemoved()), originLabel(o(k, "gone9999"), state, "me", "MacBook", copy = ::messageTextForTest))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionOriginOther()), originLabel(o(k, "gone9999"), RemoteState(), "me", "MacBook", copy = ::messageTextForTest))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionOriginOther()), originLabel(o(k), state, "me", "MacBook", copy = ::messageTextForTest))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.sessionOriginSelf()), originLabel(o(k, "me"), RemoteState(), "me", "MacBook", copy = ::messageTextForTest))
        val meta = { name: String, platform: String, description: String -> io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder().setName(name).setPlatform(platform).setDescription(description).build() }
        val zone = java.time.ZoneId.of("UTC")
        val at = { y: Int, m: Int, d: Int, h: Int, min: Int -> java.time.LocalDateTime.of(y, m, d, h, min).atZone(zone).toInstant().toEpochMilli() }
        val now = at(2026, 10, 3, 12, 0)
        val browser = phone.copy(id = "dddd8a5f", createdMs = at(2026, 9, 27, 0, 0), meta = meta("", "web", "Chrome on macOS"))
        assertEquals("Chrome · macOS", clientName(browser, browser.id, copy = ::messageTextForTest))
        assertEquals(messageTextForTest(UiMessages.securityBrowser()), clientName(browser.copy(meta = meta("", "web", "")), browser.id, copy = ::messageTextForTest))
        val pixel = browser.copy(createdMs = at(2025, 12, 1, 0, 0), meta = meta("我的手机", "android", "Android 16 · Pixel 9"))
        assertEquals("我的手机", clientName(pixel, pixel.id, copy = ::messageTextForTest))
        assertEquals("Android · Pixel 9", defaultClientName(pixel, pixel.id, copy = ::messageTextForTest))
        val iphone = pixel.copy(meta = meta("", "ios", "iOS 27.0 · iPhone"))
        assertEquals("iPhone", defaultClientName(iphone, iphone.id, copy = ::messageTextForTest))
        assertEquals("iOS", clientName(pixel.copy(meta = meta("", "ios", "")), pixel.id, copy = ::messageTextForTest))
        assertEquals("MacBook 终端", originLabel(o(io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_LOCAL), state, "me", "MacBook", copy = ::messageTextForTest))
        assertEquals(null, originLabel(null, state, "me", "MacBook", copy = ::messageTextForTest))
    }
}
