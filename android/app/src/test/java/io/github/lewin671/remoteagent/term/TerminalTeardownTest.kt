package io.github.lewin671.remoteagent.term

import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.TerminalFrame
import kotlinx.coroutines.*
import org.junit.Assert.*
import org.junit.Test
import java.lang.reflect.Proxy

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class TerminalTeardownTest {
    @get:org.junit.Rule(order = 0) val messages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()

    private fun checkRelease(reconnect: Boolean) {
        val remote = Remote(Account("https://example.com", byteArrayOf(), ByteArray(32) { 1 }, "fixture-device"), Api.defaultHttp())
        remote.showForTest(RemoteState(conn = ConnState.ONLINE,
            machines = mapOf("machine" to MachineView("machine", true, 0)),
            sessions = mapOf("session" to SessionView("session", "machine"))))
        val scope = CoroutineScope(SupervisorJob())
        val terminal = RemoteTerminal(remote, "session", scope)
        val failure = IllegalStateException("injected terminal close")
        val frameType = Class.forName("io.github.lewin671.remoteagent.core.transport.FrameStream")
        val socket = Proxy.newProxyInstance(frameType.classLoader, arrayOf(frameType)) { _, method, _ ->
            when (method.name) { "send", "close" -> true; else -> null }
        }
        val connection = TermConn::class.java.declaredConstructors.single().newInstance(socket,
            { _: TerminalFrame -> Unit }, { throw failure }) as TermConn
        val field = RemoteTerminal::class.java.getDeclaredField("conn").apply { isAccessible = true }
        field.set(terminal, connection)
        try {
            assertSame(failure, runCatching { if (reconnect) terminal.connect() else terminal.close() }.exceptionOrNull())
            assertNull("failed terminal kept in owner", field.get(terminal))
            assertTrue(runCatching { terminal.close() }.isSuccess)
        } finally { field.set(terminal, null); terminal.close(); scope.cancel(); remote.close() }
    }
    @Test fun closeDropsTheHandleBeforeInvokingItsCleanup() = checkRelease(false)
    @Test fun reconnectDropsTheOldHandleBeforeInvokingItsCleanup() = checkRelease(true)

    @Test fun mainPathChangesDoNotReplaceAnExistingTerminal() {
        val remote = Remote(Account("https://example.com", byteArrayOf(), ByteArray(32) { 1 }, "fixture-device"), Api.defaultHttp())
        val scope = CoroutineScope(SupervisorJob())
        val terminal = RemoteTerminal(remote,"session",scope)
        var closes = 0
        val type = Class.forName("io.github.lewin671.remoteagent.core.transport.FrameStream")
        val socket = Proxy.newProxyInstance(type.classLoader,arrayOf(type)) { _,method,_ -> if(method.name in listOf("send","close")) true else null }
        val connection = TermConn::class.java.declaredConstructors.single().newInstance(socket,
            { _: TerminalFrame -> Unit }, { closes++ }) as TermConn
        val field = RemoteTerminal::class.java.getDeclaredField("conn").apply { isAccessible = true }
        field.set(terminal,connection)
        try {
            for (status in listOf(ConnState.OFFLINE,ConnState.CONNECTING,ConnState.ONLINE)) {
                remote.showForTest(RemoteState(conn=status))
                terminal.connectIfNeeded()
                assertSame(connection,field.get(terminal)); assertEquals(0,closes)
            }
        } finally { terminal.close();scope.cancel();remote.close() }
    }
    @Test fun terminalClosureAfterMainRecoveryStartsANewLeg() {
        val remote=Remote(Account("https://example.com",byteArrayOf(),ByteArray(32){1},"fixture-device"),Api.defaultHttp())
        remote.showForTest(RemoteState(conn=ConnState.ONLINE,machines=mapOf("machine" to MachineView("machine",true,0)),sessions=mapOf("session" to SessionView("session","machine"))))
        val scope=CoroutineScope(SupervisorJob()+kotlinx.coroutines.test.StandardTestDispatcher())
        val terminal=RemoteTerminal(remote,"session",scope);var closes=0;var typed=0
        val type=Class.forName("io.github.lewin671.remoteagent.core.transport.FrameStream")
        val socket=Proxy.newProxyInstance(type.classLoader,arrayOf(type)){_,method,_->if(method.name in listOf("send","close"))true else null}
        val connection=TermConn::class.java.declaredConstructors.single().newInstance(socket,{_:TerminalFrame->typed++},{closes++}) as TermConn
        val conn=RemoteTerminal::class.java.getDeclaredField("conn").apply{isAccessible=true};conn.set(terminal,connection)
        try {
            val apply=RemoteTerminal::class.java.getDeclaredMethod("apply",TerminalFrame::class.java).apply{isAccessible=true}
            apply.invoke(terminal,TerminalFrame.newBuilder().setClosed("late loss").build())
            org.robolectric.Shadows.shadowOf(android.os.Looper.getMainLooper()).idle()
            assertEquals("late terminal failure did not reopen",1,closes)
            assertEquals(RemoteTerminal.State.Connecting,terminal.state.value);assertEquals(0,typed)
        }finally{terminal.close();scope.cancel();remote.close()}
    }

    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun repeatedClosedAndOverflowBeforeStabilityBackOffInsteadOfSpinning() {
        val remote = Remote(Account("https://example.com", byteArrayOf(), ByteArray(32) { 1 }, "fixture-device"), Api.defaultHttp())
        remote.showForTest(RemoteState(conn = ConnState.ONLINE, machines = mapOf("machine" to MachineView("machine", true, 0)), sessions = mapOf("session" to SessionView("session", "machine"))))
        val dispatcher = kotlinx.coroutines.test.StandardTestDispatcher()
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val terminal = RemoteTerminal(remote, "session", scope)
        val type = Class.forName("io.github.lewin671.remoteagent.core.transport.FrameStream")
        val socket = Proxy.newProxyInstance(type.classLoader, arrayOf(type)) { _, method, _ -> if (method.name in listOf("send", "close")) true else null }
        val connection = TermConn::class.java.declaredConstructors.single().newInstance(socket, { _: TerminalFrame -> Unit }, { }) as TermConn
        val connField = RemoteTerminal::class.java.getDeclaredField("conn").apply { isAccessible = true }
        val genField = RemoteTerminal::class.java.getDeclaredField("generation").apply { isAccessible = true }
        val reopenJobField = RemoteTerminal::class.java.getDeclaredField("reopenJob").apply { isAccessible = true }
        val applyMethod = RemoteTerminal::class.java.getDeclaredMethod("apply", TerminalFrame::class.java).apply { isAccessible = true }
        try {
            // Reset shared process token bucket via connect() then test reopen streak.
            connField.set(terminal, connection)
            applyMethod.invoke(terminal, TerminalFrame.newBuilder().setClosed("first drop").build())
            org.robolectric.Shadows.shadowOf(android.os.Looper.getMainLooper()).idle()
            assertEquals(RemoteTerminal.State.Connecting, terminal.state.value)
            assertNull("first reopen must not wait in backoff", reopenJobField.get(terminal))

            // Second close before 5s stability must back off via reopenJob instead of spinning connect().
            connField.set(terminal, connection)
            applyMethod.invoke(terminal, TerminalFrame.newBuilder().setClosed("second drop").build())
            org.robolectric.Shadows.shadowOf(android.os.Looper.getMainLooper()).idle()
            val genAfterClosed = genField.getInt(terminal)
            assertNotNull("second failure before 5s stability must schedule backoff reopenJob", reopenJobField.get(terminal))
            assertTrue(terminal.state.value is RemoteTerminal.State.Closed)

            // OVERFLOW also goes through reopen backoff rather than spinning connect().
            val scheduleReopen = RemoteTerminal::class.java.getDeclaredMethod("scheduleReopen", Int::class.javaPrimitiveType).apply { isAccessible = true }
            reopenJobField.set(terminal, null)
            connField.set(terminal, connection)
            scheduleReopen.invoke(terminal, genAfterClosed)
            assertNotNull("overflow must schedule backoff reopenJob", reopenJobField.get(terminal))
            assertTrue(terminal.state.value is RemoteTerminal.State.Closed)
        } finally {
            terminal.close(); scope.cancel(); remote.close()
        }
    }

    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun openingFailureAfterReopenEntersExponentialBackoff() {
        var now = 10_000L
        val recovery = io.github.lewin671.remoteagent.core.transport.TerminalRecovery.forTest({ now }, { 0.5 })
        val dispatcher = kotlinx.coroutines.test.StandardTestDispatcher()
        val remote = Remote(
            Account("https://example.com", byteArrayOf(), ByteArray(32) { 1 }, "fixture-device"),
            Api.defaultHttp(),
            dispatcher = dispatcher,
            monotonic = { now },
        )
        remote.showForTest(
            RemoteState(
                conn = ConnState.ONLINE,
                machines = mapOf("machine" to MachineView("machine", true, 0)),
                sessions = mapOf("session" to SessionView("session", "machine")),
            )
        )
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val terminal = RemoteTerminal(remote, "session", scope, recovery)
        val type = Class.forName("io.github.lewin671.remoteagent.core.transport.FrameStream")
        val socket = Proxy.newProxyInstance(type.classLoader, arrayOf(type)) { _, method, _ ->
            if (method.name in listOf("send", "close")) true else null
        }
        val connection = TermConn::class.java.declaredConstructors.single().newInstance(
            socket, { _: TerminalFrame -> Unit }, { }
        ) as TermConn
        val connField = RemoteTerminal::class.java.getDeclaredField("conn").apply { isAccessible = true }
        val reopenJobField = RemoteTerminal::class.java.getDeclaredField("reopenJob").apply { isAccessible = true }
        val applyMethod = RemoteTerminal::class.java.getDeclaredMethod("apply", TerminalFrame::class.java).apply { isAccessible = true }
        try {
            // First reopen after live drop takes a token immediately and enters openAttempt().
            connField.set(terminal, connection)
            applyMethod.invoke(terminal, TerminalFrame.newBuilder().setClosed("drop").build())
            org.robolectric.Shadows.shadowOf(android.os.Looper.getMainLooper()).idle()
            assertEquals(RemoteTerminal.State.Connecting, terminal.state.value)
            assertNull(reopenJobField.get(terminal))

            // openTerminal fails because sessionKeys["session"] is absent; catch must record failure in backoff.
            dispatcher.scheduler.runCurrent()
            assertTrue(terminal.state.value is RemoteTerminal.State.Closed)
            assertTrue("opening failure after reopen must record exponential backoff", recovery.delayMs() > 0L)

            // Main link going online / connectIfNeeded must wait on backoff instead of immediately reopening.
            terminal.connectIfNeeded()
            assertNotNull("connectIfNeeded after failed openAttempt must schedule backoff reopenJob", reopenJobField.get(terminal))
            assertTrue(terminal.state.value is RemoteTerminal.State.Closed)
        } finally {
            terminal.close(); scope.cancel(); remote.close()
        }
    }

    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun sharedProcessBucketUsesRemoteMonotonicClockAcrossClockDrift() {
        io.github.lewin671.remoteagent.core.transport.resetProcessBudgetForTest()
        val dispatcher = kotlinx.coroutines.test.StandardTestDispatcher()
        // Keep the injected elapsedRealtime clock 60s behind System.nanoTime() so any caller
        // mixing in nanoTime() would see a bogus +60s refill and bypass the 4/2s process cap.
        var elapsed = (System.nanoTime() / 1_000_000L) - 60_000L
        val holdHttp = java.util.concurrent.CountDownLatch(1)
        val stalledHttp = Api.defaultHttp().newBuilder()
            .addInterceptor { chain ->
                holdHttp.await(5, java.util.concurrent.TimeUnit.SECONDS)
                throw java.io.IOException("stalled for test")
            }
            .build()
        val hostRemote = Remote(
            Account("https://example.com", byteArrayOf(), ByteArray(32) { 10 }, "dev-host"),
            stalledHttp,
            dispatcher = dispatcher,
            monotonic = { elapsed },
        )
        val drainRecoveries = (1..4).map {
            io.github.lewin671.remoteagent.core.transport.TerminalRecovery { elapsed }
        }
        val scope = CoroutineScope(SupervisorJob() + dispatcher)
        val terminal = RemoteTerminal(hostRemote, "session", scope)
        val reopenJobField = RemoteTerminal::class.java.getDeclaredField("reopenJob").apply { isAccessible = true }
        try {
            hostRemote.showForTest(
                RemoteState(
                    conn = ConnState.ONLINE,
                    machines = mapOf("machine" to MachineView("machine", true, 0)),
                    sessions = mapOf("session" to SessionView("session", "machine")),
                )
            )
            // Exhaust all 4 global tokens on processBudget at `elapsed`.
            drainRecoveries.forEach { r -> assertNull(r.takeOrDelayMs()) }

            // TerminalRecovery must read the same injected clock (`elapsed`), seeing 0 tokens and deferring.
            terminal.connectIfNeeded()
            assertNotNull("TerminalRecovery must respect depleted shared processBudget on Remote's clock", reopenJobField.get(terminal))
            assertTrue(terminal.state.value is RemoteTerminal.State.Closed)
        } finally {
            holdHttp.countDown()
            terminal.close(); scope.cancel(); hostRemote.close()
            io.github.lewin671.remoteagent.core.transport.resetProcessBudgetForTest()
        }
    }

}
