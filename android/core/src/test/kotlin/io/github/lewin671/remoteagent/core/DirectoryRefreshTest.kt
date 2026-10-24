package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import kotlinx.coroutines.flow.first
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class DirectoryRefreshTest {
    private val fixture = ColdOpenFixture(2)

    @Test fun coldListingRetainsTaskOwnedApprovalThroughRefresh() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        peer.response = { method, _ -> if (method == "ListRemoteSessions")
            ListRemoteSessionsResponse.newBuilder().addSessions(fixture.taskApproval()).build() else null }
        val remote = peer.remote()
        try {
            assertTrue(remote.refresh()); assertTrue(remote.refresh())
            assertTrue(peer.calls.contains("ListRemoteSessions"))
            val state = remote.state.value
            assertTrue(state.sessions.getValue("empty").info!!.taskOwned)
            assertEquals(listOf("empty"), homeTimeline(state, HomeScope.All).attention.map { it.id })
            assertEquals(1, projects(state).size)
            assertEquals(listOf("empty"), projects(state).single().sessions.map { it.id })
            assertTrue(homeTimeline(state, HomeScope.All).running.isEmpty())
        } finally { remote.close(); runCurrent() }
    }

    @Test fun closingImmediatelyAfterInitialLoadDoesNotLeakMaintenanceFailures() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        val remote = peer.remote()
        try {
            remote.start()
            remote.state.first { it.loaded }
        } finally { remote.close(); runCurrent() }
    }

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

    @Test fun firstDirectoryMetadataIsCachedAndAnInvalidSameRevisionCannotReplaceIt() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        val original = fixture.machine(1, "cached-machine")
        peer.machineRows = listOf(original)
        val cache = MemoryCache()
        val remote = peer.remote(cache)
        try {
            remote.start(); runCurrent()
            assertEquals(original.meta, cache.machines().single().meta)
            peer.machineRows = listOf(original.toBuilder().setMeta(original.meta.toBuilder()
                .setSignature(com.google.protobuf.ByteString.copyFrom(ByteArray(64)))).build())
            remote.refresh()
            assertEquals(original.meta, cache.machines().single().meta)
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

    @Test fun pendingDirectoryCannotRollBackPushedMachineMetadataOrItsCache() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        peer.machineRows = listOf(fixture.machine(1, "before"))
        val cache = MemoryCache()
        val remote = peer.remote(cache)
        val release = CompletableDeferred<Unit>()
        try {
            remote.start(); runCurrent()
            val reached = CompletableDeferred<Unit>()
            peer.before = { name, _ -> if (name == "ListDevices") {
                reached.complete(Unit); release.await()
            } }
            val reading = async { remote.refresh() }
            reached.await()
            val pushed = fixture.machine(2, "after")
            peer.machineRows = listOf(pushed)
            peer.frame(ServerFrame.newBuilder().setMachineUpdated(MachineUpdated.newBuilder().setMachine(pushed)).build())
            runCurrent()
            assertEquals("after", remote.state.value.machines.getValue(fixture.machine.id).meta?.hostname)
            assertEquals(pushed.meta, cache.machines().single().meta)
            release.complete(Unit)
            assertTrue(reading.await())
            runCurrent()
            val installed = remote.state.value.machines.getValue(fixture.machine.id)
            assertEquals("after", installed.meta?.hostname)
            assertEquals(2L, installed.metaRev)
            assertEquals(pushed.meta, cache.machines().single().meta)
            // A later listing uses the retained trusted envelope as well.
            remote.refresh()
            assertEquals(pushed.meta, cache.machines().single().meta)
        } finally { release.complete(Unit); remote.close(); runCurrent() }
    }

    @Test fun anOlderMachineListingCannotReplaceAnInstalledHigherRevision() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        val newest = fixture.machine(2, "newest")
        peer.machineRows = listOf(newest)
        val cache = MemoryCache()
        val remote = peer.remote(cache)
        try {
            remote.start(); runCurrent()
            peer.machineRows = listOf(fixture.machine(1, "older"))
            remote.refresh()
            assertEquals("newest", remote.state.value.machines.getValue(fixture.machine.id).meta?.hostname)
            assertEquals(2L, remote.state.value.machines.getValue(fixture.machine.id).metaRev)
            assertEquals(newest.meta, cache.machines().single().meta)
        } finally { remote.close(); runCurrent() }
    }

    @Test fun deviceMetadataPushSupersedesAnOlderPendingDirectoryResponse() = runTest {
        val peer = ColdOpenPeer(fixture, this)
        var device = fixture.device(1, "before")
        val captured = java.util.IdentityHashMap<com.connectrpc.http.UnaryHTTPRequest, Device>()
        var hold = false
        val reached = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        peer.before = { name, request -> if (name == "ListDevices") {
            captured[request] = device
            if (hold) { hold = false; reached.complete(Unit); release.await() }
        } }
        peer.response = { name, request -> if (name == "ListDevices")
            ListDevicesResponse.newBuilder().addDevices(captured.getValue(request)).build() else null }
        val remote = peer.remote()
        try {
            remote.start(); runCurrent()
            assertEquals("before", remote.state.value.devices.getValue(fixture.phone.id).meta?.name)
            hold = true
            val reading = async { remote.refresh() }
            reached.await()
            device = fixture.device(2, "after")
            peer.frame(ServerFrame.newBuilder().setDeviceUpdated(DeviceUpdated.newBuilder().setDevice(device)).build())
            runCurrent()
            assertEquals("after", remote.state.value.devices.getValue(fixture.phone.id).meta?.name)
            release.complete(Unit)
            assertTrue(reading.await())
            runCurrent()
            val installed = remote.state.value.devices.getValue(fixture.phone.id)
            assertEquals("after", installed.meta?.name)
            assertEquals(2L, installed.metaRev)
        } finally { release.complete(Unit); remote.close(); runCurrent() }
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

    private fun supersededLoad(headChanged: Boolean) = runTest {
        val peer = ColdOpenPeer(fixture, this)
        val remote = peer.remote()
        val release = CompletableDeferred<Unit>()
        try {
            remote.start(); runCurrent()
            peer.calls.clear()
            val reached = CompletableDeferred<Unit>()
            var lists = 0
            peer.before = { name, _ -> if (name == "ListDevices" && ++lists == 1) {
                reached.complete(Unit); release.await()
            } }
            val old = launch { remote.refreshDevices() }
            reached.await()
            if (headChanged) {
                val removal = fixture.removeMachine()
                peer.securityRecord = fixture.record.installed(listOf(removal), listOf(null),
                    HeadLease.sign(removal, SigningKey(ByteArray(32) { 1 }), fixture.now))
                peer.frame(ServerFrame.newBuilder().setMachineUpdated(MachineUpdated.newBuilder()
                    .setMachine(peer.machineRows.first())).build())
            } else {
                peer.frame(ServerFrame.newBuilder().setDeviceUpdated(DeviceUpdated.newBuilder()
                    .setDevice(Device.newBuilder().setId(fixture.phone.id))).build())
            }
            runCurrent()
            assertEquals("new directory waited for a superseded response", 2, peer.calls.count { it == "ListDevices" })
            if (headChanged) assertFalse(remote.state.value.devices.containsKey(fixture.machine.id))
            release.complete(Unit); old.join(); runCurrent()
            if (headChanged) assertFalse(remote.state.value.devices.containsKey(fixture.machine.id))
        } finally { release.complete(Unit); remote.close(); runCurrent() }
    }

    @Test fun changedHeadDoesNotWaitForAnOldDirectoryResponse() = supersededLoad(true)
    @Test fun devicePushDoesNotReuseADirectoryResponseFromBeforeThePush() = supersededLoad(false)

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
