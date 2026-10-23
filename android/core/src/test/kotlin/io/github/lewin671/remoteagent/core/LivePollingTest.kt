package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class LivePollingTest {
    @Test fun followsForegroundConnectionMachineAndSessionWhileStillSubscribed() = runTest {
        val fixture = ColdOpenFixture(2)
        val peer = ColdOpenPeer(fixture, this)
        val remote = peer.remote()
        val subscription = backgroundScope.launch { remote.events("short").collect() }
        fun polls() = peer.calls.count { it == "Forward" }
        fun machine(online: Boolean) {
            peer.frame(ServerFrame.newBuilder().setMachineUpdated(MachineUpdated.newBuilder()
                .setMachine(peer.machineRows.first().toBuilder().setOnline(online))).build())
        }
        try {
            runCurrent(); assertEquals(0, polls())
            remote.start(); runCurrent(); assertEquals(1, polls())
            machine(false); runCurrent()
            val offline = polls()
            advanceTimeBy(10_001); runCurrent(); assertEquals(offline, polls())
            machine(true); runCurrent(); assertEquals(offline + 1, polls())
            remote.stop(); runCurrent()
            val stopped = polls()
            advanceTimeBy(10_001); runCurrent(); assertEquals(stopped, polls())
            remote.start(); runCurrent(); assertEquals(stopped + 1, polls())
            peer.frame(ServerFrame.newBuilder().setSessionUpdated(SessionUpdated.newBuilder()
                .setSession(fixture.session("short", 2, SessionStatus.SESSION_STATUS_EXITED))).build())
            runCurrent(); val ended = polls()
            advanceTimeBy(10_001); runCurrent(); assertEquals(ended, polls())
            peer.frame(ServerFrame.newBuilder().setSessionUpdated(SessionUpdated.newBuilder()
                .setSession(fixture.session("short", 3, SessionStatus.SESSION_STATUS_RUNNING))).build())
            runCurrent(); assertEquals(ended + 1, polls())
            peer.offline = true
            remote.reconnectNow(); runCurrent()
            val disconnected = polls()
            advanceTimeBy(6_001); runCurrent(); assertEquals(disconnected, polls())
            peer.offline = false
            remote.reconnectNow(); runCurrent(); assertEquals(disconnected + 1, polls())
            subscription.cancel(); runCurrent(); val unsubscribed = polls()
            advanceTimeBy(6_001); runCurrent(); assertEquals(unsubscribed, polls())
        } finally { subscription.cancel(); remote.close(); runCurrent() }
    }

    @Test fun stopCancelsAnInFlightSnapshotRequest() = runTest {
        val fixture = ColdOpenFixture(2)
        val peer = ColdOpenPeer(fixture, this)
        val remote = peer.remote()
        var cancelled = false
        peer.before = { name, _ -> if (name == "Forward") {
            try { awaitCancellation() } finally { cancelled = true }
        } }
        val subscription = backgroundScope.launch { remote.events("short").collect() }
        try {
            remote.start(); runCurrent()
            assertTrue(peer.calls.contains("Forward"))
            remote.stop(); runCurrent()
            assertTrue(cancelled)
        } finally { subscription.cancel(); remote.close(); runCurrent() }
    }
}
