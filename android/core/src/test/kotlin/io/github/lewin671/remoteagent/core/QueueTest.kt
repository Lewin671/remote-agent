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
    fun anEndedSessionEmptiesTheQueue() {
        val d = derive(
            listOf(
                input(1, "b", InputStatus.INPUT_STATUS_QUEUED),
                Event.newBuilder().setSeq(2).setSessionState(SessionState.newBuilder().setStatus(SessionStatus.SESSION_STATUS_EXITED)).build(),
            ),
        )
        assertEquals(emptyList<String>(), queued(d))
        assertEquals(2L, d.unsent["b"])
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
        assertEquals("Android · b71c", originLabel(o(k, "aaaab71c"), state, "me", "MacBook"))
        assertEquals("我的手机", originLabel(o(k, "cccc92de"), state, "me", "MacBook"))
        assertEquals("客户端 · 9999", originLabel(o(k, "gone9999"), state, "me", "MacBook"))
        // The server's word for who sent it is no proof.
        assertEquals("另一客户端", originLabel(o(io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_UNVERIFIED_CLIENT, "aaaab71c"), state, "me", "MacBook"))
        assertEquals("MacBook 本地终端", originLabel(o(io.github.lewin671.remoteagent.proto.OriginKind.ORIGIN_KIND_LOCAL), state, "me", "MacBook"))
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
