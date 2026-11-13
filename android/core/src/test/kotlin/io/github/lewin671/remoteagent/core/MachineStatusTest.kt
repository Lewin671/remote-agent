package io.github.lewin671.remoteagent.core
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
@OptIn(ExperimentalCoroutinesApi::class)
class MachineStatusTest {
 @Test fun lifecycle() = runTest {
  var calls=0
  val p=MachineStatusController(backgroundScope,{ calls++; if(calls in 2..4) error("offline"); MachineStatus.newBuilder().setObservedMs(calls.toLong()).build() })
  p.setActive(true,true,false);runCurrent();assertEquals(0,calls)
  p.setActive(true,true,true);runCurrent();assertEquals(MachinePollState.LIVE,p.state)
  advanceTimeBy(6000);runCurrent();assertEquals(MachinePollState.STALE,p.state);assertEquals(1L,p.last!!.observedMs)
  advanceTimeBy(2000);runCurrent();assertEquals(MachinePollState.LIVE,p.state);assertEquals(5L,p.curveSinceMs)
  p.setActive(false,true,true);advanceTimeBy(10000);runCurrent();assertEquals(5,calls)
  p.setActive(true,true,true);runCurrent();assertEquals(6,calls);assertEquals(5L,p.curveSinceMs);p.dispose()
 }
 @Test fun slowAndLate() = runTest {
  val gate=CompletableDeferred<MachineStatus>();var calls=0
  val p=MachineStatusController(backgroundScope,{calls++;gate.await()})
  p.setActive(true,true,true);runCurrent();advanceTimeBy(10000);runCurrent();assertEquals(1,calls)
  p.setActive(false,true,true);gate.complete(MachineStatus.getDefaultInstance());runCurrent();assertNull(p.last);p.dispose()
 }
 @Test fun firstFailure() = runTest {
  val p=MachineStatusController(backgroundScope,{error("offline")});p.setActive(true,true,true);runCurrent();advanceTimeBy(4000);runCurrent();assertEquals(MachinePollState.UNAVAILABLE,p.state);p.dispose()
 }
 @Test fun rankingsAndHelpers() {
  val a=ResourceUsage.newBuilder().setId("a").setCpuPercent(10.0).setMemoryBytes(100_000_000).build()
  val b=ResourceUsage.newBuilder().setId("b").setCpuPercent(12.0).setMemoryBytes(150_000_000).build()
  for(memory in listOf(false,true)) assertEquals("a",stableUsage(listOf("a","b"),listOf(b,a),memory)[0].id)
  val c=b.toBuilder().setCpuPercent(12.01).setMemoryBytes(150_000_001).build()
  for(memory in listOf(false,true)) assertEquals("b",stableUsage(listOf("a","b"),listOf(a,c),memory)[0].id)
  val m=MachineMeta.newBuilder().setCapabilities(MachineCapabilities.newBuilder().setMachineStatus(true)).setDiskStatus(DiskStatus.newBuilder().setAvailableBytes(9_000_000_000)).addProjects(ProjectSummary.newBuilder().setPath("old").setLatestCreatedMs(1)).addProjects(ProjectSummary.newBuilder().setPath("new").setLatestEndedMs(2)).build()
  assertNull(machineDiskReminder(false,m));assertNotNull(machineDiskReminder(true,m));assertEquals("new",recentMachineProject(m));assertTrue(isDiskFull(CommandException("disk_full","test")));assertFalse(isDiskFull(Exception("no space left on device")))
  assertTrue(Command.newBuilder().setGetMachineStatus(GetMachineStatusRequest.getDefaultInstance()).build().isReadOnlyCommand())
 }
}

@OptIn(ExperimentalCoroutinesApi::class)
class MachineStatusReviewTest {
 @Test fun offlineAndTimeout()=runTest {
  var calls=0;var hung=false
  val p=MachineStatusController(backgroundScope,{calls++;if(hung) awaitCancellation();MachineStatus.newBuilder().setObservedMs(1).build()},nowMillis={testScheduler.currentTime})
  p.setActive(true,true,true);runCurrent();p.setActive(true,false,true);assertEquals(MachinePollState.STALE,p.state);assertEquals(1L,p.last!!.observedMs);advanceTimeBy(20000);runCurrent();assertEquals(1,calls)
  hung=true;p.setActive(true,true,true);runCurrent();advanceTimeBy(45000);runCurrent();assertEquals(3,p.failures);assertEquals(MachinePollState.STALE,p.state);p.dispose()
 }
 @Test fun rankingMembership() {
  val rows=listOf("a","b","new","c").mapIndexed { i,id -> ResourceUsage.newBuilder().setId(id).setCpuPercent(listOf(5.0,10.0,30.0,25.0)[i]).build() }
  assertEquals(listOf("new","c","b","a"),stableUsage(listOf("gone","a","b","c"),rows).map { it.id })
 }
}
