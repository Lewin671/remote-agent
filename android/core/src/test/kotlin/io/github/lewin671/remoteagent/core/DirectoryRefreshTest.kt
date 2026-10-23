package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class DirectoryRefreshTest {
    private val fixture = ColdOpenFixture(2)

    @Test fun unchangedHeadChecksSecurityWithoutListingAgain() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        val remote = peer.remote()
        try {
            remote.start(); runCurrent()
            peer.calls.clear()
            repeat(3) {
                peer.frame(ServerFrame.newBuilder().setSessionUpdated(SessionUpdated.newBuilder()
                    .setSession(fixture.sessions.getValue("short"))).build())
                peer.frame(ServerFrame.newBuilder().setMachineUpdated(MachineUpdated.newBuilder()
                    .setMachine(peer.machineRows.first())).build())
            }
            runCurrent()
            assertEquals(6, peer.calls.count { it == "GetSecurityStates" })
            assertEquals(0, peer.calls.count { it == "ListDevices" || it == "ListMachines" })
        } finally { remote.close(); runCurrent() }
    }

    @Test fun changedHeadRemovesLiveMembershipAndUnknownMachinesReload() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        val remote = peer.remote()
        try {
            remote.start(); runCurrent()
            peer.calls.clear()
            val removal = fixture.removeMachine()
            peer.securityRecord = fixture.record.installed(listOf(removal), listOf(null),
                HeadLease.sign(removal, SigningKey(ByteArray(32) { 1 }), fixture.now))
            peer.frame(ServerFrame.newBuilder().setMachineUpdated(MachineUpdated.newBuilder()
                .setMachine(peer.machineRows.first())).build())
            runCurrent()
            assertEquals(1, peer.calls.count { it == "ListDevices" })
            assertFalse(remote.state.value.devices.containsKey(fixture.machine.id))
            assertFalse(remote.state.value.machines.getValue(fixture.machine.id).online)
            peer.calls.clear()
            peer.frame(ServerFrame.newBuilder().setMachineUpdated(MachineUpdated.newBuilder()
                .setMachine(Machine.newBuilder().setId("unknown-machine").setOnline(true))).build())
            runCurrent()
            assertEquals(1, peer.calls.count { it == "ListDevices" })
            assertFalse(remote.state.value.machines.containsKey("unknown-machine"))
        } finally { remote.close(); runCurrent() }
    }

    @Test fun unknownEpochReloadsAndDoesNotAcceptUnverifiedMetadata() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        val remote = peer.remote()
        try {
            remote.start(); runCurrent()
            peer.calls.clear()
            val row = fixture.sessions.getValue("short")
            val header = DeviceSecurity.parseHeader(row.meta.securityHeader.toByteArray())
            val forged = row.toBuilder().setMeta(row.meta.toBuilder().setSecurityHeader(
                com.google.protobuf.ByteString.copyFrom(DeviceSecurity.headerBytes(header.copy(epoch = 99uL)))
            )).setLastSeq(50).build()
            peer.frame(ServerFrame.newBuilder().setSessionUpdated(SessionUpdated.newBuilder().setSession(forged)).build())
            runCurrent()
            assertEquals(1, peer.calls.count { it == "ListDevices" })
            assertEquals(2L, remote.state.value.sessions.getValue("short").lastSeq)
        } finally { remote.close(); runCurrent() }
    }

    @Test fun changedMachineMetadataIsVerifiedWithoutRelistingMembers() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        peer.machineRows = listOf(fixture.machine(1, "before"))
        val remote = peer.remote()
        try {
            remote.start(); runCurrent()
            assertEquals("before", remote.state.value.machines.getValue(fixture.machine.id).meta?.hostname)
            peer.calls.clear()
            val renamed = fixture.machine(2, "after")
            peer.frame(ServerFrame.newBuilder().setMachineUpdated(MachineUpdated.newBuilder().setMachine(renamed)).build())
            runCurrent()
            assertEquals("after", remote.state.value.machines.getValue(fixture.machine.id).meta?.hostname)
            val corrupted = fixture.machine(3, "forged").let { row -> row.toBuilder().setMeta(
                row.meta.toBuilder().setSignature(com.google.protobuf.ByteString.copyFrom(ByteArray(64)))
            ).build() }
            peer.frame(ServerFrame.newBuilder().setMachineUpdated(MachineUpdated.newBuilder().setMachine(corrupted)).build())
            runCurrent()
            assertEquals("after", remote.state.value.machines.getValue(fixture.machine.id).meta?.hostname)
            assertEquals(0, peer.calls.count { it == "ListDevices" || it == "ListMachines" })
        } finally { remote.close(); runCurrent() }
    }

    @Test fun failedChangedHeadDirectoryDoesNotApplyPushAgainstOldMembership() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        val remote = peer.remote()
        try {
            remote.start(); runCurrent()
            val before = remote.state.value.machines.getValue(fixture.machine.id)
            val removal = fixture.removeMachine()
            peer.securityRecord = fixture.record.installed(listOf(removal), listOf(null),
                HeadLease.sign(removal, SigningKey(ByteArray(32) { 1 }), fixture.now))
            peer.unavailableMethods += "ListDevices"
            peer.frame(ServerFrame.newBuilder().setMachineUpdated(MachineUpdated.newBuilder()
                .setMachine(peer.machineRows.first().toBuilder().setLastSeenMs(123))).build())
            runCurrent()
            assertEquals(before.lastSeenMs, remote.state.value.machines.getValue(fixture.machine.id).lastSeenMs)
            assertEquals(ConnState.OFFLINE, remote.state.value.conn)
        } finally { remote.close(); runCurrent() }
    }

    @Test fun completedDetachedLoadDoesNotReplaceANewExplicitRefresh() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        val remote = peer.remote()
        try {
            remote.start(); runCurrent()
            val reached = CompletableDeferred<Unit>()
            val release = CompletableDeferred<Unit>()
            peer.before = { name, _ -> if (name == "ListDevices") { reached.complete(Unit); release.await() } }
            val abandoned = launch { remote.refreshDevices() }
            reached.await()
            abandoned.cancelAndJoin()
            release.complete(Unit); runCurrent()
            peer.calls.clear()
            remote.refreshDevices()
            assertEquals(1, peer.calls.count { it == "ListDevices" })
        } finally { remote.close(); runCurrent() }
    }

    @Test fun explicitConcurrentRefreshesShareOneDirectoryRead() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        val remote = peer.remote()
        try {
            remote.start(); runCurrent()
            peer.calls.clear()
            val reached = CompletableDeferred<Unit>()
            val release = CompletableDeferred<Unit>()
            peer.before = { name, _ -> if (name == "ListDevices") { reached.complete(Unit); release.await() } }
            val first = async { remote.refreshDevices() }
            reached.await()
            val second = async { remote.refreshDevices() }
            runCurrent()
            release.complete(Unit)
            first.await(); second.await()
            assertEquals(1, peer.calls.count { it == "ListDevices" })
            assertEquals(1, peer.calls.count { it == "ListMachines" })
        } finally { remote.close(); runCurrent() }
    }
}
