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
}
