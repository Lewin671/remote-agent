package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.ForwardRequest
import io.github.lewin671.remoteagent.proto.NotForwarded
import java.io.BufferedInputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.Base64
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicInteger

/** Real HTTP/1 bytes: a reusable connection loses the first complete command reply. */
internal class RetryWireServer(private val mode: String) : AutoCloseable {
    private val listener=ServerSocket(0,16,InetAddress.getByName("127.0.0.1"))
    private val pool=Executors.newCachedThreadPool()
    private val sockets=ConcurrentHashMap.newKeySet<Socket>()
    val url="http://127.0.0.1:${listener.localPort}"
    val posts=AtomicInteger()
    val attempts=java.util.Collections.synchronizedList(mutableListOf<String>())
    init {
        pool.submit {
            while(!listener.isClosed) {
                val socket=try {listener.accept()} catch (_:Exception){break}
                sockets+=socket
                pool.submit {try {serve(socket)} finally {sockets-=socket;socket.close()}}
            }
        }
    }
    private fun line(input:BufferedInputStream):String? {
        val text=StringBuilder()
        while(true){val b=input.read();if(b<0)return null;if(b==10)return text.toString().trimEnd('\r');text.append(b.toChar())}
    }
    private fun serve(socket:Socket) {
        socket.soTimeout=5000
        if (mode == "h2-refused") { serveH2(socket); return }
        val input=BufferedInputStream(socket.getInputStream())
        while(true) {
            val request=line(input)?:return
            val headers=mutableMapOf<String,String>()
            while(true){val value=line(input)?:return;if(value.isEmpty())break;val parts=value.split(':',limit=2);if(parts.size==2)headers[parts[0].lowercase()]=parts[1].trim()}
            val length=headers["content-length"]?.toInt()?:0
            val body=input.readNBytes(length);if(body.size!=length)return
            if(request.startsWith("GET ")) {
                socket.getOutputStream().write("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: keep-alive\r\nRemote-Agent-Relay-Epoch: wire-epoch\r\n\r\nok".toByteArray());continue
            }
            val command=ForwardRequest.parseFrom(body)
            attempts+=command.attemptId
            if(posts.incrementAndGet()==1) {
                if(mode=="disconnect")return
                if (mode in listOf("redirect", "authentication", "interceptor")) {
                    val status = when(mode) { "redirect" -> "307 Temporary Redirect"; "authentication" -> "401 Unauthorized"; else -> "200 OK" }
                    val extra = when(mode) { "redirect" -> "Location: $url/replayed\r\n"; "authentication" -> "WWW-Authenticate: Basic realm=fixture\r\n"; else -> "" }
                    socket.getOutputStream().write("HTTP/1.1 $status\r\n${extra}Content-Type: application/proto\r\nContent-Length: 0\r\nConnection: keep-alive\r\n\r\n".toByteArray())
                    continue
                }
                val status = if(mode=="request-timeout") "408 Request Timeout" else "503 Service Unavailable"
                socket.getOutputStream().write("HTTP/1.1 $status\r\nRetry-After: 0\r\nContent-Type: application/json\r\nContent-Length: 46\r\nConnection: keep-alive\r\n\r\n{\"code\":\"unavailable\",\"message\":\"retry later\"}".toByteArray())
                continue
            }
            val detail=Base64.getEncoder().encodeToString(NotForwarded.newBuilder().setAttemptId(command.attemptId).setRelayEpoch(command.relayEpoch).setReason("machine_offline").build().toByteArray())
            val error="{\"code\":\"unavailable\",\"message\":\"offline\",\"details\":[{\"type\":\"remoteagent.v1.NotForwarded\",\"value\":\"$detail\"}]}".toByteArray()
            socket.getOutputStream().write("HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/json\r\nContent-Length: ${error.size}\r\nConnection: close\r\n\r\n".toByteArray())
            socket.getOutputStream().write(error);return
        }
    }
    // Minimal cleartext HTTP/2 peer. It consumes DATA before REFUSED_STREAM,
    // modelling an opaque peer failure after the command has crossed the wire.
    private fun serveH2(socket: Socket) {
        val input = socket.getInputStream()
        val output = socket.getOutputStream()
        if (String(input.readNBytes(24)) != "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n") return
        fun frame(type:Int, flags:Int, stream:Int, bytes:ByteArray = byteArrayOf()) {
            output.write(byteArrayOf((bytes.size shr 16).toByte(),(bytes.size shr 8).toByte(),bytes.size.toByte(),type.toByte(),flags.toByte(),(stream shr 24).toByte(),(stream shr 16).toByte(),(stream shr 8).toByte(),stream.toByte()))
            output.write(bytes); output.flush()
        }
        frame(4,0,0)
        val bodies = mutableMapOf<Int,java.io.ByteArrayOutputStream>()
        while (true) {
            val h = input.readNBytes(9); if (h.size != 9) return
            val length = ((h[0].toInt() and 255) shl 16) or ((h[1].toInt() and 255) shl 8) or (h[2].toInt() and 255)
            val type=h[3].toInt(); val flags=h[4].toInt()
            val stream = java.nio.ByteBuffer.wrap(h,5,4).int and 0x7fffffff
            val bytes=input.readNBytes(length); if(bytes.size != length)return
            if (type==4 && flags and 1==0) frame(4,1,0)
            if (type==6 && flags and 1==0) frame(6,1,0,bytes)
            if (type==1 && flags and 1!=0) {
                frame(1,4,stream,byteArrayOf(0x88.toByte()));frame(0,1,stream,"ok".toByteArray())
            }
            if (type!=0) continue
            val body=bodies.getOrPut(stream) {java.io.ByteArrayOutputStream()}; body.write(bytes)
            if(flags and 1==0)continue
            val command=ForwardRequest.parseFrom(body.toByteArray()); attempts+=command.attemptId
            if(posts.incrementAndGet()==1) {frame(3,0,stream,byteArrayOf(0,0,0,7)); continue}
            val detail=Base64.getEncoder().encodeToString(NotForwarded.newBuilder().setAttemptId(command.attemptId).setRelayEpoch(command.relayEpoch).setReason("machine_offline").build().toByteArray())
            val error="{\"code\":\"unavailable\",\"message\":\"offline\",\"details\":[{\"type\":\"remoteagent.v1.NotForwarded\",\"value\":\"$detail\"}]}".toByteArray()
            // HPACK literals without indexing, static names :status and content-type.
            val headers=byteArrayOf(8,3)+"503".toByteArray()+byteArrayOf(15,16,16)+"application/json".toByteArray()
            frame(1,4,stream,headers);frame(0,1,stream,error)
        }
    }
    override fun close(){listener.close();sockets.forEach {it.close()};pool.shutdownNow()}
}
