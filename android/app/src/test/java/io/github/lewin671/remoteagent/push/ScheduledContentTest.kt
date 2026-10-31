package io.github.lewin671.remoteagent.push

import io.github.lewin671.remoteagent.proto.NotifyApproval
import io.github.lewin671.remoteagent.proto.NotifyContent
import io.github.lewin671.remoteagent.proto.NotifyKind
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ScheduledContentTest {
    private val scheduled =
        NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_SCHEDULED).setSourceEventSeq(7).setTitle("任务").setText("结果").build()

    @Test
    fun onlySignedScheduledContentForItsOwnEvent() {
        assertTrue(Notifier.scheduledContent(NotifyKind.NOTIFY_KIND_SCHEDULED, scheduled, "scheduled:7"))
        assertFalse(Notifier.scheduledContent(NotifyKind.NOTIFY_KIND_SCHEDULED, scheduled, "scheduled:8"))
        assertFalse(Notifier.scheduledContent(NotifyKind.NOTIFY_KIND_SCHEDULED, null, "scheduled:7"))
        assertFalse(Notifier.scheduledContent(NotifyKind.NOTIFY_KIND_TURN_ENDED, scheduled, "scheduled:7"))
    }

    @Test
    fun relabelledApprovalOrTurnPreviewStaysGeneric() {
        val approval =
            NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_ATTENTION).setSourceEventSeq(7)
                .setApproval(NotifyApproval.newBuilder().setApprovalId("a").setCommand("rm -rf x")).build()
        val turn = NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_TURN_ENDED).setSourceEventSeq(7).setText("预览").build()
        val scheduledWithApproval = approval.toBuilder().setKind(NotifyKind.NOTIFY_KIND_SCHEDULED).build()
        for (c in listOf(approval, turn, scheduledWithApproval))
            assertFalse(Notifier.scheduledContent(NotifyKind.NOTIFY_KIND_SCHEDULED, c, "scheduled:7"))
    }
}
