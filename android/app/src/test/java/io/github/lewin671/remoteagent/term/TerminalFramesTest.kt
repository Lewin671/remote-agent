package io.github.lewin671.remoteagent.term
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.TerminalFrame
import org.junit.Assert.*
import org.junit.Test
class TerminalFramesTest {
    @org.junit.Test fun historyPagesCountTowardsBacklogLimit() {
        val q = TerminalFrames(maxBytes = 3)
        val page = io.github.lewin671.remoteagent.proto.TerminalHistoryPage.newBuilder().setRequestId("r").addLines("中文中文").build()
        val frame = io.github.lewin671.remoteagent.proto.TerminalFrame.newBuilder().setHistoryPage(page).build()
        org.junit.Assert.assertEquals(TerminalFrames.Offer.OVERFLOW, q.offer(frame))
    }

    private fun output(text: String) = TerminalFrame.newBuilder().setOutput(ByteString.copyFromUtf8(text)).build()
    @Test fun preservesSnapshotOutputAndCloseOrderAcrossBoundedDrains() {
        val q = TerminalFrames()
        val snapshot = TerminalFrame.newBuilder().setSnapshot(io.github.lewin671.remoteagent.proto.TerminalSnapshot.newBuilder().setCols(80)).build()
        val frames = listOf(snapshot, output("abc"), output("def"), TerminalFrame.newBuilder().setClosed("done").build())
        assertEquals(TerminalFrames.Offer.SCHEDULE, q.offer(frames[0]))
        frames.drop(1).forEach { assertEquals(TerminalFrames.Offer.QUEUED, q.offer(it)) }
        val (first, more) = q.drain(3)
        assertEquals(frames.take(2), first); assertTrue(more)
        val (last, again) = q.drain(3)
        assertEquals(frames.drop(2), last); assertFalse(again)
        assertEquals(TerminalFrames.Offer.SCHEDULE, q.offer(output("new")))
    }
    @Test fun overflowIsReportedOnceAndOldQueuesCannotAffectNewConnections() {
        val old = TerminalFrames(maxBytes = 3, maxFrames = 2)
        assertEquals(TerminalFrames.Offer.SCHEDULE, old.offer(output("abc")))
        assertEquals(TerminalFrames.Offer.OVERFLOW, old.offer(output("d")))
        assertEquals(TerminalFrames.Offer.CLOSED, old.offer(output("late")))
        assertTrue(old.drain().first.isEmpty())
        val next = TerminalFrames()
        assertEquals(TerminalFrames.Offer.SCHEDULE, next.offer(output("new")))
        old.close(); assertEquals(TerminalFrames.Offer.CLOSED, old.offer(output("late")))
        assertEquals("new", next.drain().first.single().output.toStringUtf8())
        val small = TerminalFrames(maxFrames = 1)
        small.offer(output("")); assertEquals(TerminalFrames.Offer.OVERFLOW, small.offer(output("")))
    }
}
