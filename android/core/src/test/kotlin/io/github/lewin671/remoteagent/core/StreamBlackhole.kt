package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.Ready
import io.github.lewin671.remoteagent.proto.ServerFrame
import java.net.ServerSocket
import java.net.Socket
import java.security.MessageDigest
import java.util.Base64
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicInteger
import kotlin.concurrent.thread

/** Local stream fixture: by default the first upgraded socket never sends Ready. */
internal class StreamBlackhole(private val first: ((Socket) -> Unit)? = null) : AutoCloseable {
    private val listener = ServerSocket(0)
    private val sockets = CopyOnWriteArrayList<Socket>()
    val url = "http://127.0.0.1:${listener.localPort}"
    val attempts = AtomicInteger()
    val disconnections = AtomicInteger()
    init {
        thread(isDaemon = true) {
            try {
                while (!listener.isClosed) {
                    val socket = listener.accept()
                    sockets += socket
                    thread(isDaemon = true) {
                        socket.use {
                            try {
                                val reader = it.getInputStream().bufferedReader()
                                val headers = mutableListOf<String>()
                                while (true) {
                                    val line = reader.readLine() ?: return@thread
                                    if (line.isEmpty()) break
                                    headers += line
                                }
                                val key = headers.first { h -> h.startsWith("Sec-WebSocket-Key:", ignoreCase = true) }.substringAfter(':').trim()
                                val accept = Base64.getEncoder().encodeToString(MessageDigest.getInstance("SHA-1").digest((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").toByteArray()))
                                val output = it.getOutputStream()
                                output.write("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: $accept\r\n\r\n".toByteArray())
                                output.flush()
                                if (attempts.incrementAndGet() == 1 && first != null) {
                                    first.invoke(it)
                                } else if (attempts.get() > 1) {
                                    val ready = ServerFrame.newBuilder().setReady(Ready.getDefaultInstance()).build().toByteArray()
                                    output.write(byteArrayOf(0x82.toByte(), ready.size.toByte()))
                                    output.write(ready); output.flush()
                                }
                                // No application frames on the first connection. Its closure
                                // is observed independently of the client's lifecycle state.
                                while (it.getInputStream().read() != -1) { }
                            } catch (_: Exception) { }
                        }
                        sockets.remove(socket)
                        disconnections.incrementAndGet()
                    }
                }
            } catch (_: Exception) { }
        }
    }
    override fun close() {
        listener.close()
        sockets.forEach { it.close() }
    }

    companion object {
        fun writeFrame(output: java.io.OutputStream, bytes: ByteArray) {
            output.write(0x82)
            if (bytes.size < 126) output.write(bytes.size) else {
                output.write(127)
                for (shift in 56 downTo 0 step 8) output.write((bytes.size.toLong() ushr shift).toInt() and 0xff)
            }
            output.write(bytes)
            output.flush()
        }
    }
}
