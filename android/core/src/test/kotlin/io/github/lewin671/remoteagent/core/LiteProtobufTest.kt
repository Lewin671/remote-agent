package io.github.lewin671.remoteagent.core

import com.connectrpc.AnyError
import com.google.protobuf.ByteString
import com.google.protobuf.CodedOutputStream
import io.github.lewin671.remoteagent.proto.GetChallengeRequest
import io.github.lewin671.remoteagent.proto.LoginRequest
import okio.Buffer
import okio.ByteString.Companion.toByteString
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import java.io.ByteArrayOutputStream

class LiteProtobufTest {
    private val message = LoginRequest.newBuilder().setDeviceId("device").setSignature(ByteString.copyFromUtf8("sig")).build()

    @Test
    fun codecRoundTripsAMessage() {
        val codec = LiteProtobufStrategy.codec(LoginRequest::class)
        assertEquals("proto", codec.encodingName())
        assertArrayEquals(message.toByteArray(), codec.serialize(message).readByteArray())
        assertArrayEquals(message.toByteArray(), codec.deterministicSerialize(message).readByteArray())
        assertEquals(message, codec.deserialize(Buffer().write(message.toByteArray())))
        assertEquals(LoginRequest.getDefaultInstance(), codec.deserialize(Buffer()))
    }

    @Test
    fun onlyMessagesHaveACodec() {
        assertThrows(RuntimeException::class.java) { LiteProtobufStrategy.codec(String::class) }
    }

    @Test
    fun unpacksAnErrorDetail() {
        val parser = LiteProtobufStrategy.errorDetailParser()
        val any = AnyError("type.googleapis.com/remoteagent.v1.LoginRequest", message.toByteArray().toByteString())
        assertEquals(message, parser.unpack(any, LoginRequest::class))
        assertThrows(RuntimeException::class.java) { parser.unpack(any, String::class) }
    }

    /** google.rpc.Status: code = 1, message = 2, details = 3, each an Any of type_url = 1 and value = 2. */
    @Test
    fun parsesStatusDetails() {
        fun any(type: String, value: ByteArray): ByteArray = ByteArrayOutputStream().also {
            val out = CodedOutputStream.newInstance(it)
            out.writeString(1, type)
            out.writeByteArray(2, value)
            out.flush()
        }.toByteArray()
        val status = ByteArrayOutputStream().also {
            val out = CodedOutputStream.newInstance(it)
            out.writeInt32(1, 7)
            out.writeString(2, "denied")
            out.writeByteArray(3, any("type.googleapis.com/a.B", byteArrayOf(1, 2, 3)))
            out.writeByteArray(3, any("type.googleapis.com/c.D", ByteArray(0)))
            out.flush()
        }.toByteArray()
        val details = LiteProtobufStrategy.errorDetailParser().parseDetails(status)
        assertEquals(listOf("type.googleapis.com/a.B", "type.googleapis.com/c.D"), details.map { it.type })
        assertArrayEquals(byteArrayOf(1, 2, 3), details[0].payload.toByteArray())
        assertEquals(0, details[1].payload.size)
        assertEquals(emptyList<Any>(), LiteProtobufStrategy.errorDetailParser().parseDetails(GetChallengeRequest.getDefaultInstance().toByteArray()))
    }
}
