package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.Session
import io.github.lewin671.remoteagent.proto.SessionState
import io.github.lewin671.remoteagent.proto.SessionStatus
import org.junit.Assert.assertEquals
import org.junit.Test

class QueueTest {
    private fun input(seq: Long, id: String, st: InputStatus, reason: String = "") =
        Event.newBuilder().setSeq(seq).setInputState(InputState.newBuilder().setRequestId(id).setStatus(st).setReason(reason)).build()

    @Test
    fun theQueueKeepsTheDaemonsOrder() {
        val d = derive(
            listOf(
                input(1, "b", InputStatus.INPUT_STATUS_QUEUED),
                input(2, "a", InputStatus.INPUT_STATUS_QUEUED),
                input(3, "c", InputStatus.INPUT_STATUS_QUEUED),
                input(4, "a", InputStatus.INPUT_STATUS_CANCELED),
            ),
        )
        assertEquals(listOf("b", "c"), queued(d))
    }

    @Test
    fun theDaemonsReasonComesFirst() {
        val s = SessionView("s", "m", Session.newBuilder().setInputWait("draft").setAgent(Agent.AGENT_CODEX).build())
        assertEquals("终端里有草稿", queueWhy(s, Status.RUNNING).short)
        val r = SessionView("s", "m", Session.newBuilder().build())
        assertEquals("本轮结束后发送", queueWhy(r, Status.RUNNING).short)
        assertEquals("等待本轮结束", queueWhy(r, Status.RUNNING).long)
    }

    @Test
    fun aCancelOutcomeSaysWhoCanceled() {
        assertEquals(CancelOutcome.CANCELED, cancelOutcome(InputStatus.INPUT_STATUS_CANCELED, canceledHere = true))
        assertEquals(CancelOutcome.CANCELED_ELSEWHERE, cancelOutcome(InputStatus.INPUT_STATUS_CANCELED, canceledHere = false))
        assertEquals(CancelOutcome.WRITTEN, cancelOutcome(InputStatus.INPUT_STATUS_SUBMITTED, canceledHere = true))
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
        assertEquals("本客户端", originLabel(o(k, "me"), state, "me", "MacBook"))
        assertEquals("Android", originLabel(o(k, "aaaab71c"), state, "me", "MacBook"))
        assertEquals("我的手机", originLabel(o(k, "cccc92de"), state, "me", "MacBook"))
        assertEquals("已移除的设备", originLabel(o(k, "gone9999"), state, "me", "MacBook"))
        assertEquals("另一客户端", originLabel(o(k, "gone9999"), RemoteState(), "me", "MacBook"))
        assertEquals("另一客户端", originLabel(o(k), state, "me", "MacBook"))
        assertEquals("本客户端", originLabel(o(k, "me"), RemoteState(), "me", "MacBook"))
        val meta = { name: String, platform: String, description: String -> io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder().setName(name).setPlatform(platform).setDescription(description).build() }
        val zone = java.time.ZoneId.of("UTC")
        val at = { y: Int, m: Int, d: Int, h: Int, min: Int -> java.time.LocalDateTime.of(y, m, d, h, min).atZone(zone).toInstant().toEpochMilli() }
        val now = at(2026, 10, 3, 12, 0)
        val browser = phone.copy(id = "dddd8a5f", createdMs = at(2026, 9, 27, 0, 0), meta = meta("", "web", "Chrome on macOS"))
        assertEquals("Chrome · macOS", clientName(browser, browser.id))
        assertEquals("浏览器", clientName(browser.copy(meta = meta("", "web", "")), browser.id))
        assertEquals("浏览器 · Chrome on macOS", clientDetail(browser))
        val pixel = browser.copy(createdMs = at(2025, 12, 1, 0, 0), meta = meta("我的手机", "android", "Android 16 · Pixel 9"))
        assertEquals("我的手机", clientName(pixel, pixel.id))
        assertEquals("Android · Pixel 9", defaultClientName(pixel, pixel.id))
        assertEquals("App · Android 16 · Pixel 9", clientDetail(pixel))
        val iphone = pixel.copy(meta = meta("", "ios", "iOS 27.0 · iPhone"))
        assertEquals("iPhone", defaultClientName(iphone, iphone.id))
        assertEquals("App · iOS 27.0 · iPhone", clientDetail(iphone))
        assertEquals("iOS", clientName(pixel.copy(meta = meta("", "ios", "")), pixel.id))
        assertEquals("种类未知", clientDetail(null))
        assertEquals("刚刚使用", clientUsed(browser.copy(lastSeenMs = now - 30_000), now, zone))
        assertEquals("5 分钟前使用", clientUsed(browser.copy(lastSeenMs = now - 5 * 60_000), now, zone))
        assertEquals("今天 9:05 使用", clientUsed(browser.copy(lastSeenMs = at(2026, 10, 3, 9, 5)), now, zone))
        assertEquals("昨天使用", clientUsed(browser.copy(lastSeenMs = at(2026, 10, 2, 23, 0)), now, zone))
        assertEquals("8月3日使用", clientUsed(browser.copy(lastSeenMs = at(2026, 8, 3, 0, 0)), now, zone))
        assertEquals("2025年12月1日添加", clientUsed(pixel, now, zone))
        assertEquals("已配对", clientUsed(null, now, zone))
        val c = { id: String, created: Long, seen: Long -> browser.copy(id = id, createdMs = created, lastSeenMs = seen) }
        assertEquals(listOf("me", "recent", "stale", "new", "old"),
            sortClients(listOf(c("old", 1, 0), c("me", 2, 5), c("new", 3, 0), c("recent", 1, 9), c("stale", 1, 4)), "me").map { it.id })
        assertEquals("MacBook 终端", originLabel(o(io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_LOCAL), state, "me", "MacBook"))
        assertEquals(null, originLabel(null, state, "me", "MacBook"))
    }
}

class AcceptedTest {
    private val local = io.github.lewin671.remoteagent.proto.Origin.newBuilder().setKind(io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_LOCAL).build()
    private val client = io.github.lewin671.remoteagent.proto.Origin.newBuilder().setKind(io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_CLIENT).setDeviceId("d1").build()

    @Test
    fun everyClientSeesWhatWasAcceptedAndHowFarItGot() {
        val d = derive(
            listOf(
                Event.newBuilder().setSeq(1).setOrigin(client).setInputState(InputState.newBuilder().setRequestId("r").setStatus(InputStatus.INPUT_STATUS_QUEUED)
                    .setText("看看这两张").addImageIds("i1").addImageIds("i2").setStage(io.github.lewin671.remoteagent.proto.InputStage.INPUT_STAGE_WAITING)).build(),
                Event.newBuilder().setSeq(2).setInputState(InputState.newBuilder().setRequestId("r").setStatus(InputStatus.INPUT_STATUS_QUEUED)
                    .setStage(io.github.lewin671.remoteagent.proto.InputStage.INPUT_STAGE_PASTING_IMAGES)).build(),
                Event.newBuilder().setSeq(3).setInputState(InputState.newBuilder().setRequestId("r").setStatus(InputStatus.INPUT_STATUS_FAILED).setReason("image_not_recognized")).build(),
            ),
        )
        val a = d.accepted.getValue("r")
        assertEquals("看看这两张", a.text)
        assertEquals(listOf("i1", "i2"), a.imageIds)
        assertEquals("d1", a.origin!!.deviceId)
        assertEquals(io.github.lewin671.remoteagent.proto.InputStage.INPUT_STAGE_PASTING_IMAGES, a.stage)
        assertEquals("image_not_recognized", d.failures["r"])
        assertEquals(emptyList<String>(), queued(d))
        assertEquals("Claude Code 没认出图片", failureText("image_not_recognized", Agent.AGENT_CLAUDE))
    }

    @Test
    fun theOriginSaysWhatWasTypedInTheTerminal() {
        val d = derive(
            listOf(
                Event.newBuilder().setSeq(1).setOrigin(local).setUserMessage(io.github.lewin671.remoteagent.proto.UserMessage.newBuilder().setText("typed")).build(),
                Event.newBuilder().setSeq(2).setOrigin(client).setUserMessage(io.github.lewin671.remoteagent.proto.UserMessage.newBuilder().setText("sent")).build(),
            ),
            Agent.AGENT_CLAUDE,
        )
        assertEquals(setOf(1L), d.typedLocally)
    }
}
